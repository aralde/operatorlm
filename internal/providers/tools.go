package providers

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Pinned upstream releases for the local engine binaries. Every platform
// listed here ships a prebuilt archive; anything else must be installed by
// hand (or put on PATH).
const (
	llamaCppTag   = "b11669"
	whisperCppTag = "v1.9.1"
	piperTag      = "2023.11.14-2"
)

// toolSpec describes how to fetch one engine binary for the running platform.
type toolSpec struct {
	id     string   // downloader id and bin/<id>/ folder name
	binary string   // final executable name, without .exe
	names  []string // executable names that may appear in the archive
	url    string   // "" when there is no prebuilt binary for this platform
	manual string   // install hint shown when url is ""
}

func toolSpecFor(tool string) (toolSpec, error) {
	goos, arch := runtime.GOOS, runtime.GOARCH
	pick := func(m map[string]string) string { return m[goos+"/"+arch] }

	switch tool {
	case "llama-server":
		base := "https://github.com/ggml-org/llama.cpp/releases/download/" + llamaCppTag + "/llama-" + llamaCppTag + "-bin-"
		return toolSpec{
			id: tool, binary: "llama-server", names: []string{"llama-server", "llama-server.exe"},
			url: pick(map[string]string{
				"windows/amd64": base + "win-cpu-x64.zip",
				"windows/arm64": base + "win-cpu-arm64.zip",
				"linux/amd64":   base + "ubuntu-x64.tar.gz",
				"linux/arm64":   base + "ubuntu-arm64.tar.gz",
				"darwin/arm64":  base + "macos-arm64.tar.gz",
				"darwin/amd64":  base + "macos-x64.tar.gz",
			}),
			manual: "install llama.cpp and make sure llama-server is on PATH",
		}, nil
	case "whisper-server":
		base := "https://github.com/ggml-org/whisper.cpp/releases/download/" + whisperCppTag + "/"
		return toolSpec{
			id: tool, binary: "whisper-server", names: []string{"whisper-server", "whisper-server.exe", "server.exe"},
			url: pick(map[string]string{
				"windows/amd64": base + "whisper-bin-x64.zip",
				"linux/amd64":   base + "whisper-bin-ubuntu-x64.tar.gz",
				"linux/arm64":   base + "whisper-bin-ubuntu-arm64.tar.gz",
			}),
			manual: "install whisper.cpp (macOS: brew install whisper-cpp) and make sure whisper-server is on PATH",
		}, nil
	case "piper":
		base := "https://github.com/rhasspy/piper/releases/download/" + piperTag + "/piper_"
		return toolSpec{
			id: tool, binary: "piper", names: []string{"piper", "piper.exe"},
			url: pick(map[string]string{
				"windows/amd64": base + "windows_amd64.zip",
				"linux/amd64":   base + "linux_x86_64.tar.gz",
				"linux/arm64":   base + "linux_aarch64.tar.gz",
				"darwin/arm64":  base + "macos_aarch64.tar.gz",
				"darwin/amd64":  base + "macos_x64.tar.gz",
			}),
			manual: "install piper and make sure it is on PATH",
		}, nil
	}
	return toolSpec{}, fmt.Errorf("unknown tool %q", tool)
}

// ToolDownloadable reports whether tool has a prebuilt binary for this platform.
func ToolDownloadable(tool string) bool {
	spec, err := toolSpecFor(tool)
	return err == nil && spec.url != ""
}

// StartTool downloads and installs an engine binary into destDir, so the
// executable ends up at destDir/<binary>[.exe] with its libraries and data
// files next to it. No-op (returns the live state) if already running.
func (d *Downloader) StartTool(tool, destDir string) (DownloadState, error) {
	if destDir == "" {
		return DownloadState{}, fmt.Errorf("no target directory configured")
	}
	spec, err := toolSpecFor(tool)
	if err != nil {
		return DownloadState{}, err
	}
	if spec.url == "" {
		return DownloadState{}, fmt.Errorf("no prebuilt %s for %s/%s: %s", spec.binary, runtime.GOOS, runtime.GOARCH, spec.manual)
	}

	d.mu.Lock()
	if st, ok := d.states[spec.id]; ok && st.Status == "downloading" {
		cur := *st
		d.mu.Unlock()
		return cur, nil
	}
	st := &DownloadState{ID: spec.id, Status: "downloading", Dir: destDir}
	d.states[spec.id] = st
	d.mu.Unlock()

	go d.runTool(spec, destDir, st)
	return *st, nil
}

// StartLlamaServer, StartWhisperServer and StartPiper keep the per-tool
// download endpoints working.
func (d *Downloader) StartLlamaServer(destDir string) (DownloadState, error) {
	return d.StartTool("llama-server", destDir)
}

func (d *Downloader) StartWhisperServer(destDir string) (DownloadState, error) {
	return d.StartTool("whisper-server", destDir)
}

func (d *Downloader) StartPiper(destDir string) (DownloadState, error) {
	return d.StartTool("piper", destDir)
}

func (d *Downloader) runTool(spec toolSpec, destDir string, st *DownloadState) {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		d.fail(st, fmt.Errorf("create dir: %w", err))
		return
	}
	archive := filepath.Join(destDir, ".download-"+filepath.Base(spec.url))
	defer os.Remove(archive)
	d.set(st, func(s *DownloadState) { s.File = filepath.Base(spec.url) })

	var done int64
	if _, err := d.download(spec.url, archive, &done, st); err != nil {
		d.fail(st, fmt.Errorf("download: %w", err))
		return
	}

	d.set(st, func(s *DownloadState) { s.File = "Extracting files..." })
	tmp, err := os.MkdirTemp(destDir, ".extract-")
	if err != nil {
		d.fail(st, err)
		return
	}
	defer os.RemoveAll(tmp)
	if err := extractArchive(archive, tmp); err != nil {
		d.fail(st, fmt.Errorf("extract: %w", err))
		return
	}
	if err := promoteBinaryDir(tmp, destDir, spec.names, binaryFileName(spec.binary)); err != nil {
		d.fail(st, err)
		return
	}

	d.set(st, func(s *DownloadState) {
		s.Status = "done"
		s.Downloaded = s.Total
		s.File = ""
	})
	log.Printf("local engine: %s installed -> %s", spec.binary, destDir)
}

func binaryFileName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// extractArchive unpacks a .zip or .tar.gz into dest. Entries that would land
// outside dest are skipped, and symlinks are kept only when they point inside
// dest (release tarballs link versioned shared libraries this way).
func extractArchive(src, dest string) error {
	if strings.HasSuffix(strings.ToLower(src), ".zip") {
		return extractZip(src, dest)
	}
	return extractTarGz(src, dest)
}

func insideDir(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel)
}

func extractZip(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		target := filepath.Join(dest, f.Name)
		if !insideDir(dest, target) {
			continue
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		err = writeArchiveFile(target, rc, f.Mode())
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func extractTarGz(src, dest string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(dest, h.Name)
		if !insideDir(dest, target) {
			continue
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := writeArchiveFile(target, tr, os.FileMode(h.Mode)); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if runtime.GOOS == "windows" || filepath.IsAbs(h.Linkname) ||
				!insideDir(dest, filepath.Join(filepath.Dir(target), h.Linkname)) {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Symlink(h.Linkname, target); err != nil {
				return err
			}
		}
	}
}

func writeArchiveFile(target string, r io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if mode&0o777 == 0 {
		mode = 0o644
	}
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode&0o777)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, r)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// promoteBinaryDir finds the first of names under root and moves every entry
// of its folder (libraries, data dirs, symlinks) into destDir, so the binary
// keeps its runtime files next to it. The binary is renamed to finalName.
func promoteBinaryDir(root, destDir string, names []string, finalName string) error {
	var found string
	_ = filepath.WalkDir(root, func(p string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return nil
		}
		for _, n := range names {
			if strings.EqualFold(e.Name(), n) {
				found = p
				return filepath.SkipAll
			}
		}
		return nil
	})
	if found == "" {
		return fmt.Errorf("archive did not contain any of %v", names)
	}
	entries, err := os.ReadDir(filepath.Dir(found))
	if err != nil {
		return err
	}
	for _, e := range entries {
		dst := filepath.Join(destDir, e.Name())
		if err := os.RemoveAll(dst); err != nil {
			return err
		}
		if err := os.Rename(filepath.Join(filepath.Dir(found), e.Name()), dst); err != nil {
			return err
		}
	}
	bin := filepath.Join(destDir, filepath.Base(found))
	final := filepath.Join(destDir, finalName)
	if bin != final {
		_ = os.RemoveAll(final)
		if err := os.Rename(bin, final); err != nil {
			return err
		}
	}
	return os.Chmod(final, 0o755)
}

// CheckTool runs the installed binary once with --help and reports a loader
// failure (missing shared library, wrong architecture) as an error, so setup
// can say what is wrong instead of failing later on the first request.
func CheckTool(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := prepareCommand(ctx, path, "--help")
	out, err := cmd.CombinedOutput()
	if err == nil || ctx.Err() != nil {
		return nil
	}
	text := string(out)
	switch {
	case strings.Contains(text, "error while loading shared libraries"),
		strings.Contains(text, "Library not loaded"),
		strings.Contains(err.Error(), "exec format error"),
		strings.Contains(err.Error(), "executable file not found"),
		strings.Contains(err.Error(), "no such file or directory"):
		if line := strings.TrimSpace(lastLine(text)); line != "" {
			return fmt.Errorf("%s cannot start: %s", filepath.Base(path), line)
		}
		return fmt.Errorf("%s cannot start: %v", filepath.Base(path), err)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return fmt.Errorf("%s cannot start: %v", filepath.Base(path), err)
	}
	return nil
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}
