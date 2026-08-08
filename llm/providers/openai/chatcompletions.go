package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"slices"
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
	parentCtx := ctx
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

	client := llm.ClientWithAdapterTimeout(a.Client, req.AdapterTimeout)
	resp, attempt, err := transport.DoWithAPIAttempts(parentCtx, client, httpReq, func(wireRequest *http.Request, requestBody []byte) llm.APIAttemptMeta {
		return llm.APIAttemptMeta{
			ProviderInstance:   a.apiLogProviderInstance(),
			RequestModel:       req.Model,
			HistoryMode:        req.HistoryMode,
			EndpointFamily:     "openai_chat_completions",
			RequestBody:        requestBody,
			CredentialMaterial: a.apiLogCredentialMaterial(wireRequest),
		}
	})
	if err != nil {
		timeoutSource := llm.APITimeoutSourceForTransport(parentCtx, sctx, err)
		returnedErr := llm.WrapContextError("openai", err)
		attempt.Complete(llm.APIAttemptResult{Err: returnedErr}, timeoutSource, nil, err)
		cancel()
		return nil, returnedErr
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		rawBytes, readErr := io.ReadAll(resp.Body)
		var raw map[string]any
		dec := json.NewDecoder(bytes.NewReader(rawBytes))
		dec.UseNumber()
		jsonErr := dec.Decode(&raw)
		ra := llm.ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		msg := llm.ProviderFailureMessage("chat.completions(stream)", rawBytes)
		returnedErr := llm.ErrorFromHTTPStatus("openai", resp.StatusCode, msg, raw, ra)
		decodeErr := jsonErr
		if readErr != nil {
			decodeErr = readErr
		}
		attempt.Complete(llm.APIAttemptResult{
			StatusCode:   resp.StatusCode,
			ResponseBody: rawBytes,
			Err:          returnedErr,
		}, llm.APITimeoutNone, decodeErr, nil)
		cancel()
		return nil, returnedErr
	}

	s := llm.NewChanStream(cancel)
	s.Send(llm.StreamEvent{Type: llm.StreamEventStreamStart})

	go a.decodeChatCompletionsStream(sctx, cancel, resp, s, req, jsonBody, attempt)

	return s, nil
}

// chatCompletionsChunk is the wire shape of one Chat Completions SSE data
// payload (a "chat.completion.chunk"). Shared by the live streaming decoder
// (decodeChatCompletionsStream) and offline recomputation
// (extractChatCompletionsFromSSE in responses_recompute.go) so both decode
// the identical wire struct.
type chatCompletionsChunk struct {
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
	Usage map[string]any          `json:"usage"`
	Error *openaichat.InbandError `json:"error"`
}

// inbandStreamError decodes a failure payload delivered inside an HTTP 200
// stream into the typed error hierarchy. operation names the endpoint for the
// message, matching the non-2xx path's wording; raw is the undecoded event so
// the error carries the provider's own payload.
func inbandStreamError(operation string, e *openaichat.InbandError, raw map[string]any) error {
	msg := strings.TrimSpace(e.Message)
	if msg == "" {
		msg = "provider reported an in-band stream error"
	}
	return llm.ErrorFromHTTPStatus("openai", e.StatusCode(), operation+": "+msg, raw, nil)
}

// chatCompletionsToolCallState accumulates one tool call's streamed
// arguments, keyed by its wire delta index (see
// chatCompletionsChunkAccumulator).
type chatCompletionsToolCallState struct {
	id   string
	name string
	args strings.Builder
}

// chatCompletionsChunkAccumulator merges a Chat Completions SSE chunk
// stream's deltas -- text content, tool-call argument fragments keyed by
// wire index, and the trailing model/usage/finish_reason fields -- into the
// settled Response both the live stream decoder
// (decodeChatCompletionsStream) and offline recomputation
// (extractChatCompletionsFromSSE) derive once the stream ends. One
// implementation shared by both so they can't silently drift.
type chatCompletionsChunkAccumulator struct {
	toolCalls    map[int]*chatCompletionsToolCallState
	textBuf      strings.Builder
	finishReason string
	model        string
	usage        *llm.Usage
}

func newChatCompletionsChunkAccumulator() *chatCompletionsChunkAccumulator {
	return &chatCompletionsChunkAccumulator{toolCalls: map[int]*chatCompletionsToolCallState{}}
}

// HandleChunkMeta merges a chunk's model/usage fields: last non-empty model
// wins, most recent usage wins (matches the wire stream's convention of
// repeating these on later chunks).
func (acc *chatCompletionsChunkAccumulator) HandleChunkMeta(model string, usage map[string]any) {
	if model != "" {
		acc.model = model
	}
	if usage != nil {
		u := openaichat.ParseChatUsage(usage)
		acc.usage = &u
	}
}

// HandleFinishReason merges a choice's finish_reason; empty is a no-op
// (intermediate chunks in a tool-call stream carry it empty).
func (acc *chatCompletionsChunkAccumulator) HandleFinishReason(reason string) {
	if reason != "" {
		acc.finishReason = reason
	}
}

// HandleContentDelta appends one choice's content delta to the accumulated
// text.
func (acc *chatCompletionsChunkAccumulator) HandleContentDelta(content string) {
	acc.textBuf.WriteString(content)
}

// HandleToolCallDelta merges one delta chunk's tool-call fragment (keyed by
// its wire index) into accumulated state. isNew reports whether this index
// was seen for the first time -- the live decoder's cue to emit a
// tool-call-start event.
func (acc *chatCompletionsChunkAccumulator) HandleToolCallDelta(index int, id, name, argumentsDelta string) (state *chatCompletionsToolCallState, isNew bool) {
	state, exists := acc.toolCalls[index]
	if !exists {
		state = &chatCompletionsToolCallState{id: id, name: name}
		acc.toolCalls[index] = state
		isNew = true
	}
	if argumentsDelta != "" {
		state.args.WriteString(argumentsDelta)
	}
	return state, isNew
}

// SortedToolCalls returns accumulated tool calls in wire (delta-index)
// order -- the map's own iteration order is randomized, so ordering by
// index is the deterministic parallel-tool-call assembly rule both callers
// share.
func (acc *chatCompletionsChunkAccumulator) SortedToolCalls() []llm.ToolCallData {
	order := slices.Sorted(maps.Keys(acc.toolCalls))
	out := make([]llm.ToolCallData, 0, len(order))
	for _, idx := range order {
		tc := acc.toolCalls[idx]
		out = append(out, llm.ToolCallData{
			ID:        tc.id,
			Name:      tc.name,
			Arguments: json.RawMessage(tc.args.String()),
			Type:      "function",
		})
	}
	return out
}

// Settle builds the settled Response once the chunk stream ends: the
// assembled message (text, then tool calls, matching
// decodeChatCompletionsStream's wire-order convention), the normalized
// finish reason, and usage. Model is whatever the chunks themselves
// carried -- possibly empty; callers apply their own fallback (or none,
// matching the live decoder, which never falls back to the requested
// model).
func (acc *chatCompletionsChunkAccumulator) Settle() llm.Response {
	msg := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{}}
	if acc.textBuf.Len() > 0 {
		msg.Content = append(msg.Content, llm.ContentPart{Kind: llm.ContentText, Text: acc.textBuf.String()})
	}
	toolCalls := acc.SortedToolCalls()
	for i := range toolCalls {
		msg.Content = append(msg.Content, llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &toolCalls[i]})
	}
	r := llm.Response{Provider: "openai", Model: acc.model, Message: msg, Finish: llm.NormalizeFinishReason("", acc.finishReason)}
	if acc.usage != nil {
		r.Usage = *acc.usage
	}
	return r
}

// decodeChatCompletionsStream consumes the Chat Completions SSE stream in its own
// goroutine: it translates each chunk into llm stream events and emits the final
// accumulated Response on [DONE]. It owns closing the response body and the
// ChanStream.
func (a *Adapter) decodeChatCompletionsStream(sctx context.Context, cancel context.CancelFunc, resp *http.Response, s *llm.ChanStream, req llm.Request, _ []byte, attempt *transport.APIAttemptCapture) {
	defer func() {
		_ = resp.Body.Close()
		s.CloseSend()
	}()

	acc := newChatCompletionsChunkAccumulator()
	var textStarted bool
	finished := false
	var finalEvent *llm.StreamEvent

	runner := &transport.StreamRunner{
		Provider:   "openai",
		Resp:       resp,
		Stream:     s,
		Attempt:    attempt,
		StatusCode: resp.StatusCode,
		FinalEvent: func() *llm.StreamEvent {
			return finalEvent
		},
		SSEOpts:       llm.StreamReadSSEOptions(req.AdapterTimeout),
		Finished:      &finished,
		IncompleteMsg: fmt.Sprintf("chat.completions stream closed without [DONE] (model: %q)", req.Model),
		OnEvent: func(ev llm.SSEEvent) error {
			data := string(ev.Data)
			if data == "[DONE]" {
				finished = true

				// Emit tool call end events in wire (delta-index) order.
				toolCalls := acc.SortedToolCalls()
				for i := range toolCalls {
					s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &toolCalls[i]})
				}
				if textStarted {
					s.Send(llm.StreamEvent{Type: llm.StreamEventTextEnd, TextID: "text_0"})
				}

				finalResp := acc.Settle()
				llm.StampEndpointURL(&finalResp, llm.FinalResponseEndpointURL(resp, a.chatCompletionsURL()), a.apiLogCredentialMaterial(nil))
				event := llm.StreamEvent{
					Type:         llm.StreamEventFinish,
					FinishReason: &finalResp.Finish,
					Usage:        acc.usage,
					Response:     &finalResp,
				}
				if attempt.Active() {
					finalEvent = &event
				} else {
					s.Send(event)
				}
				cancel()
				return nil
			}

			var chunk chatCompletionsChunk
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				// Skip a single malformed chunk and keep the stream alive;
				// returning the error would abort the whole stream.
				return nil //nolint:nilerr // unparseable chunk is intentionally skipped, not fatal
			}
			if chunk.Error != nil {
				// In-band provider failure on an HTTP 200 stream. Decode it
				// into the typed error hierarchy and end the stream with it —
				// falling through would drop the payload at the choices check
				// and degrade the failure to the generic incomplete-stream
				// error, hiding rate-limit/quota/upstream causes from retry
				// logic and forensics.
				var raw map[string]any
				_ = json.Unmarshal([]byte(data), &raw)
				return &transport.FatalStreamError{
					Err: inbandStreamError("chat.completions(stream)", chunk.Error, raw),
				}
			}
			acc.HandleChunkMeta(chunk.Model, chunk.Usage)
			if len(chunk.Choices) == 0 {
				return nil
			}
			choice := chunk.Choices[0]
			acc.HandleFinishReason(choice.FinishReason)
			if choice.Delta.Content != "" {
				if !textStarted {
					s.Send(llm.StreamEvent{Type: llm.StreamEventTextStart, TextID: "text_0"})
					textStarted = true
				}
				acc.HandleContentDelta(choice.Delta.Content)
				s.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, TextID: "text_0", Delta: choice.Delta.Content})
			}
			for _, tc := range choice.Delta.ToolCalls {
				state, isNew := acc.HandleToolCallDelta(tc.Index, tc.ID, tc.Function.Name, tc.Function.Arguments)
				if isNew {
					s.Send(llm.StreamEvent{
						Type: llm.StreamEventToolCallStart,
						ToolCall: &llm.ToolCallData{
							ID:   state.id,
							Name: state.name,
							Type: "function",
						},
					})
				}
				if tc.Function.Arguments != "" {
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
		// max_completion_tokens, not the legacy max_tokens: reasoning models
		// (which reach this fallback — reasoning_effort is set below) reject
		// max_tokens with a 400, and current models all accept the new field.
		body["max_completion_tokens"] = *req.MaxTokens
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
		maps.Copy(body, opts)
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
							"arguments": openaichat.ToolArgumentsString(p.ToolCall.Arguments),
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
