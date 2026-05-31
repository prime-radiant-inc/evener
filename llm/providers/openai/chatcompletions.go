package openai

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

	"primeradiant.com/serf/llm"
)

// streamViaChatCompletions provides a fallback streaming path using the
// Chat Completions API (/v1/chat/completions). This handles models that do
// not support the Responses API endpoint.
func (a *Adapter) streamViaChatCompletions(ctx context.Context, req llm.Request) (llm.Stream, error) {
	sctx, cancel := context.WithCancel(ctx)
	sctx, timeoutCancel := llm.ApplyAdapterTimeout(sctx, req.AdapterTimeout, true)
	defer timeoutCancel()

	body, err := buildChatCompletionsBody(req, true)
	if err != nil {
		cancel()
		return nil, err
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		cancel()
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(sctx, http.MethodPost, a.chatCompletionsURL(), bytes.NewReader(jsonBody))
	if err != nil {
		cancel()
		return nil, err
	}
	a.setRequestHeaders(httpReq, req)
	// Chat Completions does not use the Responses-API-specific ChatGPT-Account-ID header.
	// setHeaders already sets it from a.ChatGPTAccountID; we leave it for compatibility
	// with non-standard deployments.

	client := llm.ClientWithConnectTimeout(a.Client, req.AdapterTimeout)
	resp, err := client.Do(httpReq)
	if err != nil {
		cancel()
		return nil, llm.WrapContextError("openai", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		var raw map[string]any
		dec := json.NewDecoder(resp.Body)
		dec.UseNumber()
		_ = dec.Decode(&raw)
		ra := llm.ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		msg := fmt.Sprintf("chat.completions(stream) failed: %v", raw)
		cancel()
		return nil, llm.ErrorFromHTTPStatus("openai", resp.StatusCode, msg, raw, ra)
	}

	s := llm.NewChanStream(cancel)
	s.Send(llm.StreamEvent{Type: llm.StreamEventStreamStart})

	go func() {
		defer func() {
			_ = resp.Body.Close()
			s.CloseSend()
		}()

		type toolCallState struct {
			id   string
			name string
			args strings.Builder
		}
		toolCalls := map[int]*toolCallState{}
		var textStarted bool
		var textBuf strings.Builder
		var finishReason string
		var model string
		var usage *llm.Usage
		finished := false

		var sseBody io.Reader = resp.Body
		var sseBuf *bytes.Buffer
		if llm.RawBodyEnabled() {
			sseBuf = &bytes.Buffer{}
			sseBody = io.TeeReader(resp.Body, sseBuf)
		}
		rawReqBody := string(jsonBody)

		_ = llm.ParseSSE(sctx, sseBody, func(ev llm.SSEEvent) error {
			data := string(ev.Data)
			if data == "[DONE]" {
				finished = true

				// Emit tool call end events.
				var completedToolCalls []llm.ToolCallData
				for _, tc := range toolCalls {
					tcd := llm.ToolCallData{
						ID:        tc.id,
						Name:      tc.name,
						Arguments: json.RawMessage(tc.args.String()),
						Type:      "function",
					}
					completedToolCalls = append(completedToolCalls, tcd)
					s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &tcd})
				}
				if textStarted {
					s.Send(llm.StreamEvent{Type: llm.StreamEventTextEnd, TextID: "text_0"})
				}

				msg := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{}}
				if textBuf.Len() > 0 {
					msg.Content = append(msg.Content, llm.ContentPart{Kind: llm.ContentText, Text: textBuf.String()})
				}
				for i := range completedToolCalls {
					msg.Content = append(msg.Content, llm.ContentPart{
						Kind:     llm.ContentToolCall,
						ToolCall: &completedToolCalls[i],
					})
				}

				finish := llm.NormalizeFinishReason("", finishReason)
				finalResp := &llm.Response{
					Provider: "openai",
					Model:    model,
					Message:  msg,
					Finish:   finish,
				}
				if usage != nil {
					finalResp.Usage = *usage
				}
				llm.StampEndpointURL(finalResp, a.chatCompletionsURL())
				if sseBuf != nil {
					finalResp.RawRequestBody = rawReqBody
					finalResp.RawResponseBody = sseBuf.String()
				}
				s.Send(llm.StreamEvent{
					Type:         llm.StreamEventFinish,
					FinishReason: &finish,
					Usage:        usage,
					Response:     finalResp,
				})
				cancel()
				return nil
			}

			var chunk struct {
				ID      string `json:"id"`
				Model   string `json:"model"`
				Choices []struct {
					Index        int    `json:"index"`
					FinishReason string `json:"finish_reason"`
					Delta        struct {
						Content   string `json:"content"`
						ToolCalls []struct {
							Index    int    `json:"index"`
							ID       string `json:"id,omitempty"`
							Type     string `json:"type,omitempty"`
							Function struct {
								Name      string `json:"name"`
								Arguments string `json:"arguments"`
							} `json:"function"`
						} `json:"tool_calls"`
					} `json:"delta"`
				} `json:"choices"`
				Usage map[string]any `json:"usage"`
			}
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				return nil
			}
			if chunk.Model != "" {
				model = chunk.Model
			}
			if chunk.Usage != nil {
				rawPrompt := llm.IntFromAny(chunk.Usage["prompt_tokens"])
				var cachedRead int
				if details, ok := chunk.Usage["prompt_tokens_details"].(map[string]any); ok {
					cachedRead = llm.IntFromAny(details["cached_tokens"])
				}
				uncachedInput := rawPrompt - cachedRead
				if uncachedInput < 0 {
					uncachedInput = 0
				}
				u := llm.Usage{
					InputTokens:  uncachedInput,
					OutputTokens: llm.IntFromAny(chunk.Usage["completion_tokens"]),
					Raw:          chunk.Usage,
				}
				u.TotalTokens = rawPrompt + u.OutputTokens
				if v := llm.IntFromAny(chunk.Usage["total_tokens"]); v > 0 {
					u.TotalTokens = v
				}
				if details, ok := chunk.Usage["completion_tokens_details"].(map[string]any); ok {
					if rt := llm.IntFromAny(details["reasoning_tokens"]); rt > 0 {
						u.ReasoningTokens = &rt
					}
				}
				if cachedRead > 0 {
					u.CacheReadTokens = &cachedRead
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
			if choice.Delta.Content != "" {
				if !textStarted {
					s.Send(llm.StreamEvent{Type: llm.StreamEventTextStart, TextID: "text_0"})
					textStarted = true
				}
				textBuf.WriteString(choice.Delta.Content)
				s.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, TextID: "text_0", Delta: choice.Delta.Content})
			}
			for _, tc := range choice.Delta.ToolCalls {
				state, exists := toolCalls[tc.Index]
				if !exists {
					state = &toolCallState{id: tc.ID, name: tc.Function.Name}
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
		}, llm.StreamReadSSEOptions(req.AdapterTimeout)...)

		if !finished {
			if err := sctx.Err(); err != nil {
				s.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: llm.WrapContextError("openai", err)})
			} else {
				s.Send(llm.StreamEvent{
					Type: llm.StreamEventError,
					Err:  llm.NewStreamError("openai", fmt.Sprintf("chat.completions stream closed without [DONE] (model: %q)", req.Model), nil),
				})
			}
		}
	}()

	return s, nil
}

// buildChatCompletionsBody translates an llm.Request into a Chat Completions
// request body. This is used by the Responses-API fallback path.
func buildChatCompletionsBody(req llm.Request, stream bool) (map[string]any, error) {
	if requestHasToolResultImages(req) {
		return nil, fmt.Errorf("openai chat completions fallback does not support tool-result images")
	}
	body := map[string]any{
		"model": req.Model,
	}

	msgs, err := toChatMessages(req.Messages)
	if err != nil {
		return nil, err
	}
	body["messages"] = msgs

	if len(req.Tools) > 0 {
		body["tools"] = toCompletionsTools(req.Tools)
	}
	if req.ToolChoice != nil {
		tc, err := toResponsesToolChoice(*req.ToolChoice)
		if err != nil {
			return nil, err
		}
		body["tool_choice"] = tc
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
	if req.ReasoningEffort != nil {
		body["reasoning_effort"] = *req.ReasoningEffort
	}
	if len(req.Metadata) > 0 {
		body["metadata"] = req.Metadata
	}
	if req.WebSearch {
		tools, _ := body["tools"].([]map[string]any)
		tools = append(tools, map[string]any{"type": "web_search"})
		body["tools"] = tools
	}
	if stream {
		body["stream"] = true
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	if opts, ok := req.ProviderOptions["openai"].(map[string]any); ok {
		for k, v := range opts {
			body[k] = v
		}
	}
	return body, nil
}

// toChatMessages converts llm.Message slice to Chat Completions message format.
func toChatMessages(msgs []llm.Message) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case llm.RoleSystem, llm.RoleDeveloper:
			out = append(out, map[string]any{
				"role":    "system",
				"content": m.Text(),
			})
		case llm.RoleUser:
			if hasChatRichContent(m.Content) {
				content, err := buildChatMultimodalParts(m.Content)
				if err != nil {
					return nil, err
				}
				out = append(out, map[string]any{
					"role":    "user",
					"content": content,
				})
			} else {
				out = append(out, map[string]any{
					"role":    "user",
					"content": m.Text(),
				})
			}
		case llm.RoleAssistant:
			msg := map[string]any{"role": "assistant"}
			text := m.Text()
			var calls []map[string]any
			for _, p := range m.Content {
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
					resultContent := ""
					switch v := p.ToolResult.Content.(type) {
					case string:
						resultContent = v
					default:
						b, _ := json.Marshal(v)
						resultContent = string(b)
					}
					out = append(out, map[string]any{
						"role":         "tool",
						"tool_call_id": p.ToolResult.ToolCallID,
						"content":      resultContent,
					})
				}
			}
		}
	}
	return out, nil
}

func requestHasToolResultImages(req llm.Request) bool {
	for _, m := range req.Messages {
		if m.Role != llm.RoleTool {
			continue
		}
		for _, p := range m.Content {
			if p.Kind == llm.ContentToolResult && p.ToolResult != nil && len(p.ToolResult.ImageData) > 0 {
				return true
			}
		}
	}
	return false
}

// toCompletionsTools converts tool definitions for the Chat Completions format.
func toCompletionsTools(tools []llm.ToolDefinition) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		fn := map[string]any{"name": t.Name}
		if t.Description != "" {
			fn["description"] = t.Description
		}
		if t.Parameters != nil {
			fn["parameters"] = t.Parameters
		}
		out = append(out, map[string]any{
			"type":     "function",
			"function": fn,
		})
	}
	return out
}

func hasChatRichContent(parts []llm.ContentPart) bool {
	for _, p := range parts {
		switch p.Kind {
		case llm.ContentImage, llm.ContentDocument, llm.ContentAudio:
			return true
		}
	}
	return false
}

func buildChatMultimodalParts(parts []llm.ContentPart) ([]map[string]any, error) {
	var out []map[string]any
	for _, p := range parts {
		switch p.Kind {
		case llm.ContentText:
			out = append(out, map[string]any{"type": "text", "text": p.Text})
		case llm.ContentImage:
			if p.Image == nil {
				continue
			}
			url := strings.TrimSpace(p.Image.URL)
			if url == "" && len(p.Image.Data) > 0 {
				mt := strings.TrimSpace(p.Image.MediaType)
				if mt == "" {
					mt = "image/png"
				}
				url = llm.DataURI(mt, p.Image.Data)
			} else if llm.IsLocalPath(url) {
				path := llm.ExpandTilde(url)
				b, err := os.ReadFile(path)
				if err != nil {
					return nil, err
				}
				mt := strings.TrimSpace(p.Image.MediaType)
				if mt == "" {
					mt = llm.InferMimeTypeFromPath(path)
				}
				if mt == "" {
					mt = "image/png"
				}
				url = llm.DataURI(mt, b)
			}
			if url == "" {
				continue
			}
			imageURL := map[string]any{"url": url}
			if p.Image.Detail != "" {
				imageURL["detail"] = p.Image.Detail
			}
			out = append(out, map[string]any{"type": "image_url", "image_url": imageURL})
		case llm.ContentDocument:
			if p.Document == nil {
				continue
			}
			fileData := strings.TrimSpace(p.Document.URL)
			if len(p.Document.Data) > 0 {
				mt := strings.TrimSpace(p.Document.MediaType)
				if mt == "" {
					mt = "application/pdf"
				}
				fileData = llm.DataURI(mt, p.Document.Data)
			} else if llm.IsLocalPath(fileData) {
				path := llm.ExpandTilde(fileData)
				b, err := os.ReadFile(path)
				if err != nil {
					return nil, err
				}
				mt := strings.TrimSpace(p.Document.MediaType)
				if mt == "" {
					mt = llm.InferMimeTypeFromPath(path)
				}
				if mt == "" {
					mt = "application/pdf"
				}
				fileData = llm.DataURI(mt, b)
			}
			if fileData == "" {
				continue
			}
			file := map[string]any{"file_data": fileData}
			if p.Document.FileName != "" {
				file["filename"] = p.Document.FileName
			}
			out = append(out, map[string]any{"type": "file", "file": file})
		case llm.ContentAudio:
			return nil, &llm.ConfigurationError{Message: fmt.Sprintf("unsupported content kind for openai chat fallback: %s", p.Kind)}
		}
	}
	return out, nil
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
