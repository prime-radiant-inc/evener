package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/strutil"
	authopenai "primeradiant.com/serf/internal/auth/openai"
	"primeradiant.com/serf/internal/credentials"
	"primeradiant.com/serf/llm/providercfg"
)

type hubAuthController struct {
	stateDir          string
	authEnv           map[string]string
	creds             *credentials.Store
	cfg               authopenai.Config
	client            *http.Client
	now               func() time.Time
	exchangeCode      func(context.Context, *http.Client, authopenai.Config, authopenai.TokenExchangeRequest) (authopenai.TokenSet, error)
	requestDeviceCode func(context.Context, *http.Client, authopenai.Config) (authopenai.DeviceCode, error)
	pollDeviceOnce    func(context.Context, *http.Client, authopenai.Config, authopenai.DeviceCode) (authopenai.DeviceCodeSuccess, bool, error)
	exchangeDevice    func(context.Context, *http.Client, authopenai.Config, string, string) (authopenai.TokenSet, error)
	// providersConfigPath is the path to providers.toml. When non-empty, auth
	// methods resolve the instance type from the file and key credentials and
	// OAuth state by instance name rather than provider type.
	providersConfigPath string

	mu          sync.Mutex
	flows       map[string]hubAuthFlow
	deviceFlows map[string]deviceFlow
}

type hubAuthFlow struct {
	Provider     string
	State        string
	CodeVerifier string
	RedirectURI  string
}

type deviceFlow struct {
	Provider  string
	Code      authopenai.DeviceCode
	StartedAt time.Time
}

func newHubAuthController(launchEnv ...map[string]string) *hubAuthController {
	cfg := authopenai.DefaultConfig()
	client := &http.Client{Timeout: cfg.HTTPTimeout}
	authEnv := effectiveHubAuthEnv(nil)
	if len(launchEnv) > 0 {
		authEnv = effectiveHubAuthEnv(launchEnv[0])
	}
	stateDir := openAIStateDirFromEnv(authEnv)
	credsPath := filepath.Join(filepath.Dir(stateDir), "credentials.toml")
	store, _ := credentials.LoadStore(credsPath)
	return &hubAuthController{
		stateDir:          stateDir,
		authEnv:           authEnv,
		creds:             store,
		cfg:               cfg,
		client:            client,
		now:               time.Now,
		exchangeCode:      authopenai.ExchangeCode,
		requestDeviceCode: authopenai.RequestDeviceCode,
		pollDeviceOnce:    authopenai.PollDeviceAuthOnce,
		exchangeDevice:    authopenai.ExchangeDeviceCode,
		flows:             map[string]hubAuthFlow{},
		deviceFlows:       map[string]deviceFlow{},
	}
}

// newHubAuthControllerWithStore creates a controller backed by an explicit credentials store.
// The OpenAI OAuth state directory is resolved from the process environment (XDG_STATE_HOME / HOME),
// matching the behaviour of the default constructor but without launch-env overrides.
func newHubAuthControllerWithStore(_ string, store *credentials.Store) *hubAuthController {
	authEnv := effectiveHubAuthEnv(nil)
	cfg := authopenai.DefaultConfig()
	client := &http.Client{Timeout: cfg.HTTPTimeout}
	stateDir := openAIStateDirFromEnv(authEnv)
	// A nil store should never happen in production (main.go always supplies
	// one). Fall back to the on-disk default store — the same path
	// newHubAuthController uses — rather than a path-less store whose writes
	// would silently no-op and lose credentials.
	if store == nil {
		store, _ = credentials.LoadStore(filepath.Join(filepath.Dir(stateDir), "credentials.toml"))
	}
	return &hubAuthController{
		stateDir:          stateDir,
		authEnv:           authEnv,
		creds:             store,
		cfg:               cfg,
		client:            client,
		now:               time.Now,
		exchangeCode:      authopenai.ExchangeCode,
		requestDeviceCode: authopenai.RequestDeviceCode,
		pollDeviceOnce:    authopenai.PollDeviceAuthOnce,
		exchangeDevice:    authopenai.ExchangeDeviceCode,
		flows:             map[string]hubAuthFlow{},
		deviceFlows:       map[string]deviceFlow{},
	}
}

func (c *hubAuthController) Status(params appwire.AuthStatusParams) (appwire.AuthStatusResponse, error) {
	name := normalizeAuthProvider(params.Provider)

	// Instance-aware path: resolve type from config when available.
	if c.providersConfigPath != "" {
		if typ, _, err := c.resolveInstanceType(name); err == nil {
			return c.instanceStatus(name, typ), nil
		}
		// Unknown instance — fall through to legacy type-map path.
	}

	// Legacy path: name treated as a provider type.
	if name == "openai" {
		resp, err := c.openAIStatus()
		if err != nil {
			return resp, err
		}
		resp.AuthModes = []string{"apiKey", "oauth"}
		return resp, nil
	}
	modes := credentialAuthModes(name)
	if modes == nil {
		return appwire.AuthStatusResponse{Provider: name, Supported: false, ActiveSource: string(credentials.SourceAbsent)}, nil
	}
	v, src := c.creds.Get(name)
	hasFile, envVar := c.creds.Layers(name)
	return appwire.AuthStatusResponse{
		Provider:      name,
		Supported:     true,
		SignedIn:      v != "",
		ActiveSource:  string(src),
		AuthModes:     modes,
		HasStoredFile: hasFile,
		EnvVar:        envVar,
	}, nil
}

func credentialAuthModes(provider string) []string {
	known := map[string][]string{
		"anthropic":            {"apiKey"},
		"google":               {"apiKey"},
		"gemini":               {"apiKey"},
		"minimax":              {"apiKey"},
		"openrouter":           {"apiKey"},
		"openrouter-anthropic": {"apiKey"},
		"kimi":                 {"apiKey"},
		"glm":                  {"apiKey"},
		"openai-compatible":    {"apiKey"},
		"ollama":               {"none"},
	}
	return known[provider]
}

func (c *hubAuthController) LoginStart(params appwire.AuthLoginStartParams) (appwire.AuthLoginStartResponse, error) {
	provider := normalizeAuthProvider(params.Provider)
	if err := c.requiresOpenAI(provider); err != nil {
		return appwire.AuthLoginStartResponse{}, err
	}

	state, err := authopenai.GenerateState()
	if err != nil {
		return appwire.AuthLoginStartResponse{}, fmt.Errorf("generate OAuth state: %w", err)
	}
	verifier, challenge, err := authopenai.GeneratePKCE()
	if err != nil {
		return appwire.AuthLoginStartResponse{}, fmt.Errorf("generate PKCE values: %w", err)
	}
	redirectURI := c.config().RedirectURI(authopenai.DefaultCallbackPort)
	rawURL, err := c.config().AuthorizeURL(authopenai.AuthorizeURLOptions{
		RedirectURI:   redirectURI,
		State:         state,
		CodeChallenge: challenge,
		OpenBrowser:   false,
	})
	if err != nil {
		return appwire.AuthLoginStartResponse{}, err
	}

	c.mu.Lock()
	if c.flows == nil {
		c.flows = map[string]hubAuthFlow{}
	}
	c.flows[state] = hubAuthFlow{
		Provider:     provider,
		State:        state,
		CodeVerifier: verifier,
		RedirectURI:  redirectURI,
	}
	c.mu.Unlock()

	return appwire.AuthLoginStartResponse{Provider: provider, FlowID: state, URL: rawURL}, nil
}

func (c *hubAuthController) LoginComplete(ctx context.Context, params appwire.AuthLoginCompleteParams) (appwire.AuthLoginCompleteResponse, error) {
	provider := normalizeAuthProvider(params.Provider)
	if err := c.requiresOpenAI(provider); err != nil {
		return appwire.AuthLoginCompleteResponse{}, err
	}
	flowID := strings.TrimSpace(params.FlowID)
	if flowID == "" {
		return appwire.AuthLoginCompleteResponse{}, appwire.InvalidParams("auth login flow is required")
	}

	c.mu.Lock()
	flow, ok := c.flows[flowID]
	c.mu.Unlock()
	if !ok {
		return appwire.AuthLoginCompleteResponse{}, appwire.InvalidParams("auth login flow not found")
	}
	if flow.Provider != provider {
		return appwire.AuthLoginCompleteResponse{}, appwire.InvalidParams("auth login provider does not match flow")
	}

	code, returnedState, err := authopenai.ParseRedirectURL(params.RedirectURL)
	if err != nil {
		return appwire.AuthLoginCompleteResponse{}, appwire.InvalidParams(err.Error())
	}
	if err := authopenai.ValidateState(flow.State, returnedState); err != nil {
		return appwire.AuthLoginCompleteResponse{}, appwire.InvalidParams(err.Error())
	}

	tokens, err := c.exchangeCode(ctx, c.client, c.config(), authopenai.TokenExchangeRequest{
		Code:         code,
		RedirectURI:  flow.RedirectURI,
		CodeVerifier: flow.CodeVerifier,
	})
	if err != nil {
		return appwire.AuthLoginCompleteResponse{}, err
	}

	record := c.authRecordFromTokens(tokens)
	record.Provider = provider
	if claims, err := authopenai.ParseIDTokenClaims(tokens.IDToken); err == nil {
		record.Email = strutil.FirstNonEmpty(claims.Email, record.Email)
		record.AccountID = strutil.FirstNonEmpty(claims.AccountID, record.AccountID)
		record.WorkspaceID = strutil.FirstNonEmpty(claims.WorkspaceID, record.WorkspaceID)
	}
	if err := authopenai.SaveAuth(c.stateDir, provider, record); err != nil {
		return appwire.AuthLoginCompleteResponse{}, err
	}

	c.mu.Lock()
	delete(c.flows, flowID)
	c.mu.Unlock()

	status, err := c.openAIInstanceStatus(provider)
	if err != nil {
		return appwire.AuthLoginCompleteResponse{}, err
	}
	return appwire.AuthLoginCompleteResponse{Status: status}, nil
}

func (c *hubAuthController) Logout(params appwire.AuthLogoutParams) (appwire.AuthLogoutResponse, error) {
	name := normalizeAuthProvider(params.Provider)

	// Determine whether this instance supports OAuth (i.e. is openai-type).
	isOpenAI := c.instanceIsOpenAI(name)

	if !isOpenAI {
		if err := c.creds.Clear(name); err != nil {
			return appwire.AuthLogoutResponse{}, err
		}
		status, _ := c.Status(appwire.AuthStatusParams{Provider: name})
		return appwire.AuthLogoutResponse{Removed: true, Status: status}, nil
	}

	// OpenAI-type: clear the effective layer only. An OAuth record (present or
	// corrupt) shadows the stored file key, so remove it first; otherwise clear
	// the file key. The env layer cannot be cleared.
	_, loadErr := authopenai.LoadAuth(c.stateDir, name)
	hasRecord := loadErr == nil || errors.Is(loadErr, authopenai.ErrAuthCorrupt)
	removed := false
	if hasRecord {
		r, delErr := authopenai.DeleteAuth(c.stateDir, name)
		if delErr != nil {
			return appwire.AuthLogoutResponse{}, delErr
		}
		removed = r
	} else {
		hasFile, _ := c.creds.Layers(name)
		if hasFile {
			if clrErr := c.creds.Clear(name); clrErr != nil {
				return appwire.AuthLogoutResponse{}, clrErr
			}
			removed = true
		}
	}
	status, statusErr := c.openAIInstanceStatus(name)
	if statusErr != nil {
		return appwire.AuthLogoutResponse{}, statusErr
	}
	return appwire.AuthLogoutResponse{Removed: removed, Status: status}, nil
}

func (c *hubAuthController) List(_ appwire.EmptyParams) (appwire.AuthListResponse, error) {
	out := appwire.AuthListResponse{}
	openaiResp, err := c.Status(appwire.AuthStatusParams{Provider: "openai"})
	if err == nil {
		out.Providers = append(out.Providers, openaiResp)
	}
	for _, p := range c.creds.List() {
		if p.Name == "openai" {
			continue
		}
		hasFile, envVar := c.creds.Layers(p.Name)
		out.Providers = append(out.Providers, appwire.AuthStatusResponse{
			Provider:      p.Name,
			Supported:     true,
			SignedIn:      p.Source == credentials.SourceFile || p.Source == credentials.SourceEnv,
			ActiveSource:  string(p.Source),
			AuthModes:     p.AuthModes,
			HasStoredFile: hasFile,
			EnvVar:        envVar,
		})
	}
	return out, nil
}

func (c *hubAuthController) ApiKeySet(params appwire.AuthApiKeySetParams) (appwire.AuthStatusResponse, error) {
	name := normalizeAuthProvider(params.Provider)
	if strings.TrimSpace(params.Value) == "" {
		return appwire.AuthStatusResponse{}, appwire.InvalidParams("value is required")
	}
	if err := c.creds.Set(name, params.Value); err != nil {
		return appwire.AuthStatusResponse{}, err
	}
	return c.Status(appwire.AuthStatusParams{Provider: name})
}

func (c *hubAuthController) openAIStatus() (appwire.AuthStatusResponse, error) {
	// Delegate to the instance-keyed helper using the canonical "openai" name.
	return c.openAIInstanceStatus("openai")
}

func effectiveHubAuthEnv(launchEnv map[string]string) map[string]string {
	out := envToMap(os.Environ())
	for key, value := range launchEnv {
		out[key] = value
	}
	return out
}

func openAIStateDirFromEnv(env map[string]string) string {
	return openAIStateDirFromEnvMap(env)
}

func openAIStatusFromRecord(now time.Time, record authopenai.AuthRecord) authopenai.AuthStatus {
	needsLogin := !record.Expiry.IsZero() && !record.Expiry.After(now)
	return authopenai.AuthStatus{
		SignedIn:     !needsLogin,
		Source:       record.Source,
		Email:        record.Email,
		AccountID:    record.AccountID,
		WorkspaceID:  record.WorkspaceID,
		Expiry:       record.Expiry,
		NeedsRefresh: openAIRecordNeedsRefresh(now, record) && !needsLogin,
		NeedsLogin:   needsLogin,
	}
}

func openAIRecordNeedsRefresh(now time.Time, record authopenai.AuthRecord) bool {
	if record.Expiry.IsZero() {
		return false
	}
	return !record.Expiry.After(now.Add(5 * time.Minute))
}

func (c *hubAuthController) authRecordFromTokens(tokens authopenai.TokenSet) authopenai.AuthRecord {
	return authopenai.AuthRecord{
		Version:      1,
		Provider:     "openai",
		Source:       authopenai.AuthSourceOAuth,
		ObtainedAt:   c.now(),
		TokenType:    strutil.FirstNonEmpty(tokens.TokenType, "Bearer"),
		Scope:        tokens.Scope,
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		IDToken:      tokens.IDToken,
		Expiry:       tokens.Expiry,
	}
}

func (c *hubAuthController) DeviceStart(ctx context.Context, params appwire.AuthDeviceStartParams) (appwire.AuthDeviceStartResponse, error) {
	provider := normalizeAuthProvider(params.Provider)
	if err := c.requiresOpenAI(provider); err != nil {
		return appwire.AuthDeviceStartResponse{}, err
	}
	dc, err := c.requestDeviceCode(ctx, c.client, c.config())
	if err != nil {
		if errors.Is(err, authopenai.ErrDeviceCodeNotEnabled) {
			return appwire.AuthDeviceStartResponse{Provider: provider, Fallback: true}, nil
		}
		return appwire.AuthDeviceStartResponse{}, err
	}
	flowID, err := authopenai.GenerateState()
	if err != nil {
		return appwire.AuthDeviceStartResponse{}, fmt.Errorf("generate device flow id: %w", err)
	}
	c.mu.Lock()
	if c.deviceFlows == nil {
		c.deviceFlows = map[string]deviceFlow{}
	}
	c.deviceFlows[flowID] = deviceFlow{Provider: provider, Code: dc, StartedAt: c.now()}
	c.mu.Unlock()
	return appwire.AuthDeviceStartResponse{
		Provider:        provider,
		FlowID:          flowID,
		UserCode:        dc.UserCode,
		VerificationURL: dc.VerificationURL,
		IntervalSeconds: int(dc.Interval / time.Second),
	}, nil
}

func (c *hubAuthController) DevicePoll(ctx context.Context, params appwire.AuthDevicePollParams) (appwire.AuthDevicePollResponse, error) {
	provider := normalizeAuthProvider(params.Provider)
	if err := c.requiresOpenAI(provider); err != nil {
		return appwire.AuthDevicePollResponse{}, err
	}
	flowID := strings.TrimSpace(params.FlowID)
	c.mu.Lock()
	flow, ok := c.deviceFlows[flowID]
	c.mu.Unlock()
	if !ok {
		return appwire.AuthDevicePollResponse{State: "expired"}, nil
	}
	if c.now().Sub(flow.StartedAt) >= 15*time.Minute {
		c.mu.Lock()
		delete(c.deviceFlows, flowID)
		c.mu.Unlock()
		return appwire.AuthDevicePollResponse{State: "expired"}, nil
	}

	success, pending, err := c.pollDeviceOnce(ctx, c.client, c.config(), flow.Code)
	if err != nil {
		return appwire.AuthDevicePollResponse{}, err
	}
	if pending {
		return appwire.AuthDevicePollResponse{State: "pending"}, nil
	}

	tokens, err := c.exchangeDevice(ctx, c.client, c.config(), success.AuthorizationCode, success.CodeVerifier)
	if err != nil {
		return appwire.AuthDevicePollResponse{}, err
	}
	record := c.authRecordFromTokens(tokens)
	record.Provider = provider
	if claims, err := authopenai.ParseIDTokenClaims(tokens.IDToken); err == nil {
		record.Email = strutil.FirstNonEmpty(claims.Email, record.Email)
		record.AccountID = strutil.FirstNonEmpty(claims.AccountID, record.AccountID)
		record.WorkspaceID = strutil.FirstNonEmpty(claims.WorkspaceID, record.WorkspaceID)
	}
	if err := authopenai.SaveAuth(c.stateDir, provider, record); err != nil {
		return appwire.AuthDevicePollResponse{}, err
	}
	c.mu.Lock()
	delete(c.deviceFlows, flowID)
	c.mu.Unlock()

	status, err := c.openAIInstanceStatus(provider)
	if err != nil {
		return appwire.AuthDevicePollResponse{}, err
	}
	return appwire.AuthDevicePollResponse{State: "authorized", Status: status}, nil
}

func (c *hubAuthController) config() authopenai.Config {
	if strings.TrimSpace(c.cfg.IssuerBaseURL) == "" {
		return authopenai.DefaultConfig()
	}
	return c.cfg
}

func normalizeAuthProvider(provider string) string {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return "openai"
	}
	return provider
}

// resolveInstanceType reads providers.toml (via providersConfigPath) and
// returns the type and behavior tag for the named instance. Returns an error
// when the config path is unset, the file cannot be read/parsed, or the
// instance name is not found.
func (c *hubAuthController) resolveInstanceType(name string) (typ, behaviorTag string, err error) {
	if c.providersConfigPath == "" {
		return "", "", fmt.Errorf("no providers config path configured")
	}
	cfg, exists, err := providercfg.LoadFile(c.providersConfigPath)
	if err != nil {
		return "", "", fmt.Errorf("load providers config: %w", err)
	}
	if !exists {
		return "", "", fmt.Errorf("providers config not found at %s", c.providersConfigPath)
	}
	for _, inst := range cfg.Instances {
		if inst.Name == name {
			t := string(inst.Type)
			tag := providercfg.BehaviorTag(t, string(inst.APIStyle))
			return t, tag, nil
		}
	}
	return "", "", fmt.Errorf("instance %q not found in providers config", name)
}

// instanceIsOpenAI returns true if the named instance has type "openai"
// (i.e. supports OAuth). When no config is available or the instance is not
// found, falls back to checking whether name == "openai".
func (c *hubAuthController) instanceIsOpenAI(name string) bool {
	typ, _, err := c.resolveInstanceType(name)
	if err != nil {
		// No config or unknown instance: fall back to name-based check.
		return name == "openai"
	}
	return typ == "openai"
}

// requiresOpenAI returns an InvalidParams error when the named instance does
// not support OAuth (i.e. is not an openai-type instance).
func (c *hubAuthController) requiresOpenAI(name string) error {
	if c.instanceIsOpenAI(name) {
		return nil
	}
	return appwire.InvalidParams(fmt.Sprintf("OAuth is not supported for instance %q", name))
}

// instanceStatus computes the per-instance credential status for the given
// instance name and its resolved provider type. For openai-type instances it
// checks the per-instance OAuth file (auth/<name>.json) in addition to the
// credentials store. For all other types it checks only the store.
//
// This is the shared helper reused by Status and (in the next phase) the
// instance-list controller. It is keyed purely by name+type so callers that
// have already resolved the type can avoid re-reading the config.
func (c *hubAuthController) instanceStatus(name, typ string) appwire.AuthStatusResponse {
	if typ == "openai" {
		resp, _ := c.openAIInstanceStatus(name)
		return resp
	}

	// Non-openai: key-based credential check, resolving by instance name.
	v, src := c.creds.ResolveKey(name, typ)
	hasFile, envVar := c.creds.InstanceLayers(name, typ)
	modes := credentialAuthModes(typ)
	if modes == nil {
		modes = []string{"apiKey"}
	}
	return appwire.AuthStatusResponse{
		Provider:      name,
		Supported:     true,
		SignedIn:      v != "",
		ActiveSource:  string(src),
		AuthModes:     modes,
		HasStoredFile: hasFile,
		EnvVar:        envVar,
	}
}

// openAIInstanceStatus is like openAIStatus but keyed by instance name rather
// than the hard-coded "openai". It reads auth/<name>.json and credentials[name].
func (c *hubAuthController) openAIInstanceStatus(name string) (appwire.AuthStatusResponse, error) {
	record, err := authopenai.LoadAuth(c.stateDir, name)
	hasRecord := false
	switch {
	case err == nil:
		hasRecord = true
	case errors.Is(err, authopenai.ErrAuthNotFound):
		// no OAuth layer
	case errors.Is(err, authopenai.ErrAuthCorrupt):
		// treat a corrupt record as absent; file/env layers still resolve
	default:
		return appwire.AuthStatusResponse{}, err
	}

	// For the default "openai" instance we also check the OPENAI_API_KEY env
	// var. Named instances don't get the env fallback (they have no well-known
	// env var unless the type's env vars are used).
	hasFile, _ := c.creds.Layers(name)
	envKey := ""
	envSet := false
	if name == "openai" {
		envKey = "OPENAI_API_KEY"
		envSet = strings.TrimSpace(c.authEnv["OPENAI_API_KEY"]) != ""
	}

	var active authopenai.AuthStatus
	switch {
	case hasRecord:
		active = openAIStatusFromRecord(c.now(), record)
	case hasFile:
		active = authopenai.AuthStatus{SignedIn: true, Source: string(credentials.SourceFile)}
	case envSet:
		active = authopenai.AuthStatus{SignedIn: true, Source: authopenai.AuthSourceEnv}
	default:
		active = authopenai.AuthStatus{Source: authopenai.AuthSourceSignedOut}
	}

	status := appwire.AuthStatusResponse{
		Provider:      name,
		Supported:     true,
		SignedIn:      active.SignedIn,
		ActiveSource:  active.Source,
		AuthModes:     []string{"apiKey", "oauth"},
		Email:         active.Email,
		AccountID:     active.AccountID,
		WorkspaceID:   active.WorkspaceID,
		NeedsRefresh:  active.NeedsRefresh,
		NeedsLogin:    active.NeedsLogin,
		HasStoredFile: hasFile,
	}
	if envSet {
		status.EnvVar = envKey
	}
	if hasRecord {
		status.HasStoredOAuth = true
		status.StoredEmail = record.Email
		if status.ActiveSource == authopenai.AuthSourceOAuth {
			status.Email = strutil.FirstNonEmpty(status.Email, record.Email)
			status.AccountID = strutil.FirstNonEmpty(status.AccountID, record.AccountID)
			status.WorkspaceID = strutil.FirstNonEmpty(status.WorkspaceID, record.WorkspaceID)
		}
	}
	return status, nil
}
