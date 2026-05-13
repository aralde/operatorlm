// Package update implements OTA self-updates from GitHub Releases.
//
// Flow (triggered from the tray "Check for updates" item):
//  1. GET https://api.github.com/repos/<owner>/<repo>/releases/latest
//  2. Pick the asset matching runtime.GOOS / runtime.GOARCH plus the
//     "checksums.txt" asset.
//  3. Download the binary and the checksums file to os.TempDir().
//  4. Verify SHA256 against the entry in checksums.txt.
//  5. Hand the file to minio/selfupdate, which on Windows renames the running
//     .exe to .old and writes the new binary in its place.
//  6. Re-exec the new binary, then quit the current process.
package update

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/getlantern/systray"
	"github.com/minio/selfupdate"
)

const (
	// GitHub repo that hosts the releases.
	repoOwner = "aralde"
	repoName  = "operatorlm"

	// Name of the checksums asset attached to each release.
	checksumsAsset = "checksums.txt"

	httpTimeout = 60 * time.Second
)

// State is a JSON-serialisable snapshot of the updater's current status.
type State struct {
	Status    string    `json:"status"` // idle|checking|available|downloading|verifying|applying|restarting|uptodate|error
	Current   string    `json:"current"`
	Latest    string    `json:"latest,omitempty"`
	Error     string    `json:"error,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Manager owns the updater state machine. A single instance lives for the
// process lifetime; methods are safe to call concurrently.
type Manager struct {
	current string
	client  *http.Client

	mu    sync.RWMutex
	state State

	// Guards against overlapping CheckAndUpdate runs.
	running sync.Mutex
}

// NewManager returns a Manager seeded with the build-time version string.
// An empty currentVersion means "dev build" — CheckAndUpdate will refuse to
// apply any update in that case.
func NewManager(currentVersion string) *Manager {
	m := &Manager{
		current: currentVersion,
		client:  &http.Client{Timeout: httpTimeout},
	}
	m.setState(State{Status: "idle", Current: currentVersion, UpdatedAt: time.Now()})
	return m
}

// Snapshot returns a copy of the current state.
func (m *Manager) Snapshot() State {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state
}

func (m *Manager) setState(s State) {
	s.UpdatedAt = time.Now()
	if s.Current == "" {
		s.Current = m.current
	}
	m.mu.Lock()
	m.state = s
	m.mu.Unlock()
}

func (m *Manager) setStatus(status string) {
	m.mu.Lock()
	m.state.Status = status
	m.state.UpdatedAt = time.Now()
	m.mu.Unlock()
}

// CheckAndUpdate runs the full check → download → verify → apply → restart
// pipeline. It is safe to call repeatedly; concurrent calls return immediately.
// Progress is exposed via Snapshot().
func (m *Manager) CheckAndUpdate(ctx context.Context) {
	if !m.running.TryLock() {
		log.Printf("update: check already in progress, ignoring click")
		return
	}
	defer m.running.Unlock()

	if m.current == "" {
		m.setState(State{Status: "error", Error: "dev build (no version embedded); update skipped"})
		log.Printf("update: refusing to update a dev build (Version is empty)")
		return
	}

	m.setStatus("checking")
	log.Printf("update: checking GitHub for newer release (current=%s)", m.current)

	rel, err := m.checkLatest(ctx)
	if err != nil {
		m.setState(State{Status: "error", Error: err.Error()})
		log.Printf("update: check failed: %v", err)
		return
	}
	m.mu.Lock()
	m.state.Latest = rel.TagName
	m.mu.Unlock()

	if !isNewer(m.current, rel.TagName) {
		m.setState(State{Status: "uptodate", Latest: rel.TagName})
		log.Printf("update: already on latest (%s)", m.current)
		return
	}

	binAsset, sumsAsset, err := pickAssets(rel)
	if err != nil {
		m.setState(State{Status: "error", Latest: rel.TagName, Error: err.Error()})
		log.Printf("update: %v", err)
		return
	}

	m.setStatus("downloading")
	log.Printf("update: downloading %s", binAsset.Name)
	binPath, err := m.download(ctx, binAsset.BrowserDownloadURL, binAsset.Name)
	if err != nil {
		m.setState(State{Status: "error", Latest: rel.TagName, Error: err.Error()})
		log.Printf("update: download failed: %v", err)
		return
	}
	defer os.Remove(binPath)

	m.setStatus("verifying")
	if err := m.verifyChecksum(ctx, binPath, binAsset.Name, sumsAsset.BrowserDownloadURL); err != nil {
		m.setState(State{Status: "error", Latest: rel.TagName, Error: err.Error()})
		log.Printf("update: checksum verification failed: %v", err)
		return
	}

	m.setStatus("applying")
	log.Printf("update: applying new binary")
	if err := applyBinary(binPath); err != nil {
		m.setState(State{Status: "error", Latest: rel.TagName, Error: err.Error()})
		log.Printf("update: apply failed: %v", err)
		return
	}

	m.setStatus("restarting")
	log.Printf("update: restarting into %s", rel.TagName)
	if err := restart(); err != nil {
		// Best-effort: even if restart fails, the new binary is on disk and
		// will be picked up on the next manual launch.
		m.setState(State{Status: "error", Latest: rel.TagName, Error: "applied, but restart failed: " + err.Error()})
		log.Printf("update: restart failed: %v", err)
		return
	}
}

// ---- GitHub release metadata ----

type releaseInfo struct {
	TagName string         `json:"tag_name"`
	Assets  []releaseAsset `json:"assets"`
}

type releaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func (m *Manager) checkLatest(ctx context.Context) (*releaseInfo, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", repoOwner, repoName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "operatorlm-updater")

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github releases API: HTTP %d", resp.StatusCode)
	}
	var rel releaseInfo
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("decode release: %w", err)
	}
	if rel.TagName == "" {
		return nil, errors.New("github returned empty tag_name")
	}
	return &rel, nil
}

// pickAssets finds the binary asset for this OS/arch and the checksums asset.
// Convention: assets are named `operatorlm-<os>-<arch>` or
// `OperatorLM-<os>-<arch>.exe` on Windows. Matching is case-insensitive and
// requires both the OS and arch tokens to appear in the asset name.
func pickAssets(rel *releaseInfo) (binary, checksums *releaseAsset, err error) {
	osTok := strings.ToLower(runtime.GOOS)
	archTok := strings.ToLower(runtime.GOARCH)

	for i := range rel.Assets {
		a := &rel.Assets[i]
		name := strings.ToLower(a.Name)
		if name == checksumsAsset {
			checksums = a
			continue
		}
		if !strings.Contains(name, osTok) || !strings.Contains(name, archTok) {
			continue
		}
		if runtime.GOOS == "windows" && !strings.HasSuffix(name, ".exe") {
			continue
		}
		binary = a
	}
	if binary == nil {
		return nil, nil, fmt.Errorf("no release asset matches %s/%s", osTok, archTok)
	}
	if checksums == nil {
		return nil, nil, fmt.Errorf("release is missing %s", checksumsAsset)
	}
	return binary, checksums, nil
}

// ---- download + checksum ----

func (m *Manager) download(ctx context.Context, url, name string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "operatorlm-updater")

	resp, err := m.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: HTTP %d", name, resp.StatusCode)
	}

	tmp, err := os.CreateTemp("", "operatorlm-update-*")
	if err != nil {
		return "", err
	}
	path := tmp.Name()

	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		os.Remove(path)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

func (m *Manager) verifyChecksum(ctx context.Context, binPath, assetName, sumsURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sumsURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "operatorlm-updater")
	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download checksums: HTTP %d", resp.StatusCode)
	}

	want, err := findChecksum(resp.Body, assetName)
	if err != nil {
		return err
	}

	f, err := os.Open(binPath)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("checksum mismatch: want %s, got %s", want, got)
	}
	return nil
}

// findChecksum scans a sha256sum-style file and returns the hex digest for the
// matching asset. Supported line formats:
//
//	<hex>  <filename>
//	<hex> *<filename>
func findChecksum(r io.Reader, assetName string) (string, error) {
	sc := bufio.NewScanner(r)
	// Allow lines up to 1 MiB — checksums files are tiny but safer to be generous.
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	target := strings.ToLower(assetName)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		fname := strings.TrimPrefix(fields[1], "*")
		if strings.EqualFold(filepath.Base(fname), target) {
			return fields[0], nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("checksum entry for %s not found", assetName)
}

// ---- apply + restart ----

func applyBinary(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return selfupdate.Apply(f, selfupdate.Options{})
}

// restart re-execs the currently running binary (which selfupdate has already
// replaced on disk) and then quits the current process. The new process
// inherits stdio; on Windows the parent's tray icon disappears the moment
// systray.Quit() runs and the new process draws its own.
func restart() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Start(); err != nil {
		return err
	}

	// Give the new process a beat to initialise before we tear down the tray.
	go func() {
		time.Sleep(500 * time.Millisecond)
		systray.Quit()
		// Belt and braces: if systray.Quit() doesn't unblock systray.Run()
		// quickly (e.g. headless mode), force exit.
		time.Sleep(2 * time.Second)
		os.Exit(0)
	}()
	return nil
}

// ---- semver comparison ----

// isNewer returns true if `latest` represents a strictly higher semver than
// `current`. Accepts an optional leading "v". Non-numeric or pre-release
// suffixes are ignored. Falls back to string comparison if parsing fails.
func isNewer(current, latest string) bool {
	c := parseSemver(current)
	l := parseSemver(latest)
	for i := 0; i < 3; i++ {
		if l[i] > c[i] {
			return true
		}
		if l[i] < c[i] {
			return false
		}
	}
	return false
}

func parseSemver(v string) [3]int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	// Drop pre-release/build suffix (anything after - or +).
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	var out [3]int
	for i := 0; i < 3 && i < len(parts); i++ {
		n, _ := strconv.Atoi(parts[i])
		out[i] = n
	}
	return out
}
