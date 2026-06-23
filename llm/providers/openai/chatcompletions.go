package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/internal/openaichat"
	"primeradiant.com/serf/llm/providers/internal/transport"
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
		rawBytes, _ := io.ReadAll(resp.Body)
		var raw map[string]any
		dec := json.NewDecoder(bytes.NewReader(rawBytes))
		dec.UseNumber()
		_ = dec.Decode(&raw)
		ra := llm.ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		msg := fmt.Sprintf("chat.completions(stream) failed: %v", raw)
		cancel()
		return nil, llm.ErrorFromHTTPStatusWithRawBodies("openai", resp.StatusCode, msg, raw, ra, string(jsonBody), string(rawBytes))
	}

	s := llm.NewChanStream(cancel)
	s.Send(llm.StreamEvent{Type: llm.StreamEventStreamStart})

	go a.decodeChatCompletionsStream(sctx, cancel, resp, s, req, jsonBody)

	return s, nil
}

// decodeChatCompletionsStream consumes the Chat Completions SSE stream in its own
// goroutine: it translates each chunk into llm stream events and emits the final
// accumulated Response on [DONE]. It owns closing the response body and the
// ChanStream.
func (a *Adapter) decodeChatCompletionsStream(sctx context.Context, cancel context.CancelFunc, resp *http.Response, s *llm.ChanStream, req llm.Request, jsonBody []byte) {
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

	rawReqBody := string(jsonBody)

	runner := &transport.StreamRunner{
		Provider:       "openai",
		Resp:           resp,
		RawRequestBody: rawReqBody,
		Stream:         s,
		SSEOpts:        llm.StreamReadSSEOptions(req.AdapterTimeout),
		Finished:       &finished,
		IncompleteMsg:  fmt.Sprintf("chat.completions stream closed without [DONE] (model: %q)", req.Model),
		OnEvent: func(ev llm.SSEEvent, sseBuf *bytes.Buffer) error {
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
				// Skip a single malformed chunk and keep the stream alive;
				// returning the error would abort the whole stream.
				return nil //nolint:nilerr // unparseable chunk is intentionally skipped, not fatal
			}
			if chunk.Model != "" {
				model = chunk.Model
			}
			if chunk.Usage != nil {
				u := openaichat.ParseChatUsage(chunk.Usage)
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
		},
	}
	runner.Run(sctx)
}

// buildChatCompletionsBody translates an llm.Request into a Chat Completions
// request body. This is used by the Responses-API fallback path.
func buildChatCompletionsBody(req llm.Request, stream bool) (map[string]any, error) {
	if requestHasToolResultImages(req) {
		return nil, errors.New("openai chat completions fallback does not support tool-result images")
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
		body["tools"] = openaichat.ToChatTools(req.Tools)
	}
	if req.ToolChoice != nil {
		tc, err := toChatCompletionsToolChoice(*req.ToolChoice)
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
		body["response_format"] = openaichat.ToChatResponseFormat(*req.ResponseFormat)
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

// toChatCompletionsToolChoice converts a tool choice to the OpenAI Chat Completions
// wire shape. A forced function is expressed as
// {"type":"function","function":{"name":"X"}} — the function name NESTED under
// "function". This is the shape the Chat Completions endpoint requires, and differs
// from the Responses API, which puts the name at the top level (see
// toResponsesToolChoice in responses.go).
func toChatCompletionsToolChoice(tc llm.ToolChoice) (any, error) {
	switch strings.ToLower(strings.TrimSpace(tc.Mode)) {
	case "", "auto":
		return "auto", nil
	case "none":
		return "none", nil
	case "required":
		return "required", nil
	case "named":
		if strings.TrimSpace(tc.Name) == "" {
			return nil, &llm.ConfigurationError{Message: "tool_choice mode=named requires name"}
		}
		return map[string]any{
			"type":     "function",
			"function": map[string]any{"name": tc.Name},
		}, nil
	default:
		// Backward-compatible: some callers may have used an unspecified mode to force
		// a particular tool. Prefer explicit mode="named".
		if strings.TrimSpace(tc.Name) != "" {
			return map[string]any{
				"type":     "function",
				"function": map[string]any{"name": tc.Name},
			}, nil
		}
		return nil, llm.NewUnsupportedToolChoiceError("openai", tc.Mode)
	}
}
