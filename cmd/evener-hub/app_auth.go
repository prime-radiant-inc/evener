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

	// credMu serializes the instances controller's providers.toml mutations
	// against every credential write. A mutation asks which credentials
	// already sit under a name, or which endpoint it resolves to, and then
	// rewrites the file and reloads the registry; a stored key or OAuth
	// record written between those steps is one the check never saw and lands
	// against an instance the client never reviewed. A reload commits what it
	// read, so one running across a credential clear would publish a view the
	// clear had already invalidated. Credential writers take the write side
	// too - credentialWrite and credentialWriteExclusive hold it across the
	// store write and the registry reload that follows it, so a reload can
	// never pair a pre-write registry with a post-write credential - and
	// Create, Edit (including a rename), Remove, SetDefault and logout hold it
	// exclusively for their complete check-and-write operations. List is the
	// read side.
	credMu sync.RWMutex

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

// hubAuthFlowTTL bounds both sign-in flow maps: a login flow the user never
// completes and a device flow they never authorize. LoginStart sweeps expired
// entries as it records a new one, and a completion refuses a flow older than
// the window and drops it - without that, an abandoned flow and its PKCE
// verifier would sit in the map for the life of the process. Device codes
// expire on the provider's side in about this time.
const hubAuthFlowTTL = 15 * time.Minute

type hubAuthFlow struct {
	Provider string
	State    string
	// EndpointFingerprint is the endpoint the instance resolved to when the
	// flow was created: the destination the user began signing in for.
	EndpointFingerprint string
	CodeVerifier        string
	RedirectURI         string
	// StartedAt is when the flow was created, for the same expiry deviceFlows
	// carry (hubAuthFlowTTL).
	StartedAt time.Time
}

type deviceFlow struct {
	Provider string
	Code     authopenai.DeviceCode
	// EndpointFingerprint is the endpoint the instance resolved to when the
	// flow was created, as on hubAuthFlow.
	EndpointFingerprint string
	StartedAt           time.Time
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

// newHubAuthControllerWithStore creates a controller backed by an explicit credentials store,
// storing its OpenAI OAuth records under stateRoot — the state root whose auth/<instance>.json
// the registry resolves credentials from (the hub passes its registry's state root, see
// hubAuthStateRoot). An empty stateRoot resolves the directory from the process environment
// (XDG_STATE_HOME / HOME), matching the default constructor but without launch-env overrides.
func newHubAuthControllerWithStore(stateRoot string, store *credentials.Store) *hubAuthController {
	cfg := authopenai.DefaultConfig()
	client := &http.Client{Timeout: cfg.HTTPTimeout}
	stateDir := strings.TrimSpace(stateRoot)
	if stateDir == "" {
		stateDir = openAIStateDirFromEnv(effectiveHubAuthEnv(nil))
	}
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
//
// The shared side of credMu covers the answer, so a status cannot be generated
// inside an in-flight credential write's exclusive section: the write and the
// reload that re-derives the instance set from it are one step, and an answer
// read between them would pair the credential the write had landed with a
// registry snapshot that predates it - ActiveSource "none" beside
// HasStoredFile true. Everything the answer reads (the registry snapshot, the
// credential store's own mutex, the OAuth state files) is a different lock, so
// taking credMu here cannot recurse; the listing, which already holds the
// shared side, calls statusLocked instead.
func (c *hubAuthController) Status(params appwire.AuthStatusParams) (appwire.AuthStatusResponse, error) {
	c.credMu.RLock()
	defer c.credMu.RUnlock()
	return c.statusLocked(params)
}

// statusLocked is Status's body for a caller that already holds credMu's shared
// side - the listing, whose one snapshot answers many statuses at once. It must
// not take credMu itself.
func (c *hubAuthController) statusLocked(params appwire.AuthStatusParams) (appwire.AuthStatusResponse, error) {
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

	// Captured with the flow: the authorize round trip below is long enough
	// for an edit to re-point the instance, and this is the only value that
	// can say which endpoint the user began signing in for. An empty capture is
	// only ever "no destination to check", a hub with no state root to key
	// with, or a destination whose fingerprint the hub could not key right now:
	// the last is one the completion would have no comparison for, so it is
	// refused here, before a flow is recorded (verifyFlowEndpoint).
	endpoint := c.endpointFingerprintFor(provider)
	if endpoint == "" && c.endpointHasDestination(provider) && c.hasEndpointStateRoot() {
		return appwire.AuthLoginStartResponse{}, appwire.Conflict(provider + " cannot be checked against its endpoint: the hub cannot key its endpoint fingerprints right now, so this destination could not be checked when the sign-in completes; review its destination and start the sign-in again")
	}
	c.mu.Lock()
	if c.flows == nil {
		c.flows = map[string]hubAuthFlow{}
	}
	// Abandoned flows have no other reaper: a start that was never completed
	// would otherwise keep its PKCE verifier for the life of the process. The
	// sweep runs as a new flow is recorded, under the same lock, so the map
	// holds only live flows plus the one being added.
	now := c.now()
	for id, flow := range c.flows {
		if now.Sub(flow.StartedAt) >= hubAuthFlowTTL {
			delete(c.flows, id)
		}
	}
	c.flows[state] = hubAuthFlow{
		Provider:            provider,
		State:               state,
		EndpointFingerprint: endpoint,
		CodeVerifier:        verifier,
		RedirectURI:         redirectURI,
		StartedAt:           now,
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
	// The same window deviceFlows expire on: a sign-in started long ago is one
	// the browser abandoned, and its flow is dropped here rather than left to
	// the next start's sweep.
	if c.now().Sub(flow.StartedAt) >= hubAuthFlowTTL {
		c.mu.Lock()
		delete(c.flows, flowID)
		c.mu.Unlock()
		return appwire.AuthLoginCompleteResponse{}, appwire.Conflict("the sign-in flow expired; start the sign-in again")
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
	// Asked again inside the lock: the check at the top of this call ran
	// before the token exchange, which is a browser round trip long, and an
	// instance mutation holds credMu exclusively while it rewrites
	// providers.toml and reloads. Only the answer under the lock describes
	// the instance, and the endpoint, this record lands under.
	if err := c.credentialWrite(func() error {
		if err := c.requiresCodex(provider); err != nil {
			return err
		}
		if err := c.verifyFlowEndpoint(provider, flow.EndpointFingerprint); err != nil {
			return err
		}
		return c.saveAuth(c.stateDir, provider, record)
	}); err != nil {
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

	// The scheme names which layer a logout clears, so it is read inside the
	// same locked write as the removal: a rename holds credMu exclusively
	// while it re-keys providers.toml and reloads, so a scheme read outside
	// the lock can aim the clear at a store this name no longer
	// authenticates from. The endpoint assertion is checked there too, for the
	// same reason: the sign-out was confirmed for the row the client listed,
	// and a name another client has re-pointed since belongs to a different
	// instance.
	codex := false
	removed := false
	if err := c.credentialWriteExclusive(func() error {
		if err := c.verifyEndpointFingerprint(name, params.ExpectedEndpointFingerprint); err != nil {
			return err
		}
		codex = c.instanceIsCodex(name)
		if !codex {
			_, hadFile := c.creds.Get(name)
			if clrErr := c.clearCredential(name); clrErr != nil {
				return clrErr
			}
			removed = hadFile
			return nil
		}
		// The Codex transport: clear the effective layer only. An OAuth record
		// (present or corrupt) shadows the stored file key, so remove it first;
		// otherwise clear the file key. The env layer cannot be cleared. Which
		// layer is active decides what is removed, so the read and the removal
		// hold the credential lock together.
		_, loadErr := c.loadAuth(c.stateDir, name)
		hasRecord := loadErr == nil || errors.Is(loadErr, authopenai.ErrAuthCorrupt)
		if hasRecord {
			r, delErr := c.deleteAuth(c.stateDir, name)
			if delErr != nil {
				return delErr
			}
			removed = r
			return nil
		}
		if _, hasFile := c.creds.Get(name); hasFile {
			if clrErr := c.clearCredential(name); clrErr != nil {
				return clrErr
			}
			removed = true
		}
		return nil
	}); err != nil {
		return appwire.AuthLogoutResponse{}, err
	}
	if !codex {
		status, _ := c.Status(appwire.AuthStatusParams{Provider: name})
		return appwire.AuthLogoutResponse{Removed: removed, Status: status}, nil
	}
	status, statusErr := c.openAIInstanceStatus(name)
	if statusErr != nil {
		return appwire.AuthLogoutResponse{}, statusErr
	}
	return appwire.AuthLogoutResponse{Removed: removed, Status: status}, nil
}

// credentialWriteBetween runs inside a credential write's critical section,
// after the credential has landed and before the reload that re-derives the
// instance set from it. A test seam, so the state in between can be held
// still and a concurrent listing asked what it sees there.
var credentialWriteBetween = func() {}

// credentialWrite runs one stored-key or OAuth-record write and the registry
// reload that re-derives the instance set from it as one exclusive critical
// section of credMu. The two must not be split: a listing that ran between
// them would pair the credential the write had landed with a registry snapshot
// that predates it - ActiveSource "none" beside HasStoredFile true - and that
// incoherence is what credMu exists to prevent. Taking the exclusive side also
// keeps the section whole against the listings' shared side; writers remain
// safe against each other through the credentials store's own mutex, they just
// no longer run in parallel.
//
// A reload failure is deliberately not returned, as it never was: the
// credential is stored either way, so failing the response would report a
// landed write as lost and have the caller retype one the hub already has. The
// failure is not lost either: reloadRegistryLocked leaves it on the registry,
// which is where the pane reads it (Diagnostics) and where instance writes are
// refused until the file loads (WritesRefused, spec §10).
func (c *hubAuthController) credentialWrite(write func() error) error {
	c.credMu.Lock()
	defer c.credMu.Unlock()
	if err := write(); err != nil {
		return err
	}
	credentialWriteBetween()
	_ = c.reloadRegistryLocked()
	return nil
}

// credentialWriteExclusive keeps a check-and-remove operation together against
// other controller credential writers, and closes with the same reload
// credentialWrite performs: its callers rely on the instance set being
// re-derived from the removal before the section ends, exactly as the shared
// writers do, and its reload failure is not returned either, for the reason
// credentialWrite states.
func (c *hubAuthController) credentialWriteExclusive(write func() error) error {
	c.credMu.Lock()
	defer c.credMu.Unlock()
	if err := write(); err != nil {
		return err
	}
	credentialWriteBetween()
	_ = c.reloadRegistryLocked() // not returned; see credentialWrite
	return nil
}

// reloadRegistryLocked re-derives the instance set after a credential changed:
// a key that has just been stored can make an implicit instance exist, and
// clearing one can take it away (spec §5.1). The caller holds credMu.
//
// It must not take credMu itself. credentialWrite and credentialWriteExclusive
// call it while holding their exclusive section, which is what makes a write
// and the reload that derives the instance set from it one step. A reload is
// not just a read - it commits what it read - so one that ran across another
// writer's section could publish a view that section had already invalidated.
func (c *hubAuthController) reloadRegistryLocked() error {
	if c.reg == nil {
		return nil
	}
	return c.reg.Reload()
}

// reloadRegistry is the locked form of reloadRegistryLocked, for callers that
// changed something outside a credential write. Nobody may call it while
// holding credMu.
func (c *hubAuthController) reloadRegistry() error {
	c.credMu.Lock()
	defer c.credMu.Unlock()
	return c.reloadRegistryLocked()
}

// List is what the credentials pane renders: one row per curated implicit
// provider — whether or not it currently has a credential, since that is
// where a fresh install signs in or enters its first key — followed by every
// explicit instance not already listed (spec §11.3).
func (c *hubAuthController) List(_ appwire.EmptyParams) (appwire.AuthListResponse, error) {
	// The shared side of credMu covers the whole listing: a credential write
	// holds it exclusively from the moment the write lands until the reload
	// that re-derives the instance set from it has committed, so a listing that
	// ran inside that section would pair the credential the write had landed
	// with a registry snapshot that predates it - ActiveSource "none" beside
	// HasStoredFile true. Everything below (statusLocked, instanceStatus, the
	// registry snapshot) reads the credential store's own mutex and files,
	// never credMu, so taking it here cannot recurse.
	c.credMu.RLock()
	defer c.credMu.RUnlock()
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
		status, err := c.statusLocked(appwire.AuthStatusParams{Provider: id})
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
	// Both refusals below ask what name authenticates with, and a rename can
	// change the answer: it holds credMu exclusively while it re-keys
	// providers.toml and reloads, so only a check inside that lock describes
	// the instance this write actually lands on.
	if err := c.credentialWrite(func() error {
		// A key stored under a Codex instance is one nothing reads: the transport
		// authenticates with its OAuth record (spec §5.1), so storing it and
		// reporting success would describe a credential the launch cannot use.
		if c.instanceIsCodex(name) {
			return appwire.InvalidParams(fmt.Sprintf("%s authenticates with an OAuth record, not an API key: run `evener openai login --instance %s`", name, name))
		}
		// A bare key under a gcp-adc instance is one the authenticator would
		// reject as JSON at first request; point at the flow that stores what
		// this scheme actually reads.
		if c.instanceUsesGCPADC(name) {
			return appwire.InvalidParams(name + " authenticates with Google application-default credentials or a stored credential JSON, not an API key: use evener/auth/credentialJson/set")
		}
		// An instance that authenticates nothing reads no key either: the
		// transport sends no credential at all (llm/authenticators.go), so a
		// stored key would be one nothing reads, reported as a successful
		// credential save, and left under a name a later edit can point at a
		// scheme that does read it.
		if auth, ok := c.instanceAuthScheme(name); ok && auth == registry.AuthNone {
			return appwire.InvalidParams(name + " authenticates without a credential: it reads no API key, so a stored one would be a credential nothing sends")
		}
		// A name that is neither keeps the key where nothing reads it: the pane
		// only offers this write for a row its listing had, so a name that no
		// longer resolves is one an instance was removed from since - and the
		// key would wait under it for whatever instance is authored under that
		// name next (see Remove). Asking here, under the credential lock, is
		// what makes the answer describe the state the write lands in: a removal
		// holds that lock exclusively across its cleanup and its reload, so one
		// cannot be in flight while the name is checked.
		if !c.nameIsConnectable(name) {
			return appwire.InvalidParams(fmt.Sprintf("%q is not a configured provider or instance: nothing reads a key stored under it", name))
		}
		if err := c.verifyEndpointFingerprint(name, params.ExpectedEndpointFingerprint); err != nil {
			return err
		}
		return c.setCredential(name, params.Value)
	}); err != nil {
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
	// The endpoint check and the clear are one step, the way the set is: the
	// clear was confirmed for the row the client listed, and a name another
	// client has re-pointed since must not have its replacement instance's key
	// removed.
	if err := c.credentialWrite(func() error {
		if err := c.verifyEndpointFingerprint(name, params.ExpectedEndpointFingerprint); err != nil {
			return err
		}
		return c.clearCredential(name)
	}); err != nil {
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
	// Captured before the device-code request, which is itself a network round
	// trip: this is the endpoint the flow starts on. One an edit arrives at
	// during that request has to fail the completion's comparison rather than
	// be adopted as the destination the user asked for. As on LoginStart, an
	// empty capture with a destination to name and a state root that cannot key
	// it is refused before a device code is requested at all: the poll would
	// have nothing to compare (verifyFlowEndpoint).
	endpoint := c.endpointFingerprintFor(provider)
	if endpoint == "" && c.endpointHasDestination(provider) && c.hasEndpointStateRoot() {
		return appwire.AuthDeviceStartResponse{}, appwire.Conflict(provider + " cannot be checked against its endpoint: the hub cannot key its endpoint fingerprints right now, so this destination could not be checked when the sign-in completes; review its destination and start the sign-in again")
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
	c.deviceFlows[flowID] = deviceFlow{Provider: provider, Code: dc, EndpointFingerprint: endpoint, StartedAt: c.now()}
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
	if flow.Provider != provider {
		return appwire.AuthDevicePollResponse{}, appwire.InvalidParams("auth device provider does not match flow")
	}
	if c.now().Sub(flow.StartedAt) >= hubAuthFlowTTL {
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
	// Re-checked under the lock for the same reason LoginComplete re-checks
	// it: the poll's own exchange is the long step an instance mutation can
	// land in.
	if err := c.credentialWrite(func() error {
		if err := c.requiresCodex(provider); err != nil {
			return err
		}
		if err := c.verifyFlowEndpoint(provider, flow.EndpointFingerprint); err != nil {
			return err
		}
		return c.saveAuth(c.stateDir, provider, record)
	}); err != nil {
		return appwire.AuthDevicePollResponse{}, err
	}
	c.mu.Lock()
	delete(c.deviceFlows, flowID)
	c.mu.Unlock()

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

// nameIsConnectable reports whether name is something this hub authenticates
// with: an instance the registry holds or a curated implicit provider it
// declares, the pair instanceAuthScheme answers for (spec §5.2, §11.3). A
// credential write asks before it stores a secret, so a name that has stopped
// meaning anything - an instance removed since the pane listed it - cannot take
// a key nothing reads and hand it to whatever instance is authored under that
// name next. Without a registry there is no view to answer from (Status and
// List call the same configuration unsupported), so the write is not refused
// on its account.
func (c *hubAuthController) nameIsConnectable(name string) bool {
	if c.registry() == nil {
		return true
	}
	_, ok := c.instanceAuthScheme(name)
	return ok
}

// endpointInstanceFor resolves name the way every endpoint question reads it:
// the instance the registry holds under the name, or, for a curated provider
// with no instance yet, the listing view resolvedInstanceFor builds - the same
// lookup List serves a setup entry from. The registry snapshot travels back
// with the instance so the caller's fingerprint and destination questions
// describe one generation of providers.toml.
func (c *hubAuthController) endpointInstanceFor(name string) (*registry.Registry, registry.Instance, bool) {
	r := c.registry()
	if r == nil {
		return nil, registry.Instance{}, false
	}
	if inst, ok := r.Instance(name); ok {
		return r, inst, true
	}
	// A curated provider with no instance yet - no credential - is still listed,
	// with a setup entry whose fingerprint is built by resolving the provider.
	// A client asserts that value, so this answers from the same lookup List
	// uses; anything else would refuse the first key for every provider that
	// needs one.
	p, ok := r.Provider(name)
	if !ok {
		return nil, registry.Instance{}, false
	}
	inst, ok := resolvedInstanceFor(r, name, p.Hidden)
	if !ok {
		return nil, registry.Instance{}, false
	}
	return r, inst, true
}

// endpointFingerprintFor is the destination fingerprint a client was shown for
// name, asked of the same registry the write lands against and over the same
// identity a listing row serves (destinationFingerprint). Empty when the name
// resolves to no destination here, or when the hub has no key to digest with -
// which is also what a client that asserts nothing sends. The two are different
// states this one value cannot tell apart; endpointHasDestination answers which
// one it is for the flow guards that have to (verifyFlowEndpoint, and the
// start-time refusals in LoginStart and DeviceStart).
func (c *hubAuthController) endpointFingerprintFor(name string) string {
	r, inst, ok := c.endpointInstanceFor(name)
	if !ok {
		return ""
	}
	return destinationFingerprint(c.stateDir, r, inst)
}

// endpointHasDestination reports whether name has a destination this hub must
// be able to check at all, whether or not it can key a fingerprint for it
// right now: it resolves to an instance that is not hidden and carries a base
// URL (destinationInstance). A configured state root that cannot yield its key
// leaves endpointFingerprintFor empty for exactly the names this answers true
// for, and a sign-in flow started there would have nothing to compare when it
// completes - so the flow guards refuse it rather than file the record
// wherever the instance points by then.
func (c *hubAuthController) endpointHasDestination(name string) bool {
	r, inst, ok := c.endpointInstanceFor(name)
	if !ok {
		return false
	}
	_, ok = destinationInstance(r, inst)
	return ok
}

// hasEndpointStateRoot reports whether this hub keeps endpoint fingerprints at
// all: a state root to key them under. A controller with none has nothing to
// key with, so there is nothing to refuse (verifyEndpointFingerprint's rule),
// and an empty capture is the only thing a flow on it can carry; a state root
// that exists but cannot yield its key is the state the flow guards refuse.
func (c *hubAuthController) hasEndpointStateRoot() bool { return strings.TrimSpace(c.stateDir) != "" }

// verifyEndpointFingerprint refuses a credential write whose client asserted an
// endpoint this name no longer resolves to. Callers run it inside the
// credential lock, which is what makes the check and the write one step: a
// client's own comparison reads a listing that a concurrent change can outdate,
// so between its check and this RPC the name can move to an endpoint the user
// never reviewed - and then the secret would land there.
//
// An empty assertion is a client that was shown no endpoint. That is nothing to
// check only while the hub itself has nothing to key with - no state root at
// all, a bare controller - or can key a fingerprint: a state root that exists
// but cannot be read or written is exactly the state that serves empty
// fingerprints to every client, and accepting the assertions that follow would
// let a concurrent endpoint change receive the credential with no verification
// at all. There the write fails closed, with a refusal the user can act on.
func (c *hubAuthController) verifyEndpointFingerprint(name, asserted string) error {
	if asserted == "" {
		if _, err := endpointFingerprintKeyState(c.stateDir); err != nil {
			return appwire.Conflict(name + " cannot be checked against the endpoint this form was opened on: the hub cannot key its endpoint fingerprints right now; this destination cannot be verified, so review its destination and enter the credential again")
		}
		return nil
	}
	current := c.endpointFingerprintFor(name)
	// A fingerprint the hub cannot match is a refusal, not a bypass: the client
	// was shown an endpoint, and a hub that can no longer describe where the
	// name points cannot say the credential would land there. Landing it anyway
	// would let a deleted key file or an unreachable state root switch the guard
	// off without a word. A client that asserted nothing is handled above: that
	// is nothing to check only while the hub can key a fingerprint or has no
	// state root at all.
	if current == "" {
		return appwire.Conflict(name + " cannot be checked against the endpoint this form was opened on: the hub cannot resolve it now, so review its destination and enter the credential again")
	}
	if current != asserted {
		return appwire.Conflict(name + " no longer resolves to the endpoint this form was opened on: review its destination and enter the credential again")
	}
	return nil
}

// verifyFlowEndpoint refuses a sign-in flow whose instance no longer resolves
// to the endpoint the flow was started for. The flow's own exchange is a
// browser round trip long, and an edit that re-points base_url keeps the auth
// scheme the completion re-checks, so the scheme alone cannot say that the
// record would land where the user signed in.
//
// An empty capture means one of two states, and only one of them is refused. A
// hub with no state root at all has nothing to key with - the rule
// verifyEndpointFingerprint states for the same bare controller - so an empty
// capture is the only thing a flow on it can carry, and it is accepted. A hub
// with a state root that could not yield its key when the flow started has an
// empty capture for a destination that is real, and the completion has nothing
// to compare: accepting it would file the record wherever the instance points
// now, so it is refused (LoginStart and DeviceStart refuse to start that flow
// in the first place). A hidden or unresolvable name, which has no destination
// to name, is accepted in either state. An empty current value (the hub has no
// key now) is refused as before: the hub cannot say the record would land
// where the flow was started.
func (c *hubAuthController) verifyFlowEndpoint(name, started string) error {
	if started == "" {
		if c.endpointHasDestination(name) && c.hasEndpointStateRoot() {
			return appwire.Conflict(name + " cannot be checked against the endpoint this sign-in was started on: the hub cannot key its endpoint fingerprints right now, so review its destination and start the sign-in again")
		}
		return nil
	}
	current := c.endpointFingerprintFor(name)
	// As verifyEndpointFingerprint reads it: a flow bound to an endpoint the hub
	// can no longer describe is one whose record nobody can place, so it is
	// refused rather than filed somewhere the user never signed in for.
	if current == "" {
		return appwire.Conflict(name + " cannot be checked against the endpoint this sign-in was started on: the hub cannot resolve it now, so review its destination and start the sign-in again")
	}
	if current != started {
		return appwire.Conflict(name + " no longer resolves to the endpoint this sign-in was started on: review its destination and start the sign-in again")
	}
	return nil
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
	if err := c.requiresGCPADC(name); err != nil {
		return appwire.AuthStatusResponse{}, err
	}
	if err := tokenauth.ValidateCredentialJSON([]byte(value)); err != nil {
		return appwire.AuthStatusResponse{}, appwire.InvalidParams(fmt.Sprintf("not a Google credential JSON: %v", err))
	}
	if err := c.credentialWrite(func() error {
		// Asked again inside the lock, because it is the answer at the moment
		// of the write that matters: a rename holds credMu exclusively while
		// it re-keys providers.toml and reloads, so the check above can
		// describe an instance this name no longer reaches. The one above
		// stays for the caller who pasted for the wrong instance, so the
		// refusal still beats a complaint about the JSON.
		if err := c.requiresGCPADC(name); err != nil {
			return err
		}
		if err := c.verifyEndpointFingerprint(name, params.ExpectedEndpointFingerprint); err != nil {
			return err
		}
		return c.setCredential(name, value)
	}); err != nil {
		return appwire.AuthStatusResponse{}, err
	}
	return c.Status(appwire.AuthStatusParams{Provider: name})
}

// requiresGCPADC returns an InvalidParams error when the named instance does
// not authenticate through Google application-default credentials, the only
// scheme that reads a stored credential JSON.
func (c *hubAuthController) requiresGCPADC(name string) error {
	if c.instanceUsesGCPADC(name) {
		return nil
	}
	return appwire.InvalidParams(name + " does not authenticate with Google application-default credentials; key-based instances use evener/auth/apiKey/set and Codex instances use evener/auth/login/start")
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
