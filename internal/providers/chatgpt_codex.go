package providers

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aralde/operatorlm/internal/config"
	"github.com/aralde/operatorlm/internal/router"
)

type chatgptCodex struct {
	cfg config.Provider

	sessionID string

	mu sync.Mutex // serializes refreshes per provider instance
}

func newChatGPTCodex(cfg config.Provider) Provider {
	return &chatgptCodex{cfg: cfg, sessionID: randomUUID()}
}

func (c *chatgptCodex) Name() string     { return c.cfg.Name }
func (c *chatgptCodex) Type() string     { return c.cfg.Type }
func (c *chatgptCodex) Prefix() string   { return c.cfg.Prefix }
func (c *chatgptCodex) Models() []string { return c.cfg.Models }

func (c *chatgptCodex) BuildRequest(ctx context.Context, kind Kind, body []byte, att router.Attempt, _ bool) (*http.Request, error) {
	switch kind {
	case KindResponses:
		// body is already in Responses shape.
	case KindChat:
		// Translate chat/completions into a Responses body; transformCodexBody
		// below then applies the codex-endpoint constraints.
		body = chatToResponsesBody(body)
	default:
		return nil, fmt.Errorf("chatgpt-codex only supports the /v1/chat/completions and /v1/responses endpoints")
	}

	tokens, err := c.loadTokens(att.KeyRef)
	if err != nil {
		return nil, err
	}
	if time.Now().After(tokens.ExpiresAt.Add(-60 * time.Second)) {
		fresh, err := RefreshChatGPTTokens(ctx, tokens.RefreshToken)
		if err != nil {
			return nil, fmt.Errorf("chatgpt token refresh failed: %w", err)
		}
		if fresh.AccountID == "" {
			fresh.AccountID = tokens.AccountID
		}
		if err := c.saveTokens(att.KeyRef, fresh); err != nil {
			return nil, err
		}
		tokens = fresh
	}

	rewritten := transformCodexBody(body, att.UpstreamModel)
	url := chatgptAPIBase + "/responses"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(rewritten))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	if tokens.AccountID != "" {
		req.Header.Set("chatgpt-account-id", tokens.AccountID)
	}
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("originator", chatgptOriginator)
	req.Header.Set("session_id", c.sessionID)
	req.Header.Set("conversation_id", randomUUID())
	return req, nil
}

func (c *chatgptCodex) WriteResponse(w http.ResponseWriter, resp *http.Response, kind Kind, model string, stream bool) error {
	if kind == KindChat {
		return writeCodexChatResponse(w, resp, model, stream)
	}
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

func (c *chatgptCodex) loadTokens(ref string) (ChatGPTTokens, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	raw, err := config.GetSecret(ref)
	if err != nil {
		return ChatGPTTokens{}, fmt.Errorf("chatgpt tokens not found for %q (run login flow): %w", ref, err)
	}
	var t ChatGPTTokens
	if err := json.Unmarshal([]byte(raw), &t); err != nil {
		return ChatGPTTokens{}, fmt.Errorf("malformed chatgpt tokens at %q: %w", ref, err)
	}
	return t, nil
}

func (c *chatgptCodex) saveTokens(ref string, t ChatGPTTokens) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return config.SetSecret(ref, string(data))
}

// transformCodexBody adapts an OpenAI-shaped /responses body to the constraints
// of the chatgpt.com/backend-api/codex endpoint. Reproduces what the Codex CLI
// and the opencode-openai-codex-auth plugin do:
//   - force store=false, stream=true
//   - require instructions (inject a default if missing)
//   - require include=["reasoning.encrypted_content"]
//   - require reasoning.{effort,summary} with values valid for the model
//   - require text.verbosity (default "medium")
//   - drop unsupported fields (max_output_tokens, max_completion_tokens)
//   - strip ids and item_reference items from input[]
func transformCodexBody(body []byte, upstreamModel string) []byte {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}

	if upstreamModel != "" {
		m["model"] = upstreamModel
	}

	m["store"] = false
	m["stream"] = true

	if v, ok := m["instructions"].(string); !ok || v == "" {
		m["instructions"] = defaultCodexInstructions
	}

	delete(m, "max_output_tokens")
	delete(m, "max_completion_tokens")

	if s, ok := m["input"].(string); ok {
		m["input"] = []any{
			map[string]any{
				"type": "message",
				"role": "user",
				"content": []any{
					map[string]any{"type": "input_text", "text": s},
				},
			},
		}
	}

	if items, ok := m["input"].([]any); ok {
		filtered := make([]any, 0, len(items))
		for _, it := range items {
			obj, ok := it.(map[string]any)
			if !ok {
				filtered = append(filtered, it)
				continue
			}
			if t, _ := obj["type"].(string); t == "item_reference" {
				continue
			}
			delete(obj, "id")
			filtered = append(filtered, obj)
		}
		m["input"] = filtered
	}

	modelName, _ := m["model"].(string)
	effort, summary := codexReasoningDefaults(modelName)
	if r, ok := m["reasoning"].(map[string]any); ok {
		if _, has := r["effort"]; !has {
			r["effort"] = effort
		} else {
			r["effort"] = sanitizeReasoningEffort(modelName, fmt.Sprint(r["effort"]))
		}
		if _, has := r["summary"]; !has {
			r["summary"] = summary
		}
		m["reasoning"] = r
	} else {
		m["reasoning"] = map[string]any{"effort": effort, "summary": summary}
	}

	if t, ok := m["text"].(map[string]any); ok {
		if _, has := t["verbosity"]; !has {
			t["verbosity"] = "medium"
		}
		m["text"] = t
	} else {
		m["text"] = map[string]any{"verbosity": "medium"}
	}

	if inc, ok := m["include"].([]any); ok {
		seen := false
		for _, v := range inc {
			if s, _ := v.(string); s == "reasoning.encrypted_content" {
				seen = true
				break
			}
		}
		if !seen {
			inc = append(inc, "reasoning.encrypted_content")
		}
		m["include"] = inc
	} else {
		m["include"] = []any{"reasoning.encrypted_content"}
	}

	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

const defaultCodexInstructions = "You are a helpful assistant. Respond concisely and accurately."

func codexReasoningDefaults(model string) (effort, summary string) {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "codex-mini") || strings.Contains(m, "codex_mini"):
		return "medium", "auto"
	case strings.Contains(m, "gpt-5.2") || strings.Contains(m, "codex-max"):
		return "high", "auto"
	default:
		return "medium", "auto"
	}
}

func sanitizeReasoningEffort(model, raw string) string {
	e := strings.ToLower(strings.TrimSpace(raw))
	if e == "minimal" {
		e = "low"
	}
	m := strings.ToLower(model)
	isCodexMini := strings.Contains(m, "codex-mini") || strings.Contains(m, "codex_mini")
	isCodex := strings.Contains(m, "codex") && !isCodexMini
	supportsXhigh := strings.Contains(m, "gpt-5.2") || strings.Contains(m, "codex-max")
	supportsNone := !isCodex && !isCodexMini && (strings.Contains(m, "gpt-5.1") || strings.Contains(m, "gpt-5.2"))

	if isCodexMini {
		if e != "high" && e != "medium" {
			return "medium"
		}
		return e
	}
	if e == "xhigh" && !supportsXhigh {
		return "high"
	}
	if e == "none" && !supportsNone {
		return "low"
	}
	switch e {
	case "none", "low", "medium", "high", "xhigh":
		return e
	}
	return "medium"
}

// randomUUID returns a v4-shaped UUID string. Good enough for opaque IDs.
func randomUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
