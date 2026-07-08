package providers

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanLocalModels(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "gemma-3-4b-Q4_K_M.gguf"))
	writeFile(t, filepath.Join(dir, "sub", "qwen2.5-coder-7b.gguf"))
	writeFile(t, filepath.Join(dir, "notamodel.txt"))
	// split model: only the first shard should surface, with the suffix stripped
	writeFile(t, filepath.Join(dir, "big-70b-00001-of-00003.gguf"))
	writeFile(t, filepath.Join(dir, "big-70b-00002-of-00003.gguf"))
	writeFile(t, filepath.Join(dir, "big-70b-00003-of-00003.gguf"))

	models, err := ScanLocalModels(dir)
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]string{}
	for _, m := range models {
		got[m.ID] = m.Path
	}

	want := []string{"gemma-3-4b-Q4_K_M", "qwen2.5-coder-7b", "big-70b"}
	if len(got) != len(want) {
		t.Fatalf("got %d models %v, want %d", len(got), got, len(want))
	}
	for _, id := range want {
		if _, ok := got[id]; !ok {
			t.Errorf("missing model id %q (got %v)", id, got)
		}
	}
	if p := got["big-70b"]; filepath.Base(p) != "big-70b-00001-of-00003.gguf" {
		t.Errorf("split model should point at first shard, got %q", p)
	}
}

func TestScanLocalModelsEmptyDir(t *testing.T) {
	models, err := ScanLocalModels("")
	if err != nil || models != nil {
		t.Fatalf("empty dir: got %v, %v", models, err)
	}
}

func TestScanLocalModelsMMProj(t *testing.T) {
	dir := t.TempDir()
	d := filepath.Join(dir, "llama-server")
	writeFile(t, filepath.Join(d, "google_gemma-4-E4B-it-Q3_K_M.gguf"))
	writeFile(t, filepath.Join(d, "google_gemma-4-E4B-it-Q4_K_M.gguf"))
	writeFile(t, filepath.Join(d, "mmproj-google_gemma-4-E4B-it-f16.gguf"))
	writeFile(t, filepath.Join(d, "DeepSeek-R1-0528-Qwen3-8B-Q4_K_M.gguf")) // no projector

	models, err := ScanLocalModels(dir)
	if err != nil {
		t.Fatal(err)
	}

	byID := map[string]LocalModel{}
	for _, m := range models {
		byID[m.ID] = m
	}

	// The projector must NOT appear as a model.
	for id := range byID {
		if mmprojRe.MatchString(id) {
			t.Errorf("projector leaked as a model: %q", id)
		}
	}

	// Both gemma quants should be vision-enabled and point at the same projector.
	for _, id := range []string{"google_gemma-4-E4B-it-Q3_K_M", "google_gemma-4-E4B-it-Q4_K_M"} {
		m, ok := byID[id]
		if !ok {
			t.Fatalf("missing model %q (got %v)", id, byID)
		}
		if !m.HasVision() {
			t.Errorf("%q should have a projector attached", id)
		}
		if filepath.Base(m.MMProjPath) != "mmproj-google_gemma-4-E4B-it-f16.gguf" {
			t.Errorf("%q wrong projector: %q", id, m.MMProjPath)
		}
	}

	// A model without a matching projector stays text-only.
	if m := byID["DeepSeek-R1-0528-Qwen3-8B-Q4_K_M"]; m.HasVision() {
		t.Errorf("DeepSeek should not have a projector, got %q", m.MMProjPath)
	}
}

// A model alone in its own folder with a generically-named projector (how
// catalog downloads are laid out, e.g. Gemma's "mmproj-model-f16.gguf") must
// still pair via the per-folder fallback.
func TestScanLocalModelsGenericProjectorOwnFolder(t *testing.T) {
	dir := t.TempDir()
	own := filepath.Join(dir, "gemma-3-4b")
	writeFile(t, filepath.Join(own, "gemma-3-4b-it-Q4_K_M.gguf"))
	writeFile(t, filepath.Join(own, "mmproj-model-f16.gguf"))

	models, err := ScanLocalModels(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 {
		t.Fatalf("want 1 model, got %d: %v", len(models), models)
	}
	if !models[0].HasVision() {
		t.Errorf("model in own folder should pair the lone projector, got none")
	}
}
