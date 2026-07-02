// Package openaicompat implements a Chat Completions adapter for OpenAI-compatible
// services (Ollama, vLLM, LiteLLM, etc.). It registers as provider "openai-compatible".
// BaseURL should include the version prefix (e.g. "https://api.openai.com/v1").
package openaicompat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
	"primeradiant.com/serf/llm/providers/internal/openaichat"
	"primeradiant.com/serf/llm/providers/internal/transport"
	openairesponses "primeradiant.com/serf/llm/providers/openai"
)

type Adapter struct {
	name           string
	APIKey         string
	BaseURL        string
	Client         *http.Client
	DefaultHeaders map[string]string
	Quirks         ProviderQuirks
	// Models holds fully-resolved per-model wire behavior keyed by wire model
	// id; models absent here use Quirks. Populated from providers.toml
	// [instances.X.models] via NewForInstance.
	Models   map[string]ModelCompat
	Adaptive bool
	// CatalogTag is the provider behavior tag used for provider-qualified
	// catalog lookups (e.g. "openrouter" so "minimax/minimax-m2" resolves the
	// bundled "openrouter/minimax/minimax-m2" entry), mirroring
	// newOpenAICompatProfile's lookup precedence. Empty means bare-only.
	CatalogTag string
	// SuppressCatalogDefaults skips the embedded-catalog fallback fill
	// (fillFromCatalog) for models undeclared in Models. Set by providers
	// whose bare model ids are locally-defined and unrelated to any
	// upstream catalog entry of the same name (e.g. ollama) — without this,
	// a local model that happens to share a name with an upstream catalog
	// entry would silently inherit that entry's defaults (e.g. max_tokens).
	SuppressCatalogDefaults bool
}

// OpenAICompatInstanceParams holds the configuration for a single openai-compatible adapter instance.
type OpenAICompatInstanceParams struct {
	Name    string
	BaseURL string
	APIKey  string
	// Quirks is the preset base; Compat (instance-wide) and Models (per-model)
	// overlay it field by field.
	Quirks   ProviderQuirks
	Compat   *providercfg.CompatConfig
	Models   map[string]providercfg.ModelConfig
	Adaptive bool
	// Headers are user-configured request headers (providers.toml
	// [instances.X.headers], already $ENV-resolved). They override
	// ProviderHeaders on key collision.
	Headers map[string]string
	// ProviderHeaders are provider-set default headers (e.g. kimi's coding-plan
	// User-Agent). User Headers override these but do not erase them: a default
	// survives when the user configures no header of the same name.
	ProviderHeaders map[string]string
	// SuppressCatalogDefaults is passed through to the constructed Adapter;
	// see Adapter.SuppressCatalogDefaults.
	SuppressCatalogDefaults bool
	// CatalogTag is passed through to the constructed Adapter; see
	// Adapter.CatalogTag.
	CatalogTag string
}

// NewForInstance constructs an Adapter from explicit parameters.
func NewForInstance(params OpenAICompatInstanceParams) *Adapter {
	quirks := ApplyCompatConfig(params.Quirks, params.Compat)
	return &Adapter{
		name:                    params.Name,
		APIKey:                  params.APIKey,
		BaseURL:                 strings.TrimRight(params.BaseURL, "/"),
		Client:                  &http.Client{Timeout: 0},
		Quirks:                  quirks,
		Models:                  resolveModelCompat(quirks, params.Models),
		Adaptive:                params.Adaptive,
		DefaultHeaders:          llm.MergeHeaders(params.ProviderHeaders, params.Headers),
		SuppressCatalogDefaults: params.SuppressCatalogDefaults,
		CatalogTag:              params.CatalogTag,
	}
}

func init() {
	llm.RegisterEnvAdapterFactory(func(_ llm.EnvConfig) (llm.ProviderAdapter, bool, error) {
		base := envvars.OpenAICompatibleBaseURL.Trimmed()
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
			Name:       inst.Name,
			BaseURL:    inst.BaseURL,
			APIKey:     inst.APIKey,
			Quirks:     QuirksPreset(inst.Quirks),
			Compat:     inst.Compat,
			Models:     inst.Models,
			Headers:    inst.Headers,
			CatalogTag: "openai-compatible",
		}), nil
	})
}

func NewFromEnv() (*Adapter, error) {
	base := envvars.OpenAICompatibleBaseURL.Trimmed()
	if base == "" {
		return nil, fmt.Errorf("%s is required", envvars.OpenAICompatibleBaseURL.Name)
	}
	key := envvars.OpenAICompatibleAPIKey.Trimmed()

	var quirks ProviderQuirks
	if preset := envvars.OpenAICompatibleProviderQuirks.Trimmed(); preset != "" {
		quirks = QuirksPreset(preset)
	}

	return NewForInstance(OpenAICompatInstanceParams{
		Name:     "openai-compatible",
		BaseURL:  base,
		APIKey:   key,
		Quirks:   quirks,
		Adaptive: true,
	}), nil
}

func (a *Adapter) Name() string {
	if a.name != "" {
		return a.name
	}
	return "openai-compatible"
}

// sessionAffinityHeaders are the per-request headers a gateway uses to pin a
// conversation's turns to one backend/cache (mirrors Pi's createClient).
var sessionAffinityHeaders = []string{"session_id", "x-client-request-id", "x-session-affinity"}

func (a *Adapter) setChatHeaders(httpReq *http.Request, req llm.Request) {
	// Session-affinity headers are derived per-request and go first so a user's
	// DefaultHeaders of the same name override them.
	if a.compatFor(req.Model).Quirks.SendSessionAffinityHeaders {
		if sid := strings.TrimSpace(req.SessionID); sid != "" {
			for _, h := range sessionAffinityHeaders {
				httpReq.Header.Set(h, sid)
			}
		}
	}
	// Apply default headers next so they win over affinity headers; Content-Type
	// and Authorization below take precedence over everything.
	for k, v := range a.DefaultHeaders {
		httpReq.Header.Set(k, v)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if a.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+a.APIKey)
	}
}

// ChatCompletionsBody returns the OpenAI-compatible chat request body used by
// this adapter. Provider wrappers use it when an adjacent endpoint accepts the
// same message shape.
func (a *Adapter) ChatCompletionsBody(req llm.Request, stream bool) (map[string]any, error) {
	return buildRequestBody(req, stream, a.compatFor(req.Model))
}

// Complete sends a non-streaming Chat Completions request.
func (a *Adapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if a.Adaptive {
		resp, err := a.completeViaResponses(ctx, req)
		if err == nil {
			return resp, nil
		}
		if !canFallbackFromResponses(req, err) {
			return llm.Response{}, err
		}
	}
	return a.completeViaChatCompletions(ctx, req)
}

func (a *Adapter) completeViaChatCompletions(ctx context.Context, req llm.Request) (llm.Response, error) {
	if a.Client == nil {
		a.Client = &http.Client{Timeout: 0}
	}

	body, err := buildRequestBody(req, false, a.compatFor(req.Model))
	if err != nil {
		return llm.Response{}, err
	}

	ctx, adapterCancel := llm.ApplyAdapterTimeout(ctx, req.AdapterTimeout, false)
	defer adapterCancel()

	raw, rawReqBody, rawRespBody, statusCode, headers, err := a.doHTTP(ctx, req, body)
	if err != nil {
		return llm.Response{}, err
	}
	if statusCode != http.StatusOK {
		msg := extractErrorMessage(raw)
		retryAfter := llm.ParseRetryAfter(headers.Get("Retry-After"), time.Now())
		return llm.Response{}, llm.ErrorFromHTTPStatusWithRawBodies("openai-compatible", statusCode, msg, raw, retryAfter, string(rawReqBody), string(rawRespBody))
	}

	resp, err := fromChatCompletionResponse(raw, a.compatFor(req.Model).Quirks)
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
	if a.Adaptive {
		stream, err := a.streamViaResponses(ctx, req)
		if err == nil {
			return stream, nil
		}
		if !canFallbackFromResponses(req, err) {
			return nil, err
		}
	}
	return a.streamViaChatCompletions(ctx, req)
}

func (a *Adapter) streamViaChatCompletions(ctx context.Context, req llm.Request) (llm.Stream, error) {
	if a.Client == nil {
		a.Client = &http.Client{Timeout: 0}
	}
	ctx, timeoutCancel := llm.ApplyAdapterTimeout(ctx, req.AdapterTimeout, true)
	defer timeoutCancel()

	body, err := buildRequestBody(req, true, a.compatFor(req.Model))
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
	a.setChatHeaders(httpReq, req)

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
		return nil, llm.ErrorFromHTTPStatusWithRawBodies("openai-compatible", resp.StatusCode, msg, raw, retryAfter, string(jsonBody), string(b))
	}

	sctx, cancel := context.WithCancel(ctx)
	s := llm.NewChanStream(cancel)
	rl := llm.ParseRateLimitHeaders(resp.Header)

	s.Send(llm.StreamEvent{Type: llm.StreamEventStreamStart})

	go a.decodeStream(sctx, resp, s, req, jsonBody, rl)

	return s, nil
}

func (a *Adapter) completeViaResponses(ctx context.Context, req llm.Request) (llm.Response, error) {
	resp, err := a.responsesAdapter().Complete(ctx, req)
	if err != nil {
		return llm.Response{}, llm.RewriteErrorProvider(err, "openai-compatible")
	}
	resp.Provider = "openai-compatible"
	return resp, nil
}

func (a *Adapter) streamViaResponses(ctx context.Context, req llm.Request) (llm.Stream, error) {
	stream, err := a.responsesAdapter().Stream(ctx, req)
	if err != nil {
		return nil, llm.RewriteErrorProvider(err, "openai-compatible")
	}
	return restampResponsesStream(stream), nil
}

func (a *Adapter) responsesAdapter() *openairesponses.Adapter {
	client := a.Client
	if client == nil {
		client = &http.Client{Timeout: 0}
	}
	return &openairesponses.Adapter{
		APIKey:              a.APIKey,
		BaseURL:             a.BaseURL,
		ResponsesPath:       "/responses",
		Client:              client,
		DefaultHeaders:      a.DefaultHeaders,
		DisableChatFallback: true,
	}
}

func canFallbackFromResponses(req llm.Request, err error) bool {
	if err == nil || hasResponsesContinuationState(req) {
		return false
	}
	var status interface{ StatusCode() int }
	if !errors.As(err, &status) {
		return false
	}
	code := status.StatusCode()
	return code == http.StatusNotFound || code == http.StatusUnprocessableEntity
}

func hasResponsesContinuationState(req llm.Request) bool {
	return strings.TrimSpace(req.PreviousResponseID) != "" ||
		strings.TrimSpace(req.ConversationID) != "" ||
		req.Continuation != nil
}

func restampResponsesStream(inner llm.Stream) llm.Stream {
	proxy := llm.NewChanStream(func() { _ = inner.Close() })
	go func() {
		defer proxy.CloseSend()
		defer inner.Close() //nolint:errcheck
		for ev := range inner.Events() {
			if ev.Response != nil {
				ev.Response.Provider = "openai-compatible"
			}
			if ev.Err != nil {
				ev.Err = llm.RewriteErrorProvider(ev.Err, "openai-compatible")
			}
			proxy.Send(ev)
		}
	}()
	return proxy
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
	// reasoningField remembers which wire field the first reasoning delta
	// arrived on so replay can route thinking back to the same field.
	var reasoningField string
	// encryptedDetails accumulates opaque reasoning.encrypted items (OpenRouter
	// Gemini/o-series) in arrival order; they ride the thinking part's
	// EncryptedContent so the reasoning chain survives replay.
	var encryptedDetails []map[string]any
	var model string
	var finishReason string
	var usage *llm.Usage
	// sawTopLevelUsage gives top-level usage strict precedence across the
	// WHOLE stream (matching the non-stream path), not just within a chunk —
	// a mixed emitter must not have late choice-level numbers overwrite
	// earlier top-level ones.
	var sawTopLevelUsage bool
	finished := false

	rawReqBody := string(jsonBody)

	runner := &transport.StreamRunner{
		Provider:       "openai-compatible",
		Resp:           resp,
		RawRequestBody: rawReqBody,
		Stream:         s,
		SSEOpts:        llm.StreamReadSSEOptions(req.AdapterTimeout),
		Finished:       &finished,
		IncompleteMsg:  "openai-compatible stream ended without completion",
		OnEvent: func(ev llm.SSEEvent, sseBuf *bytes.Buffer) error {
			data := string(ev.Data)
			if data == "[DONE]" {
				finished = true

				// Close reasoning if still open.
				if reasoningStarted && !textStarted && len(toolCalls) == 0 {
					s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningEnd})
				}

				// Collect and emit tool call end events in wire (delta-index)
				// order. The state is keyed in a map, whose iteration order is
				// randomized; sort by index so parallel tool calls assemble
				// deterministically.
				var completedToolCalls []llm.ToolCallData
				for _, idx := range slices.Sorted(maps.Keys(toolCalls)) {
					tc := toolCalls[idx]
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
				if reasoningBuf.Len() > 0 || len(encryptedDetails) > 0 {
					td := &llm.ThinkingData{Text: reasoningBuf.String(), Signature: reasoningField}
					if len(encryptedDetails) > 0 {
						if b, err := json.Marshal(encryptedDetails); err == nil {
							td.EncryptedContent = string(b)
						}
					}
					msg.Content = append(msg.Content, llm.ContentPart{
						Kind:     llm.ContentThinking,
						Thinking: td,
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
				mappedFinish := a.compatFor(req.Model).Quirks.mapFinishReason(rawFinish)
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
				sawTopLevelUsage = true
			}

			if len(chunk.Choices) == 0 {
				return nil
			}
			choice := chunk.Choices[0]

			// Usage fallback: Moonshot/Kimi report usage on choices[0].usage
			// instead of the top-level chunk usage. Top-level wins.
			if chunk.Usage == nil && choice.Usage != nil && !sawTopLevelUsage {
				u := openaichat.ParseChatUsage(choice.Usage)
				usage = &u
			}

			if choice.FinishReason != "" {
				finishReason = choice.FinishReason
			}

			// Accumulate opaque encrypted reasoning items (they carry no text,
			// so reasoningFromDelta skips them).
			encryptedDetails = append(encryptedDetails, collectEncryptedDetails(choice.Delta.ReasoningDetails)...)

			// Reasoning delta — providers vary the field name; take the first
			// non-empty variant per chunk (duplicated identical fields must
			// not double the text) and remember the field for replay.
			if delta, field := reasoningFromDelta(choice.Delta); delta != "" {
				if !reasoningStarted {
					s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningStart})
					reasoningStarted = true
					reasoningField = field
				}
				reasoningBuf.WriteString(delta)
				s.Send(llm.StreamEvent{
					Type:           llm.StreamEventReasoningDelta,
					ReasoningDelta: delta,
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

func (a *Adapter) doHTTP(ctx context.Context, req llm.Request, body map[string]any) (raw map[string]any, rawReqBody []byte, rawRespBody []byte, status int, hdr http.Header, err error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, nil, nil, 0, nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.BaseURL+"/chat/completions", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, nil, nil, 0, nil, err
	}
	a.setChatHeaders(httpReq, req)

	client := llm.ClientWithConnectTimeout(a.Client, req.AdapterTimeout)
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
