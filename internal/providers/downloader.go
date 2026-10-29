package providers

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DownloadState is the live status of a catalog model download.
type DownloadState struct {
	ID         string `json:"id"`
	Status     string `json:"status"` // "downloading" | "done" | "error"
	File       string `json:"file"`   // current filename
	Downloaded int64  `json:"downloaded"`
	Total      int64  `json:"total"`
	Error      string `json:"error,omitempty"`
	Dir        string `json:"dir,omitempty"`
}

// Downloader runs catalog downloads in the background and tracks their progress.
type Downloader struct {
	mu     sync.Mutex
	states map[string]*DownloadState
}

func NewDownloader() *Downloader {
	return &Downloader{states: make(map[string]*DownloadState)}
}

// Start kicks off a download of all of entry's files into destDir/<entry.ID>/.
// It is a no-op (returns the live state) if a download for this id is already
// running. Returns an error only for an immediately-detectable problem.
func (d *Downloader) Start(entry CatalogEntry, destDir string) (DownloadState, error) {
	if destDir == "" {
		return DownloadState{}, fmt.Errorf("no models directory configured")
	}

	d.mu.Lock()
	if st, ok := d.states[entry.ID]; ok && st.Status == "downloading" {
		cur := *st
		d.mu.Unlock()
		return cur, nil
	}
	dir := filepath.Join(destDir, entry.ID)
	st := &DownloadState{ID: entry.ID, Status: "downloading", Total: entry.TotalSize(), Dir: dir}
	d.states[entry.ID] = st
	d.mu.Unlock()

	go d.run(entry, dir, st)
	return *st, nil
}

func (d *Downloader) run(entry CatalogEntry, dir string, st *DownloadState) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		d.fail(st, fmt.Errorf("create dir: %w", err))
		return
	}

	var done int64
	for _, f := range entry.Files {
		d.set(st, func(s *DownloadState) { s.File = f.Filename })
		dest := filepath.Join(dir, f.Filename)

		// Skip if already fully present (matches expected size when known).
		if info, err := os.Stat(dest); err == nil && (f.Size == 0 || info.Size() >= f.Size) {
			done += info.Size()
			d.set(st, func(s *DownloadState) { s.Downloaded = done })
			continue
		}

		n, err := d.download(f.URL, dest, &done, st)
		if err != nil {
			d.fail(st, fmt.Errorf("%s: %w", f.Filename, err))
			return
		}
		done = n
	}

	d.set(st, func(s *DownloadState) {
		s.Status = "done"
		s.Downloaded = s.Total
	})
	log.Printf("local catalog: download %q complete -> %s", entry.ID, dir)
}

// download streams url to dest (via a .part temp file) and advances st.Downloaded.
// It retries transient failures (network resets, 5xx, 429) up to maxAttempts,
// resuming from the partial file with an HTTP Range request so a multi-GB
// download survives a flaky connection without restarting. Returns the new
// cumulative total (baseDone + final file size).
func (d *Downloader) download(url, dest string, baseDone *int64, st *DownloadState) (int64, error) {
	const maxAttempts = 6
	tmp := dest + ".part"
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}

		var existing int64
		if info, err := os.Stat(tmp); err == nil {
			existing = info.Size()
		}

		written, err := d.downloadOnce(url, tmp, *baseDone+existing, existing, st)
		if err == nil {
			if rerr := os.Rename(tmp, dest); rerr != nil {
				return *baseDone, rerr
			}
			return *baseDone + written, nil
		}
		lastErr = err
		if isPermanent(err) {
			break
		}
		log.Printf("local catalog: %s attempt %d/%d failed (%v), retrying", filepath.Base(dest), attempt, maxAttempts, err)
	}
	return *baseDone, lastErr
}

// downloadOnce performs a single (possibly resumed) GET. `base` is the progress
// baseline; `from` is how many bytes already exist in tmp. Returns the total
// bytes in tmp after this attempt.
func (d *Downloader) downloadOnce(url, tmp string, base, from int64, st *DownloadState) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return from, err
	}
	if from > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", from))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return from, err
	}
	defer resp.Body.Close()

	var out *os.File
	switch resp.StatusCode {
	case http.StatusPartialContent: // resume accepted, append
		out, err = os.OpenFile(tmp, os.O_WRONLY|os.O_APPEND, 0o644)
	case http.StatusOK: // server ignored Range, restart from scratch
		from, base = 0, base-from
		out, err = os.Create(tmp)
		if err == nil && st.Total == 0 && resp.ContentLength > 0 {
			d.set(st, func(s *DownloadState) { s.Total = resp.ContentLength })
		}
	default:
		return from, fmt.Errorf("status %d", resp.StatusCode)
	}
	if err != nil {
		return from, err
	}

	pw := &progressWriter{base: base, st: st, d: d}
	_, copyErr := io.Copy(out, io.TeeReader(resp.Body, pw))
	closeErr := out.Close()
	if copyErr != nil {
		return from, copyErr
	}
	if closeErr != nil {
		return from, closeErr
	}
	return from + pw.n, nil
}

// isPermanent reports whether err is a non-retryable HTTP status (4xx except 429).
func isPermanent(err error) bool {
	for code := 400; code < 500; code++ {
		if code == 429 {
			continue
		}
		if err.Error() == fmt.Sprintf("status %d", code) {
			return true
		}
	}
	return false
}

type progressWriter struct {
	base int64
	n    int64
	st   *DownloadState
	d    *Downloader
	last time.Time
}

func (p *progressWriter) Write(b []byte) (int, error) {
	p.n += int64(len(b))
	// Throttle status updates to ~4/s to avoid lock churn.
	if now := time.Now(); now.Sub(p.last) > 250*time.Millisecond {
		p.last = now
		total := p.base + p.n
		p.d.set(p.st, func(s *DownloadState) { s.Downloaded = total })
	}
	return len(b), nil
}

func (d *Downloader) set(st *DownloadState, fn func(*DownloadState)) {
	d.mu.Lock()
	fn(st)
	d.mu.Unlock()
}

func (d *Downloader) fail(st *DownloadState, err error) {
	log.Printf("local catalog: download %q failed: %v", st.ID, err)
	d.set(st, func(s *DownloadState) {
		s.Status = "error"
		s.Error = err.Error()
	})
}

// Status returns the state for one id (zero value with empty Status if unknown).
func (d *Downloader) Status(id string) DownloadState {
	d.mu.Lock()
	defer d.mu.Unlock()
	if st, ok := d.states[id]; ok {
		return *st
	}
	return DownloadState{ID: id}
}

// All returns a snapshot of every tracked download.
func (d *Downloader) All() map[string]DownloadState {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[string]DownloadState, len(d.states))
	for k, v := range d.states {
		out[k] = *v
	}
	return out
}
