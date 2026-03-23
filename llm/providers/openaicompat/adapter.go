// Package openaicompat implements a Chat Completions adapter for OpenAI-compatible
// services (Ollama, vLLM, LiteLLM, etc.). It registers as provider "openai-compatible"
// and uses the standard /v1/chat/completions endpoint.
package openaicompat

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"primeradiant.com/serf/llm"
)

// ProviderQuirks configures per-provider behavioral overrides for OpenAI-compatible
// APIs that deviate from the standard Chat Completions contract.
type ProviderQuirks struct {
	// LockTemperature strips temperature from requests (provider fixes it).
	LockTemperature bool
	// LockTopP strips top_p from requests (provider fixes it).
	LockTopP bool
	// LockFrequencyPenalty strips frequency_penalty from requests.
	LockFrequencyPenalty bool
	// LockPresencePenalty strips presence_penalty from requests.
	LockPresencePenalty bool
	// ToolChoiceAutoOnly restricts tool_choice to "auto" or "none" (no "required" or named).
	ToolChoiceAutoOnly bool
	// MaxStopSequences limits the number of stop sequences (0 = unlimited).
	MaxStopSequences int
	// StripEmptyContent removes message content parts with empty text.
	StripEmptyContent bool
	// NoJSONSchema downgrades json_schema response_format to json_object.
	NoJSONSchema bool
	// FinishReasonMap maps non-standard finish reasons to canonical values.
	// E.g., {"sensitive": "content_filter", "network_error": "error"}
	FinishReasonMap map[string]string
}

// mapFinishReason translates a provider-specific finish reason using the quirk map.
// If no mapping exists, returns the original value unchanged.
func (q ProviderQuirks) mapFinishReason(raw string) string {
	if q.FinishReasonMap == nil {
		return raw
	}
	if mapped, ok := q.FinishReasonMap[raw]; ok {
		return mapped
	}
	return raw
}

// QuirksPreset returns a ProviderQuirks configuration for a known provider name.
// Unknown names return zero-value quirks (no restrictions).
func QuirksPreset(name string) ProviderQuirks {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "kimi-k2.5", "kimi", "moonshot":
		return ProviderQuirks{
			LockTemperature:      true,
			LockTopP:             true,
			LockFrequencyPenalty: true,
			LockPresencePenalty:  true,
			ToolChoiceAutoOnly:   true,
			NoJSONSchema:         true,
		}
	case "glm-5", "glm-5-turbo", "glm", "zhipu":
		return ProviderQuirks{
			StripEmptyContent:  true,
			ToolChoiceAutoOnly: true,
			MaxStopSequences:   1,
			NoJSONSchema:       true,
			FinishReasonMap: map[string]string{
				"sensitive":     "content_filter",
				"network_error": "error",
			},
		}
	default:
		return ProviderQuirks{}
	}
}

// Adapter implements llm.ProviderAdapter for OpenAI-compatible services.
type Adapter struct {
	APIKey         string
	BaseURL        string
	Client         *http.Client
	DefaultHeaders map[string]string
	Quirks         ProviderQuirks
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

	var quirks ProviderQuirks
	if preset := strings.TrimSpace(os.Getenv("OPENAI_COMPATIBLE_PROVIDER_QUIRKS")); preset != "" {
		quirks = QuirksPreset(preset)
	}

	return &Adapter{
		APIKey:  key,
		BaseURL: strings.TrimRight(base, "/"),
		Client:  &http.Client{Timeout: 0},
		Quirks:  quirks,
	}, nil
}

func (a *Adapter) Name() string { return "openai-compatible" }

// ListModels fetches available models from the /v1/models endpoint.
func (a *Adapter) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	if a.Client == nil {
		a.Client = &http.Client{Timeout: 0}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, a.BaseURL+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	for k, v := range a.DefaultHeaders {
		httpReq.Header.Set(k, v)
	}
	if a.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+a.APIKey)
	}

	resp, err := a.Client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list models: HTTP %d", resp.StatusCode)
	}

	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	models := make([]llm.ModelInfo, 0, len(result.Data))
	for _, m := range result.Data {
		models = append(models, llm.ModelInfo{
			ID:          m.ID,
			Provider:    "openai-compatible",
			DisplayName: m.ID,
		})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

// Complete sends a non-streaming Chat Completions request.
func (a *Adapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if a.Client == nil {
		a.Client = &http.Client{Timeout: 0}
	}

	body, err := buildRequestBody(req, false, a.Quirks)
	if err != nil {
		return llm.Response{}, err
	}

	ctx, adapterCancel := llm.ApplyAdapterTimeout(ctx, req.AdapterTimeout, false)
	defer adapterCancel()

	raw, statusCode, headers, err := a.doHTTP(ctx, body, req.AdapterTimeout)
	if err != nil {
		return llm.Response{}, err
	}
	if statusCode != http.StatusOK {
		msg := extractErrorMessage(raw)
		retryAfter := llm.ParseRetryAfter(headers.Get("Retry-After"), time.Now())
		return llm.Response{}, llm.ErrorFromHTTPStatus("openai-compatible", statusCode, msg, raw, retryAfter)
	}

	resp, err := fromChatCompletionResponse(raw, a.Quirks)
	if err != nil {
		return resp, err
	}
	resp.RateLimit = llm.ParseRateLimitHeaders(headers)
	return resp, nil
}

// Stream sends a streaming Chat Completions request.
func (a *Adapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	if a.Client == nil {
		a.Client = &http.Client{Timeout: 0}
	}
	ctx, timeoutCancel := llm.ApplyAdapterTimeout(ctx, req.AdapterTimeout, true)
	defer timeoutCancel()

	body, err := buildRequestBody(req, true, a.Quirks)
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
	// Apply default headers first so provider-specific headers take precedence.
	for k, v := range a.DefaultHeaders {
		httpReq.Header.Set(k, v)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if a.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+a.APIKey)
	}

	client := llm.ClientWithConnectTimeout(a.Client, req.AdapterTimeout)
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, llm.WrapContextError("openai-compatible", err)
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close() //nolint:errcheck
		b, _ := io.ReadAll(resp.Body)
		var raw map[string]any
		_ = json.Unmarshal(b, &raw)
		msg := extractErrorMessage(raw)
		retryAfter := llm.ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		return nil, llm.ErrorFromHTTPStatus("openai-compatible", resp.StatusCode, msg, raw, retryAfter)
	}

	sctx, cancel := context.WithCancel(ctx)
	s := llm.NewChanStream(cancel)
	rl := llm.ParseRateLimitHeaders(resp.Header)

	s.Send(llm.StreamEvent{Type: llm.StreamEventStreamStart})

	go func() {
		defer func() {
			resp.Body.Close() //nolint:errcheck
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
		var reasoningStarted bool
		var reasoningBuf strings.Builder
		var model string
		var finishReason string
		var usage *llm.Usage
		finished := false

		_ = llm.ParseSSE(sctx, resp.Body, func(ev llm.SSEEvent) error {
			data := string(ev.Data)
			if data == "[DONE]" {
				finished = true

				// Close reasoning if still open (e.g., reasoning only, no text or tool calls).
				if reasoningStarted && !textStarted && len(toolCalls) == 0 {
					s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningEnd})
				}

				// Collect tool call data before emitting end events (for final response).
				var completedToolCalls []llm.ToolCallData
				for idx, tc := range toolCalls {
					tcd := llm.ToolCallData{
						ID:        tc.id,
						Name:      tc.name,
						Arguments: json.RawMessage(tc.args.String()),
						Type:      "function",
					}
					completedToolCalls = append(completedToolCalls, tcd)
					s.Send(llm.StreamEvent{
						Type:     llm.StreamEventToolCallEnd,
						ToolCall: &tcd,
					})
					delete(toolCalls, idx)
				}

				if textStarted {
					s.Send(llm.StreamEvent{Type: llm.StreamEventTextEnd, TextID: "text_0"})
				}

				// Build final response.
				msg := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{}}
				if reasoningBuf.Len() > 0 {
					msg.Content = append(msg.Content, llm.ContentPart{
						Kind:     llm.ContentThinking,
						Thinking: &llm.ThinkingData{Text: reasoningBuf.String()},
					})
				}
				if textBuf.Len() > 0 {
					msg.Content = append(msg.Content, llm.ContentPart{Kind: llm.ContentText, Text: textBuf.String()})
				}
				for i := range completedToolCalls {
					msg.Content = append(msg.Content, llm.ContentPart{
						Kind:     llm.ContentToolCall,
						ToolCall: &completedToolCalls[i],
					})
				}
				// Apply quirk finish reason mapping, preserving the original raw value.
				rawFinish := finishReason
				mappedFinish := a.Quirks.mapFinishReason(rawFinish)
				var finish llm.FinishReason
				if mappedFinish == rawFinish {
					finish = llm.NormalizeFinishReason("", rawFinish)
				} else {
					finish = llm.FinishReason{Reason: mappedFinish, Raw: rawFinish}
				}
				finalResp := &llm.Response{
					Provider:  "openai-compatible",
					Model:     model,
					Message:   msg,
					Finish:    finish,
					RateLimit: rl,
				}
				if usage != nil {
					finalResp.Usage = *usage
				}
				// Estimate reasoning tokens from reasoning buffer when not natively reported.
				if finalResp.Usage.ReasoningTokens == nil && reasoningBuf.Len() > 0 {
					estimated := reasoningBuf.Len() / 4
					if estimated < 1 {
						estimated = 1
					}
					finalResp.Usage.ReasoningTokens = &estimated
					if usage != nil {
						usage.ReasoningTokens = &estimated
					}
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
					InputTokens:  llm.IntFromAny(chunk.Usage["prompt_tokens"]),
					OutputTokens: llm.IntFromAny(chunk.Usage["completion_tokens"]),
					Raw:          chunk.Usage,
				}
				u.TotalTokens = u.InputTokens + u.OutputTokens
				if v := llm.IntFromAny(chunk.Usage["total_tokens"]); v > 0 {
					u.TotalTokens = v
				}
				// Extract native reasoning tokens from completion_tokens_details.
				if details, ok := chunk.Usage["completion_tokens_details"].(map[string]any); ok {
					if rt := llm.IntFromAny(details["reasoning_tokens"]); rt > 0 {
						u.ReasoningTokens = &rt
					}
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

			// Reasoning content delta.
			if choice.Delta.ReasoningContent != "" {
				if !reasoningStarted {
					s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningStart})
					reasoningStarted = true
				}
				reasoningBuf.WriteString(choice.Delta.ReasoningContent)
				s.Send(llm.StreamEvent{
					Type:           llm.StreamEventReasoningDelta,
					ReasoningDelta: choice.Delta.ReasoningContent,
				})
			}

			// Text delta — close reasoning first if transitioning from reasoning to text.
			if choice.Delta.Content != "" {
				if reasoningStarted && !textStarted {
					s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningEnd})
				}
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
					// Close reasoning before the first tool call if needed.
					if reasoningStarted && !textStarted {
						s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningEnd})
						reasoningStarted = false // Prevent double-close in [DONE].
					}
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
		}, llm.StreamReadSSEOptions(req.AdapterTimeout)...)

		if !finished {
			if err := sctx.Err(); err != nil {
				s.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: llm.WrapContextError("openai-compatible", err)})
			}
		}
	}()

	return s, nil
}

// --- Request building ---

func buildRequestBody(req llm.Request, stream bool, quirks ProviderQuirks) (map[string]any, error) {
	body := map[string]any{
		"model": req.Model,
	}

	msgs, err := toChatMessages(req.Messages, quirks)
	if err != nil {
		return nil, err
	}
	body["messages"] = msgs

	if len(req.Tools) > 0 {
		body["tools"] = toChatTools(req.Tools)
	}
	if req.ToolChoice != nil {
		tc, err := toChatToolChoice(*req.ToolChoice)
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

	// Apply provider quirks.
	if quirks.LockTemperature {
		delete(body, "temperature")
	}
	if quirks.LockTopP {
		delete(body, "top_p")
	}
	if quirks.LockFrequencyPenalty {
		delete(body, "frequency_penalty")
	}
	if quirks.LockPresencePenalty {
		delete(body, "presence_penalty")
	}
	if quirks.ToolChoiceAutoOnly {
		if tc, ok := body["tool_choice"]; ok {
			switch tc {
			case "auto", "none":
				// allowed
			default:
				body["tool_choice"] = "auto"
			}
		}
	}
	if quirks.MaxStopSequences > 0 {
		if stops, ok := body["stop"].([]string); ok && len(stops) > quirks.MaxStopSequences {
			body["stop"] = stops[:quirks.MaxStopSequences]
		}
	}
	if quirks.NoJSONSchema {
		if rf, ok := body["response_format"].(map[string]any); ok {
			if rf["type"] == "json_schema" {
				body["response_format"] = map[string]any{"type": "json_object"}
			}
		}
	}

	return body, nil
}

func toChatMessages(messages []llm.Message, quirks ProviderQuirks) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(messages))
	for _, m := range messages {
		// Strip empty text parts when the quirk is enabled.
		content := m.Content
		if quirks.StripEmptyContent {
			filtered := make([]llm.ContentPart, 0, len(content))
			for _, p := range content {
				if p.Kind == llm.ContentText && p.Text == "" {
					continue
				}
				filtered = append(filtered, p)
			}
			content = filtered
		}
		switch m.Role {
		case llm.RoleSystem, llm.RoleDeveloper:
			out = append(out, map[string]any{
				"role":    "system",
				"content": textFromParts(content),
			})
		case llm.RoleUser:
			if hasImageContent(content) {
				out = append(out, map[string]any{
					"role":    "user",
					"content": buildMultimodalParts(content),
				})
			} else {
				out = append(out, map[string]any{
					"role":    "user",
					"content": textFromParts(content),
				})
			}
		case llm.RoleAssistant:
			msg := map[string]any{
				"role": "assistant",
			}
			text := textFromParts(content)
			calls := toolCallsFromParts(content)
			reasoning := thinkingFromParts(content)
			if reasoning != "" {
				msg["reasoning_content"] = reasoning
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
			for _, p := range content {
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

// thinkingFromParts concatenates all ContentThinking parts' text.
func thinkingFromParts(parts []llm.ContentPart) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Kind == llm.ContentThinking && p.Thinking != nil && p.Thinking.Text != "" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(p.Thinking.Text)
		}
	}
	return b.String()
}

// hasImageContent returns true if any content part is an image.
func hasImageContent(parts []llm.ContentPart) bool {
	for _, p := range parts {
		if p.Kind == llm.ContentImage {
			return true
		}
	}
	return false
}

// buildMultimodalParts converts content parts to Chat Completions content array format
// with {"type": "text", ...} and {"type": "image_url", ...} objects.
func buildMultimodalParts(parts []llm.ContentPart) []map[string]any {
	var out []map[string]any
	for _, p := range parts {
		switch p.Kind {
		case llm.ContentText:
			out = append(out, map[string]any{"type": "text", "text": p.Text})
		case llm.ContentImage:
			if p.Image == nil {
				continue
			}
			imgURL := p.Image.URL
			if imgURL == "" && len(p.Image.Data) > 0 {
				mt := p.Image.MediaType
				if mt == "" {
					mt = "image/png"
				}
				imgURL = "data:" + mt + ";base64," + base64.StdEncoding.EncodeToString(p.Image.Data)
			}
			if imgURL == "" {
				continue
			}
			urlObj := map[string]any{"url": imgURL}
			if p.Image.Detail != "" {
				urlObj["detail"] = p.Image.Detail
			}
			out = append(out, map[string]any{"type": "image_url", "image_url": urlObj})
		}
	}
	return out
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

func toChatToolChoice(tc llm.ToolChoice) (any, error) {
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
		return nil, llm.NewUnsupportedToolChoiceError("openai-compatible", tc.Mode)
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
	ID      string         `json:"id"`
	Model   string         `json:"model"`
	Choices []chatChoice   `json:"choices"`
	Usage   map[string]any `json:"usage"`
}

type chatChoice struct {
	Index        int         `json:"index"`
	Message      chatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type chatMessage struct {
	Role             string         `json:"role"`
	Content          string         `json:"content"`
	ReasoningContent string         `json:"reasoning_content,omitempty"`
	ToolCalls        []chatToolCall `json:"tool_calls,omitempty"`
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
	ID      string            `json:"id"`
	Model   string            `json:"model"`
	Choices []chatChunkChoice `json:"choices"`
	Usage   map[string]any    `json:"usage"`
}

type chatChunkChoice struct {
	Index        int       `json:"index"`
	Delta        chatDelta `json:"delta"`
	FinishReason string    `json:"finish_reason"`
}

type chatDelta struct {
	Role             string              `json:"role"`
	Content          string              `json:"content"`
	ReasoningContent string              `json:"reasoning_content,omitempty"`
	ToolCalls        []chatChunkToolCall `json:"tool_calls,omitempty"`
}

type chatChunkToolCall struct {
	Index    int              `json:"index"`
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type,omitempty"`
	Function chatFunctionCall `json:"function"`
}

func fromChatCompletionResponse(raw map[string]any, quirks ProviderQuirks) (llm.Response, error) {
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
	if choice.Message.ReasoningContent != "" {
		parts = append(parts, llm.ContentPart{
			Kind:     llm.ContentThinking,
			Thinking: &llm.ThinkingData{Text: choice.Message.ReasoningContent},
		})
	}
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

	// Map the provider-specific finish reason using quirks, preserving the original in .Raw.
	rawFinish := choice.FinishReason
	mappedFinish := quirks.mapFinishReason(rawFinish)
	var finish llm.FinishReason
	if mappedFinish == rawFinish {
		finish = llm.NormalizeFinishReason("", rawFinish)
	} else {
		finish = llm.FinishReason{Reason: mappedFinish, Raw: rawFinish}
	}

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
			InputTokens:  llm.IntFromAny(parsed.Usage["prompt_tokens"]),
			OutputTokens: llm.IntFromAny(parsed.Usage["completion_tokens"]),
			Raw:          parsed.Usage,
		}
		resp.Usage.TotalTokens = resp.Usage.InputTokens + resp.Usage.OutputTokens
		if v := llm.IntFromAny(parsed.Usage["total_tokens"]); v > 0 {
			resp.Usage.TotalTokens = v
		}

		// Extract native reasoning tokens from completion_tokens_details.
		if details, ok := parsed.Usage["completion_tokens_details"].(map[string]any); ok {
			if rt := llm.IntFromAny(details["reasoning_tokens"]); rt > 0 {
				resp.Usage.ReasoningTokens = &rt
			}
		}
	}

	// Estimate reasoning tokens from thinking content when not natively reported.
	if resp.Usage.ReasoningTokens == nil {
		var thinkingChars int
		for _, p := range parts {
			if p.Kind == llm.ContentThinking && p.Thinking != nil {
				thinkingChars += len(p.Thinking.Text)
			}
		}
		if thinkingChars > 0 {
			estimated := thinkingChars / 4
			if estimated < 1 {
				estimated = 1
			}
			resp.Usage.ReasoningTokens = &estimated
		}
	}

	return resp, nil
}

// --- HTTP helpers ---

func (a *Adapter) doHTTP(ctx context.Context, body map[string]any, at *llm.AdapterTimeout) (map[string]any, int, http.Header, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, 0, nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.BaseURL+"/v1/chat/completions", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, 0, nil, err
	}
	// Apply default headers first so provider-specific headers take precedence.
	for k, v := range a.DefaultHeaders {
		httpReq.Header.Set(k, v)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if a.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+a.APIKey)
	}

	client := llm.ClientWithConnectTimeout(a.Client, at)
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, 0, nil, llm.WrapContextError("openai-compatible", err)
	}
	defer resp.Body.Close() //nolint:errcheck

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
