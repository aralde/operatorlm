package providers

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// This file makes the chatgpt-codex provider speak /v1/chat/completions even
// though the upstream (chatgpt.com/backend-api/codex) only understands the
// Responses API. It translates the request shape on the way in and the
// streamed Responses events back into chat-completions shape on the way out.
//
// Everything here is scoped to chatgpt-codex; the generic openai provider keeps
// its own native chat/responses handling untouched.

// --- request translation: chat/completions -> responses ---------------------

type codexChatToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type codexChatMessage struct {
	Role       string              `json:"role"`
	Content    any                 `json:"content"`
	Name       string              `json:"name"`
	ToolCalls  []codexChatToolCall `json:"tool_calls"`
	ToolCallID string              `json:"tool_call_id"`
}

type codexChatRequest struct {
	Model       string             `json:"model"`
	Messages    []codexChatMessage `json:"messages"`
	Tools       []json.RawMessage  `json:"tools"`
	ToolChoice  json.RawMessage    `json:"tool_choice"`
	Temperature *float64           `json:"temperature"`
	TopP        *float64           `json:"top_p"`
}

// chatToResponsesBody converts an OpenAI chat/completions body into a Responses
// API body. The result is then handed to transformCodexBody, which applies the
// codex-endpoint-specific constraints (store=false, stream=true, reasoning, …).
// On any parse failure it returns the original bytes so we never break a request
// just because translation tripped.
func chatToResponsesBody(body []byte) []byte {
	var req codexChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return body
	}

	out := map[string]any{}
	if req.Model != "" {
		out["model"] = req.Model
	}
	if req.Temperature != nil {
		out["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		out["top_p"] = *req.TopP
	}

	var instructions strings.Builder
	input := make([]any, 0, len(req.Messages))

	for _, m := range req.Messages {
		switch m.Role {
		case "system", "developer":
			if instructions.Len() > 0 {
				instructions.WriteString("\n\n")
			}
			instructions.WriteString(codexContentToString(m.Content))

		case "tool":
			input = append(input, map[string]any{
				"type":    "function_call_output",
				"call_id": m.ToolCallID,
				"output":  codexContentToString(m.Content),
			})

		case "assistant":
			if text := codexContentToString(m.Content); text != "" {
				input = append(input, map[string]any{
					"type": "message",
					"role": "assistant",
					"content": []any{
						map[string]any{"type": "output_text", "text": text},
					},
				})
			}
			for _, tc := range m.ToolCalls {
				input = append(input, map[string]any{
					"type":      "function_call",
					"call_id":   tc.ID,
					"name":      tc.Function.Name,
					"arguments": tc.Function.Arguments,
				})
			}

		default: // user (and anything unexpected) treated as user input
			input = append(input, map[string]any{
				"type": "message",
				"role": "user",
				"content": []any{
					map[string]any{"type": "input_text", "text": codexContentToString(m.Content)},
				},
			})
		}
	}

	if instructions.Len() > 0 {
		out["instructions"] = instructions.String()
	}
	out["input"] = input

	if tools := codexTranslateTools(req.Tools); tools != nil {
		out["tools"] = tools
	}
	if tc := codexTranslateToolChoice(req.ToolChoice); tc != nil {
		out["tool_choice"] = tc
	}

	rewritten, err := json.Marshal(out)
	if err != nil {
		return body
	}
	return rewritten
}

// codexTranslateTools flattens chat-style tools
// ({type:"function", function:{name,description,parameters}}) into the
// Responses-style ({type:"function", name, description, parameters}).
func codexTranslateTools(raw []json.RawMessage) []any {
	if len(raw) == 0 {
		return nil
	}
	out := make([]any, 0, len(raw))
	for _, r := range raw {
		var t struct {
			Type     string `json:"type"`
			Function struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
				Strict      *bool           `json:"strict"`
			} `json:"function"`
		}
		if err := json.Unmarshal(r, &t); err != nil {
			continue
		}
		if t.Type != "" && t.Type != "function" {
			// Pass non-function tools through untouched (e.g. web_search).
			var passthrough any
			if json.Unmarshal(r, &passthrough) == nil {
				out = append(out, passthrough)
			}
			continue
		}
		tool := map[string]any{
			"type":        "function",
			"name":        t.Function.Name,
			"description": t.Function.Description,
		}
		if len(t.Function.Parameters) > 0 {
			tool["parameters"] = json.RawMessage(t.Function.Parameters)
		}
		if t.Function.Strict != nil {
			tool["strict"] = *t.Function.Strict
		}
		out = append(out, tool)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// codexTranslateToolChoice maps chat tool_choice to the Responses equivalent.
// "auto"/"none"/"required" pass through; {type:function,function:{name}}
// becomes {type:function,name}.
func codexTranslateToolChoice(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var obj struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.Function.Name != "" {
		return map[string]any{"type": "function", "name": obj.Function.Name}
	}
	var passthrough any
	if json.Unmarshal(raw, &passthrough) == nil {
		return passthrough
	}
	return nil
}

func codexContentToString(c any) string {
	switch v := c.(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if t, ok := m["text"].(string); ok {
				b.WriteString(t)
			}
		}
		return b.String()
	}
	return ""
}

// --- response translation: responses SSE -> chat/completions ----------------

type codexStreamEvent struct {
	Type        string           `json:"type"`
	Delta       string           `json:"delta"`
	ItemID      string           `json:"item_id"`
	OutputIndex int              `json:"output_index"`
	Item        *codexOutputItem `json:"item"`
	Response    *codexResponse   `json:"response"`
}

type codexOutputItem struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type codexResponse struct {
	Status            string `json:"status"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Usage *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

// writeCodexChatResponse converts the upstream Responses SSE stream into a
// chat-completions response. The upstream always streams (codex forces
// stream=true), so when the client asked for stream=false we aggregate the
// whole stream into a single chat.completion object.
func writeCodexChatResponse(w http.ResponseWriter, resp *http.Response, model string, stream bool) error {
	id := "chatcmpl-" + randomUUID()
	created := time.Now().Unix()
	if stream {
		return streamCodexChat(w, resp.Body, model, id, created)
	}
	return aggregateCodexChat(w, resp.Body, model, id, created)
}

func scanCodexEvents(body io.Reader, fn func(ev *codexStreamEvent)) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var ev codexStreamEvent
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			continue
		}
		fn(&ev)
	}
	return scanner.Err()
}

func streamCodexChat(w http.ResponseWriter, body io.Reader, model, id string, created int64) error {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, _ := w.(http.Flusher)

	emit := func(delta map[string]any, finish string) {
		choice := map[string]any{"index": 0, "delta": delta}
		if finish != "" {
			choice["finish_reason"] = finish
		}
		chunk := map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model,
			"choices": []any{choice},
		}
		out, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\n", out)
		if flusher != nil {
			flusher.Flush()
		}
	}

	emit(map[string]any{"role": "assistant"}, "")

	toolIndex := map[string]int{}
	nextTool := 0
	hasTool := false
	finishReason := "stop"

	err := scanCodexEvents(body, func(ev *codexStreamEvent) {
		switch ev.Type {
		case "response.output_text.delta":
			if ev.Delta != "" {
				emit(map[string]any{"content": ev.Delta}, "")
			}
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			if ev.Delta != "" {
				emit(map[string]any{"reasoning_content": ev.Delta}, "")
			}
		case "response.output_item.added":
			if ev.Item != nil && ev.Item.Type == "function_call" {
				idx := nextTool
				nextTool++
				toolIndex[ev.Item.ID] = idx
				hasTool = true
				emit(map[string]any{"tool_calls": []any{map[string]any{
					"index":    idx,
					"id":       ev.Item.CallID,
					"type":     "function",
					"function": map[string]any{"name": ev.Item.Name, "arguments": ""},
				}}}, "")
			}
		case "response.function_call_arguments.delta":
			if idx, ok := toolIndex[ev.ItemID]; ok && ev.Delta != "" {
				emit(map[string]any{"tool_calls": []any{map[string]any{
					"index":    idx,
					"function": map[string]any{"arguments": ev.Delta},
				}}}, "")
			}
		case "response.incomplete", "response.completed":
			if ev.Response != nil && ev.Response.IncompleteDetails != nil &&
				ev.Response.IncompleteDetails.Reason == "max_output_tokens" {
				finishReason = "length"
			}
		}
	})

	if hasTool {
		finishReason = "tool_calls"
	}
	emit(map[string]any{}, finishReason)
	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
	return err
}

func aggregateCodexChat(w http.ResponseWriter, body io.Reader, model, id string, created int64) error {
	var content, reasoning strings.Builder

	type toolAcc struct {
		id   string
		name string
		args strings.Builder
	}
	var tools []*toolAcc
	toolByItem := map[string]*toolAcc{}

	finishReason := "stop"
	var usage map[string]any

	err := scanCodexEvents(body, func(ev *codexStreamEvent) {
		switch ev.Type {
		case "response.output_text.delta":
			content.WriteString(ev.Delta)
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			reasoning.WriteString(ev.Delta)
		case "response.output_item.added":
			if ev.Item != nil && ev.Item.Type == "function_call" {
				t := &toolAcc{id: ev.Item.CallID, name: ev.Item.Name}
				tools = append(tools, t)
				toolByItem[ev.Item.ID] = t
			}
		case "response.function_call_arguments.delta":
			if t, ok := toolByItem[ev.ItemID]; ok {
				t.args.WriteString(ev.Delta)
			}
		case "response.incomplete", "response.completed":
			if ev.Response != nil {
				if ev.Response.IncompleteDetails != nil &&
					ev.Response.IncompleteDetails.Reason == "max_output_tokens" {
					finishReason = "length"
				}
				if u := ev.Response.Usage; u != nil {
					usage = map[string]any{
						"prompt_tokens":     u.InputTokens,
						"completion_tokens": u.OutputTokens,
						"total_tokens":      u.TotalTokens,
					}
				}
			}
		}
	})
	if err != nil {
		return err
	}

	message := map[string]any{"role": "assistant"}
	if c := content.String(); c != "" {
		message["content"] = c
	} else {
		message["content"] = nil
	}
	if r := reasoning.String(); r != "" {
		message["reasoning_content"] = r
	}
	if len(tools) > 0 {
		toolCalls := make([]any, 0, len(tools))
		for i, t := range tools {
			toolCalls = append(toolCalls, map[string]any{
				"index": i,
				"id":    t.id,
				"type":  "function",
				"function": map[string]any{
					"name":      t.name,
					"arguments": t.args.String(),
				},
			})
		}
		message["tool_calls"] = toolCalls
		finishReason = "tool_calls"
	}

	out := map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": created,
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": finishReason,
		}},
	}
	if usage != nil {
		out["usage"] = usage
	}

	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(out)
}
