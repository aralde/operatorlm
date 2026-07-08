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
	engines map[string]*LocalEngine // maps provider Name to its LocalEngine instance
}

func NewRegistry(cfg *config.Config) *Registry {
	r := &Registry{cfg: cfg, engines: make(map[string]*LocalEngine)}
	r.Reload()
	return r
}

func (r *Registry) Reload() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.engines == nil {
		r.engines = make(map[string]*LocalEngine)
	}

	newByKey := make(map[string]Provider)
	activeEngines := make(map[string]bool)

	for _, p := range r.cfg.Snapshot() {
		if p.Disabled {
			continue
		}
		if p.Type == "llama-server" {
			lm := config.LocalModelsConfig{
				Enabled:         true,
				ModelsDir:       p.ModelsDir,
				LlamaServerPath: p.LlamaServerPath,
				Prefix:          p.Prefix,
				Port:            p.Port,
				ContextSize:     p.ContextSize,
				NGPULayers:      p.NGPULayers,
				ExtraArgs:       p.ExtraArgs,
			}
			engine := r.engines[p.Name]
			if engine == nil {
				engine = NewLocalEngine(lm)
				r.engines[p.Name] = engine
			} else {
				engine.Reconfigure(lm)
			}
			engine.Refresh()
			activeEngines[p.Name] = true
			newByKey[p.Name] = newLocalProviderWithConfig(p, engine)
		} else {
			if prov := build(p); prov != nil {
				newByKey[p.Name] = prov
			}
		}
	}

	// Legacy global local models config support (if enabled)
	lm := r.cfg.GetLocalModels()
	engine := r.engines["_legacy_local"]
	if engine == nil {
		engine = NewLocalEngine(lm)
		r.engines["_legacy_local"] = engine
	} else {
		engine.Reconfigure(lm)
	}
	activeEngines["_legacy_local"] = true

	if lm.Enabled {
		engine.Refresh()
		newByKey[localProviderName] = newLocalProvider(lm, engine)
	}

	if lm.WhisperEnabled {
		newByKey["whisper-local"] = newOpenAILike(config.Provider{
			Name:    "whisper-local",
			Type:    "openai",
			BaseURL: "http://127.0.0.1:" + strconv.Itoa(lm.WhisperPort),
			Models:  []string{"whisper-1"},
		}, nil)
	}
	if lm.PiperEnabled {
		newByKey["piper-local"] = newOpenAILike(config.Provider{
			Name:    "piper-local",
			Type:    "openai",
			BaseURL: "http://127.0.0.1:" + strconv.Itoa(lm.PiperPort),
			Models:  []string{"tts-1"},
		}, nil)
	}

	// Stop and clean up any engines that are no longer active/present
	for name, engine := range r.engines {
		if !activeEngines[name] {
			engine.Stop()
			delete(r.engines, name)
		}
	}

	r.byKey = newByKey
}

// LocalEngine returns the legacy built-in engine, or nil when legacy local models are disabled.
func (r *Registry) LocalEngine() *LocalEngine {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.engines["_legacy_local"]
}

// RefreshLocalEngines rescans the models directory of every active local engine.
// Call after downloading new model files so they become available without a
// full reload.
func (r *Registry) RefreshLocalEngines() {
	r.mu.RLock()
	engines := make([]*LocalEngine, 0, len(r.engines))
	for _, e := range r.engines {
		engines = append(engines, e)
	}
	r.mu.RUnlock()
	for _, e := range engines {
		e.Refresh()
	}
}

// LocalModelsDir returns the models directory of the first configured
// llama-server provider, falling back to the legacy local-models config. Used
// as the download target for catalog models. Empty if none is configured.
func (r *Registry) LocalModelsDir() string {
	for _, p := range r.cfg.Snapshot() {
		if p.Type == "llama-server" && p.ModelsDir != "" {
			return p.ModelsDir
		}
	}
	return r.cfg.GetLocalModels().ModelsDir
}

// Shutdown stops all running local engines. Call on process exit.
func (r *Registry) Shutdown() {
	r.mu.Lock()
	engines := r.engines
	r.engines = nil
	r.mu.Unlock()
	for _, engine := range engines {
		engine.Stop()
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
