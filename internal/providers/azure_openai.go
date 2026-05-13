package providers

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/aralde/operatorlm/internal/config"
	"github.com/aralde/operatorlm/internal/router"
)

// defaultAzureApiVersion is used when the provider does not specify one.
// Pinned to a stable GA api-version for chat/responses.
const defaultAzureApiVersion = "2024-10-21"

// azureOpenAI talks to Azure OpenAI deployments. The URL shape is
//
//	{base}/openai/deployments/{deployment}/{path}?api-version={ver}
//
// where the OpenAI-shaped `model` field is reinterpreted as the deployment
// name. Auth uses the `api-key` header instead of `Authorization: Bearer`.
type azureOpenAI struct {
	openAILike
}

func newAzureOpenAI(cfg config.Provider) Provider {
	return &azureOpenAI{openAILike: openAILike{cfg: cfg}}
}

func (a *azureOpenAI) BuildRequest(ctx context.Context, kind Kind, body []byte, att router.Attempt, _ bool) (*http.Request, error) {
	deployment := att.UpstreamModel
	if deployment == "" {
		return nil, fmt.Errorf("azure-openai: upstream model (deployment name) is required")
	}

	var suffix string
	switch kind {
	case KindImages:
		suffix = "/images/generations"
	case KindResponses:
		suffix = "/responses"
	default:
		suffix = "/chat/completions"
	}
	base := strings.TrimRight(a.cfg.BaseURL, "/")
	apiVersion := a.cfg.ApiVersion
	if apiVersion == "" {
		apiVersion = defaultAzureApiVersion
	}
	url := base + "/openai/deployments/" + deployment + suffix + "?api-version=" + apiVersion

	rewritten := rewriteModel(body, deployment)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(rewritten))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	apiKey, err := config.GetSecret(att.KeyRef)
	if err != nil {
		return nil, fmt.Errorf("missing api key %q: %w", att.KeyRef, err)
	}
	req.Header.Set("api-key", apiKey)
	return req, nil
}
