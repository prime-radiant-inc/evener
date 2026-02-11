// Package openaicompat implements a Chat Completions adapter for OpenAI-compatible
// services (Ollama, vLLM, LiteLLM, etc.). It registers as provider "openai-compatible"
// and uses the standard /v1/chat/completions endpoint.
package openaicompat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"primeradiant.com/serf/internal/llm"
)

type Adapter struct {
	APIKey  string
	BaseURL string
	Client  *http.Client
}

func init() {
	llm.RegisterEnvAdapterFactory(func() (llm.ProviderAdapter, bool, error) {
		base := strings.TrimSpace(os.Getenv("OPENAI_COMPATIBLE_BASE_URL"))
		if base == "" {
			return nil, false, nil
		}
		a, err := NewFromEnv()
		if err != nil {
			return nil, true, err
		}
		return a, true, nil
	})
}

func NewFromEnv() (*Adapter, error) {
	base := strings.TrimSpace(os.Getenv("OPENAI_COMPATIBLE_BASE_URL"))
	if base == "" {
		return nil, fmt.Errorf("OPENAI_COMPATIBLE_BASE_URL is required")
	}
	key := strings.TrimSpace(os.Getenv("OPENAI_COMPATIBLE_API_KEY"))
	return &Adapter{
		APIKey:  key,
		BaseURL: strings.TrimRight(base, "/"),
		Client:  &http.Client{Timeout: 0},
	}, nil
}

func (a *Adapter) Name() string { return "openai-compatible" }

// Complete sends a non-streaming Chat Completions request.
func (a *Adapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if a.Client == nil {
		a.Client = &http.Client{Timeout: 0}
	}

	body, err := buildRequestBody(req, false)
	if err != nil {
		return llm.Response{}, err
	}

	raw, statusCode, headers, err := a.doHTTP(ctx, body)
	if err != nil {
		return llm.Response{}, err
	}
	if statusCode != http.StatusOK {
		msg := extractErrorMessage(raw)
		retryAfter := llm.ParseRetryAfter(headers.Get("Retry-After"), time.Now())
		return llm.Response{}, llm.ErrorFromHTTPStatus("openai-compatible", statusCode, msg, raw, retryAfter)
	}

	return fromChatCompletionResponse(raw)
}

// Stream sends a streaming Chat Completions request.
func (a *Adapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	if a.Client == nil {
		a.Client = &http.Client{Timeout: 0}
	}

	body, err := buildRequestBody(req, true)
	if err != nil {
		return nil, err
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.BaseURL+"/v1/chat/completions", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if a.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+a.APIKey)
	}

	resp, err := a.Client.Do(httpReq)
	if err != nil {
		return nil, llm.WrapContextError("openai-compatible", err)
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		var raw map[string]any
		_ = json.Unmarshal(b, &raw)
		msg := extractErrorMessage(raw)
		retryAfter := llm.ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		return nil, llm.ErrorFromHTTPStatus("openai-compatible", resp.StatusCode, msg, raw, retryAfter)
	}

	sctx, cancel := context.WithCancel(ctx)
	s := llm.NewChanStream(cancel)

	s.Send(llm.StreamEvent{Type: llm.StreamEventStreamStart})

	go func() {
		defer func() {
			resp.Body.Close()
			s.CloseSend()
		}()

		// Track tool call state for streaming.
		type toolCallState struct {
			id   string
			name string
			args strings.Builder
		}
		toolCalls := map[int]*toolCallState{}
		var textStarted bool
		var textBuf strings.Builder
		var model string
		var finishReason string
		var usage *llm.Usage
		finished := false

		llm.ParseSSE(sctx, resp.Body, func(ev llm.SSEEvent) error {
			data := string(ev.Data)
			if data == "[DONE]" {
				finished = true
				// Emit tool call end events for any open tool calls.
				for idx, tc := range toolCalls {
					s.Send(llm.StreamEvent{
						Type: llm.StreamEventToolCallEnd,
						ToolCall: &llm.ToolCallData{
							ID:        tc.id,
							Name:      tc.name,
							Arguments: json.RawMessage(tc.args.String()),
							Type:      "function",
						},
					})
					delete(toolCalls, idx)
				}

				if textStarted {
					s.Send(llm.StreamEvent{Type: llm.StreamEventTextEnd, TextID: "text_0"})
				}

				// Build final response.
				msg := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{}}
				if textBuf.Len() > 0 {
					msg.Content = append(msg.Content, llm.ContentPart{Kind: llm.ContentText, Text: textBuf.String()})
				}
				finish := llm.NormalizeFinishReason("", finishReason)
				finalResp := &llm.Response{
					Provider: "openai-compatible",
					Model:    model,
					Message:  msg,
					Finish:   finish,
				}
				if usage != nil {
					finalResp.Usage = *usage
				}
				s.Send(llm.StreamEvent{
					Type:         llm.StreamEventFinish,
					FinishReason: &finish,
					Usage:        usage,
					Response:     finalResp,
				})
				return nil
			}

			var chunk chatCompletionChunk
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				return nil
			}
			if chunk.Model != "" {
				model = chunk.Model
			}
			if chunk.Usage != nil {
				u := llm.Usage{
					InputTokens:  intFromAny(chunk.Usage["prompt_tokens"]),
					OutputTokens: intFromAny(chunk.Usage["completion_tokens"]),
					Raw:          chunk.Usage,
				}
				u.TotalTokens = u.InputTokens + u.OutputTokens
				if v := intFromAny(chunk.Usage["total_tokens"]); v > 0 {
					u.TotalTokens = v
				}
				usage = &u
			}

			if len(chunk.Choices) == 0 {
				return nil
			}
			choice := chunk.Choices[0]

			if choice.FinishReason != "" {
				finishReason = choice.FinishReason
			}

			// Text delta.
			if choice.Delta.Content != "" {
				if !textStarted {
					s.Send(llm.StreamEvent{Type: llm.StreamEventTextStart, TextID: "text_0"})
					textStarted = true
				}
				textBuf.WriteString(choice.Delta.Content)
				s.Send(llm.StreamEvent{
					Type:   llm.StreamEventTextDelta,
					TextID: "text_0",
					Delta:  choice.Delta.Content,
				})
			}

			// Tool call deltas.
			for _, tc := range choice.Delta.ToolCalls {
				state, exists := toolCalls[tc.Index]
				if !exists {
					state = &toolCallState{
						id:   tc.ID,
						name: tc.Function.Name,
					}
					toolCalls[tc.Index] = state
					s.Send(llm.StreamEvent{
						Type: llm.StreamEventToolCallStart,
						ToolCall: &llm.ToolCallData{
							ID:   tc.ID,
							Name: tc.Function.Name,
							Type: "function",
						},
					})
				}
				if tc.Function.Arguments != "" {
					state.args.WriteString(tc.Function.Arguments)
					s.Send(llm.StreamEvent{
						Type: llm.StreamEventToolCallDelta,
						ToolCall: &llm.ToolCallData{
							ID:        state.id,
							Name:      state.name,
							Arguments: json.RawMessage(tc.Function.Arguments),
							Type:      "function",
						},
					})
				}
			}

			return nil
		})

		if !finished {
			if err := sctx.Err(); err != nil {
				s.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: llm.WrapContextError("openai-compatible", err)})
			}
		}
	}()

	return s, nil
}

// --- Request building ---

func buildRequestBody(req llm.Request, stream bool) (map[string]any, error) {
	body := map[string]any{
		"model": req.Model,
	}

	msgs, err := toChatMessages(req.Messages)
	if err != nil {
		return nil, err
	}
	body["messages"] = msgs

	if len(req.Tools) > 0 {
		body["tools"] = toChatTools(req.Tools)
	}
	if req.ToolChoice != nil {
		body["tool_choice"] = toChatToolChoice(*req.ToolChoice)
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	if req.MaxTokens != nil {
		body["max_tokens"] = *req.MaxTokens
	}
	if len(req.StopSequences) > 0 {
		body["stop"] = req.StopSequences
	}
	if req.ResponseFormat != nil {
		body["response_format"] = toChatResponseFormat(*req.ResponseFormat)
	}
	if stream {
		body["stream"] = true
		body["stream_options"] = map[string]any{"include_usage": true}
	}

	// Provider options passthrough.
	if req.ProviderOptions != nil {
		if ov, ok := req.ProviderOptions["openai-compatible"].(map[string]any); ok {
			for k, v := range ov {
				body[k] = v
			}
		}
	}

	return body, nil
}

func toChatMessages(messages []llm.Message) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(messages))
	for _, m := range messages {
		switch m.Role {
		case llm.RoleSystem, llm.RoleDeveloper:
			out = append(out, map[string]any{
				"role":    "system",
				"content": textFromParts(m.Content),
			})
		case llm.RoleUser:
			out = append(out, map[string]any{
				"role":    "user",
				"content": textFromParts(m.Content),
			})
		case llm.RoleAssistant:
			msg := map[string]any{
				"role": "assistant",
			}
			text := textFromParts(m.Content)
			calls := toolCallsFromParts(m.Content)
			if len(calls) > 0 {
				msg["tool_calls"] = calls
				if text != "" {
					msg["content"] = text
				}
			} else {
				msg["content"] = text
			}
			out = append(out, msg)
		case llm.RoleTool:
			for _, p := range m.Content {
				if p.Kind == llm.ContentToolResult && p.ToolResult != nil {
					content := ""
					switch v := p.ToolResult.Content.(type) {
					case string:
						content = v
					default:
						b, _ := json.Marshal(v)
						content = string(b)
					}
					out = append(out, map[string]any{
						"role":         "tool",
						"tool_call_id": p.ToolResult.ToolCallID,
						"content":      content,
					})
				}
			}
		}
	}
	return out, nil
}

func textFromParts(parts []llm.ContentPart) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Kind == llm.ContentText {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

func toolCallsFromParts(parts []llm.ContentPart) []map[string]any {
	var calls []map[string]any
	for _, p := range parts {
		if p.Kind == llm.ContentToolCall && p.ToolCall != nil {
			calls = append(calls, map[string]any{
				"id":   p.ToolCall.ID,
				"type": "function",
				"function": map[string]any{
					"name":      p.ToolCall.Name,
					"arguments": string(p.ToolCall.Arguments),
				},
			})
		}
	}
	return calls
}

func toChatTools(tools []llm.ToolDefinition) []map[string]any {
	out := make([]map[string]any, len(tools))
	for i, t := range tools {
		fn := map[string]any{
			"name": t.Name,
		}
		if t.Description != "" {
			fn["description"] = t.Description
		}
		if t.Parameters != nil {
			fn["parameters"] = t.Parameters
		}
		out[i] = map[string]any{
			"type":     "function",
			"function": fn,
		}
	}
	return out
}

func toChatToolChoice(tc llm.ToolChoice) any {
	switch tc.Mode {
	case "auto":
		return "auto"
	case "none":
		return "none"
	case "required":
		return "required"
	default:
		if tc.Name != "" {
			return map[string]any{
				"type":     "function",
				"function": map[string]any{"name": tc.Name},
			}
		}
		return "auto"
	}
}

func toChatResponseFormat(rf llm.ResponseFormat) map[string]any {
	switch rf.Type {
	case "json", "json_object":
		return map[string]any{"type": "json_object"}
	case "json_schema":
		out := map[string]any{"type": "json_schema"}
		if rf.JSONSchema != nil {
			schema := map[string]any{
				"name":   "response",
				"schema": rf.JSONSchema,
			}
			if rf.Strict {
				schema["strict"] = true
			}
			out["json_schema"] = schema
		}
		return out
	default:
		return map[string]any{"type": "text"}
	}
}

// --- Response parsing ---

type chatCompletionResponse struct {
	ID      string            `json:"id"`
	Model   string            `json:"model"`
	Choices []chatChoice      `json:"choices"`
	Usage   map[string]any    `json:"usage"`
}

type chatChoice struct {
	Index        int               `json:"index"`
	Message      chatMessage       `json:"message"`
	FinishReason string            `json:"finish_reason"`
}

type chatMessage struct {
	Role      string            `json:"role"`
	Content   string            `json:"content"`
	ToolCalls []chatToolCall    `json:"tool_calls,omitempty"`
}

type chatToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function chatFunctionCall `json:"function"`
}

type chatFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatCompletionChunk struct {
	ID      string              `json:"id"`
	Model   string              `json:"model"`
	Choices []chatChunkChoice   `json:"choices"`
	Usage   map[string]any      `json:"usage"`
}

type chatChunkChoice struct {
	Index        int              `json:"index"`
	Delta        chatDelta        `json:"delta"`
	FinishReason string           `json:"finish_reason"`
}

type chatDelta struct {
	Role      string              `json:"role"`
	Content   string              `json:"content"`
	ToolCalls []chatChunkToolCall `json:"tool_calls,omitempty"`
}

type chatChunkToolCall struct {
	Index    int              `json:"index"`
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type,omitempty"`
	Function chatFunctionCall `json:"function"`
}

func fromChatCompletionResponse(raw map[string]any) (llm.Response, error) {
	b, err := json.Marshal(raw)
	if err != nil {
		return llm.Response{}, err
	}
	var parsed chatCompletionResponse
	if err := json.Unmarshal(b, &parsed); err != nil {
		return llm.Response{}, fmt.Errorf("failed to parse chat completion response: %w", err)
	}

	if len(parsed.Choices) == 0 {
		return llm.Response{}, fmt.Errorf("no choices in response")
	}
	choice := parsed.Choices[0]

	// Build message.
	parts := []llm.ContentPart{}
	if choice.Message.Content != "" {
		parts = append(parts, llm.ContentPart{Kind: llm.ContentText, Text: choice.Message.Content})
	}
	for _, tc := range choice.Message.ToolCalls {
		parts = append(parts, llm.ContentPart{
			Kind: llm.ContentToolCall,
			ToolCall: &llm.ToolCallData{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: json.RawMessage(tc.Function.Arguments),
				Type:      "function",
			},
		})
	}

	msg := llm.Message{Role: llm.RoleAssistant, Content: parts}
	finish := llm.NormalizeFinishReason("", choice.FinishReason)

	resp := llm.Response{
		Provider: "openai-compatible",
		Model:    parsed.Model,
		ID:       parsed.ID,
		Message:  msg,
		Finish:   finish,
		Raw:      raw,
	}

	if parsed.Usage != nil {
		resp.Usage = llm.Usage{
			InputTokens:  intFromAny(parsed.Usage["prompt_tokens"]),
			OutputTokens: intFromAny(parsed.Usage["completion_tokens"]),
			Raw:          parsed.Usage,
		}
		resp.Usage.TotalTokens = resp.Usage.InputTokens + resp.Usage.OutputTokens
		if v := intFromAny(parsed.Usage["total_tokens"]); v > 0 {
			resp.Usage.TotalTokens = v
		}
	}

	return resp, nil
}

// --- HTTP helpers ---

func (a *Adapter) doHTTP(ctx context.Context, body map[string]any) (map[string]any, int, http.Header, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, 0, nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.BaseURL+"/v1/chat/completions", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, 0, nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if a.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+a.APIKey)
	}

	resp, err := a.Client.Do(httpReq)
	if err != nil {
		return nil, 0, nil, llm.WrapContextError("openai-compatible", err)
	}
	defer resp.Body.Close()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, resp.Header, err
	}

	var raw map[string]any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		// If we can't parse JSON, return what we have.
		return nil, resp.StatusCode, resp.Header, nil
	}

	return raw, resp.StatusCode, resp.Header, nil
}

func extractErrorMessage(raw map[string]any) string {
	if raw == nil {
		return ""
	}
	if errObj, ok := raw["error"].(map[string]any); ok {
		if msg, ok := errObj["message"].(string); ok {
			return msg
		}
	}
	return ""
}

func intFromAny(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	case int:
		return n
	default:
		return 0
	}
}
