package providers

import (
	"strconv"
	"sync"

	"github.com/aralde/operatorlm/internal/config"
)

type Registry struct {
	cfg *config.Config

	mu      sync.RWMutex
	byKey   map[string]Provider
	builtin map[string]bool // names synthesized at runtime, not from [[providers]]
	engine  *LocalEngine    // the single built-in llama.cpp engine
}

func NewRegistry(cfg *config.Config) *Registry {
	r := &Registry{cfg: cfg}
	r.Reload()
	return r
}

func (r *Registry) Reload() {
	r.mu.Lock()
	defer r.mu.Unlock()

	newByKey := make(map[string]Provider)
	newBuiltin := make(map[string]bool)

	for _, p := range r.cfg.Snapshot() {
		if p.Disabled {
			continue
		}
		if prov := build(p); prov != nil {
			newByKey[p.Name] = prov
		}
	}

	// Built-in local engine ([local_models] is its single source of truth).
	// Reconfigure also starts/stops the whisper and piper sidecars according
	// to their own enabled flags, so it must run even on first load.
	lm := r.cfg.GetLocalModels()
	if r.engine == nil {
		r.engine = NewLocalEngine(lm)
	}
	r.engine.Reconfigure(lm)
	if lm.Enabled {
		r.engine.Refresh()
		newByKey[localProviderName] = newLocalProvider(lm, r.engine)
		newBuiltin[localProviderName] = true
	} else {
		r.engine.StopChat()
	}

	if lm.WhisperEnabled {
		// whisper.cpp's server speaks multipart like OpenAI but serves it on
		// /inference and returns the same {"text": ...} JSON shape.
		newByKey["whisper-local"] = &openAILike{
			cfg: config.Provider{
				Name:    "whisper-local",
				Type:    "openai",
				BaseURL: "http://127.0.0.1:" + strconv.Itoa(lm.WhisperPort),
				Models:  []string{"whisper-1"},
			},
			pathOverrides: map[Kind]string{
				KindTranscriptions: "/inference",
				KindTranslations:   "/inference",
			},
			// OpenAI transcribes in the source language; whisper.cpp defaults
			// to language=en which translates instead. Only applied when the
			// client didn't send its own language field.
			defaultFormFields: map[string]string{"language": "auto"},
		}
		newBuiltin["whisper-local"] = true
	}
	if lm.PiperEnabled {
		newByKey["piper-local"] = newOpenAILike(config.Provider{
			Name:    "piper-local",
			Type:    "openai",
			BaseURL: "http://127.0.0.1:" + strconv.Itoa(lm.PiperPort) + "/v1",
			Models:  []string{"tts-1"},
		}, nil)
		newBuiltin["piper-local"] = true
	}

	r.byKey = newByKey
	r.builtin = newBuiltin
}

// IsBuiltin reports whether name is a runtime-synthesized provider (the local
// engine or an audio sidecar) rather than a [[providers]] entry. Builtins are
// configured in the Local models tab, not through the provider CRUD.
func (r *Registry) IsBuiltin(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.builtin[name]
}

// LocalEngine returns the built-in engine (never nil after NewRegistry).
func (r *Registry) LocalEngine() *LocalEngine {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.engine
}

// RefreshLocalEngines rescans the models directory. Call after downloading
// new model files so they become available without a full reload.
func (r *Registry) RefreshLocalEngines() {
	if e := r.LocalEngine(); e != nil {
		e.Refresh()
	}
}

// LocalModelsDir returns the models directory used as the download target for
// catalog models. Empty if not configured.
func (r *Registry) LocalModelsDir() string {
	return r.cfg.GetLocalModels().ModelsDir
}

// Shutdown stops the local engine and its sidecars. Call on process exit.
func (r *Registry) Shutdown() {
	if e := r.LocalEngine(); e != nil {
		e.Stop()
	}
}

func (r *Registry) ByName(name string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.byKey[name]
	return p, ok
}

func (r *Registry) All() []Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Provider, 0, len(r.byKey))
	for _, p := range r.byKey {
		out = append(out, p)
	}
	return out
}

func build(p config.Provider) Provider {
	switch p.Type {
	case "openai":
		return newOpenAILike(p, nil)
	case "groq":
		return newOpenAILike(p, nil)
	case "opencode-zen":
		return newOpenAILike(p, nil)
	case "nvidia-nim":
		return newOpenAILike(p, nil)
	case "mistral":
		return newOpenAILike(p, nil)
	case "bedrock":
		return newOpenAILike(p, nil)
	case "custom":
		return newOpenAILike(p, nil)
	case "azure-openai":
		return newAzureOpenAI(p)
	case "openrouter":
		return newOpenRouter(p)
	case "gemini":
		return newGemini(p)
	case "chatgpt-codex":
		return newChatGPTCodex(p)
	case "anthropic":
		return newAnthropic(p)
	case "antigravity":
		return newAntigravity(p)
	default:
		return nil
	}
}
