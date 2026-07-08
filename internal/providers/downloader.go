package providers

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

// unzip extracts a zip archive src into dest directory.
func unzip(src string, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		fpath := filepath.Join(dest, f.Name)

		// Check for Zip Slip vulnerability
		if !strings.HasPrefix(fpath, filepath.Clean(dest)+string(os.PathSeparator)) {
			continue
		}

		if f.FileInfo().IsDir() {
			os.MkdirAll(fpath, os.ModePerm)
			continue
		}

		if err = os.MkdirAll(filepath.Dir(fpath), os.ModePerm); err != nil {
			return err
		}

		outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			return err
		}

		rc, err := f.Open()
		if err != nil {
			outFile.Close()
			return err
		}

		_, err = io.Copy(outFile, rc)
		outFile.Close()
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// StartLlamaServer starts downloading the appropriate llama-server binary.
func (d *Downloader) StartLlamaServer(destDir string) (DownloadState, error) {
	if destDir == "" {
		return DownloadState{}, fmt.Errorf("no target directory configured")
	}

	var url string
	switch runtime.GOOS {
	case "windows":
		url = "https://github.com/ggml-org/llama.cpp/releases/download/b3300/llama-b3300-bin-win-cpu-x64.zip"
	case "darwin":
		if runtime.GOARCH == "arm64" {
			url = "https://github.com/ggml-org/llama.cpp/releases/download/b3300/llama-b3300-bin-macos-arm64.zip"
		} else {
			url = "https://github.com/ggml-org/llama.cpp/releases/download/b3300/llama-b3300-bin-macos-x64.zip"
		}
	default:
		url = "https://github.com/ggml-org/llama.cpp/releases/download/b3300/llama-b3300-bin-ubuntu-x64.zip"
	}

	d.mu.Lock()
	if st, ok := d.states["llama-server"]; ok && st.Status == "downloading" {
		cur := *st
		d.mu.Unlock()
		return cur, nil
	}
	st := &DownloadState{
		ID:     "llama-server",
		Status: "downloading",
		Total:  0,
		Dir:    destDir,
	}
	d.states["llama-server"] = st
	d.mu.Unlock()

	go d.runLlamaServer(url, destDir, st)
	return *st, nil
}

func (d *Downloader) runLlamaServer(url string, destDir string, st *DownloadState) {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		d.fail(st, fmt.Errorf("create dir: %w", err))
		return
	}

	zipPath := filepath.Join(destDir, "llama-server.zip")
	d.set(st, func(s *DownloadState) { s.File = "Downloading zip package..." })

	var done int64
	_, err := d.download(url, zipPath, &done, st)
	if err != nil {
		d.fail(st, fmt.Errorf("download zip: %w", err))
		return
	}

	d.set(st, func(s *DownloadState) { s.File = "Extracting files..." })
	if err := unzip(zipPath, destDir); err != nil {
		d.fail(st, fmt.Errorf("extract zip: %w", err))
		os.Remove(zipPath)
		return
	}

	os.Remove(zipPath)

	d.set(st, func(s *DownloadState) {
		s.Status = "done"
		s.Downloaded = s.Total
		s.File = ""
	})
	log.Printf("local engine: llama-server download and extraction complete -> %s", destDir)
}

// StartWhisperServer starts downloading the appropriate whisper-server binary.
func (d *Downloader) StartWhisperServer(destDir string) (DownloadState, error) {
	if destDir == "" {
		return DownloadState{}, fmt.Errorf("no target directory configured")
	}
	if runtime.GOOS != "windows" {
		return DownloadState{}, fmt.Errorf("automatic download of whisper-server is only supported on Windows. On macOS/Linux, please compile or install whisper.cpp manually")
	}

	url := "https://github.com/ggerganov/whisper.cpp/releases/download/v1.7.1/whisper-v1.7.1-bin-win-msvc-x64.zip"

	d.mu.Lock()
	if st, ok := d.states["whisper-server"]; ok && st.Status == "downloading" {
		cur := *st
		d.mu.Unlock()
		return cur, nil
	}
	st := &DownloadState{
		ID:     "whisper-server",
		Status: "downloading",
		Total:  0,
		Dir:    destDir,
	}
	d.states["whisper-server"] = st
	d.mu.Unlock()

	go d.runWhisperServer(url, destDir, st)
	return *st, nil
}

func (d *Downloader) runWhisperServer(url string, destDir string, st *DownloadState) {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		d.fail(st, fmt.Errorf("create dir: %w", err))
		return
	}

	zipPath := filepath.Join(destDir, "whisper-server.zip")
	d.set(st, func(s *DownloadState) { s.File = "Downloading zip package..." })

	var done int64
	_, err := d.download(url, zipPath, &done, st)
	if err != nil {
		d.fail(st, fmt.Errorf("download zip: %w", err))
		return
	}

	d.set(st, func(s *DownloadState) { s.File = "Extracting files..." })
	if err := unzip(zipPath, destDir); err != nil {
		d.fail(st, fmt.Errorf("extract zip: %w", err))
		os.Remove(zipPath)
		return
	}

	os.Remove(zipPath)

	// In whisper.cpp zip, the binary is server.exe. Rename it to whisper-server.exe
	oldPath := filepath.Join(destDir, "server.exe")
	newPath := filepath.Join(destDir, "whisper-server.exe")
	if _, err := os.Stat(oldPath); err == nil {
		_ = os.Rename(oldPath, newPath)
	}

	d.set(st, func(s *DownloadState) {
		s.Status = "done"
		s.Downloaded = s.Total
		s.File = ""
	})
	log.Printf("local engine: whisper-server download and extraction complete -> %s", destDir)
}

// StartPiper starts downloading the appropriate piper binary.
func (d *Downloader) StartPiper(destDir string) (DownloadState, error) {
	if destDir == "" {
		return DownloadState{}, fmt.Errorf("no target directory configured")
	}
	if runtime.GOOS != "windows" {
		return DownloadState{}, fmt.Errorf("automatic download of piper is only supported on Windows. On macOS/Linux, please install piper manually")
	}

	url := "https://github.com/rhasspy/piper/releases/download/v1.2.0/piper_windows_amd64.zip"

	d.mu.Lock()
	if st, ok := d.states["piper"]; ok && st.Status == "downloading" {
		cur := *st
		d.mu.Unlock()
		return cur, nil
	}
	st := &DownloadState{
		ID:     "piper",
		Status: "downloading",
		Total:  0,
		Dir:    destDir,
	}
	d.states["piper"] = st
	d.mu.Unlock()

	go d.runPiper(url, destDir, st)
	return *st, nil
}

func (d *Downloader) runPiper(url string, destDir string, st *DownloadState) {
	// Piper zip extracts to a subfolder named 'piper', so we unzip in the parent directory
	parentDir := filepath.Dir(destDir)
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		d.fail(st, fmt.Errorf("create dir: %w", err))
		return
	}

	zipPath := filepath.Join(parentDir, "piper.zip")
	d.set(st, func(s *DownloadState) { s.File = "Downloading zip package..." })

	var done int64
	_, err := d.download(url, zipPath, &done, st)
	if err != nil {
		d.fail(st, fmt.Errorf("download zip: %w", err))
		return
	}

	d.set(st, func(s *DownloadState) { s.File = "Extracting files..." })
	if err := unzip(zipPath, parentDir); err != nil {
		d.fail(st, fmt.Errorf("extract zip: %w", err))
		os.Remove(zipPath)
		return
	}

	os.Remove(zipPath)

	d.set(st, func(s *DownloadState) {
		s.Status = "done"
		s.Downloaded = s.Total
		s.File = ""
	})
	log.Printf("local engine: piper download and extraction complete -> %s", destDir)
}
