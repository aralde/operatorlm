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

// Anthropic types
type AnthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string or []AnthropicContentBlock
}

type AnthropicContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type AnthropicRequest struct {
	Model         string             `json:"model"`
	Messages      []AnthropicMessage `json:"messages"`
	System        any                `json:"system,omitempty"` // string or content block array
	MaxTokens     int                `json:"max_tokens"`
	Stream        bool               `json:"stream,omitempty"`
	Temperature   *float64           `json:"temperature,omitempty"`
	TopP          *float64           `json:"top_p,omitempty"`
	StopSequences []string           `json:"stop_sequences,omitempty"`
}

type AnthropicResponse struct {
	ID           string                  `json:"id"`
	Type         string                  `json:"type"`
	Role         string                  `json:"role"`
	Model        string                  `json:"model"`
	Content      []AnthropicContentBlock `json:"content"`
	StopReason   string                  `json:"stop_reason,omitempty"`
	StopSequence string                  `json:"stop_sequence,omitempty"`
	Usage        AnthropicUsage          `json:"usage"`
}

type AnthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Anthropic provider implementation
type anthropic struct {
	cfg config.Provider
}

func newAnthropic(cfg config.Provider) Provider {
	return &anthropic{cfg: cfg}
}

func (a *anthropic) Name() string     { return a.cfg.Name }
func (a *anthropic) Type() string     { return a.cfg.Type }
func (a *anthropic) Prefix() string   { return a.cfg.Prefix }
func (a *anthropic) Models() []string { return a.cfg.Models }

func (a *anthropic) BuildRequest(ctx context.Context, kind Kind, body []byte, att router.Attempt, stream bool) (*http.Request, error) {
	if kind != KindChat {
		return nil, fmt.Errorf("anthropic provider only supports chat completions")
	}

	// 1. Unmarshal OpenAI request
	var oaiReq oaiChatReq
	if err := json.Unmarshal(body, &oaiReq); err != nil {
		return nil, fmt.Errorf("invalid json: %w", err)
	}

	// 2. Translate to Anthropic request
	antReq := translateOAIToAnthropic(&oaiReq)
	antReq.Model = att.UpstreamModel

	antBody, err := json.Marshal(antReq)
	if err != nil {
		return nil, err
	}

	// 3. Prepare HTTP Request
	baseURL := strings.TrimRight(a.cfg.BaseURL, "/")
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	url := baseURL + "/v1/messages"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(antBody))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Anthropic-Version", "2023-06-01")

	if att.KeyRef != "" {
		apiKey, err := config.GetSecret(att.KeyRef)
		if err != nil {
			return nil, fmt.Errorf("missing api key %q: %w", att.KeyRef, err)
		}
		if apiKey != "" {
			req.Header.Set("x-api-key", apiKey)
		}
	}

	return req, nil
}

func (a *anthropic) WriteResponse(w http.ResponseWriter, resp *http.Response, _ Kind, model string, stream bool) error {
	if stream {
		return translateAnthropicStreamToOAI(w, resp.Body, model)
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode >= 400 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(respBody)
		return nil
	}

	var antResp AnthropicResponse
	if err := json.Unmarshal(respBody, &antResp); err != nil {
		return err
	}

	oaiResp := translateAnthropicToOAIResponse(&antResp, model)
	oaiBody, err := json.Marshal(oaiResp)
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, err = w.Write(oaiBody)
	return err
}

// Request and Response Translation Functions for Anthropic Provider

func translateOAIToAnthropic(oaiReq *oaiChatReq) *AnthropicRequest {
	antReq := &AnthropicRequest{
		Stream:      oaiReq.Stream,
		Temperature: oaiReq.Temperature,
		TopP:        oaiReq.TopP,
	}

	if oaiReq.MaxTokens != nil {
		antReq.MaxTokens = *oaiReq.MaxTokens
	} else {
		antReq.MaxTokens = 4096 // default fallback required by Anthropic
	}

	var systemParts []string
	for _, m := range oaiReq.Messages {
		if m.Role == "system" {
			systemParts = append(systemParts, contentToString(m.Content))
		} else {
			role := m.Role
			if role == "system" {
				role = "user"
			}
			antReq.Messages = append(antReq.Messages, AnthropicMessage{
				Role:    role,
				Content: m.Content,
			})
		}
	}

	if len(systemParts) > 0 {
		antReq.System = strings.Join(systemParts, "\n\n")
	}

	return antReq
}

func translateAnthropicToOAIResponse(antResp *AnthropicResponse, model string) map[string]any {
	var text string
	for _, part := range antResp.Content {
		if part.Type == "text" {
			text += part.Text
		}
	}

	finishReason := "stop"
	if antResp.StopReason != "" {
		switch antResp.StopReason {
		case "end_turn":
			finishReason = "stop"
		case "max_tokens":
			finishReason = "length"
		case "stop_sequence":
			finishReason = "stop"
		default:
			finishReason = antResp.StopReason
		}
	}

	return map[string]any{
		"id":      antResp.ID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []any{
			map[string]any{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": text,
				},
				"finish_reason": finishReason,
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     antResp.Usage.InputTokens,
			"completion_tokens": antResp.Usage.OutputTokens,
			"total_tokens":      antResp.Usage.InputTokens + antResp.Usage.OutputTokens,
		},
	}
}

// Anthropic SSE Stream -> OpenAI SSE Stream translator
func translateAnthropicStreamToOAI(w http.ResponseWriter, body io.Reader, model string) error {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, _ := w.(http.Flusher)

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	var streamID string
	var inputTokens, outputTokens int

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "" {
			continue
		}

		var event struct {
			Type         string `json:"type"`
			Index        int    `json:"index"`
			Message      *struct {
				ID    string `json:"id"`
				Model string `json:"model"`
				Usage *struct {
					InputTokens  int `json:"input_tokens"`
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			} `json:"message"`
			ContentBlock *struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content_block"`
			Delta *struct {
				Type         string `json:"type"`
				Text         string `json:"text"`
				StopReason   string `json:"stop_reason"`
				StopSequence string `json:"stop_sequence"`
			} `json:"delta"`
			Usage *struct {
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}

		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}

		var oaiChunk map[string]any

		switch event.Type {
		case "message_start":
			if event.Message != nil {
				streamID = event.Message.ID
				if event.Message.Usage != nil {
					inputTokens = event.Message.Usage.InputTokens
				}
			}
			oaiChunk = map[string]any{
				"id":      streamID,
				"object":  "chat.completion.chunk",
				"created": time.Now().Unix(),
				"model":   model,
				"choices": []any{
					map[string]any{
						"index": 0,
						"delta": map[string]any{
							"role":    "assistant",
							"content": "",
						},
						"finish_reason": nil,
					},
				},
			}
		case "content_block_delta":
			if event.Delta != nil && event.Delta.Text != "" {
				oaiChunk = map[string]any{
					"id":      streamID,
					"object":  "chat.completion.chunk",
					"created": time.Now().Unix(),
					"model":   model,
					"choices": []any{
						map[string]any{
							"index": 0,
							"delta": map[string]any{
								"content": event.Delta.Text,
							},
							"finish_reason": nil,
						},
					},
				}
			}
		case "message_delta":
			var finishReason any = nil
			if event.Delta != nil && event.Delta.StopReason != "" {
				switch event.Delta.StopReason {
				case "end_turn":
					finishReason = "stop"
				case "max_tokens":
					finishReason = "length"
				case "stop_sequence":
					finishReason = "stop"
				default:
					finishReason = event.Delta.StopReason
				}
			}
			if event.Usage != nil {
				outputTokens = event.Usage.OutputTokens
			}

			oaiChunk = map[string]any{
				"id":      streamID,
				"object":  "chat.completion.chunk",
				"created": time.Now().Unix(),
				"model":   model,
				"choices": []any{
					map[string]any{
						"index":         0,
						"delta":         map[string]any{},
						"finish_reason": finishReason,
					},
				},
				"usage": map[string]any{
					"prompt_tokens":     inputTokens,
					"completion_tokens": outputTokens,
					"total_tokens":      inputTokens + outputTokens,
				},
			}
		}

		if oaiChunk != nil {
			out, _ := json.Marshal(oaiChunk)
			fmt.Fprintf(w, "data: %s\n\n", out)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}

	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
	return nil
}

// Global translation helpers for translating incoming Anthropic requests to OpenAI
// and outgoing OpenAI responses to Anthropic for the /v1/messages endpoint.

func TranslateAnthropicRequestToOpenAI(antBody []byte) ([]byte, error) {
	var antReq AnthropicRequest
	if err := json.Unmarshal(antBody, &antReq); err != nil {
		return nil, err
	}

	oaiReq := &oaiChatReq{
		Model:       antReq.Model,
		Stream:      antReq.Stream,
		Temperature: antReq.Temperature,
		TopP:        antReq.TopP,
	}

	if antReq.MaxTokens > 0 {
		oaiReq.MaxTokens = &antReq.MaxTokens
	}

	if antReq.System != nil {
		sysStr := contentToString(antReq.System)
		if sysStr != "" {
			oaiReq.Messages = append(oaiReq.Messages, oaiMessage{
				Role:    "system",
				Content: sysStr,
			})
		}
	}

	for _, msg := range antReq.Messages {
		oaiReq.Messages = append(oaiReq.Messages, oaiMessage{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}

	return json.Marshal(oaiReq)
}

func TranslateOpenAIResponseToAnthropic(oaiBody []byte) ([]byte, error) {
	var oaiResp struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Index   int `json:"index"`
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(oaiBody, &oaiResp); err != nil {
		return nil, err
	}

	var contentText string
	var stopReason string
	if len(oaiResp.Choices) > 0 {
		contentText = oaiResp.Choices[0].Message.Content
		stopReason = oaiResp.Choices[0].FinishReason
		switch stopReason {
		case "stop":
			stopReason = "end_turn"
		case "length":
			stopReason = "max_tokens"
		}
	}

	antResp := AnthropicResponse{
		ID:    oaiResp.ID,
		Type:  "message",
		Role:  "assistant",
		Model: oaiResp.Model,
		Content: []AnthropicContentBlock{
			{
				Type: "text",
				Text: contentText,
			},
		},
		StopReason: stopReason,
		Usage: AnthropicUsage{
			InputTokens:  oaiResp.Usage.PromptTokens,
			OutputTokens: oaiResp.Usage.CompletionTokens,
		},
	}

	return json.Marshal(antResp)
}

// AnthropicStreamTranslator wraps an http.ResponseWriter that accepts OpenAI SSE chunks,
// parses them, and writes translated Anthropic SSE events to the wrapped writer.
type AnthropicStreamTranslator struct {
	w                 http.ResponseWriter
	flusher           http.Flusher
	sentMessageStart  bool
	sentContentStart  bool
	streamID          string
	model             string
	inputTokens       int
	outputTokens      int
	lineBuf           bytes.Buffer
}

func NewAnthropicStreamTranslator(w http.ResponseWriter) *AnthropicStreamTranslator {
	flusher, _ := w.(http.Flusher)
	return &AnthropicStreamTranslator{
		w:       w,
		flusher: flusher,
	}
}

func (t *AnthropicStreamTranslator) Header() http.Header {
	return t.w.Header()
}

func (t *AnthropicStreamTranslator) WriteHeader(statusCode int) {
	t.w.WriteHeader(statusCode)
}

func (t *AnthropicStreamTranslator) Write(p []byte) (int, error) {
	// We might receive partial lines, buffer them
	n := len(p)
	t.lineBuf.Write(p)

	for {
		lineBytes, err := t.lineBuf.ReadBytes('\n')
		if err != nil {
			// put back uncompleted bytes
			t.lineBuf.Write(lineBytes)
			break
		}

		line := strings.TrimSpace(string(lineBytes))
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		payload := strings.TrimPrefix(line, "data: ")
		if payload == "" {
			continue
		}

		if payload == "[DONE]" {
			event := map[string]any{
				"type": "message_stop",
			}
			eb, _ := json.Marshal(event)
			fmt.Fprintf(t.w, "event: message_stop\ndata: %s\n\n", eb)
			if t.flusher != nil {
				t.flusher.Flush()
			}
			continue
		}

		var chunk struct {
			ID      string `json:"id"`
			Model   string `json:"model"`
			Choices []struct {
				Index        int `json:"index"`
				Delta        map[string]any `json:"delta"`
				FinishReason string         `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}

		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}

		if chunk.ID != "" {
			t.streamID = chunk.ID
		}
		if chunk.Model != "" {
			t.model = chunk.Model
		}
		if chunk.Usage != nil {
			t.inputTokens = chunk.Usage.PromptTokens
			t.outputTokens = chunk.Usage.CompletionTokens
		}

		if !t.sentMessageStart {
			t.sentMessageStart = true
			msgStart := map[string]any{
				"type": "message_start",
				"message": map[string]any{
					"id":            t.streamID,
					"type":          "message",
					"role":          "assistant",
					"model":         t.model,
					"content":       []any{},
					"stop_reason":   nil,
					"stop_sequence": nil,
					"usage": map[string]any{
						"input_tokens":  t.inputTokens,
						"output_tokens": t.outputTokens,
					},
				},
			}
			mb, _ := json.Marshal(msgStart)
			fmt.Fprintf(t.w, "event: message_start\ndata: %s\n\n", mb)

			t.sentContentStart = true
			cbStart := map[string]any{
				"type":  "content_block_start",
				"index": 0,
				"content_block": map[string]any{
					"type": "text",
					"text": "",
				},
			}
			cb, _ := json.Marshal(cbStart)
			fmt.Fprintf(t.w, "event: content_block_start\ndata: %s\n\n", cb)
		}

		var contentText string
		var finishReason string
		if len(chunk.Choices) > 0 {
			c := chunk.Choices[0]
			if txt, ok := c.Delta["content"].(string); ok {
				contentText = txt
			}
			finishReason = c.FinishReason
		}

		if contentText != "" {
			cbDelta := map[string]any{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]any{
					"type": "text_delta",
					"text": contentText,
				},
			}
			db, _ := json.Marshal(cbDelta)
			fmt.Fprintf(t.w, "event: content_block_delta\ndata: %s\n\n", db)
		}

		if finishReason != "" {
			cbStop := map[string]any{
				"type":  "content_block_stop",
				"index": 0,
			}
			sb, _ := json.Marshal(cbStop)
			fmt.Fprintf(t.w, "event: content_block_stop\ndata: %s\n\n", sb)

			switch finishReason {
			case "stop":
				finishReason = "end_turn"
			case "length":
				finishReason = "max_tokens"
			}

			msgDelta := map[string]any{
				"type": "message_delta",
				"delta": map[string]any{
					"stop_reason":   finishReason,
					"stop_sequence": nil,
				},
				"usage": map[string]any{
					"output_tokens": t.outputTokens,
				},
			}
			db, _ := json.Marshal(msgDelta)
			fmt.Fprintf(t.w, "event: message_delta\ndata: %s\n\n", db)
		}

		if t.flusher != nil {
			t.flusher.Flush()
		}
	}

	return n, nil
}

func (t *AnthropicStreamTranslator) Close() error {
	return nil
}
