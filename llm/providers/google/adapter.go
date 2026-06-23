package google

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
	"primeradiant.com/serf/llm/providers/internal/transport"
)

type Adapter struct {
	name           string
	APIKey         string
	BaseURL        string
	Client         *http.Client
	DefaultHeaders map[string]string
}

// GoogleInstanceParams holds the configuration for a single Google adapter instance.
type GoogleInstanceParams struct {
	Name    string
	APIKey  string
	BaseURL string
}

// NewForInstance constructs an Adapter from explicit parameters.
// Empty BaseURL falls back to the default Gemini API endpoint.
func NewForInstance(params GoogleInstanceParams) (*Adapter, error) {
	if strings.TrimSpace(params.APIKey) == "" {
		return nil, fmt.Errorf("%s is required", envvars.GeminiAPIKey.Name)
	}
	base := strings.TrimSpace(params.BaseURL)
	if base == "" {
		base = "https://generativelanguage.googleapis.com"
	}
	return &Adapter{
		name:   params.Name,
		APIKey: params.APIKey,
		// Avoid short client-level timeouts; rely on request context deadlines instead.
		BaseURL: strings.TrimRight(base, "/"),
		Client:  &http.Client{Timeout: 0},
	}, nil
}

func init() {
	llm.RegisterEnvAdapterFactory(func(_ llm.EnvConfig) (llm.ProviderAdapter, bool, error) {
		if envvars.GeminiAPIKey.Trimmed() == "" && envvars.GoogleAPIKey.Trimmed() == "" {
			return nil, false, nil
		}
		a, err := NewFromEnv()
		if err != nil {
			return nil, true, err
		}
		return a, true, nil
	})
	for _, typ := range []string{"google", "gemini"} {
		t := typ
		llm.RegisterInstanceAdapterFactory(t, "", func(inst providercfg.InstanceConfig, _ string) (llm.ProviderAdapter, error) {
			return NewForInstance(GoogleInstanceParams{
				Name:    inst.Name,
				BaseURL: inst.BaseURL,
				APIKey:  inst.APIKey,
			})
		})
	}
}

func NewFromEnv() (*Adapter, error) {
	key := envvars.GeminiAPIKey.Trimmed()
	if key == "" {
		// Common alias.
		key = envvars.GoogleAPIKey.Trimmed()
	}
	return NewForInstance(GoogleInstanceParams{
		Name:    "google",
		APIKey:  key,
		BaseURL: envvars.GeminiBaseURL.Trimmed(),
	})
}

func (a *Adapter) Name() string {
	if a.name != "" {
		return a.name
	}
	return "google"
}

func (a *Adapter) setJSONHeaders(httpReq *http.Request) {
	// Apply default headers first so provider-specific headers take precedence.
	for k, v := range a.DefaultHeaders {
		httpReq.Header.Set(k, v)
	}
	httpReq.Header.Set("Content-Type", "application/json")
}

func (a *Adapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if a.Client == nil {
		a.Client = &http.Client{Timeout: 0}
	}

	system, contents, err := toGeminiContents(req.Messages)
	if err != nil {
		return llm.Response{}, err
	}

	body, err := a.buildRequestBody(req, system, contents)
	if err != nil {
		return llm.Response{}, err
	}

	b, err := json.Marshal(body)
	if err != nil {
		return llm.Response{}, err
	}

	ctx, adapterCancel := llm.ApplyAdapterTimeout(ctx, req.AdapterTimeout, false)
	defer adapterCancel()

	endpoint := fmt.Sprintf("%s/v1beta/models/%s:generateContent", a.BaseURL, url.PathEscape(req.Model))
	u, err := url.Parse(endpoint)
	if err != nil {
		return llm.Response{}, err
	}
	q := u.Query()
	q.Set("key", a.APIKey)
	u.RawQuery = q.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(b))
	if err != nil {
		return llm.Response{}, err
	}
	a.setJSONHeaders(httpReq)

	client := llm.ClientWithConnectTimeout(a.Client, req.AdapterTimeout)
	resp, err := client.Do(httpReq)
	if err != nil {
		return llm.Response{}, llm.WrapContextError("google", err)
	}
	defer func() { _ = resp.Body.Close() }()

	rawBytes, _ := io.ReadAll(resp.Body)
	var raw map[string]any
	_ = json.Unmarshal(rawBytes, &raw)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		ra := llm.ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		msg := "generateContent failed: " + strings.TrimSpace(string(rawBytes))
		httpErr := llm.ErrorFromHTTPStatusWithRawBodies("google", resp.StatusCode, msg, raw, ra, string(b), string(rawBytes))
		return llm.Response{}, classifyGeminiError(resp.StatusCode, rawBytes, ra, httpErr)
	}

	r := fromGeminiResponse(raw, req.Model)
	llm.StampEndpointURL(&r, endpoint)
	r.RateLimit = llm.ParseRateLimitHeaders(resp.Header)
	if llm.RawBodyEnabled() {
		r.RawRequestBody = string(b)
		r.RawResponseBody = string(rawBytes)
	}
	return r, nil
}

func (a *Adapter) CountInputTokens(ctx context.Context, req llm.Request) (llm.InputTokenCount, error) {
	if a.Client == nil {
		a.Client = &http.Client{Timeout: 0}
	}

	system, contents, err := toGeminiContents(req.Messages)
	if err != nil {
		return llm.InputTokenCount{}, err
	}
	genReq, err := a.buildRequestBody(req, system, contents)
	if err != nil {
		return llm.InputTokenCount{}, err
	}
	genReq["model"] = "models/" + req.Model
	body := map[string]any{"generateContentRequest": genReq}

	b, err := json.Marshal(body)
	if err != nil {
		return llm.InputTokenCount{}, err
	}

	ctx, adapterCancel := llm.ApplyAdapterTimeout(ctx, req.AdapterTimeout, false)
	defer adapterCancel()

	endpoint := fmt.Sprintf("%s/v1beta/models/%s:countTokens", a.BaseURL, url.PathEscape(req.Model))
	u, err := url.Parse(endpoint)
	if err != nil {
		return llm.InputTokenCount{}, err
	}
	q := u.Query()
	q.Set("key", a.APIKey)
	u.RawQuery = q.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(b))
	if err != nil {
		return llm.InputTokenCount{}, err
	}
	a.setJSONHeaders(httpReq)

	client := llm.ClientWithConnectTimeout(a.Client, req.AdapterTimeout)
	resp, err := client.Do(httpReq)
	if err != nil {
		return llm.InputTokenCount{}, llm.WrapContextError("google", err)
	}
	defer func() { _ = resp.Body.Close() }()

	rawBytes, _ := io.ReadAll(resp.Body)
	var raw map[string]any
	_ = json.Unmarshal(rawBytes, &raw)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		ra := llm.ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		msg := "countTokens failed: " + strings.TrimSpace(string(rawBytes))
		httpErr := llm.ErrorFromHTTPStatus("google", resp.StatusCode, msg, raw, ra)
		return llm.InputTokenCount{}, classifyGeminiError(resp.StatusCode, rawBytes, ra, httpErr)
	}

	tokens := tokenCountInt(raw["totalTokens"])
	return llm.InputTokenCount{
		Tokens:   tokens,
		Exact:    true,
		Source:   llm.TokenCountSourceProvider,
		Provider: a.Name(),
		Model:    req.Model,
		Raw:      raw,
	}, nil
}

func tokenCountInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case json.Number:
		i, _ := x.Int64()
		return int(i)
	default:
		return 0
	}
}

func (a *Adapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	if a.Client == nil {
		a.Client = &http.Client{Timeout: 0}
	}
	sctx, cancel := context.WithCancel(ctx)
	sctx, timeoutCancel := llm.ApplyAdapterTimeout(sctx, req.AdapterTimeout, true)
	defer timeoutCancel()

	system, contents, err := toGeminiContents(req.Messages)
	if err != nil {
		cancel()
		return nil, err
	}

	body, err := a.buildRequestBody(req, system, contents)
	if err != nil {
		cancel()
		return nil, err
	}

	b, err := json.Marshal(body)
	if err != nil {
		cancel()
		return nil, err
	}

	endpoint := fmt.Sprintf("%s/v1beta/models/%s:streamGenerateContent", a.BaseURL, url.PathEscape(req.Model))
	u, err := url.Parse(endpoint)
	if err != nil {
		cancel()
		return nil, err
	}
	q := u.Query()
	q.Set("key", a.APIKey)
	q.Set("alt", "sse")
	u.RawQuery = q.Encode()

	httpReq, err := http.NewRequestWithContext(sctx, http.MethodPost, u.String(), bytes.NewReader(b))
	if err != nil {
		cancel()
		return nil, err
	}
	a.setJSONHeaders(httpReq)

	client := llm.ClientWithConnectTimeout(a.Client, req.AdapterTimeout)
	resp, err := client.Do(httpReq)
	if err != nil {
		cancel()
		return nil, llm.WrapContextError("google", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		rawBytes, _ := io.ReadAll(resp.Body)
		var raw map[string]any
		_ = json.Unmarshal(rawBytes, &raw)
		ra := llm.ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		msg := "streamGenerateContent failed: " + strings.TrimSpace(string(rawBytes))
		httpErr := llm.ErrorFromHTTPStatusWithRawBodies("google", resp.StatusCode, msg, raw, ra, string(b), string(rawBytes))
		cancel()
		return nil, classifyGeminiError(resp.StatusCode, rawBytes, ra, httpErr)
	}

	s := llm.NewChanStream(cancel)
	s.Send(llm.StreamEvent{Type: llm.StreamEventStreamStart})

	go a.decodeStream(sctx, cancel, resp, s, req, b, endpoint)

	return s, nil
}

// decodeStream consumes the streamGenerateContent SSE stream in its own goroutine:
// it translates each chunk into llm stream events and emits the final accumulated
// Response when the model signals a finish reason. It owns closing the response
// body and the ChanStream.
func (a *Adapter) decodeStream(sctx context.Context, cancel context.CancelFunc, resp *http.Response, s *llm.ChanStream, req llm.Request, b []byte, endpoint string) {
	defer func() {
		_ = resp.Body.Close()
		s.CloseSend()
	}()

	textID := "text_1"
	textStarted := false
	finished := false
	var textBuf strings.Builder
	var contentParts []llm.ContentPart
	var usage llm.Usage
	finish := llm.FinishReason{Reason: "stop"}

	flushTextPart := func() {
		if textBuf.Len() == 0 {
			return
		}
		contentParts = append(contentParts, llm.ContentPart{Kind: llm.ContentText, Text: textBuf.String()})
		textBuf.Reset()
	}

	rawReqBody := string(b)

	runner := &transport.StreamRunner{
		Provider:      "google",
		Resp:          resp,
		Stream:        s,
		SSEOpts:       llm.StreamReadSSEOptions(req.AdapterTimeout),
		Finished:      &finished,
		IncompleteMsg: "google stream ended without completion",
		OnEvent: func(ev llm.SSEEvent, sseBuf *bytes.Buffer) error {
			if len(ev.Data) == 0 {
				return nil
			}
			var raw map[string]any
			dec := json.NewDecoder(bytes.NewReader(ev.Data))
			dec.UseNumber()
			if err := dec.Decode(&raw); err != nil {
				// Undecodable event: forward it raw and keep the stream alive
				// rather than aborting on a single malformed/unknown event.
				s.Send(llm.StreamEvent{Type: llm.StreamEventProviderEvent, Raw: map[string]any{"event": ev.Event, "data": string(ev.Data)}})
				return nil //nolint:nilerr // decode failure is surfaced as a raw passthrough event, not a fatal error
			}

			// candidates[0].content.parts
			if cands, ok := raw["candidates"].([]any); ok && len(cands) > 0 {
				if c0, ok := cands[0].(map[string]any); ok {
					if content, ok := c0["content"].(map[string]any); ok {
						if parts, ok := content["parts"].([]any); ok {
							for _, pAny := range parts {
								p, ok := pAny.(map[string]any)
								if !ok {
									continue
								}
								// Thought parts must be checked BEFORE text parts
								// because thought parts also have a "text" field.
								if thought, _ := p["thought"].(bool); thought {
									text, _ := p["text"].(string)
									if text != "" {
										flushTextPart()
										s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningStart})
										s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningDelta, ReasoningDelta: text})
										s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningEnd})
										contentParts = append(contentParts, llm.ContentPart{
											Kind: llm.ContentThinking,
											Thinking: &llm.ThinkingData{
												Text: text,
											},
										})
									}
									continue
								}
								if t, _ := p["text"].(string); t != "" {
									if !textStarted {
										textStarted = true
										s.Send(llm.StreamEvent{Type: llm.StreamEventTextStart, TextID: textID})
									}
									textBuf.WriteString(t)
									s.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, TextID: textID, Delta: t})
									continue
								}
								if fc, ok := p["functionCall"].(map[string]any); ok {
									name, _ := fc["name"].(string)
									argsAny := normalizeJSONNumbers(fc["args"])
									argsRaw, _ := json.Marshal(argsAny)
									thoughtSig := geminiThoughtSignature(p, fc)
									flushTextPart()

									id := "call_" + ulid.Make().String() // synthetic per spec
									tc := llm.ToolCallData{
										ID:               id,
										Name:             name,
										Type:             "function",
										ThoughtSignature: thoughtSig,
									}
									s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &tc})
									tcEnd := llm.ToolCallData{
										ID:               id,
										Name:             name,
										Arguments:        argsRaw,
										Type:             "function",
										ThoughtSignature: thoughtSig,
									}
									s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &tcEnd})

									// Preserve tool call in the accumulated response.
									contentParts = append(contentParts, llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &tcEnd})
								}
							}
						}
					}
					// Parse groundingMetadata for web search results (mirrors fromGeminiResponse).
					if gm, ok := c0["groundingMetadata"].(map[string]any); ok && len(gm) > 0 {
						query := ""
						if wq, ok := gm["webSearchQueries"].([]any); ok && len(wq) > 0 {
							qs := make([]string, 0, len(wq))
							for _, q := range wq {
								if s, ok := q.(string); ok {
									qs = append(qs, s)
								}
							}
							query = strings.Join(qs, "; ")
						}
						gmRaw, _ := json.Marshal(gm)
						contentParts = append(contentParts, llm.ContentPart{
							Kind: llm.ContentWebSearch,
							WebSearch: &llm.WebSearchData{
								Query: query,
								Raw:   gmRaw,
							},
						})
					}
					if fr, _ := c0["finishReason"].(string); fr != "" {
						finish = llm.NormalizeFinishReason("google", fr)
						if textStarted {
							s.Send(llm.StreamEvent{Type: llm.StreamEventTextEnd, TextID: textID})
							textStarted = false
						}
						// Finish on explicit finishReason chunk.
						flushTextPart()
						msg := llm.Message{Role: llm.RoleAssistant, Content: contentParts}
						r := llm.Response{
							Provider: "google",
							Model:    req.Model,
							Message:  msg,
							Finish:   finish,
							Usage:    usage,
							Raw:      raw,
						}
						llm.StampEndpointURL(&r, endpoint)
						if len(r.ToolCalls()) > 0 {
							r.Finish = llm.FinishReason{Reason: "tool_calls", Raw: r.Finish.Raw}
						} else if r.Finish.Reason == "" {
							r.Finish = llm.FinishReason{Reason: "stop"}
						}
						rp := r
						if sseBuf != nil {
							rp.RawRequestBody = rawReqBody
							rp.RawResponseBody = sseBuf.String()
						}
						s.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &r.Finish, Usage: &r.Usage, Response: &rp})
						finished = true
						cancel()
						return nil
					}
				}
			}

			if um, ok := raw["usageMetadata"].(map[string]any); ok {
				usage = parseUsage(um)
			}

			s.Send(llm.StreamEvent{Type: llm.StreamEventProviderEvent, Raw: raw})
			return nil
		},
	}
	runner.Run(sctx)
}
