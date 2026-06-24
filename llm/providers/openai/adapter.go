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
	name               string
	APIKey             string
	BaseURL            string
	ResponsesPath      string
	OrgID              string
	ProjectID          string
	ChatGPTAccountID   string
	AuthScopeIdentity  llm.AuthScopeIdentity
	OrgIDHash          string
	ProjectIDHash      string
	ContinuationHasher *llm.ContinuationHasher
	Client             *http.Client
	DefaultHeaders     map[string]string
}

// OpenAIInstanceParams holds the configuration for a single OpenAI adapter instance.
type OpenAIInstanceParams struct {
	Name               string
	APIKey             string
	BaseURL            string
	OrgID              string
	ProjectID          string
	ChatGPTBaseURL     string
	StateHome          string
	ContinuationHasher *llm.ContinuationHasher
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
		workspaceID := strings.TrimSpace(status.WorkspaceID)
		chatGPTAccountID := accountID
		if chatGPTAccountID == "" {
			chatGPTAccountID = workspaceID
		}
		if chatGPTAccountID == "" {
			record, loadErr := authopenai.LoadAuth(authStateDir, instanceName)
			if loadErr == nil {
				if claims, parseErr := authopenai.ParseIDTokenClaims(record.IDToken); parseErr == nil {
					if accountID == "" {
						accountID = strings.TrimSpace(claims.AccountID)
					}
					if workspaceID == "" {
						workspaceID = strings.TrimSpace(claims.WorkspaceID)
					}
					chatGPTAccountID = accountID
					if chatGPTAccountID == "" {
						chatGPTAccountID = workspaceID
					}
				}
			}
		}
		authScope, err := authScopeForOAuth(params.ContinuationHasher, accountID, workspaceID)
		if err != nil {
			return nil, err
		}
		return &Adapter{
			name:               params.Name,
			APIKey:             creds.BearerToken,
			BaseURL:            strings.TrimRight(base, "/"),
			ResponsesPath:      defaultCodexResponses,
			ChatGPTAccountID:   chatGPTAccountID,
			AuthScopeIdentity:  authScope,
			ContinuationHasher: params.ContinuationHasher,
			Client:             &http.Client{Timeout: 0},
		}, nil
	}

	key := strings.TrimSpace(params.APIKey)
	if key != "" {
		base := strings.TrimSpace(params.BaseURL)
		if base == "" {
			base = defaultAPIBaseURL
		}
		orgID := strings.TrimSpace(params.OrgID)
		projectID := strings.TrimSpace(params.ProjectID)
		authScope, err := authScopeForAPIKey(params.ContinuationHasher, key)
		if err != nil {
			return nil, err
		}
		orgIDHash, err := hashOpenAIScopeIdentifier(params.ContinuationHasher, "org_id", orgID)
		if err != nil {
			return nil, err
		}
		projectIDHash, err := hashOpenAIScopeIdentifier(params.ContinuationHasher, "project_id", projectID)
		if err != nil {
			return nil, err
		}
		return &Adapter{
			name:               params.Name,
			APIKey:             key,
			BaseURL:            strings.TrimRight(base, "/"),
			ResponsesPath:      defaultResponsesPath,
			OrgID:              orgID,
			ProjectID:          projectID,
			AuthScopeIdentity:  authScope,
			OrgIDHash:          orgIDHash,
			ProjectIDHash:      projectIDHash,
			ContinuationHasher: params.ContinuationHasher,
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
		params, err := instanceParamsFromConfig(inst.Name, inst.BaseURL, inst.APIKey, stateHome)
		if err != nil {
			return nil, err
		}
		return NewForInstance(params)
	}
	llm.RegisterInstanceAdapterFactory("openai", "responses", factory)
	llm.RegisterInstanceAdapterFactory("openai", "", factory)
}

// instanceParamsFromConfig builds OpenAIInstanceParams for a config-driven
// instance, threading OPENAI_ORG_ID, OPENAI_PROJECT_ID, and
// OPENAI_CHATGPT_BASE_URL from the environment to mirror NewFromEnv. The API
// key is injected by the loader (never read from env here).
func instanceParamsFromConfig(name, baseURL, apiKey, stateHome string) (OpenAIInstanceParams, error) {
	hasher, err := continuationHasherForStateHome(stateHome)
	if err != nil {
		return OpenAIInstanceParams{}, err
	}
	return OpenAIInstanceParams{
		Name:               name,
		BaseURL:            baseURL,
		APIKey:             apiKey,
		OrgID:              envvars.OpenAIOrgID.Trimmed(),
		ProjectID:          envvars.OpenAIProjectID.Trimmed(),
		ChatGPTBaseURL:     envvars.OpenAIChatGPTBaseURL.Trimmed(),
		StateHome:          stateHome,
		ContinuationHasher: hasher,
	}, nil
}

func authScopeForAPIKey(hasher *llm.ContinuationHasher, apiKey string) (llm.AuthScopeIdentity, error) {
	if hasher == nil {
		return llm.AuthScopeIdentity{}, nil
	}
	hash, err := hasher.HashContinuationScopeValue("credential", apiKey)
	if err != nil {
		return llm.AuthScopeIdentity{}, err
	}
	return llm.AuthScopeIdentity{
		Version:        "cont-scope-v1",
		AuthSource:     "api_key",
		CredentialHash: hash,
	}, nil
}

func authScopeForOAuth(hasher *llm.ContinuationHasher, accountID, workspaceID string) (llm.AuthScopeIdentity, error) {
	if hasher == nil {
		return llm.AuthScopeIdentity{}, nil
	}
	accountHash, err := hashOpenAIScopeIdentifier(hasher, "account", accountID)
	if err != nil {
		return llm.AuthScopeIdentity{}, err
	}
	workspaceHash, err := hashOpenAIScopeIdentifier(hasher, "workspace", workspaceID)
	if err != nil {
		return llm.AuthScopeIdentity{}, err
	}
	credentialHash, err := hasher.HashContinuationScopeValue("credential", "oauth:"+strings.TrimSpace(accountID)+":"+strings.TrimSpace(workspaceID))
	if err != nil {
		return llm.AuthScopeIdentity{}, err
	}
	return llm.AuthScopeIdentity{
		Version:        "cont-scope-v1",
		AuthSource:     "oauth",
		CredentialHash: credentialHash,
		AccountHash:    accountHash,
		WorkspaceHash:  workspaceHash,
	}, nil
}

func hashOpenAIScopeIdentifier(hasher *llm.ContinuationHasher, kind, value string) (string, error) {
	if hasher == nil || strings.TrimSpace(value) == "" {
		return "", nil
	}
	return hasher.HashContinuationScopeValue(kind, value)
}

func NewFromEnv(cfgs ...Config) (*Adapter, error) {
	var cfg Config
	for _, next := range cfgs {
		if strings.TrimSpace(next.StateHome) != "" {
			cfg.StateHome = next.StateHome
		}
	}
	hasher, err := continuationHasherForStateHome(cfg.StateHome)
	if err != nil {
		return nil, err
	}
	return NewForInstance(OpenAIInstanceParams{
		Name:               "openai",
		APIKey:             envvars.OpenAIAPIKey.Trimmed(),
		BaseURL:            envvars.OpenAIBaseURL.Trimmed(),
		OrgID:              envvars.OpenAIOrgID.Trimmed(),
		ProjectID:          envvars.OpenAIProjectID.Trimmed(),
		ChatGPTBaseURL:     envvars.OpenAIChatGPTBaseURL.Trimmed(),
		StateHome:          cfg.StateHome,
		ContinuationHasher: hasher,
	})
}

func continuationHasherForStateHome(stateHome string) (*llm.ContinuationHasher, error) {
	return llm.ContinuationHasherForStateDir(authopenai.DefaultStateDirWithStateHome(stateHome))
}

func (a *Adapter) Name() string {
	if a.name != "" {
		return a.name
	}
	return "openai"
}

func (a *Adapter) PlanResponsesContinuation(req llm.Request) (llm.ResponsesContinuationPlan, error) {
	endpointFamily := llm.ResponsesEndpointFamilyOpenAIPublic
	if a.usesCodexBackend() {
		endpointFamily = llm.ResponsesEndpointFamilyOpenAICodex
	}

	body, err := a.buildRequestBody(req)
	if err != nil {
		return llm.ResponsesContinuationPlan{}, err
	}
	requestFingerprint, err := requestFingerprintForResponsesBody(endpointFamily, body)
	if err != nil {
		return llm.ResponsesContinuationPlan{}, err
	}
	if a.ContinuationHasher == nil {
		return llm.ResponsesContinuationPlan{}, fmt.Errorf("%w: missing OpenAI continuation hasher", llm.ErrContinuationSecretUnavailable)
	}
	storagePolicy, storageAllowed := responsesStoragePolicyForPlan(endpointFamily, body)
	conversationIDHash := ""
	if strings.TrimSpace(req.ConversationID) != "" {
		conversationIDHash, err = a.ContinuationHasher.HashContinuationScopeValue("conversation_id", req.ConversationID)
		if err != nil {
			return llm.ResponsesContinuationPlan{}, err
		}
	}
	storageScope := llm.ContinuationStorageScope{
		HashVersion:        llm.ContinuationScopeHashVersion,
		Provider:           "openai",
		EndpointFamily:     string(endpointFamily),
		BaseURL:            normalizedResponsesBaseURL(a.BaseURL, endpointFamily),
		Path:               normalizedResponsesPath(a.ResponsesPath),
		AuthSource:         a.AuthScopeIdentity.AuthSource,
		OrgIDHash:          a.OrgIDHash,
		ProjectIDHash:      a.ProjectIDHash,
		AccountHash:        a.AuthScopeIdentity.AccountHash,
		WorkspaceHash:      a.AuthScopeIdentity.WorkspaceHash,
		CredentialHash:     a.AuthScopeIdentity.CredentialHash,
		ConversationIDHash: conversationIDHash,
		StoragePolicy:      storagePolicy,
	}
	storageScopeFingerprint, err := a.ContinuationHasher.HashContinuationStorageScope(storageScope)
	if err != nil {
		return llm.ResponsesContinuationPlan{}, err
	}
	storageScope.Fingerprint = storageScopeFingerprint

	plan := llm.PlanResponsesContinuation(llm.ResponsesContinuationPlanInput{
		EndpointFamily:    endpointFamily,
		AuthScopeIdentity: a.AuthScopeIdentity,
		OrgIDHash:         a.OrgIDHash,
		ProjectIDHash:     a.ProjectIDHash,
		Request:           req,
	})
	plan.RequestFingerprint = requestFingerprint
	plan.StorageScope = storageScope
	plan.StorageScopeFingerprint = storageScopeFingerprint
	plan.StoragePolicyLabel = storagePolicy
	plan.ContinuationStorageAllowed = storageAllowed
	plan.CanFallbackToChat = true
	return plan, nil
}

func responsesStoragePolicyForPlan(endpointFamily llm.ResponsesEndpointFamily, body map[string]any) (string, bool) {
	if endpointFamily == llm.ResponsesEndpointFamilyOpenAICodex {
		return llm.ResponsesStoragePolicyCodexUnproven, false
	}
	store, _ := body["store"].(bool)
	if store {
		return llm.ResponsesStoragePolicyPublicOpenAIStore, true
	}
	return llm.ResponsesStoragePolicyPublicOpenAINoStore, false
}

func normalizedResponsesBaseURL(baseURL string, endpointFamily llm.ResponsesEndpointFamily) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base != "" {
		return base
	}
	if endpointFamily == llm.ResponsesEndpointFamilyOpenAICodex {
		return defaultChatGPTBaseURL
	}
	return defaultAPIBaseURL
}

func normalizedResponsesPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return defaultResponsesPath
	}
	return path
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
			a.recordResponsesFallbackAttempt(ctx, req, err)
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
			a.recordResponsesFallbackAttempt(out, req, ev.Err)
			responsesStream.Close() //nolint:errcheck
			fallbackReq := chatFallbackRequest(req)
			ccStream, ccErr := a.streamViaChatCompletions(out, fallbackReq)
			if ccErr != nil {
				a.recordChatFallbackAttempt(out, fallbackReq, nil, ccErr)
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
				if ccEv.Type == llm.StreamEventFinish && ccEv.Response != nil {
					a.recordChatFallbackAttempt(out, fallbackReq, ccEv.Response, nil)
				}
				if ccEv.Type == llm.StreamEventError && ccEv.Err != nil {
					a.recordChatFallbackAttempt(out, fallbackReq, nil, ccEv.Err)
				}
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
	fallbackReq := chatFallbackRequest(req)
	ccStream, ccErr := a.streamViaChatCompletions(ctx, fallbackReq)
	if ccErr != nil {
		a.recordChatFallbackAttempt(ctx, fallbackReq, nil, ccErr)
		return nil, fmt.Errorf(
			"openai: model %q failed on both endpoints — "+
				"/v1/responses: %w; /v1/chat/completions: %w",
			req.Model, responsesErr, ccErr,
		)
	}
	return a.recordChatFallbackStream(ctx, fallbackReq, ccStream), nil
}

func chatFallbackRequest(req llm.Request) llm.Request {
	fallbackReq := req
	fallbackReq.HistoryMode = llm.HistoryModeChatFallback
	if len(req.FullHistoryFallbackMessages) > 0 {
		fallbackReq.Messages = append([]llm.Message(nil), req.FullHistoryFallbackMessages...)
	}
	fallbackReq.PreviousResponseID = ""
	fallbackReq.ConversationID = ""
	fallbackReq.Continuation = nil
	fallbackReq.FullHistoryFallbackMessages = nil
	return fallbackReq
}

func (a *Adapter) recordChatFallbackStream(ctx context.Context, req llm.Request, inner llm.Stream) llm.Stream {
	proxy := llm.NewChanStream(func() { _ = inner.Close() })
	go func() {
		defer proxy.CloseSend()
		defer inner.Close() //nolint:errcheck
		for ev := range inner.Events() {
			if ev.Type == llm.StreamEventFinish && ev.Response != nil {
				a.recordChatFallbackAttempt(ctx, req, ev.Response, nil)
			}
			if ev.Type == llm.StreamEventError && ev.Err != nil {
				a.recordChatFallbackAttempt(ctx, req, nil, ev.Err)
			}
			proxy.Send(ev)
		}
	}()
	return proxy
}

func (a *Adapter) recordResponsesFallbackAttempt(ctx context.Context, req llm.Request, err error) {
	attemptReq := req
	if attemptReq.HistoryMode == "" {
		attemptReq.HistoryMode = llm.HistoryModeFullHistory
	}
	llm.RecordAdapterAttempt(ctx, llm.AdapterAttemptRecord{
		Request:     attemptReq,
		Error:       err,
		Mode:        "stream",
		HistoryMode: attemptReq.HistoryMode,
		EndpointURL: a.responsesURL(),
	})
}

func (a *Adapter) recordChatFallbackAttempt(ctx context.Context, req llm.Request, resp *llm.Response, err error) {
	attemptReq := req
	attemptReq.HistoryMode = llm.HistoryModeChatFallback
	if resp != nil {
		llm.StampEndpointURL(resp, a.chatCompletionsURL())
	}
	llm.RecordAdapterAttempt(ctx, llm.AdapterAttemptRecord{
		Request:     attemptReq,
		Response:    resp,
		Error:       err,
		Mode:        "stream",
		HistoryMode: llm.HistoryModeChatFallback,
		EndpointURL: a.chatCompletionsURL(),
		Terminal:    true,
	})
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
