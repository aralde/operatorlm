package providers

import (
	"sync"

	"github.com/aralde/operatorlm/internal/config"
)

type Registry struct {
	cfg *config.Config

	mu    sync.RWMutex
	byKey map[string]Provider // key = provider.Name
}

func NewRegistry(cfg *config.Config) *Registry {
	r := &Registry{cfg: cfg}
	r.Reload()
	return r
}

func (r *Registry) Reload() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byKey = make(map[string]Provider)
	for _, p := range r.cfg.Snapshot() {
		if p.Disabled {
			continue
		}
		if prov := build(p); prov != nil {
			r.byKey[p.Name] = prov
		}
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
		extra := map[string]string{
			"HTTP-Referer": "https://github.com/aralde/operatorlm",
			"X-Title":      "OperatorLM",
		}
		return newOpenAILike(p, extra)
	case "gemini":
		return newGemini(p)
	case "chatgpt-codex":
		return newChatGPTCodex(p)
	case "anthropic":
		return newAnthropic(p)
	default:
		return nil
	}
}
