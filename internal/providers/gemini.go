package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aralde/operatorlm/internal/config"
	"github.com/aralde/operatorlm/internal/router"
)

type gemini struct {
	cfg config.Provider
}

func newGemini(cfg config.Provider) Provider {
	return &gemini{cfg: cfg}
}

func (g *gemini) Name() string     { return g.cfg.Name }
func (g *gemini) Type() string     { return g.cfg.Type }
func (g *gemini) Prefix() string   { return g.cfg.Prefix }
func (g *gemini) Models() []string { return g.cfg.Models }

type oaiMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type oaiChatReq struct {
	Model           string          `json:"model"`
	Messages        []oaiMessage    `json:"messages"`
	Stream          bool            `json:"stream,omitempty"`
	Temperature     *float64        `json:"temperature,omitempty"`
	TopP            *float64        `json:"top_p,omitempty"`
	MaxTokens       *int            `json:"max_tokens,omitempty"`
	ReasoningEffort string          `json:"reasoning_effort,omitempty"`
	Reasoning       *oaiReasoning   `json:"reasoning,omitempty"`
}

type oaiReasoning struct {
	Effort    string `json:"effort,omitempty"`
	MaxTokens *int   `json:"max_tokens,omitempty"`
	Exclude   *bool  `json:"exclude,omitempty"`
}

type gemPart struct {
	Text    string `json:"text"`
	Thought bool   `json:"thought,omitempty"`
}

type gemContent struct {
	Role  string    `json:"role"`
	Parts []gemPart `json:"parts"`
}

type gemThinkingConfig struct {
	IncludeThoughts bool `json:"includeThoughts,omitempty"`
	ThinkingBudget  *int `json:"thinkingBudget,omitempty"`
}

type gemGenerationConfig struct {
	Temperature     *float64           `json:"temperature,omitempty"`
	TopP            *float64           `json:"topP,omitempty"`
	MaxOutputTokens *int               `json:"maxOutputTokens,omitempty"`
	ThinkingConfig  *gemThinkingConfig `json:"thinkingConfig,omitempty"`
}

type gemReq struct {
	Contents          []gemContent         `json:"contents"`
	SystemInstruction *gemContent          `json:"systemInstruction,omitempty"`
	GenerationConfig  *gemGenerationConfig `json:"generationConfig,omitempty"`
}

type gemRespCandidate struct {
	Content      gemContent `json:"content"`
	FinishReason string     `json:"finishReason"`
}

type gemResp struct {
	Candidates []gemRespCandidate `json:"candidates"`
}

func (g *gemini) BuildRequest(ctx context.Context, kind Kind, body []byte, att router.Attempt, stream bool) (*http.Request, error) {
	if kind == KindImages {
		return nil, fmt.Errorf("image generation not implemented for gemini")
	}
	if kind == KindResponses {
		return nil, fmt.Errorf("responses API not implemented for gemini")
	}
	var req oaiChatReq
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("invalid json: %w", err)
	}

	gReq := translateOAItoGemini(&req)
	gBody, err := json.Marshal(gReq)
	if err != nil {
		return nil, err
	}

	apiKey, err := config.GetSecret(att.KeyRef)
	if err != nil {
		return nil, fmt.Errorf("missing api key %q: %w", att.KeyRef, err)
	}

	method := "generateContent"
	queryAlt := ""
	if stream {
		method = "streamGenerateContent"
		queryAlt = "&alt=sse"
	}
	url := fmt.Sprintf("%s/models/%s:%s?key=%s%s",
		strings.TrimRight(g.cfg.BaseURL, "/"), att.UpstreamModel, method, apiKey, queryAlt)

	upstream, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(gBody))
	if err != nil {
		return nil, err
	}
	upstream.Header.Set("Content-Type", "application/json")
	return upstream, nil
}

func (g *gemini) WriteResponse(w http.ResponseWriter, resp *http.Response, model string, stream bool) error {
	if stream {
		return g.streamSSE(w, resp.Body, model)
	}
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	var gr gemResp
	if err := json.Unmarshal(respBody, &gr); err != nil {
		return err
	}
	out := translateGemToOAI(&gr, model, false)
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(out)
}

func (g *gemini) streamSSE(w http.ResponseWriter, body io.Reader, model string) error {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, _ := w.(http.Flusher)

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "" {
			continue
		}
		var gr gemResp
		if err := json.Unmarshal([]byte(payload), &gr); err != nil {
			continue
		}
		chunk := translateGemToOAI(&gr, model, true)
		out, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\n", out)
		if flusher != nil {
			flusher.Flush()
		}
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
	return nil
}

func translateOAItoGemini(req *oaiChatReq) *gemReq {
	out := &gemReq{}
	for _, m := range req.Messages {
		text := contentToString(m.Content)
		if m.Role == "system" {
			out.SystemInstruction = &gemContent{Role: "user", Parts: []gemPart{{Text: text}}}
			continue
		}
		role := "user"
		if m.Role == "assistant" {
			role = "model"
		}
		out.Contents = append(out.Contents, gemContent{Role: role, Parts: []gemPart{{Text: text}}})
	}

	thinking := buildGeminiThinkingConfig(req)
	if req.Temperature != nil || req.TopP != nil || req.MaxTokens != nil || thinking != nil {
		out.GenerationConfig = &gemGenerationConfig{
			Temperature:     req.Temperature,
			TopP:            req.TopP,
			MaxOutputTokens: req.MaxTokens,
			ThinkingConfig:  thinking,
		}
	}
	return out
}

// buildGeminiThinkingConfig maps OpenAI/OpenRouter-style reasoning hints onto
// Gemini's generationConfig.thinkingConfig. Returns nil when the caller did
// not request thinking — letting Gemini apply its per-model default.
//
// Mapping:
//   - reasoning_effort / reasoning.effort: "none"→budget 0 (off);
//     "low"→1024, "medium"→8192, "high"→24576; unknown values fall back to
//     includeThoughts only.
//   - reasoning.max_tokens: used verbatim as thinkingBudget when present
//     (overrides the effort-derived value).
//   - reasoning.exclude=true: budget still applied but includeThoughts=false
//     so the model thinks silently.
func buildGeminiThinkingConfig(req *oaiChatReq) *gemThinkingConfig {
	effort := strings.ToLower(strings.TrimSpace(req.ReasoningEffort))
	var maxTokens *int
	exclude := false
	if req.Reasoning != nil {
		if effort == "" {
			effort = strings.ToLower(strings.TrimSpace(req.Reasoning.Effort))
		}
		maxTokens = req.Reasoning.MaxTokens
		if req.Reasoning.Exclude != nil {
			exclude = *req.Reasoning.Exclude
		}
	}
	if effort == "" && maxTokens == nil {
		return nil
	}

	cfg := &gemThinkingConfig{IncludeThoughts: !exclude}
	switch effort {
	case "none", "off", "disabled":
		zero := 0
		cfg.ThinkingBudget = &zero
		cfg.IncludeThoughts = false
	case "low":
		b := 1024
		cfg.ThinkingBudget = &b
	case "medium":
		b := 8192
		cfg.ThinkingBudget = &b
	case "high":
		b := 24576
		cfg.ThinkingBudget = &b
	}
	if maxTokens != nil {
		cfg.ThinkingBudget = maxTokens
	}
	return cfg
}

func contentToString(c any) string {
	switch v := c.(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					b.WriteString(t)
				}
			}
		}
		return b.String()
	}
	return ""
}

func translateGemToOAI(g *gemResp, model string, streaming bool) map[string]any {
	var content, reasoning string
	finish := ""
	if len(g.Candidates) > 0 {
		c := g.Candidates[0]
		for _, p := range c.Content.Parts {
			if p.Thought {
				reasoning += p.Text
			} else {
				content += p.Text
			}
		}
		finish = strings.ToLower(c.FinishReason)
	}

	choiceKey := "message"
	objectKind := "chat.completion"
	if streaming {
		choiceKey = "delta"
		objectKind = "chat.completion.chunk"
	}

	payload := map[string]any{
		"role":    "assistant",
		"content": content,
	}
	if reasoning != "" {
		// Use reasoning_content — the de-facto field used by DeepSeek, xAI Grok,
		// Mistral, NVIDIA NIM. Mirror it under reasoning too so OpenRouter-style
		// clients pick it up without changes.
		payload["reasoning_content"] = reasoning
		payload["reasoning"] = reasoning
	}
	choice := map[string]any{
		"index":    0,
		choiceKey: payload,
	}
	if finish != "" && finish != "finish_reason_unspecified" {
		choice["finish_reason"] = mapFinishReason(finish)
	}

	return map[string]any{
		"id":      fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
		"object":  objectKind,
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []any{choice},
	}
}

func mapFinishReason(g string) string {
	switch g {
	case "stop":
		return "stop"
	case "max_tokens":
		return "length"
	case "safety", "recitation":
		return "content_filter"
	default:
		return "stop"
	}
}
