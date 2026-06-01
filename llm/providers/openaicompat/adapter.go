// Package openaicompat implements a Chat Completions adapter for OpenAI-compatible
// services (Ollama, vLLM, LiteLLM, etc.). It registers as provider "openai-compatible".
// BaseURL should include the version prefix (e.g. "https://api.openai.com/v1").
package openaicompat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
	"primeradiant.com/serf/llm/providers/internal/openaichat"
	"primeradiant.com/serf/llm/providers/internal/transport"
)

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
	llm.RegisterInstanceAdapterFactory("openai", "chat-completions", func(inst providercfg.InstanceConfig, _ string) (llm.ProviderAdapter, error) {
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
		return nil, errors.New("OPENAI_COMPATIBLE_BASE_URL is required")
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

func (a *Adapter) setChatHeaders(httpReq *http.Request) {
	// Apply default headers first so provider-specific headers take precedence.
	for k, v := range a.DefaultHeaders {
		httpReq.Header.Set(k, v)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if a.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+a.APIKey)
	}
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
	llm.StampEndpointURL(&resp, a.BaseURL+"/chat/completions")
	resp.RateLimit = llm.ParseRateLimitHeaders(headers)
	if llm.RawBodyEnabled() {
		resp.RawRequestBody = string(rawReqBody)
		resp.RawResponseBody = string(rawRespBody)
	}
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

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.BaseURL+"/chat/completions", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}
	a.setChatHeaders(httpReq)

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

	go a.decodeStream(sctx, resp, s, req, jsonBody, rl)

	return s, nil
}

// decodeStream consumes the Chat Completions SSE stream in its own goroutine: it
// translates each chunk into llm stream events and emits the final accumulated
// Response on [DONE]. It owns closing the response body and the ChanStream.
func (a *Adapter) decodeStream(sctx context.Context, resp *http.Response, s *llm.ChanStream, req llm.Request, jsonBody []byte, rl *llm.RateLimitInfo) {
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

	rawReqBody := string(jsonBody)

	runner := &transport.StreamRunner{
		Provider:      "openai-compatible",
		Resp:          resp,
		Stream:        s,
		SSEOpts:       llm.StreamReadSSEOptions(req.AdapterTimeout),
		Finished:      &finished,
		IncompleteMsg: "openai-compatible stream ended without completion",
		OnEvent: func(ev llm.SSEEvent, sseBuf *bytes.Buffer) error {
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
				llm.StampEndpointURL(finalResp, a.BaseURL+"/chat/completions")
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
		},
	}
	runner.Run(sctx)
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
	a.setChatHeaders(httpReq)

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
