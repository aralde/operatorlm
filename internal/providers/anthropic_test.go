package providers

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestTranslateAnthropicRequestToOpenAI(t *testing.T) {
	antJSON := `{
		"model": "claude-3-5-sonnet-20241022",
		"messages": [
			{"role": "user", "content": "Hello"}
		],
		"system": "You are system",
		"max_tokens": 100,
		"stream": true,
		"temperature": 0.7,
		"top_p": 0.9
	}`

	oaiBytes, err := TranslateAnthropicRequestToOpenAI([]byte(antJSON))
	if err != nil {
		t.Fatalf("failed to translate request: %v", err)
	}

	var oaiReq oaiChatReq
	if err := json.Unmarshal(oaiBytes, &oaiReq); err != nil {
		t.Fatalf("failed to unmarshal output: %v", err)
	}

	if oaiReq.Model != "claude-3-5-sonnet-20241022" {
		t.Errorf("expected model claude-3-5-sonnet-20241022, got %s", oaiReq.Model)
	}
	if oaiReq.Stream != true {
		t.Errorf("expected stream true")
	}
	if oaiReq.Temperature == nil || *oaiReq.Temperature != 0.7 {
		t.Errorf("expected temperature 0.7")
	}
	if oaiReq.TopP == nil || *oaiReq.TopP != 0.9 {
		t.Errorf("expected top_p 0.9")
	}
	if oaiReq.MaxTokens == nil || *oaiReq.MaxTokens != 100 {
		t.Errorf("expected max_tokens 100")
	}

	// Should have 2 messages: system prompt and user message
	if len(oaiReq.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(oaiReq.Messages))
	}
	if oaiReq.Messages[0].Role != "system" || oaiReq.Messages[0].Content != "You are system" {
		t.Errorf("expected system message first, got: %+v", oaiReq.Messages[0])
	}
	if oaiReq.Messages[1].Role != "user" || oaiReq.Messages[1].Content != "Hello" {
		t.Errorf("expected user message second, got: %+v", oaiReq.Messages[1])
	}
}

func TestTranslateOpenAIResponseToAnthropic(t *testing.T) {
	oaiJSON := `{
		"id": "chatcmpl-123",
		"model": "gpt-4o",
		"choices": [
			{
				"index": 0,
				"message": {
					"role": "assistant",
					"content": "Hello from OpenAI"
				},
				"finish_reason": "stop"
			}
		],
		"usage": {
			"prompt_tokens": 10,
			"completion_tokens": 15
		}
	}`

	antBytes, err := TranslateOpenAIResponseToAnthropic([]byte(oaiJSON))
	if err != nil {
		t.Fatalf("failed to translate response: %v", err)
	}

	var antResp AnthropicResponse
	if err := json.Unmarshal(antBytes, &antResp); err != nil {
		t.Fatalf("failed to unmarshal output: %v", err)
	}

	if antResp.ID != "chatcmpl-123" {
		t.Errorf("expected ID chatcmpl-123, got %s", antResp.ID)
	}
	if antResp.Model != "gpt-4o" {
		t.Errorf("expected model gpt-4o, got %s", antResp.Model)
	}
	if len(antResp.Content) != 1 || antResp.Content[0].Type != "text" || antResp.Content[0].Text != "Hello from OpenAI" {
		t.Errorf("expected content Hello from OpenAI, got %+v", antResp.Content)
	}
	if antResp.StopReason != "end_turn" {
		t.Errorf("expected stop_reason end_turn, got %s", antResp.StopReason)
	}
	if antResp.Usage.InputTokens != 10 || antResp.Usage.OutputTokens != 15 {
		t.Errorf("expected usage 10/15, got %+v", antResp.Usage)
	}
}

func TestAnthropicStreamTranslator(t *testing.T) {
	w := httptest.NewRecorder()
	translator := NewAnthropicStreamTranslator(w)

	// Feed standard OpenAI SSE chunks
	chunk1 := `data: {"id":"chatcmpl-123","model":"gpt-4","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}` + "\n\n"
	chunk2 := `data: {"id":"chatcmpl-123","model":"gpt-4","choices":[{"index":0,"delta":{"content":"Hello "},"finish_reason":null}]}` + "\n\n"
	chunk3 := `data: {"id":"chatcmpl-123","model":"gpt-4","choices":[{"index":0,"delta":{"content":"world"},"finish_reason":null}]}` + "\n\n"
	chunk4 := `data: {"id":"chatcmpl-123","model":"gpt-4","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n"
	chunk5 := `data: [DONE]` + "\n\n"

	_, _ = translator.Write([]byte(chunk1))
	_, _ = translator.Write([]byte(chunk2))
	_, _ = translator.Write([]byte(chunk3))
	_, _ = translator.Write([]byte(chunk4))
	_, _ = translator.Write([]byte(chunk5))

	output := w.Body.String()

	// Should contain event headers and data lines
	if !bytes.Contains(w.Body.Bytes(), []byte("event: message_start")) {
		t.Errorf("missing event: message_start, got:\n%s", output)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("event: content_block_start")) {
		t.Errorf("missing event: content_block_start")
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("Hello ")) {
		t.Errorf("missing text delta: Hello ")
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("world")) {
		t.Errorf("missing text delta: world")
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("event: content_block_stop")) {
		t.Errorf("missing event: content_block_stop")
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("event: message_delta")) {
		t.Errorf("missing event: message_delta")
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("event: message_stop")) {
		t.Errorf("missing event: message_stop")
	}
}
