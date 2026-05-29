// Package openaicompat implements a Chat Completions adapter for OpenAI-compatible
// services (Ollama, vLLM, LiteLLM, etc.). It registers as provider "openai-compatible".
// BaseURL should include the version prefix (e.g. "https://api.openai.com/v1").
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
	"regexp"
	"sort"
	"strings"
	"time"

	"primeradiant.com/serf/internal/providerconfig"
	"primeradiant.com/serf/llm"
)

// ProviderQuirks configures per-provider behavioral overrides for OpenAI-compatible
// APIs that deviate from the standard Chat Completions contract.
type ProviderQuirks struct {
	LockTemperature       bool
	LockTopP              bool
	LockFrequencyPenalty  bool
	LockPresencePenalty   bool
	ToolChoiceAutoOnly    bool
	MaxStopSequences      int
	StripEmptyContent     bool
	NoJSONSchema          bool
	FinishReasonMap       map[string]string
	TranslateMaxToXHigh   bool // OpenRouter vocab: our "max" → their "xhigh"
}

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
	case "openrouter":
		return ProviderQuirks{
			TranslateMaxToXHigh: true,
		}
	default:
		return ProviderQuirks{}
	}
}

type Adapter struct {
	name           string
	APIKey         string
	BaseURL        string
	Client         *http.Client
	DefaultHeaders map[string]string
	Quirks         ProviderQuirks
}

// OpenAICompatInstanceParams holds the configuration for a single openai-compatible adapter instance.
type OpenAICompatInstanceParams struct {
	Name    string
	BaseURL string
	APIKey  string
	Quirks  ProviderQuirks
}

// NewForInstance constructs an Adapter from explicit parameters.
func NewForInstance(params OpenAICompatInstanceParams) *Adapter {
	return &Adapter{
		name:    params.Name,
		APIKey:  params.APIKey,
		BaseURL: strings.TrimRight(params.BaseURL, "/"),
		Client:  &http.Client{Timeout: 0},
		Quirks:  params.Quirks,
	}
}

func init() {
	llm.RegisterEnvAdapterFactory(func(_ llm.EnvConfig) (llm.ProviderAdapter, bool, error) {
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
	// Register for the openai+chat-completions fold-in: an openai instance with
	// apiStyle=chat-completions routes through the openaicompat adapter.
	llm.RegisterInstanceAdapterFactory("openai", "chat-completions", func(inst providerconfig.InstanceConfig, _ string) (llm.ProviderAdapter, error) {
		return NewForInstance(OpenAICompatInstanceParams{
			Name:    inst.Name,
			BaseURL: inst.BaseURL,
			APIKey:  inst.APIKey,
			Quirks:  QuirksPreset(inst.Quirks),
		}), nil
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

	return NewForInstance(OpenAICompatInstanceParams{
		Name:    "openai-compatible",
		BaseURL: base,
		APIKey:  key,
		Quirks:  quirks,
	}), nil
}

func (a *Adapter) Name() string {
	if a.name != "" {
		return a.name
	}
	return "openai-compatible"
}

// ListModels fetches available models from the /v1/models endpoint.
func (a *Adapter) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	if a.Client == nil {
		a.Client = &http.Client{Timeout: 0}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, a.BaseURL+"/models", nil)
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

	raw, rawReqBody, rawRespBody, statusCode, headers, err := a.doHTTP(ctx, body, req.AdapterTimeout)
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
	stampEndpointURL(&resp, a.BaseURL+"/chat/completions")
	resp.RateLimit = llm.ParseRateLimitHeaders(headers)
	if llm.RawBodyEnabled() {
		resp.RawRequestBody = string(rawReqBody)
		resp.RawResponseBody = string(rawRespBody)
	}
	return resp, nil
}

// stampEndpointURL records the full URL dialed for this call onto resp.Raw so
// the APILogger can promote it to a top-level field in the api_call transcript.
// Initialises Raw if nil.
func stampEndpointURL(resp *llm.Response, endpoint string) {
	if resp == nil || endpoint == "" {
		return
	}
	if resp.Raw == nil {
		resp.Raw = map[string]any{}
	}
	resp.Raw["endpoint_url"] = endpoint
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

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.BaseURL+"/chat/completions", bytes.NewReader(jsonBody))
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

				// Close reasoning if still open.
				if reasoningStarted && !textStarted && len(toolCalls) == 0 {
					s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningEnd})
				}

				// Collect and emit tool call end events.
				var completedToolCalls []llm.ToolCallData
				for idx, tc := range toolCalls {
					rescuedArgs := rescueClaudeXMLArgs(tc.args.String())
					tcd := llm.ToolCallData{
						ID:        tc.id,
						Name:      tc.name,
						Arguments: json.RawMessage(rescuedArgs),
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
				if finalResp.Usage.ReasoningTokens == nil && finalResp.Usage.ReasoningTokensEstimated == nil {
					if est := estimateThinkingFromBuf(reasoningBuf.Len()); est > 0 {
						e := est
						finalResp.Usage.ReasoningTokensEstimated = &e
					}
				}
				stampEndpointURL(finalResp, a.BaseURL+"/chat/completions")
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
				// OpenAI-compat endpoints report prompt_tokens as
				// total-including-cached. Subtract cached_tokens to honor
				// llm.Usage's "InputTokens means new uncached input" invariant.
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

			// Text delta — close reasoning first if transitioning.
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

	// Check if provider options supply a custom "reasoning" field (e.g. OpenRouter
	// MiniMax format: {"reasoning": {"enabled": true}}). When present, skip the
	// default reasoning_effort field and use reasoning_details for multi-turn.
	useReasoningDetails := false
	if req.ProviderOptions != nil {
		if ov, ok := req.ProviderOptions["openai-compatible"].(map[string]any); ok {
			if _, has := ov["reasoning"]; has {
				useReasoningDetails = true
			}
		}
	}

	msgs, err := toChatMessages(req.Messages, quirks, useReasoningDetails)
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
	if req.ReasoningEffort != nil && !useReasoningDetails {
		effort := *req.ReasoningEffort
		// OpenRouter uses "xhigh" where we say "max".
		if quirks.TranslateMaxToXHigh && strings.EqualFold(effort, "max") {
			effort = "xhigh"
		}
		body["reasoning_effort"] = effort
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

func toChatMessages(messages []llm.Message, quirks ProviderQuirks, useReasoningDetails bool) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(messages))
	for _, m := range messages {
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
				if useReasoningDetails {
					// MiniMax format: {type: "reasoning.text", text: "...", format: "unknown", index: 0}
					msg["reasoning_details"] = []map[string]any{
						{
							"type":   "reasoning.text",
							"text":   reasoning,
							"format": "unknown",
							"index":  0,
						},
					}
				} else {
					msg["reasoning_content"] = reasoning
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

// extractReasoning returns the reasoning text from a chat message, checking
// reasoning_details (OpenRouter MiniMax format) first, then reasoning_content.
// MiniMax uses {type: "reasoning.text", text: "..."}; older/alternate formats
// use {type: "thinking", thinking: "..."}.
func extractReasoning(msg chatMessage) string {
	if len(msg.ReasoningDetails) > 0 {
		var b strings.Builder
		for _, d := range msg.ReasoningDetails {
			var piece string
			if d.Text != "" {
				piece = d.Text
			} else if d.Thinking != "" {
				piece = d.Thinking
			}
			if piece != "" {
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString(piece)
			}
		}
		if b.Len() > 0 {
			return b.String()
		}
	}
	return msg.ReasoningContent
}

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

// claudeXMLParamOpenRE matches the OPEN of an Anthropic-style <parameter name="KEY"> tag.
// Some models (notably MiniMax M2.7 via OpenRouter) emit Claude/MiniMax XML
// tool-call syntax inside JSON tool arguments when they revert from JSON to
// XML mid-generation. Closing </parameter> tags are often missing, so we scan
// opens and slice to the next open or end-of-string.
//
// Pattern details (adapted from zeroclaw-labs/zeroclaw PR #1189 which solved
// the same parsing problem for MiniMax at the content level):
//   - Case-insensitive (`(?i)`) matches tag-case variation.
//   - Permissive attribute matching: other attributes can appear before `name=`.
//   - Both double and single quoted attribute values.
var claudeXMLParamOpenRE = regexp.MustCompile(
	`(?i)<parameter\b[^>]*\bname\s*=\s*(?:"([^"]+)"|'([^']+)')[^>]*>`,
)

// rescueClaudeXMLArgs attempts to recover tool call arguments when a model
// mixes Claude-style XML tool call syntax into the JSON arguments field.
//
// Examples:
//
//	Input:  {"action":"append\">\n<parameter name=\"tasks\">[{...}]</parameter>"}
//	Output: {"action":"append","tasks":[{...}]}
//
//	Input:  {"action":"update\">\n<parameter name=\"updates\">[{...}]"}   (no close tag)
//	Output: {"action":"update","updates":[{...}]}
//
// If the input is already valid and contains no XML syntax, it is returned
// unchanged. If rescue is not possible, the original input is returned so the
// usual schema-validation error path can surface the problem.
func rescueClaudeXMLArgs(raw string) string {
	if raw == "" {
		return raw
	}
	// The raw JSON is escaped — `<parameter name=\"KEY\">` — so we cannot
	// detect the pattern in the raw bytes. Parse first, then check the
	// unescaped string values.
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return raw
	}
	// Second-order rescue: for any top-level field whose value is a string
	// that looks like a JSON array or object (starts with `[` or `{`), try
	// parsing it. Models sometimes emit JSON-encoded strings for array/object
	// fields instead of the parsed value. This is a separate bug from the
	// Claude-XML corruption but benefits from the same rescue path.
	parsedAnyJSONString := false
	for k, v := range parsed {
		s, ok := v.(string)
		if !ok {
			continue
		}
		trimmed := strings.TrimSpace(s)
		if !(strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "{")) {
			continue
		}
		var asJSON any
		if err := json.Unmarshal([]byte(trimmed), &asJSON); err == nil {
			// Only replace if the parsed result is a composite type (array/object),
			// not a scalar — avoid mangling genuine strings that happen to start
			// with `[` or `{`.
			switch asJSON.(type) {
			case []any, map[string]any:
				parsed[k] = asJSON
				parsedAnyJSONString = true
			}
		}
	}

	// Quick exit: if no string value contains an XML <parameter ... tag,
	// nothing more to rescue. Case-insensitive because models sometimes
	// emit uppercase tag names.
	hasXML := false
	for _, v := range parsed {
		if s, ok := v.(string); ok {
			lower := strings.ToLower(s)
			if strings.Contains(lower, "<parameter ") || strings.Contains(lower, "<parameter\t") || strings.Contains(lower, "<parameter\n") {
				hasXML = true
				break
			}
		}
	}
	if !hasXML {
		if parsedAnyJSONString {
			if out, err := json.Marshal(parsed); err == nil {
				return string(out)
			}
		}
		return raw
	}

	// For each string value, check whether it carries a `"><parameter...` tail.
	// If so, split at the `">` boundary: the prefix is the real value, the tail
	// holds the <parameter> blocks whose name/value pairs should become siblings.
	changed := false
	extracted := make(map[string]any)
	for k, v := range parsed {
		s, ok := v.(string)
		if !ok {
			continue
		}
		// Find where the XML <parameter starts (case-insensitive, tolerant of
		// tab/newline after "parameter"). Use regex so we can find the exact
		// offset and let the caller split there.
		openMatch := claudeXMLParamOpenRE.FindStringIndex(s)
		if openMatch == nil {
			continue
		}
		// Split at the first `">` before the parameter block. If there's no
		// `">` (value not terminated with a quote-close-bracket), fall back to
		// splitting at the `<` of the parameter tag.
		idx := strings.Index(s[:openMatch[0]], `">`)
		if idx < 0 {
			idx = openMatch[0]
		}
		cleanValue := s[:idx]
		parsed[k] = cleanValue
		changed = true

		tail := s[idx:]
		// Find all <parameter name="KEY"> opens. Between consecutive opens (or
		// from an open to end-of-string), the value is the parameter content.
		// The regex has two alternations for the name: double-quoted (group 1)
		// and single-quoted (group 2). Submatch indices are:
		//   m[0..1] = whole match
		//   m[2..3] = double-quoted name (or -1 if single-quoted)
		//   m[4..5] = single-quoted name (or -1 if double-quoted)
		opens := claudeXMLParamOpenRE.FindAllStringSubmatchIndex(tail, -1)
		for i, m := range opens {
			var paramName string
			if m[2] >= 0 {
				paramName = tail[m[2]:m[3]]
			} else if m[4] >= 0 {
				paramName = tail[m[4]:m[5]]
			}
			if paramName == "" {
				continue
			}
			valStart := m[1] // index just after the `>` of this open tag
			valEnd := len(tail)
			if i+1 < len(opens) {
				valEnd = opens[i+1][0]
			}
			paramValue := tail[valStart:valEnd]
			// Strip trailing </parameter> (case-insensitive) if present.
			lowerVal := strings.ToLower(paramValue)
			if cut := strings.LastIndex(lowerVal, "</parameter>"); cut >= 0 {
				paramValue = paramValue[:cut]
			}
			// Strip any leading/trailing whitespace and quotes that may remain.
			paramValue = strings.TrimSpace(paramValue)
			// If the parameter value is itself JSON (array/object/number/bool/null),
			// parse it. Otherwise keep as string.
			var asJSON any
			if err := json.Unmarshal([]byte(paramValue), &asJSON); err == nil {
				extracted[paramName] = asJSON
			} else {
				extracted[paramName] = paramValue
			}
		}
	}

	if !changed {
		return raw
	}

	// Merge extracted params into the top-level object. Parent fields take
	// precedence if there's a collision — the parent had the "primary" value.
	for k, v := range extracted {
		if _, exists := parsed[k]; !exists {
			parsed[k] = v
		}
	}

	out, err := json.Marshal(parsed)
	if err != nil {
		return raw
	}
	return string(out)
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
	Role             string                `json:"role"`
	Content          string                `json:"content"`
	ReasoningContent string                `json:"reasoning_content,omitempty"`
	ReasoningDetails []reasoningDetailItem `json:"reasoning_details,omitempty"`
	ToolCalls        []chatToolCall        `json:"tool_calls,omitempty"`
}

// reasoningDetailItem represents an element in the reasoning_details array
// used by OpenRouter for models like MiniMax M2.7. MiniMax's actual format
// is {type: "reasoning.text", text: "...", format: "...", index: N}.
// We preserve unknown fields via the Extra map so round-tripping the message
// back to the model keeps the reasoning chain intact.
type reasoningDetailItem struct {
	Type   string `json:"type"`
	Text   string `json:"text,omitempty"`
	Format string `json:"format,omitempty"`
	Index  int    `json:"index,omitempty"`
	// Thinking is kept for backward compatibility with older OpenRouter format
	// variants that used {type: "thinking", thinking: "..."}.
	Thinking string `json:"thinking,omitempty"`
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
	if reasoning := extractReasoning(choice.Message); reasoning != "" {
		parts = append(parts, llm.ContentPart{
			Kind:     llm.ContentThinking,
			Thinking: &llm.ThinkingData{Text: reasoning},
		})
	}
	if choice.Message.Content != "" {
		parts = append(parts, llm.ContentPart{Kind: llm.ContentText, Text: choice.Message.Content})
	}
	for _, tc := range choice.Message.ToolCalls {
		args := rescueClaudeXMLArgs(tc.Function.Arguments)
		parts = append(parts, llm.ContentPart{
			Kind: llm.ContentToolCall,
			ToolCall: &llm.ToolCallData{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: json.RawMessage(args),
				Type:      "function",
			},
		})
	}

	msg := llm.Message{Role: llm.RoleAssistant, Content: parts}

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
		rawPrompt := llm.IntFromAny(parsed.Usage["prompt_tokens"])
		output := llm.IntFromAny(parsed.Usage["completion_tokens"])
		var cachedRead int
		if details, ok := parsed.Usage["prompt_tokens_details"].(map[string]any); ok {
			cachedRead = llm.IntFromAny(details["cached_tokens"])
		}
		uncachedInput := rawPrompt - cachedRead
		if uncachedInput < 0 {
			uncachedInput = 0
		}
		resp.Usage = llm.Usage{
			InputTokens:  uncachedInput,
			OutputTokens: output,
			Raw:          parsed.Usage,
		}
		resp.Usage.TotalTokens = rawPrompt + output
		if v := llm.IntFromAny(parsed.Usage["total_tokens"]); v > 0 {
			resp.Usage.TotalTokens = v
		}
		if details, ok := parsed.Usage["completion_tokens_details"].(map[string]any); ok {
			if rt := llm.IntFromAny(details["reasoning_tokens"]); rt > 0 {
				resp.Usage.ReasoningTokens = &rt
			}
		}
		if cachedRead > 0 {
			resp.Usage.CacheReadTokens = &cachedRead
		}
	}

	if resp.Usage.ReasoningTokens == nil && resp.Usage.ReasoningTokensEstimated == nil {
		chars := 0
		for _, p := range parts {
			if p.Kind == llm.ContentThinking && p.Thinking != nil {
				chars += len(p.Thinking.Text)
			}
		}
		if est := estimateThinkingFromBuf(chars); est > 0 {
			e := est
			resp.Usage.ReasoningTokensEstimated = &e
		}
	}

	return resp, nil
}

// estimateThinkingFromBuf returns a char/4 rough estimate from a
// thinking-content character count. Used only for the Usage metadata
// field ReasoningTokensEstimated — never for billing.
func estimateThinkingFromBuf(chars int) int {
	if chars == 0 {
		return 0
	}
	est := chars / 4
	if est < 1 {
		est = 1
	}
	return est
}

// --- HTTP helpers ---

func (a *Adapter) doHTTP(ctx context.Context, body map[string]any, at *llm.AdapterTimeout) (raw map[string]any, rawReqBody []byte, rawRespBody []byte, status int, hdr http.Header, err error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, nil, nil, 0, nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.BaseURL+"/chat/completions", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, nil, nil, 0, nil, err
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
		return nil, nil, nil, 0, nil, llm.WrapContextError("openai-compatible", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, jsonBody, b, resp.StatusCode, resp.Header, err
	}

	var parsed map[string]any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&parsed); err != nil {
		return nil, jsonBody, b, resp.StatusCode, resp.Header, nil
	}

	return parsed, jsonBody, b, resp.StatusCode, resp.Header, nil
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
