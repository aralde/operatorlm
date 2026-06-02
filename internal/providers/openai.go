package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/aralde/operatorlm/internal/config"
	"github.com/aralde/operatorlm/internal/router"
)

type openAILike struct {
	cfg          config.Provider
	extraHeaders map[string]string
}

func newOpenAILike(cfg config.Provider, extraHeaders map[string]string) Provider {
	return &openAILike{cfg: cfg, extraHeaders: extraHeaders}
}

func (o *openAILike) Name() string     { return o.cfg.Name }
func (o *openAILike) Type() string     { return o.cfg.Type }
func (o *openAILike) Prefix() string   { return o.cfg.Prefix }
func (o *openAILike) Models() []string { return o.cfg.Models }

func (o *openAILike) BuildRequest(ctx context.Context, kind Kind, body []byte, att router.Attempt, _ bool) (*http.Request, error) {
	path := "/chat/completions"
	switch kind {
	case KindImages:
		path = "/images/generations"
	case KindResponses:
		path = "/responses"
	case KindEmbeddings:
		path = "/embeddings"
	}
	url := strings.TrimRight(o.cfg.BaseURL, "/") + path

	rewritten := rewriteModel(body, att.UpstreamModel)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(rewritten))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	if att.KeyRef != "" {
		apiKey, err := config.GetSecret(att.KeyRef)
		if err != nil {
			return nil, fmt.Errorf("missing api key %q: %w", att.KeyRef, err)
		}
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
	}
	for k, v := range o.extraHeaders {
		req.Header.Set(k, v)
	}
	return req, nil
}

func (o *openAILike) WriteResponse(w http.ResponseWriter, resp *http.Response, _ Kind, _ string, _ bool) error {
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 4*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return werr
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return rerr
		}
	}
}

// ClampOutputTokens caps any of max_tokens / max_completion_tokens /
// max_output_tokens in the request body to `max`. No-op when max <= 0 or
// the field is absent / already below the cap. Returns the original bytes
// on parse failure so we never break a request just to clamp it.
func ClampOutputTokens(body []byte, max int) []byte {
	if max <= 0 {
		return body
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}
	changed := false
	for _, k := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
		v, ok := m[k]
		if !ok {
			continue
		}
		n, ok := numericTokens(v)
		if !ok {
			continue
		}
		if n > max {
			m[k] = max
			changed = true
		}
	}
	if !changed {
		return body
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

func numericTokens(v any) (int, bool) {
	switch t := v.(type) {
	case float64:
		return int(t), true
	case int:
		return t, true
	case json.Number:
		n, err := t.Int64()
		if err == nil {
			return int(n), true
		}
	}
	return 0, false
}

func rewriteModel(body []byte, model string) []byte {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}
	if model != "" {
		m["model"] = model
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}
