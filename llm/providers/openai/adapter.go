package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"time"

	authopenai "primeradiant.com/serf/auth/openai"
	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
)

// errEmptyResponsesStream is a sentinel error emitted when the Responses API
// stream returns 200 OK but closes without any content events (no text delta,
// no tool call, no finish). This indicates the model is not supported on the
// Responses API endpoint and triggers an automatic fallback to Chat Completions.
var errEmptyResponsesStream = errors.New("openai responses stream closed with no events (model may not support /v1/responses)")

// errNoCredentials is the sentinel returned by NewFromEnv when no OpenAI
// credentials (API key or stored auth) are configured. The env-adapter factory
// matches it with errors.Is to skip the provider silently rather than surfacing
// a hard error.
var errNoCredentials = errors.New("no OpenAI credentials configured")

const (
	defaultAPIBaseURL     = "https://api.openai.com"
	defaultResponsesPath  = "/v1/responses"
	defaultChatGPTBaseURL = "https://chatgpt.com"
	defaultCodexResponses = "/backend-api/codex/responses"
	// ChatGPT Codex /models requires semver, and 0.0.0 is the Codex
	// workspace development version without claiming a Codex release.
	codexClientVersion = "0.0.0"
	defaultOriginator  = "serf"
	encryptedReasoning = "reasoning.encrypted_content"
)

type Config struct {
	StateHome string
}

type Adapter struct {
	name             string
	APIKey           string
	BaseURL          string
	ResponsesPath    string
	OrgID            string
	ProjectID        string
	ChatGPTAccountID string
	Client           *http.Client
	DefaultHeaders   map[string]string
}

// OpenAIInstanceParams holds the configuration for a single OpenAI adapter instance.
type OpenAIInstanceParams struct {
	Name           string
	APIKey         string
	BaseURL        string
	OrgID          string
	ProjectID      string
	ChatGPTBaseURL string
	StateHome      string
}

// NewForInstance constructs an Adapter from explicit parameters.
// OAuth resolution uses StateHome and instanceName for per-instance OAuth files
// (auth/<instanceName>.json). Empty BaseURL/ChatGPTBaseURL fall back to the
// same defaults as the env path.
func NewForInstance(params OpenAIInstanceParams) (*Adapter, error) {
	// Prefer stored OAuth over APIKey: once a user has signed in via
	// `serf openai login`, route through the ChatGPT/Codex backend instead of
	// the key-based API. This mirrors the preference order in NewFromEnv.
	authStateDir := authopenai.DefaultStateDirWithStateHome(params.StateHome)
	instanceName := params.Name
	service := authopenai.NewService(authopenai.DefaultConfig(), nil)
	status, err := service.Status(authStateDir, instanceName)
	if err != nil {
		return nil, fmt.Errorf("load OpenAI auth: %w", err)
	}
	if status.SignedIn && status.Source == authopenai.AuthSourceOAuth {
		creds, err := service.ResolveRuntimeCredentials(context.Background(), authStateDir, instanceName)
		if err != nil {
			return nil, fmt.Errorf("resolve OpenAI auth: %w", err)
		}
		status, err = service.Status(authStateDir, instanceName)
		if err != nil {
			return nil, fmt.Errorf("refresh OpenAI auth status: %w", err)
		}
		base := strings.TrimSpace(params.ChatGPTBaseURL)
		if base == "" {
			base = defaultChatGPTBaseURL
		}
		accountID := strings.TrimSpace(status.AccountID)
		if accountID == "" {
			accountID = strings.TrimSpace(status.WorkspaceID)
		}
		if accountID == "" {
			record, loadErr := authopenai.LoadAuth(authStateDir, instanceName)
			if loadErr == nil {
				if claims, parseErr := authopenai.ParseIDTokenClaims(record.IDToken); parseErr == nil {
					accountID = strings.TrimSpace(claims.AccountID)
					if accountID == "" {
						accountID = strings.TrimSpace(claims.WorkspaceID)
					}
				}
			}
		}
		return &Adapter{
			name:             params.Name,
			APIKey:           creds.BearerToken,
			BaseURL:          strings.TrimRight(base, "/"),
			ResponsesPath:    defaultCodexResponses,
			ChatGPTAccountID: accountID,
			Client:           &http.Client{Timeout: 0},
		}, nil
	}

	key := strings.TrimSpace(params.APIKey)
	if key != "" {
		base := strings.TrimSpace(params.BaseURL)
		if base == "" {
			base = defaultAPIBaseURL
		}
		return &Adapter{
			name:          params.Name,
			APIKey:        key,
			BaseURL:       strings.TrimRight(base, "/"),
			ResponsesPath: defaultResponsesPath,
			OrgID:         strings.TrimSpace(params.OrgID),
			ProjectID:     strings.TrimSpace(params.ProjectID),
			// Avoid short client-level timeouts; rely on request context deadlines instead.
			Client: &http.Client{Timeout: 0},
		}, nil
	}

	return nil, errNoCredentials
}

func init() {
	llm.RegisterEnvAdapterFactory(func(env llm.EnvConfig) (llm.ProviderAdapter, bool, error) {
		a, err := NewFromEnv(Config{StateHome: env.StateHome})
		if err != nil {
			if isUnconfigured(err) {
				return nil, false, nil
			}
			return nil, true, err
		}
		return a, true, nil
	})
	// Register for config-driven construction: openai + responses (or empty) style.
	factory := func(inst providercfg.InstanceConfig, stateHome string) (llm.ProviderAdapter, error) {
		return NewForInstance(instanceParamsFromConfig(inst.Name, inst.BaseURL, inst.APIKey, stateHome))
	}
	llm.RegisterInstanceAdapterFactory("openai", "responses", factory)
	llm.RegisterInstanceAdapterFactory("openai", "", factory)
}

// instanceParamsFromConfig builds OpenAIInstanceParams for a config-driven
// instance, threading OPENAI_ORG_ID, OPENAI_PROJECT_ID, and
// OPENAI_CHATGPT_BASE_URL from the environment to mirror NewFromEnv. The API
// key is injected by the loader (never read from env here).
func instanceParamsFromConfig(name, baseURL, apiKey, stateHome string) OpenAIInstanceParams {
	return OpenAIInstanceParams{
		Name:           name,
		BaseURL:        baseURL,
		APIKey:         apiKey,
		OrgID:          envvars.OpenAIOrgID.Trimmed(),
		ProjectID:      envvars.OpenAIProjectID.Trimmed(),
		ChatGPTBaseURL: envvars.OpenAIChatGPTBaseURL.Trimmed(),
		StateHome:      stateHome,
	}
}

func NewFromEnv(cfgs ...Config) (*Adapter, error) {
	var cfg Config
	for _, next := range cfgs {
		if strings.TrimSpace(next.StateHome) != "" {
			cfg.StateHome = next.StateHome
		}
	}
	return NewForInstance(OpenAIInstanceParams{
		Name:           "openai",
		APIKey:         envvars.OpenAIAPIKey.Trimmed(),
		BaseURL:        envvars.OpenAIBaseURL.Trimmed(),
		OrgID:          envvars.OpenAIOrgID.Trimmed(),
		ProjectID:      envvars.OpenAIProjectID.Trimmed(),
		ChatGPTBaseURL: envvars.OpenAIChatGPTBaseURL.Trimmed(),
		StateHome:      cfg.StateHome,
	})
}

func (a *Adapter) Name() string {
	if a.name != "" {
		return a.name
	}
	return "openai"
}

func (a *Adapter) setHeaders(req *http.Request) {
	// Apply default headers first so provider-specific headers take precedence.
	for k, v := range a.DefaultHeaders {
		req.Header.Set(k, v)
	}
	if a.usesCodexBackend() {
		if req.Header.Get("originator") == "" {
			req.Header.Set("originator", defaultOriginator)
		}
		if req.Header.Get("User-Agent") == "" {
			req.Header.Set("User-Agent", defaultUserAgent())
		}
	}
	req.Header.Set("Authorization", "Bearer "+a.APIKey)
	req.Header.Set("Content-Type", "application/json")
	if a.OrgID != "" {
		req.Header.Set("OpenAI-Organization", a.OrgID)
	}
	if a.ProjectID != "" {
		req.Header.Set("OpenAI-Project", a.ProjectID)
	}
	if a.ChatGPTAccountID != "" {
		req.Header.Set("ChatGPT-Account-ID", a.ChatGPTAccountID)
	}
}

func (a *Adapter) setRequestHeaders(httpReq *http.Request, req llm.Request) {
	a.setHeaders(httpReq)
	if !a.usesCodexBackend() {
		return
	}
	if strings.TrimSpace(req.SessionID) != "" {
		httpReq.Header.Set("session-id", strings.TrimSpace(req.SessionID))
	}
	if strings.TrimSpace(req.ThreadID) != "" {
		threadID := strings.TrimSpace(req.ThreadID)
		httpReq.Header.Set("thread-id", threadID)
		httpReq.Header.Set("x-client-request-id", threadID)
	}
}

// ClientVersion is reported in the User-Agent the OpenAI Codex backend expects.
// It defaults to "dev"; an embedding application may set it to its own version
// (the serf binaries set it to the build version at startup).
var ClientVersion = "dev"

func defaultUserAgent() string {
	version := strings.TrimSpace(ClientVersion)
	if version == "" {
		version = "dev"
	}
	return fmt.Sprintf("%s/%s (%s %s)", defaultOriginator, version, runtime.GOOS, runtime.GOARCH)
}

func mergeStringMaps(maps ...map[string]string) map[string]string {
	var out map[string]string
	for _, m := range maps {
		for k, v := range m {
			if out == nil {
				out = map[string]string{}
			}
			out[k] = v
		}
	}
	return out
}

func appendUniqueString(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func (a *Adapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if a.requiresStreamingComplete() {
		return a.completeViaStream(ctx, req)
	}

	if a.Client == nil {
		a.Client = &http.Client{Timeout: 0}
	}

	body, err := a.buildRequestBody(req)
	if err != nil {
		return llm.Response{}, err
	}

	b, err := json.Marshal(body)
	if err != nil {
		return llm.Response{}, err
	}

	ctx, adapterCancel := llm.ApplyAdapterTimeout(ctx, req.AdapterTimeout, false)
	defer adapterCancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.responsesURL(), bytes.NewReader(b))
	if err != nil {
		return llm.Response{}, err
	}
	a.setRequestHeaders(httpReq, req)

	client := llm.ClientWithConnectTimeout(a.Client, req.AdapterTimeout)
	resp, err := client.Do(httpReq)
	if err != nil {
		return llm.Response{}, llm.WrapContextError("openai", err)
	}
	defer func() { _ = resp.Body.Close() }()

	rawBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return llm.Response{}, err
	}
	var raw map[string]any
	dec := json.NewDecoder(bytes.NewReader(rawBytes))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return llm.Response{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		ra := llm.ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		msg := fmt.Sprintf("responses.create failed: %v", raw)
		return llm.Response{}, llm.ErrorFromHTTPStatusWithRawBodies("openai", resp.StatusCode, msg, raw, ra, string(b), string(rawBytes))
	}

	r := fromResponses(raw, req.Model)
	llm.StampEndpointURL(&r, a.responsesURL())
	r.RateLimit = llm.ParseRateLimitHeaders(resp.Header)
	if llm.RawBodyEnabled() {
		r.RawRequestBody = string(b)
		r.RawResponseBody = string(rawBytes)
	}
	return r, nil
}

func (a *Adapter) completeViaStream(ctx context.Context, req llm.Request) (llm.Response, error) {
	stream, err := a.Stream(ctx, req)
	if err != nil {
		return llm.Response{}, err
	}
	defer func() { _ = stream.Close() }()

	acc := llm.NewStreamAccumulator()
	for ev := range stream.Events() {
		if ev.Type == llm.StreamEventError {
			if ev.Err != nil {
				return llm.Response{}, ev.Err
			}
			return llm.Response{}, errors.New("openai stream failed")
		}
		acc.Process(ev)
	}

	resp := acc.Response()
	if resp == nil {
		return llm.Response{}, errors.New("openai stream completed without final response")
	}
	return *resp, nil
}

// Stream implements llm.ProviderAdapter. It tries the Responses API first; if
// the stream closes with 200 OK but zero content events (a silent failure mode
// observed for models that do not support /v1/responses), it automatically
// falls back to /v1/chat/completions. Both failures are surfaced — never silent.
func (a *Adapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	if a.Client == nil {
		a.Client = &http.Client{Timeout: 0}
	}

	// Try Responses API first.
	responsesStream, err := a.streamResponses(ctx, req)
	if err != nil {
		// Hard error from Responses API (non-2xx, etc.) — fall through to Chat
		// Completions only if it looks like a model-compatibility error.
		if isFallbackEligible(err) {
			return a.fallbackToChatCompletions(ctx, req, err)
		}
		return nil, err
	}

	// Intercept the event channel. If the first error we see is the empty-stream
	// sentinel (and no content was already forwarded), swap to Chat Completions.
	out, outCancel := context.WithCancel(ctx)
	proxy := llm.NewChanStream(outCancel)

	go a.decodeStream(out, proxy, responsesStream, req)

	return &responsesFallbackProxyStream{
		proxy:           proxy,
		responsesStream: responsesStream,
	}, nil
}

// decodeStream proxies the Responses API stream in its own goroutine, watching
// for the empty-stream sentinel. If the Responses stream yields no content before
// signalling errEmptyResponsesStream, it swaps to Chat Completions and forwards
// those events instead; otherwise it forwards Responses events verbatim. It owns
// closing the proxy ChanStream and the underlying Responses stream.
func (a *Adapter) decodeStream(out context.Context, proxy *llm.ChanStream, responsesStream llm.Stream, req llm.Request) {
	defer proxy.CloseSend()

	sentContent := false
	for ev := range responsesStream.Events() {
		if isContentEvent(ev.Type) {
			sentContent = true
		}
		if ev.Type == llm.StreamEventError && errors.Is(ev.Err, errEmptyResponsesStream) && !sentContent {
			// Responses API gave us nothing. Fall back to Chat Completions.
			responsesStream.Close() //nolint:errcheck
			ccStream, ccErr := a.streamViaChatCompletions(out, req)
			if ccErr != nil {
				combinedMsg := fmt.Sprintf(
					"openai: model %q failed on both endpoints — "+
						"/v1/responses: empty stream (model not supported); "+
						"/v1/chat/completions: %v",
					req.Model, ccErr,
				)
				proxy.Send(llm.StreamEvent{
					Type: llm.StreamEventError,
					Err:  llm.NewStreamError("openai", combinedMsg, ccErr),
				})
				return
			}
			// Forward all events from the Chat Completions stream.
			for ccEv := range ccStream.Events() {
				proxy.Send(ccEv)
			}
			ccStream.Close() //nolint:errcheck
			return
		}
		proxy.Send(ev)
	}
	responsesStream.Close() //nolint:errcheck
}

type responsesFallbackProxyStream struct {
	proxy           llm.Stream
	responsesStream llm.Stream
	once            sync.Once
	closeErr        error
}

func (s *responsesFallbackProxyStream) Events() <-chan llm.StreamEvent {
	return s.proxy.Events()
}

func (s *responsesFallbackProxyStream) Close() error {
	s.once.Do(func() {
		respErr := s.responsesStream.Close()
		proxyErr := s.proxy.Close()
		if respErr != nil {
			s.closeErr = respErr
		} else {
			s.closeErr = proxyErr
		}
	})
	return s.closeErr
}

// isContentEvent reports whether an event type carries model-generated content.
// We use this to decide whether the Responses stream was "empty".
func isContentEvent(t llm.StreamEventType) bool {
	switch t {
	case llm.StreamEventTextStart, llm.StreamEventTextDelta,
		llm.StreamEventToolCallStart, llm.StreamEventToolCallDelta,
		llm.StreamEventToolCallEnd, llm.StreamEventFinish:
		return true
	}
	return false
}

// isFallbackEligible reports whether an error from the Responses API indicates
// the model may not be supported there (and Chat Completions may work instead).
// Specifically: 404 (model/endpoint not found) and 422 (unprocessable entity)
// are the documented error codes OpenAI returns for endpoint/model mismatches.
func isFallbackEligible(err error) bool {
	if err == nil {
		return false
	}
	var nf interface{ StatusCode() int }
	if errors.As(err, &nf) {
		sc := nf.StatusCode()
		return sc == 404 || sc == 422
	}
	return errors.Is(err, errEmptyResponsesStream)
}

// fallbackToChatCompletions wraps a Responses API error with fallback context.
// It attempts /v1/chat/completions and, if that also fails, returns a combined
// error naming the model and both endpoints.
func (a *Adapter) fallbackToChatCompletions(ctx context.Context, req llm.Request, responsesErr error) (llm.Stream, error) {
	ccStream, ccErr := a.streamViaChatCompletions(ctx, req)
	if ccErr != nil {
		return nil, fmt.Errorf(
			"openai: model %q failed on both endpoints — "+
				"/v1/responses: %w; /v1/chat/completions: %w",
			req.Model, responsesErr, ccErr,
		)
	}
	return ccStream, nil
}

// chatCompletionsURL returns the /v1/chat/completions URL for this adapter,
// derived from the same BaseURL used for the Responses API.
func (a *Adapter) chatCompletionsURL() string {
	base := strings.TrimRight(a.BaseURL, "/")
	// If this adapter is pointed at an API-key-based OpenAI endpoint, the base
	// is https://api.openai.com and completions lives at /v1/chat/completions.
	// For custom base URLs we keep the same path convention.
	if strings.HasSuffix(base, "/v1") {
		return base + "/chat/completions"
	}
	return base + "/v1/chat/completions"
}

func (a *Adapter) responsesURL() string {
	base := strings.TrimRight(a.BaseURL, "/")
	path := a.ResponsesPath
	if path == "" {
		path = defaultResponsesPath
	}
	if strings.HasPrefix(path, "/") {
		return base + path
	}
	return base + "/" + path
}

func (a *Adapter) requiresStreamingComplete() bool {
	return a.ChatGPTAccountID != "" || a.ResponsesPath == defaultCodexResponses
}

func responseHasAssistantContent(r llm.Response) bool {
	return strings.TrimSpace(r.Text()) != "" || len(r.ToolCalls()) > 0
}

// isUnconfigured reports whether err means "no usable OpenAI credentials" — the
// no-credentials sentinel, or an auth-not-found error surfaced through the OAuth
// resolution path (login required). The env-adapter factory uses it to skip the
// provider silently. Both arms are matched via errors.Is, not by string.
func isUnconfigured(err error) bool {
	return errors.Is(err, errNoCredentials) || errors.Is(err, authopenai.ErrAuthNotFound)
}

func firstPositiveInt(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func (a *Adapter) modelsURL() string {
	if a.usesCodexBackend() {
		u := a.codexBackendURL("models")
		sep := "?"
		if strings.Contains(u, "?") {
			sep = "&"
		}
		return u + sep + url.Values{"client_version": []string{codexClientVersion}}.Encode()
	}
	return strings.TrimRight(a.BaseURL, "/") + "/v1/models"
}

func (a *Adapter) usesCodexBackend() bool {
	return a.ChatGPTAccountID != "" || strings.TrimSpace(a.ResponsesPath) == defaultCodexResponses
}

func (a *Adapter) codexBackendURL(path string) string {
	base := strings.TrimRight(a.BaseURL, "/")
	if strings.HasSuffix(base, "/backend-api/codex") {
		return base + "/" + strings.TrimLeft(path, "/")
	}
	return base + "/backend-api/codex/" + strings.TrimLeft(path, "/")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func parseUsage(u map[string]any) llm.Usage {
	// OpenAI's Responses API reports input_tokens as total-including-cached,
	// with cached_tokens a subset in input_tokens_details. The llm.Usage
	// invariant is that InputTokens means new uncached input only, so we
	// subtract cached here.
	rawInput := llm.IntFromAny(u["input_tokens"])
	output := llm.IntFromAny(u["output_tokens"])
	var cachedRead int
	if inDetails, ok := u["input_tokens_details"].(map[string]any); ok {
		cachedRead = llm.IntFromAny(inDetails["cached_tokens"])
	}
	uncachedInput := rawInput - cachedRead
	if uncachedInput < 0 {
		uncachedInput = 0
	}
	usage := llm.Usage{
		InputTokens:  uncachedInput,
		OutputTokens: output,
		TotalTokens:  llm.IntFromAny(u["total_tokens"]),
		Raw:          u,
	}
	if outDetails, ok := u["output_tokens_details"].(map[string]any); ok {
		rt := llm.IntFromAny(outDetails["reasoning_tokens"])
		usage.ReasoningTokens = &rt
	}
	if _, ok := u["input_tokens_details"].(map[string]any); ok {
		ct := cachedRead
		usage.CacheReadTokens = &ct
	}
	return usage
}
