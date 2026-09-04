package hub

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"primeradiant.com/evener/appwire"
	authopenai "primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm/providers/tokenauth"
	"primeradiant.com/evener/llm/registry"
)

type hubAuthController struct {
	stateDir          string
	creds             *credentials.Store
	cfg               authopenai.Config
	client            *http.Client
	now               func() time.Time
	generateState     func() (string, error)
	generatePKCE      func() (verifier, challenge string, err error)
	exchangeCode      func(context.Context, *http.Client, authopenai.Config, authopenai.TokenExchangeRequest) (authopenai.TokenSet, error)
	requestDeviceCode func(context.Context, *http.Client, authopenai.Config) (authopenai.DeviceCode, error)
	pollDeviceOnce    func(context.Context, *http.Client, authopenai.Config, authopenai.DeviceCode) (authopenai.DeviceCodeSuccess, bool, error)
	exchangeDevice    func(context.Context, *http.Client, authopenai.Config, string, string) (authopenai.TokenSet, error)
	loadAuth          func(string, string) (authopenai.AuthRecord, error)
	saveAuth          func(string, string, authopenai.AuthRecord) error
	deleteAuth        func(string, string) (bool, error)
	setCredential     func(string, string) error
	clearCredential   func(string) error
	// reg is the live registry every auth answer is derived from: which
	// instances exist, how each authenticates, and which credential source
	// resolved for it. Credentials and OAuth state are keyed by instance
	// name (spec §10).
	reg *hubcore.ProviderRegistry
	// providersConfigPath is the file the credential probe builds its client
	// from, so evener/auth/test resolves the instance exactly as the spawn
	// path will (spec §11.3). Every other answer comes from reg.
	providersConfigPath string
	// noUserLayer is EVENER_PROVIDERS_CONFIG's tri-state: present and empty
	// means the probe must build a client with no user layer, as a child would.
	noUserLayer bool

	credentialTestLoader credentialProbeLoader
	credentialTests      map[string]*credentialTestCall
	credentialTestMu     sync.Mutex
	// credentialTestJoined is a deterministic test seam for observing a
	// duplicate caller before the shared probe completes.
	credentialTestJoined func()

	mu          sync.Mutex
	flows       map[string]hubAuthFlow
	deviceFlows map[string]deviceFlow
}

// hubAuthControllerSetup, when non-nil, is invoked on every hub auth controller
// built by newHubAuthControllerWithStore (the constructor newHubAppServer uses),
// just before it is returned. It exists solely so a fuzz/test sandbox can
// redirect the controller's real-machine seams — the OAuth state directory, the
// credentials store, and the device/login HTTP calls — into a contained
// environment before any handler runs. Production leaves it nil, so the
// controller is byte-for-byte identical to before this hook existed.
var hubAuthControllerSetup func(*hubAuthController)

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
	c := &hubAuthController{
		stateDir:             stateDir,
		creds:                store,
		cfg:                  cfg,
		client:               client,
		now:                  time.Now,
		generateState:        authopenai.GenerateState,
		generatePKCE:         authopenai.GeneratePKCE,
		exchangeCode:         authopenai.ExchangeCode,
		requestDeviceCode:    authopenai.RequestDeviceCode,
		pollDeviceOnce:       authopenai.PollDeviceAuthOnce,
		exchangeDevice:       authopenai.ExchangeDeviceCode,
		loadAuth:             authopenai.LoadAuth,
		saveAuth:             authopenai.SaveAuth,
		deleteAuth:           authopenai.DeleteAuth,
		flows:                map[string]hubAuthFlow{},
		deviceFlows:          map[string]deviceFlow{},
		credentialTestLoader: loadCredentialTestClient,
		credentialTests:      map[string]*credentialTestCall{},
	}
	c.setCredential = c.creds.Set
	c.clearCredential = c.creds.Clear
	return c
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
	c := &hubAuthController{
		stateDir:             stateDir,
		creds:                store,
		cfg:                  cfg,
		client:               client,
		now:                  time.Now,
		generateState:        authopenai.GenerateState,
		generatePKCE:         authopenai.GeneratePKCE,
		exchangeCode:         authopenai.ExchangeCode,
		requestDeviceCode:    authopenai.RequestDeviceCode,
		pollDeviceOnce:       authopenai.PollDeviceAuthOnce,
		exchangeDevice:       authopenai.ExchangeDeviceCode,
		loadAuth:             authopenai.LoadAuth,
		saveAuth:             authopenai.SaveAuth,
		deleteAuth:           authopenai.DeleteAuth,
		flows:                map[string]hubAuthFlow{},
		deviceFlows:          map[string]deviceFlow{},
		credentialTestLoader: loadCredentialTestClient,
		credentialTests:      map[string]*credentialTestCall{},
	}
	c.setCredential = c.creds.Set
	c.clearCredential = c.creds.Clear
	if hubAuthControllerSetup != nil {
		hubAuthControllerSetup(c)
	}
	return c
}

// Status reports one instance's credential state. A name that is not an
// instance may still be a curated implicit provider with no credential in
// this environment; the pane lists those too, so they resolve without one
// (spec §5.2, §11.3). Anything else is unsupported.
func (c *hubAuthController) Status(params appwire.AuthStatusParams) (appwire.AuthStatusResponse, error) {
	name := normalizeAuthProvider(params.Provider)
	r := c.registry()
	if r == nil {
		return appwire.AuthStatusResponse{Provider: name, Supported: false, ActiveSource: "none"}, nil
	}
	if inst, ok := r.Instance(name); ok {
		return c.instanceStatus(inst), nil
	}
	if p, ok := r.Provider(name); ok && registry.BoolValue(p.Implicit) {
		res, err := r.ResolveInstance(name)
		if err != nil {
			//nolint:nilerr // a provider the registry cannot resolve is reported as unsupported, which is the answer, not an RPC failure
			return appwire.AuthStatusResponse{Provider: name, Supported: false, ActiveSource: "none"}, nil
		}
		return c.instanceStatus(registry.Instance{
			Name:             name,
			ProviderID:       name,
			Protocol:         res.Protocol,
			Auth:             res.Transport.Auth,
			Implicit:         true,
			CredentialSource: res.Credential.Source,
			ShadowedEnvVar:   res.ShadowedEnvVar,
			Warnings:         res.Warnings,
		}), nil
	}
	return appwire.AuthStatusResponse{Provider: name, Supported: false, ActiveSource: "none"}, nil
}

// registry is the registry the controller answers from, or nil when none was
// wired (tests that construct a bare controller).
func (c *hubAuthController) registry() *registry.Registry {
	if c.reg == nil {
		return nil
	}
	return c.reg.Get()
}

func (c *hubAuthController) LoginStart(params appwire.AuthLoginStartParams) (appwire.AuthLoginStartResponse, error) {
	provider := normalizeAuthProvider(params.Provider)
	if err := c.requiresCodex(provider); err != nil {
		return appwire.AuthLoginStartResponse{}, err
	}

	state, err := c.generateState()
	if err != nil {
		return appwire.AuthLoginStartResponse{}, fmt.Errorf("generate OAuth state: %w", err)
	}
	verifier, challenge, err := c.generatePKCE()
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
	if err := c.requiresCodex(provider); err != nil {
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
		record.Email = envvars.FirstNonEmpty(claims.Email, record.Email)
		record.AccountID = envvars.FirstNonEmpty(claims.AccountID, record.AccountID)
		record.WorkspaceID = envvars.FirstNonEmpty(claims.WorkspaceID, record.WorkspaceID)
	}
	if err := c.saveAuth(c.stateDir, provider, record); err != nil {
		return appwire.AuthLoginCompleteResponse{}, err
	}

	c.mu.Lock()
	delete(c.flows, flowID)
	c.mu.Unlock()
	if err := c.reloadRegistry(); err != nil {
		return appwire.AuthLoginCompleteResponse{}, err
	}

	status, err := c.openAIInstanceStatus(provider)
	if err != nil {
		return appwire.AuthLoginCompleteResponse{}, err
	}
	return appwire.AuthLoginCompleteResponse{Status: status}, nil
}

func (c *hubAuthController) Logout(params appwire.AuthLogoutParams) (appwire.AuthLogoutResponse, error) {
	name := normalizeAuthProvider(params.Provider)

	if !c.instanceIsCodex(name) {
		if err := c.clearCredential(name); err != nil {
			return appwire.AuthLogoutResponse{}, err
		}
		if err := c.reloadRegistry(); err != nil {
			return appwire.AuthLogoutResponse{}, err
		}
		status, _ := c.Status(appwire.AuthStatusParams{Provider: name})
		return appwire.AuthLogoutResponse{Removed: true, Status: status}, nil
	}

	// The Codex transport: clear the effective layer only. An OAuth record
	// (present or corrupt) shadows the stored file key, so remove it first;
	// otherwise clear the file key. The env layer cannot be cleared.
	_, loadErr := c.loadAuth(c.stateDir, name)
	hasRecord := loadErr == nil || errors.Is(loadErr, authopenai.ErrAuthCorrupt)
	removed := false
	if hasRecord {
		r, delErr := c.deleteAuth(c.stateDir, name)
		if delErr != nil {
			return appwire.AuthLogoutResponse{}, delErr
		}
		removed = r
	} else {
		_, hasFile := c.creds.Get(name)
		if hasFile {
			if clrErr := c.clearCredential(name); clrErr != nil {
				return appwire.AuthLogoutResponse{}, clrErr
			}
			removed = true
		}
	}
	if err := c.reloadRegistry(); err != nil {
		return appwire.AuthLogoutResponse{}, err
	}
	status, statusErr := c.openAIInstanceStatus(name)
	if statusErr != nil {
		return appwire.AuthLogoutResponse{}, statusErr
	}
	return appwire.AuthLogoutResponse{Removed: removed, Status: status}, nil
}

// reloadRegistry re-derives the instance set after a credential changed: a
// key that has just been stored can make an implicit instance exist, and
// clearing one can take it away (spec §5.1).
func (c *hubAuthController) reloadRegistry() error {
	if c.reg == nil {
		return nil
	}
	return c.reg.Reload()
}

// List is what the credentials pane renders: one row per curated implicit
// provider — whether or not it currently has a credential, since that is
// where a fresh install signs in or enters its first key — followed by every
// explicit instance not already listed (spec §11.3).
func (c *hubAuthController) List(_ appwire.EmptyParams) (appwire.AuthListResponse, error) {
	out := appwire.AuthListResponse{}
	r := c.registry()
	if r == nil {
		return out, nil
	}
	listed := map[string]bool{}
	for _, id := range r.ProviderIDs() {
		p, ok := r.Provider(id)
		if !ok || !registry.BoolValue(p.Implicit) {
			continue
		}
		status, err := c.Status(appwire.AuthStatusParams{Provider: id})
		if err != nil {
			return appwire.AuthListResponse{}, err
		}
		listed[id] = true
		out.Providers = append(out.Providers, status)
	}
	for _, inst := range r.Instances() {
		if listed[inst.Name] {
			continue
		}
		listed[inst.Name] = true
		out.Providers = append(out.Providers, c.instanceStatus(inst))
	}
	return out, nil
}

func (c *hubAuthController) ApiKeySet(params appwire.AuthApiKeySetParams) (appwire.AuthStatusResponse, error) {
	name := normalizeAuthProvider(params.Provider)
	if strings.TrimSpace(params.Value) == "" {
		return appwire.AuthStatusResponse{}, appwire.InvalidParams("value is required")
	}
	// A key stored under a Codex instance is one nothing reads: the transport
	// authenticates with its OAuth record (spec §5.1), so storing it and
	// reporting success would describe a credential the launch cannot use.
	if c.instanceIsCodex(name) {
		return appwire.AuthStatusResponse{}, appwire.InvalidParams(fmt.Sprintf("%s authenticates with an OAuth record, not an API key: run `evener openai login --instance %s`", name, name))
	}
	// A bare key under a gcp-adc instance is one the authenticator would
	// reject as JSON at first request; point at the flow that stores what
	// this scheme actually reads.
	if c.instanceUsesGCPADC(name) {
		return appwire.AuthStatusResponse{}, appwire.InvalidParams(name + " authenticates with Google application-default credentials or a stored credential JSON, not an API key: use evener/auth/credentialJson/set")
	}
	if err := c.setCredential(name, params.Value); err != nil {
		return appwire.AuthStatusResponse{}, err
	}
	if err := c.reloadRegistry(); err != nil {
		return appwire.AuthStatusResponse{}, err
	}
	return c.Status(appwire.AuthStatusParams{Provider: name})
}

// ApiKeyClear removes a stored file-layer key without touching any other
// credential layer - the counterpart to ApiKeySet, and the instance sheet's
// affordance for a stray stored key sitting shadowed behind an active
// oauth/adc source (issue #713). Unlike Logout, it never refuses or
// branches on the Codex transport: Logout's Codex path removes whichever
// layer is currently active, which for a signed-in row means the OAuth
// record, not the stray key, so it cannot express "clear the stray key but
// keep the login." ApiKeyClear always targets the store entry alone,
// leaving any OAuth record, ADC resolution, or environment credential
// exactly as it was.
func (c *hubAuthController) ApiKeyClear(params appwire.AuthApiKeyClearParams) (appwire.AuthStatusResponse, error) {
	name := normalizeAuthProvider(params.Provider)
	if err := c.clearCredential(name); err != nil {
		return appwire.AuthStatusResponse{}, err
	}
	if err := c.reloadRegistry(); err != nil {
		return appwire.AuthStatusResponse{}, err
	}
	return c.Status(appwire.AuthStatusParams{Provider: name})
}

func effectiveHubAuthEnv(launchEnv map[string]string) map[string]string {
	out := envToMap(os.Environ())
	maps.Copy(out, launchEnv)
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
		TokenType:    envvars.FirstNonEmpty(tokens.TokenType, "Bearer"),
		Scope:        tokens.Scope,
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		IDToken:      tokens.IDToken,
		Expiry:       tokens.Expiry,
	}
}

func (c *hubAuthController) DeviceStart(ctx context.Context, params appwire.AuthDeviceStartParams) (appwire.AuthDeviceStartResponse, error) {
	provider := normalizeAuthProvider(params.Provider)
	if err := c.requiresCodex(provider); err != nil {
		return appwire.AuthDeviceStartResponse{}, err
	}
	dc, err := c.requestDeviceCode(ctx, c.client, c.config())
	if err != nil {
		if errors.Is(err, authopenai.ErrDeviceCodeNotEnabled) {
			return appwire.AuthDeviceStartResponse{Provider: provider, Fallback: true}, nil
		}
		return appwire.AuthDeviceStartResponse{}, err
	}
	flowID, err := c.generateState()
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
	if err := c.requiresCodex(provider); err != nil {
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
		record.Email = envvars.FirstNonEmpty(claims.Email, record.Email)
		record.AccountID = envvars.FirstNonEmpty(claims.AccountID, record.AccountID)
		record.WorkspaceID = envvars.FirstNonEmpty(claims.WorkspaceID, record.WorkspaceID)
	}
	if err := c.saveAuth(c.stateDir, provider, record); err != nil {
		return appwire.AuthDevicePollResponse{}, err
	}
	c.mu.Lock()
	delete(c.deviceFlows, flowID)
	c.mu.Unlock()
	if err := c.reloadRegistry(); err != nil {
		return appwire.AuthDevicePollResponse{}, err
	}

	status, err := c.openAIInstanceStatus(provider)
	if err != nil {
		return appwire.AuthDevicePollResponse{}, err
	}
	return appwire.AuthDevicePollResponse{State: "authorized", Status: &status}, nil
}

func (c *hubAuthController) config() authopenai.Config {
	if strings.TrimSpace(c.cfg.IssuerBaseURL) == "" {
		return authopenai.DefaultConfig()
	}
	return c.cfg
}

// normalizeAuthProvider defaults an empty provider to the Codex instance,
// which is what the pane's OAuth button means (spec §9.5, §11.3).
func normalizeAuthProvider(provider string) string {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return "openai-codex"
	}
	return provider
}

// authModesFor is the sign-in vocabulary one transport auth scheme offers the
// credentials pane (spec §11.3).
func authModesFor(auth string) []string {
	switch auth {
	case registry.AuthOAuthOpenAICodex:
		return []string{"oauth"}
	case registry.AuthNone:
		return []string{"none"}
	case registry.AuthOptionalBearer:
		return []string{"none", "apiKey"}
	case registry.AuthGCPADC:
		return []string{"adc", "credentialJson"}
	default:
		return []string{"apiKey"}
	}
}

// instanceAuthScheme returns the transport auth scheme for name - an
// authored instance or a curated implicit provider - and whether one was
// found, the shared lookup behind instanceIsCodex and instanceUsesGCPADC.
func (c *hubAuthController) instanceAuthScheme(name string) (string, bool) {
	r := c.registry()
	if r == nil {
		return "", false
	}
	if inst, ok := r.Instance(name); ok {
		return inst.Auth, true
	}
	if p, ok := r.Provider(name); ok && registry.BoolValue(p.Implicit) {
		return p.Transport.Auth, true
	}
	return "", false
}

// instanceIsCodex reports whether name authenticates through the Codex
// OAuth flow (spec §9.5): its transport auth is oauth-openai-codex.
func (c *hubAuthController) instanceIsCodex(name string) bool {
	auth, ok := c.instanceAuthScheme(name)
	return ok && auth == registry.AuthOAuthOpenAICodex
}

// instanceUsesGCPADC reports whether name authenticates through Google
// application-default credentials (spec §9.4): its transport auth is gcp-adc.
func (c *hubAuthController) instanceUsesGCPADC(name string) bool {
	auth, ok := c.instanceAuthScheme(name)
	return ok && auth == registry.AuthGCPADC
}

// CredentialJsonSet stores a Google credential JSON for a gcp-adc instance
// (spec 2026-09-04 google-vertex-express §4.4): validated the way the
// authenticator will parse it, written to the credentials store under the
// instance name, then the registry reloads so the instance resolves with
// source store.
func (c *hubAuthController) CredentialJsonSet(params appwire.AuthCredentialJsonSetParams) (appwire.AuthStatusResponse, error) {
	name := normalizeAuthProvider(params.Provider)
	value := strings.TrimSpace(params.Value)
	if value == "" {
		return appwire.AuthStatusResponse{}, appwire.InvalidParams("value is required")
	}
	if !c.instanceUsesGCPADC(name) {
		return appwire.AuthStatusResponse{}, appwire.InvalidParams(name + " does not authenticate with Google application-default credentials; use evener/auth/apiKey/set")
	}
	if err := tokenauth.ValidateCredentialJSON([]byte(value)); err != nil {
		return appwire.AuthStatusResponse{}, appwire.InvalidParams(fmt.Sprintf("not a Google credential JSON: %v", err))
	}
	if err := c.setCredential(name, value); err != nil {
		return appwire.AuthStatusResponse{}, err
	}
	if err := c.reloadRegistry(); err != nil {
		return appwire.AuthStatusResponse{}, err
	}
	return c.Status(appwire.AuthStatusParams{Provider: name})
}

// requiresCodex returns an InvalidParams error when the named instance does
// not authenticate through the Codex OAuth flow, which is the only OAuth the
// hub can start.
func (c *hubAuthController) requiresCodex(name string) error {
	if c.instanceIsCodex(name) {
		return nil
	}
	return appwire.InvalidParams(fmt.Sprintf("OAuth is not supported for instance %q", name))
}

// instanceStatus is the credential status of one instance or curated
// implicit provider: the registry's credential source, the store's file
// layer, and for the Codex transport the OAuth record.
func (c *hubAuthController) instanceStatus(inst registry.Instance) appwire.AuthStatusResponse {
	if inst.Auth == registry.AuthOAuthOpenAICodex {
		resp, _ := c.openAIInstanceStatus(inst.Name)
		return resp
	}
	_, hasFile := c.creds.Get(inst.Name)
	// The registry names an environment credential "env:<VAR>", and that
	// variable is the one the pane shows.
	envVar := ""
	if v, ok := strings.CutPrefix(inst.CredentialSource, "env:"); ok {
		envVar = v
	}
	return appwire.AuthStatusResponse{
		Provider:  inst.Name,
		Supported: true,
		// A credential resolved from anywhere is a sign-in; "none" is the one
		// state that is not one, and for an auth-none instance it is also not
		// anything missing.
		SignedIn:       inst.CredentialSource != "none",
		ActiveSource:   inst.CredentialSource,
		AuthModes:      authModesFor(inst.Auth),
		HasStoredFile:  hasFile,
		EnvVar:         envVar,
		ShadowedEnvVar: inst.ShadowedEnvVar,
	}
}

// openAIInstanceStatus is the credential status of one instance on the Codex
// transport, keyed by instance name: it reads auth/<name>.json and
// credentials[name] (spec §9.5).
func (c *hubAuthController) openAIInstanceStatus(name string) (appwire.AuthStatusResponse, error) {
	record, err := c.loadAuth(c.stateDir, name)
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

	// The Codex transport authenticates with its OAuth record and nothing else:
	// the registry ignores the store and the environment for this scheme
	// (spec §5.1, §10), so the source is "oauth" when a record exists and
	// "none" when one does not. A stored key under this name is reported as a
	// diagnostic only — calling it a sign-in would claim a credential the
	// spawn gate refuses (kata z1gm).
	_, hasFile := c.creds.Get(name)

	source := "none"
	var active authopenai.AuthStatus
	if hasRecord {
		active = openAIStatusFromRecord(c.now(), record)
		source = authopenai.AuthSourceOAuth
	}

	// Every caller has already resolved this instance to the Codex transport,
	// so its mode is the "oauth" one this helper's OAuth-record path serves.
	modes := authModesFor(registry.AuthOAuthOpenAICodex)

	status := appwire.AuthStatusResponse{
		Provider:      name,
		Supported:     true,
		SignedIn:      active.SignedIn,
		ActiveSource:  source,
		AuthModes:     modes,
		Email:         active.Email,
		AccountID:     active.AccountID,
		WorkspaceID:   active.WorkspaceID,
		NeedsRefresh:  active.NeedsRefresh,
		NeedsLogin:    active.NeedsLogin,
		HasStoredFile: hasFile,
	}
	if hasRecord {
		status.HasStoredOAuth = true
		status.StoredEmail = record.Email
		status.Email = envvars.FirstNonEmpty(status.Email, record.Email)
		status.AccountID = envvars.FirstNonEmpty(status.AccountID, record.AccountID)
		status.WorkspaceID = envvars.FirstNonEmpty(status.WorkspaceID, record.WorkspaceID)
	}
	return status, nil
}
