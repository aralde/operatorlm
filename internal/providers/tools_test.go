package providers

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func writeTarGz(t *testing.T, path string, entries []tar.Header, bodies map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, h := range entries {
		h := h
		body := bodies[h.Name]
		h.Size = int64(len(body))
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if body != "" {
			if _, err := tw.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	tw.Close()
	gz.Close()
	f.Close()
}

func TestInstallFromTarGzKeepsLibsAndDataNextToBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks are skipped on Windows")
	}
	tmp := t.TempDir()
	archive := filepath.Join(tmp, "piper_linux_x86_64.tar.gz")
	writeTarGz(t, archive, []tar.Header{
		{Name: "piper/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "piper/piper", Typeflag: tar.TypeReg, Mode: 0o755},
		{Name: "piper/libonnxruntime.so.1.14.1", Typeflag: tar.TypeReg, Mode: 0o644},
		{Name: "piper/libonnxruntime.so", Typeflag: tar.TypeSymlink, Linkname: "libonnxruntime.so.1.14.1"},
		{Name: "piper/escape", Typeflag: tar.TypeSymlink, Linkname: "../../etc/passwd"},
		{Name: "piper/espeak-ng-data/phontab", Typeflag: tar.TypeReg, Mode: 0o644},
		{Name: "../outside", Typeflag: tar.TypeReg, Mode: 0o644},
	}, map[string]string{"piper/piper": "bin", "piper/libonnxruntime.so.1.14.1": "lib", "piper/espeak-ng-data/phontab": "data", "../outside": "x"})

	extract := filepath.Join(tmp, "extract")
	dest := filepath.Join(tmp, "bin", "piper")
	os.MkdirAll(extract, 0o755)
	os.MkdirAll(dest, 0o755)
	if err := extractArchive(archive, extract); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "outside")); err == nil {
		t.Fatal("entry outside the extract dir was written")
	}
	if _, err := os.Lstat(filepath.Join(extract, "piper", "escape")); err == nil {
		t.Fatal("symlink pointing outside the extract dir was kept")
	}
	if err := promoteBinaryDir(extract, dest, []string{"piper"}, "piper"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"piper", "libonnxruntime.so", "espeak-ng-data/phontab"} {
		if _, err := os.Stat(filepath.Join(dest, p)); err != nil {
			t.Fatalf("%s missing after install: %v", p, err)
		}
	}
	if info, _ := os.Stat(filepath.Join(dest, "piper")); info.Mode()&0o100 == 0 {
		t.Fatal("binary is not executable")
	}
}

func TestInstallFromZipRenamesBinaryInSubfolder(t *testing.T) {
	tmp := t.TempDir()
	archive := filepath.Join(tmp, "whisper-bin-x64.zip")
	f, _ := os.Create(archive)
	zw := zip.NewWriter(f)
	for name, body := range map[string]string{"Release/server.exe": "bin", "Release/whisper.dll": "dll"} {
		w, _ := zw.Create(name)
		w.Write([]byte(body))
	}
	zw.Close()
	f.Close()

	extract := filepath.Join(tmp, "extract")
	dest := filepath.Join(tmp, "dest")
	os.MkdirAll(extract, 0o755)
	os.MkdirAll(dest, 0o755)
	if err := extractArchive(archive, extract); err != nil {
		t.Fatal(err)
	}
	if err := promoteBinaryDir(extract, dest, []string{"whisper-server.exe", "server.exe"}, "whisper-server.exe"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"whisper-server.exe", "whisper.dll"} {
		if _, err := os.Stat(filepath.Join(dest, p)); err != nil {
			t.Fatalf("%s missing after install: %v", p, err)
		}
	}
}

func TestEngineBinariesDownloadableOnMainPlatforms(t *testing.T) {
	if runtime.GOARCH != "amd64" || (runtime.GOOS != "linux" && runtime.GOOS != "windows") {
		t.Skip("checked on linux/windows amd64")
	}
	for _, tool := range []string{"llama-server", "whisper-server", "piper"} {
		if !ToolDownloadable(tool) {
			t.Errorf("%s has no prebuilt download on %s/%s", tool, runtime.GOOS, runtime.GOARCH)
		}
	}
}
