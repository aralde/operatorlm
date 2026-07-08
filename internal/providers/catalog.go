package providers

import "strings"

// CatalogFile is one downloadable artifact of a catalog model.
type CatalogFile struct {
	URL      string `json:"url"`
	Filename string `json:"filename"`
	Role     string `json:"role"`       // "model" | "mmproj"
	Size     int64  `json:"size_bytes"` // approximate, for progress totals
}

// CatalogEntry is a curated, ready-to-run local model with sensible defaults.
type CatalogEntry struct {
	ID          string        `json:"id"`          // catalog id (also the download subfolder)
	Name        string        `json:"name"`        // display name
	Description string        `json:"description"` // one-liner
	Tags        []string      `json:"tags"`        // chat | vision | agentic | text
	Backend     string        `json:"backend"`     // "gpu" | "cpu"
	Recommended bool          `json:"recommended"`
	ModelID     string        `json:"model_id"`     // routable id once installed (model filename stem)
	NGPULayers  int           `json:"ngpu_layers"`  // recommended default
	ContextSize int           `json:"context_size"` // recommended default
	Files       []CatalogFile `json:"files"`
}

// TotalSize returns the sum of all file sizes (for the UI / progress).
func (e CatalogEntry) TotalSize() int64 {
	var t int64
	for _, f := range e.Files {
		t += f.Size
	}
	return t
}

const (
	hfQwenVL  = "https://huggingface.co/ggml-org/Qwen2.5-VL-3B-Instruct-GGUF/resolve/main"
	hfGemma   = "https://huggingface.co/ggml-org/gemma-3-4b-it-GGUF/resolve/main"
	hfQwen    = "https://huggingface.co/Qwen/Qwen2.5-3B-Instruct-GGUF/resolve/main"
	hfLlama32 = "https://huggingface.co/bartowski/Llama-3.2-3B-Instruct-GGUF/resolve/main"
)

// catalog is the curated set of recommended models. Tuned for a ~4 GB VRAM GPU
// (Q4_K_M, the smallest quant that stays reliable) plus one CPU-friendly pick.
var catalog = []CatalogEntry{
	{
		ID:          "qwen2.5-vl-3b",
		Name:        "Qwen2.5-VL 3B Instruct",
		Description: "All-rounder: chat + vision + best-in-class tool calling for its size. Best single local model for a 4 GB GPU.",
		Tags:        []string{"chat", "vision", "agentic"},
		Backend:     "gpu",
		Recommended: true,
		ModelID:     "Qwen2.5-VL-3B-Instruct-Q4_K_M",
		NGPULayers:  99,
		ContextSize: 8192,
		Files: []CatalogFile{
			{URL: hfQwenVL + "/Qwen2.5-VL-3B-Instruct-Q4_K_M.gguf", Filename: "Qwen2.5-VL-3B-Instruct-Q4_K_M.gguf", Role: "model", Size: 1929 << 20},
			{URL: hfQwenVL + "/mmproj-Qwen2.5-VL-3B-Instruct-f16.gguf", Filename: "mmproj-Qwen2.5-VL-3B-Instruct-f16.gguf", Role: "mmproj", Size: 1338 << 20},
		},
	},
	{
		ID:          "gemma-3-4b",
		Name:        "Gemma 3 4B Instruct",
		Description: "Strong chat + vision, excellent multilingual (Spanish). Slightly weaker tool calling than Qwen.",
		Tags:        []string{"chat", "vision"},
		Backend:     "gpu",
		ModelID:     "gemma-3-4b-it-Q4_K_M",
		NGPULayers:  99,
		ContextSize: 8192,
		Files: []CatalogFile{
			{URL: hfGemma + "/gemma-3-4b-it-Q4_K_M.gguf", Filename: "gemma-3-4b-it-Q4_K_M.gguf", Role: "model", Size: 2489 << 20},
			{URL: hfGemma + "/mmproj-model-f16.gguf", Filename: "mmproj-model-f16.gguf", Role: "mmproj", Size: 851 << 20},
		},
	},
	{
		ID:          "qwen2.5-3b",
		Name:        "Qwen2.5 3B Instruct",
		Description: "Text-only but the fastest and most reliable for agentic / tool-calling workflows on the GPU.",
		Tags:        []string{"chat", "agentic", "text"},
		Backend:     "gpu",
		ModelID:     "qwen2.5-3b-instruct-q4_k_m",
		NGPULayers:  99,
		ContextSize: 8192,
		Files: []CatalogFile{
			{URL: hfQwen + "/qwen2.5-3b-instruct-q4_k_m.gguf", Filename: "qwen2.5-3b-instruct-q4_k_m.gguf", Role: "model", Size: 2104 << 20},
		},
	},
	{
		ID:          "llama-3.2-3b-cpu",
		Name:        "Llama 3.2 3B Instruct (CPU)",
		Description: "Best pick when running without a GPU: small, fast on CPU, good chat + tool calling. Loads fully into RAM.",
		Tags:        []string{"chat", "agentic", "text"},
		Backend:     "cpu",
		ModelID:     "Llama-3.2-3B-Instruct-Q4_K_M",
		NGPULayers:  0,
		ContextSize: 8192,
		Files: []CatalogFile{
			{URL: hfLlama32 + "/Llama-3.2-3B-Instruct-Q4_K_M.gguf", Filename: "Llama-3.2-3B-Instruct-Q4_K_M.gguf", Role: "model", Size: 2019 << 20},
		},
	},
	{
		ID:          "whisper-base",
		Name:        "Whisper Base (STT)",
		Description: "Local speech-to-text model. Highly accurate and fast multilingual transcription.",
		Tags:        []string{"audio", "stt"},
		Backend:     "cpu",
		ModelID:     "ggml-base.bin",
		Files: []CatalogFile{
			{URL: "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-base.bin", Filename: "ggml-base.bin", Role: "model", Size: 141 << 20},
		},
	},
	{
		ID:          "piper-davefx-es",
		Name:        "Piper DaveFX (TTS - Spanish)",
		Description: "Local text-to-speech voice in Spanish (DaveFX, medium quality). High-speed generation.",
		Tags:        []string{"audio", "tts"},
		Backend:     "cpu",
		ModelID:     "es_ES-davefx-medium.onnx",
		Files: []CatalogFile{
			{URL: "https://huggingface.co/rhasspy/piper-voices/resolve/v1.0.0/es/es_ES/davefx/medium/es_ES-davefx-medium.onnx", Filename: "es_ES-davefx-medium.onnx", Role: "model", Size: 63 << 20},
			{URL: "https://huggingface.co/rhasspy/piper-voices/resolve/v1.0.0/es/es_ES/davefx/medium/es_ES-davefx-medium.onnx.json", Filename: "es_ES-davefx-medium.onnx.json", Role: "config", Size: 15 << 10},
		},
	},
}

// Catalog returns the curated model list.
func Catalog() []CatalogEntry { return catalog }

// CatalogByID looks up a catalog entry.
func CatalogByID(id string) (CatalogEntry, bool) {
	for _, e := range catalog {
		if e.ID == id {
			return e, true
		}
	}
	return CatalogEntry{}, false
}

// CatalogSettingsFor returns the recommended ngpu_layers / context_size for a
// scanned model id (filename stem), so the engine can apply per-model defaults
// — notably ngpu_layers=0 for the CPU pick even when the engine's global GPU
// setting is higher.
func CatalogSettingsFor(modelID string) (ngl int, ctx int, ok bool) {
	for _, e := range catalog {
		if strings.EqualFold(e.ModelID, modelID) {
			return e.NGPULayers, e.ContextSize, true
		}
	}
	return 0, 0, false
}
