package providers

import (
	"context"
	"net/http"

	"github.com/aralde/operatorlm/internal/config"
	"github.com/aralde/operatorlm/internal/router"
)

// localProviderName is the fixed registry/router name for the built-in engine.
const localProviderName = "local"

// localProvider exposes the LocalEngine through the Provider interface. It is
// OpenAI-compatible (llama-server speaks the OpenAI API), so request building
// and response writing reuse openAILike; the only twist is that the upstream
// base URL is resolved per-request by spawning/swapping the model first.
type localProvider struct {
	name   string
	pType  string
	prefix string
	engine *LocalEngine
}

func newLocalProvider(cfg config.LocalModelsConfig, engine *LocalEngine) Provider {
	prefix := cfg.Prefix
	if prefix == "" {
		prefix = "local/"
	}
	return &localProvider{name: localProviderName, pType: "llamacpp", prefix: prefix, engine: engine}
}

func newLocalProviderWithConfig(p config.Provider, engine *LocalEngine) Provider {
	prefix := p.Prefix
	if prefix == "" {
		prefix = "local/"
	}
	return &localProvider{name: p.Name, pType: p.Type, prefix: prefix, engine: engine}
}

func (p *localProvider) Name() string     { return p.name }
func (p *localProvider) Type() string     { return p.pType }
func (p *localProvider) Prefix() string   { return p.prefix }
func (p *localProvider) Models() []string { return p.engine.ModelIDs() }

func (p *localProvider) BuildRequest(ctx context.Context, kind Kind, body []byte, att router.Attempt, stream bool) (*http.Request, error) {
	baseURL, err := p.engine.EnsureRunning(ctx, att.UpstreamModel)
	if err != nil {
		return nil, err
	}
	oa := &openAILike{cfg: config.Provider{
		Name:    p.name,
		Type:    p.pType,
		BaseURL: baseURL,
		Prefix:  p.prefix,
	}}
	att.KeyRef = "" // llama-server doesn't use API keys
	return oa.BuildRequest(ctx, kind, body, att, stream)
}

func (p *localProvider) WriteResponse(w http.ResponseWriter, resp *http.Response, kind Kind, model string, stream bool) error {
	oa := &openAILike{cfg: config.Provider{Name: p.name, Type: p.pType}}
	return oa.WriteResponse(w, resp, kind, model, stream)
}
