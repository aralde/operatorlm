package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"strings"

	"github.com/aralde/operatorlm/internal/config"
	"github.com/aralde/operatorlm/internal/router"
)

type openAILike struct {
	cfg          config.Provider
	extraHeaders map[string]string
	// bodyTransform, when set, rewrites the JSON request body after the model
	// field is rewritten. Used by OpenRouter to inject provider-routing prefs.
	bodyTransform func([]byte) []byte
	// pathOverrides remaps the upstream path for a request kind. Used by the
	// whisper.cpp sidecar, whose transcription endpoint is /inference.
	pathOverrides map[Kind]string
}

func newOpenAILike(cfg config.Provider, extraHeaders map[string]string) Provider {
	return &openAILike{cfg: cfg, extraHeaders: extraHeaders}
}

// newOpenRouter builds an OpenAI-compatible provider for OpenRouter, adding the
// attribution headers plus a body transform that injects provider-routing
// preferences (see injectOpenRouterProvider).
func newOpenRouter(cfg config.Provider) Provider {
	return &openAILike{
		cfg: cfg,
		extraHeaders: map[string]string{
			"HTTP-Referer": "https://github.com/aralde/operatorlm",
			"X-Title":      "OperatorLM",
		},
		bodyTransform: injectOpenRouterProvider,
	}
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
	case KindSpeech:
		path = "/audio/speech"
	case KindTranscriptions:
		path = "/audio/transcriptions"
	case KindTranslations:
		path = "/audio/translations"
	}
	if override, ok := o.pathOverrides[kind]; ok {
		path = override
	}
	url := strings.TrimRight(o.cfg.BaseURL, "/") + path

	var rewritten []byte
	var contentType string = "application/json"
	var err error

	if kind == KindTranscriptions || kind == KindTranslations {
		origContentType, err := getMultipartContentType(body)
		if err != nil {
			return nil, fmt.Errorf("detect multipart content type: %w", err)
		}
		rewritten, contentType, err = rewriteMultipartModel(body, origContentType, att.UpstreamModel)
		if err != nil {
			return nil, fmt.Errorf("rewrite multipart model: %w", err)
		}
	} else {
		rewritten = rewriteModel(body, att.UpstreamModel)
		if o.bodyTransform != nil {
			rewritten = o.bodyTransform(rewritten)
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(rewritten))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)

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

// injectOpenRouterProvider adds OpenRouter provider-routing preferences to the
// request body so flaky upstreams can be excluded. The ignore list comes from
// OPERATORLM_OPENROUTER_IGNORE (comma-separated); when unset it is a no-op.
//
// NOTE: do not blanket-ignore a backend for ":free" models — those are usually
// served by a single provider (e.g. gpt-oss-120b:free is OpenInference-only), so
// ignoring it leaves zero providers and OpenRouter returns 404 "All providers
// have been ignored". This knob is meant for paid models with multiple backends.
// A client-supplied "provider" object is respected and left untouched.
func injectOpenRouterProvider(body []byte) []byte {
	raw := os.Getenv("OPERATORLM_OPENROUTER_IGNORE")
	var ignore []string
	for _, s := range strings.Split(raw, ",") {
		if s = strings.TrimSpace(s); s != "" {
			ignore = append(ignore, s)
		}
	}
	if len(ignore) == 0 {
		return body
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}
	if _, exists := m["provider"]; exists {
		return body // respect client-supplied routing
	}
	m["provider"] = map[string]any{"ignore": ignore}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
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

func rewriteMultipartModel(body []byte, contentType string, newModel string) ([]byte, string, error) {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, "", err
	}
	boundary := params["boundary"]
	if boundary == "" {
		return nil, "", fmt.Errorf("no boundary in content-type")
	}

	mr := multipart.NewReader(bytes.NewReader(body), boundary)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", err
		}

		pw, err := mw.CreatePart(part.Header)
		if err != nil {
			return nil, "", err
		}

		if part.FormName() == "model" && newModel != "" {
			_, _ = pw.Write([]byte(newModel))
		} else {
			_, _ = io.Copy(pw, part)
		}
	}
	_ = mw.Close()
	return buf.Bytes(), mw.FormDataContentType(), nil
}

func getMultipartContentType(body []byte) (string, error) {
	if !bytes.HasPrefix(body, []byte("--")) {
		return "", fmt.Errorf("body is not a multipart form")
	}
	lineEnd := bytes.Index(body, []byte("\r\n"))
	if lineEnd == -1 {
		lineEnd = bytes.Index(body, []byte("\n"))
	}
	if lineEnd == -1 {
		return "", fmt.Errorf("invalid multipart form")
	}
	boundary := string(body[2:lineEnd])
	boundary = strings.TrimSuffix(boundary, "--")
	return "multipart/form-data; boundary=" + boundary, nil
}
