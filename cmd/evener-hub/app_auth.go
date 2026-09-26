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
	"primeradiant.com/evener/execsupport/valueexpr"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm/providers/tokenauth"
	"primeradiant.com/evener/llm/registry"
)

type hubAuthController struct {
	stateDir string
	creds    *credentials.Store
	// credsErr is the error of resolving that store at construction
	// (hubCredentialStore) when there was none to resolve: creds is nil then,
	// every read answers "no stored key" (storedKey) and every write refuses
	// with this reason (credentialsUnavailable) rather than dereferencing nil.
	credsErr          error
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
	store, storeErr := hubCredentialStore(stateDir, nil)
	c := &hubAuthController{
		stateDir:             stateDir,
		creds:                store,
		credsErr:             storeErr,
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
	c.wireCredentialStore()
	return c
}

// hubAuthCredentialsPath is where a hub resolves the credentials store it has
// no explicit one for: credentials.toml beside the state root its OAuth records
// live under, which is where newHubAuthController has always found the default
// store. An empty stateRoot resolves the directory from the process environment
// (XDG_STATE_HOME / HOME), matching that constructor without launch-env
// overrides.
func hubAuthCredentialsPath(stateRoot string) string {
	stateDir := strings.TrimSpace(stateRoot)
	if stateDir == "" {
		stateDir = openAIStateDirFromEnv(effectiveHubAuthEnv(nil))
	}
	return filepath.Join(filepath.Dir(stateDir), "credentials.toml")
}

// hubCredentialStore resolves the one credentials store every credential
// surface of a hub reads and writes: store when the caller supplied one, and
// the on-disk default under stateRoot otherwise — the same fallback
// newHubAuthControllerWithStore makes. Callers resolve it once and pass the
// result to each surface (app_rpc.go), because two surfaces resolving it
// separately can disagree: a hub whose auth controller fell back on its own
// supported evener/auth/apiKey/set while the credential push refused it with
// "requires a local credentials store".
//
// It never returns a nil store with no reason: LoadStore's error travels back
// with it, because there is no path-less store that is better than none (its
// writes would silently no-op and lose credentials) and a caller that dropped
// the error would dereference nil on the next credential read or write. Callers
// keep the pair - the controller carries it as credsErr, and app_rpc.go hands
// it to the push through credentialStore - so an unreadable credentials.toml is
// one fact every credential surface answers from.
func hubCredentialStore(stateRoot string, store *credentials.Store) (*credentials.Store, error) {
	if store != nil {
		return store, nil
	}
	loaded, err := credentials.LoadStore(hubAuthCredentialsPath(stateRoot))
	if err != nil {
		return nil, err
	}
	return loaded, nil
}

// wireCredentialStore installs the store's write paths on c, or the refusal
// that stands in for them when there is no usable store. This is the one place
// a nil store could be dereferenced through setCredential or clearCredential,
// so it is also the one place that decides what happens instead.
func (c *hubAuthController) wireCredentialStore() {
	if c.creds == nil {
		c.setCredential = func(string, string) error { return c.credentialsUnavailable() }
		c.clearCredential = func(string) error { return c.credentialsUnavailable() }
		return
	}
	c.setCredential = c.creds.Set
	c.clearCredential = c.creds.Clear
}

// storedKey reads the instance's file-layer key, answering "no key" when this
// hub has no usable store: a read must not panic where a write refuses.
func (c *hubAuthController) storedKey(name string) (string, bool) {
	if c.creds == nil {
		return "", false
	}
	return c.creds.Get(name)
}

// credentialsUnavailable is the refusal every credential write answers with
// when this hub has no usable store. That is the hub's own state rather than
// the caller's, so it is InternalError, and it names the file when the file is
// why: an operator cannot fix a store by changing the request.
func (c *hubAuthController) credentialsUnavailable() error {
	if c.credsErr != nil {
		return appwire.InternalError("the credentials store cannot be read, so no credential can be saved: " + c.credsErr.Error())
	}
	return appwire.InternalError("this hub has no credentials store to save a credential in")
}

// credentialStore is the store this controller resolved at construction
// (hubCredentialStore) and the error of that resolution, so a second credential
// surface - app_rpc.go's remote credential push - reads the one answer instead
// of resolving again and possibly disagreeing.
func (c *hubAuthController) credentialStore() (*credentials.Store, error) {
	return c.creds, c.credsErr
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
	// would silently no-op and lose credentials. hubCredentialStore is that one
	// resolution, and app_rpc.go reads its result off this controller
	// (credentialStore) so every credential surface gets the same store.
	// Its error is kept rather than dropped: a store that cannot be loaded
	// leaves creds nil, and the guards below answer from that instead of
	// dereferencing nil.
	store, storeErr := hubCredentialStore(stateRoot, store)
	c := &hubAuthController{
		stateDir:             stateDir,
		creds:                store,
		credsErr:             storeErr,
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
	c.wireCredentialStore()
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
	// The revision served with the status is keyed with the hub's endpoint
	// fingerprint key, resolved before credMu is taken: resolving can repair the
	// key file (an inter-process lock and a write), and a repair under the lock
	// would stall every credential op behind it. A hub with no key serves no
	// revision, and the write that would fence on it refuses (verifyConfigRevision).
	key, _ := resolveEndpointFingerprintKey(c.stateDir)
	c.credMu.RLock()
	defer c.credMu.RUnlock()
	return c.statusLocked(params, key)
}

// statusLocked is Status's body for a caller that already holds credMu's shared
// side - the listing, whose one snapshot answers many statuses at once. It must
// not take credMu itself. key is the fingerprint key the caller resolved before
// taking the lock; it keys the revision this answer serves and must not be
// resolved here, because a resolution can repair the key file under the lock.
func (c *hubAuthController) statusLocked(params appwire.AuthStatusParams, key []byte) (appwire.AuthStatusResponse, error) {
	name := normalizeAuthProvider(params.Provider)
	r := c.registry()
	if r == nil {
		return appwire.AuthStatusResponse{Provider: name, Supported: false, ActiveSource: "none"}, nil
	}
	if inst, ok := r.Instance(name); ok {
		return c.instanceStatusKeyed(key, inst), nil
	}
	if p, ok := r.Provider(name); ok && registry.BoolValue(p.Implicit) {
		res, err := r.ResolveInstancePresence(name)
		if err != nil {
			//nolint:nilerr // a provider the registry cannot resolve is reported as unsupported, which is the answer, not an RPC failure
			return appwire.AuthStatusResponse{Provider: name, Supported: false, ActiveSource: "none"}, nil
		}
		return c.instanceStatusKeyed(key, registry.Instance{
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
		return appwire.AuthLoginStartResponse{}, appwire.EndpointConflict(provider + " cannot be checked against its endpoint: the hub cannot key its endpoint fingerprints right now, so this destination could not be checked when the sign-in completes; review its destination and start the sign-in again")
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
	// The fingerprint key is resolved once, before the credential lock is taken:
	// resolving it can repair the key file (an inter-process lock and a write),
	// and a repair under that lock would hold every listing and credential op
	// behind it. Only the key is threaded into the check below: the flow guard's
	// empty capture is refused on the destination and the state root it reads,
	// not on the resolution error.
	key, _ := resolveEndpointFingerprintKey(c.stateDir)
	// Asked again inside the lock: the check at the top of this call ran
	// before the token exchange, which is a browser round trip long, and an
	// instance mutation holds credMu exclusively while it rewrites
	// providers.toml and reloads. Only the answer under the lock describes
	// the instance, and the endpoint, this record lands under.
	applied, err := c.credentialWrite(func() error {
		if err := c.requiresCodex(provider); err != nil {
			return err
		}
		if err := c.verifyFlowEndpointWithKey(provider, flow.EndpointFingerprint, key); err != nil {
			return err
		}
		return c.saveAuth(c.stateDir, provider, record)
	})
	if err != nil {
		return appwire.AuthLoginCompleteResponse{}, err
	}

	c.mu.Lock()
	delete(c.flows, flowID)
	c.mu.Unlock()

	status, err := c.statusAfterWrite(provider, applied, c.openAIInstanceStatusByName)
	return appwire.AuthLoginCompleteResponse{Status: status}, err
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
	//
	// The fingerprint key is resolved once, before the credential lock is taken:
	// resolving it can repair the key file (an inter-process lock and a write),
	// and a repair under that lock would hold every listing and credential op
	// behind it. One key for the whole write, so the assertion below is checked
	// against the key the caller's row was served with.
	key, keyErr := resolveEndpointFingerprintKey(c.stateDir)
	codex := false
	removed := false
	applied, err := c.credentialWriteExclusive(func() error {
		if err := c.verifyEndpointFingerprintWithKey(name, params.ExpectedEndpointFingerprint, key, keyErr); err != nil {
			return err
		}
		codex = c.instanceIsCodex(name)
		if !codex {
			_, hadFile := c.storedKey(name)
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
		if _, hasFile := c.storedKey(name); hasFile {
			if clrErr := c.clearCredential(name); clrErr != nil {
				return clrErr
			}
			removed = true
		}
		return nil
	})
	if err != nil {
		return appwire.AuthLogoutResponse{}, err
	}
	if !codex {
		status, _ := c.Status(appwire.AuthStatusParams{Provider: name})
		return appwire.AuthLogoutResponse{Removed: removed, Status: status}, nil
	}
	status, err := c.statusAfterWrite(name, applied, c.openAIInstanceStatusByName)
	return appwire.AuthLogoutResponse{Removed: removed, Status: status}, err
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
func (c *hubAuthController) credentialWrite(write func() error) (applied bool, err error) {
	return c.credentialWriteConditional(func() (bool, error) { return true, write() })
}

// credentialWriteConditional is credentialWrite for a write that may decide not
// to write after all: its closure reports whether a write landed, and the
// between-hook and the registry reload run only when one did. A closure that
// reports false ran its decision under the same exclusive section - that is the
// point, the classification cannot be split from the write - but changed
// nothing, so re-deriving the instance set from it would be a reload that
// describes no change. The lock discipline is credentialWrite's exactly: the
// decision (and, when it says yes, the write) is one critical section.
func (c *hubAuthController) credentialWriteConditional(write func() (applied bool, err error)) (applied bool, err error) {
	c.credMu.Lock()
	defer c.credMu.Unlock()
	applied, err = write()
	if err != nil {
		return false, err
	}
	if !applied {
		return false, nil
	}
	credentialWriteBetween()
	_ = c.reloadRegistryLocked()
	// The credential landed. The applied answer is returned per call, captured
	// while credMu is held, so a later status read (which runs outside the lock)
	// folds it into the caller's error without depending on shared state another
	// credential write could have reset or taken.
	return true, nil
}

// credentialWriteExclusive keeps a check-and-remove operation together against
// other controller credential writers, and closes with the same reload
// credentialWrite performs: its callers rely on the instance set being
// re-derived from the removal before the section ends, exactly as the shared
// writers do, and its reload failure is not returned either, for the reason
// credentialWrite states.
func (c *hubAuthController) credentialWriteExclusive(write func() error) (applied bool, err error) {
	c.credMu.Lock()
	defer c.credMu.Unlock()
	if err := write(); err != nil {
		return false, err
	}
	credentialWriteBetween()
	_ = c.reloadRegistryLocked() // not returned; see credentialWrite
	return true, nil
}

// statusByProvider adapts Status to statusAfterWrite's reader shape, for a
// caller whose write is a file-layer credential (ApiKeySet, ApiKeyClear,
// CredentialJsonSet).
func (c *hubAuthController) statusByProvider(name string) (appwire.AuthStatusResponse, error) {
	return c.Status(appwire.AuthStatusParams{Provider: name})
}

// statusAfterWrite answers a write that has already landed with the
// instance's status, read through read - c.statusByProvider for a file-layer
// credential write, c.openAIInstanceStatus for one that already landed the
// OAuth record (LoginComplete, Logout, DevicePoll). A read that fails does
// not unwrite what it is reading, so it is reported as an applied write,
// with the provider named in the returned status and the rest left at what
// could not be read.
func (c *hubAuthController) statusAfterWrite(name string, applied bool, read func(string) (appwire.AuthStatusResponse, error)) (appwire.AuthStatusResponse, error) {
	status, err := read(name)
	if err != nil {
		// The credential already landed (applied), so the read failure is still
		// a change other clients need to hear about; a write that never landed
		// reports its failure unchanged.
		if applied {
			err = writeApplied(err)
		}
		return appwire.AuthStatusResponse{Provider: name}, err
	}
	return status, nil
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
	// The revision key is resolved once, before the lock, for the same reason
	// the instances listing resolves one there: a resolution can repair the key
	// file, and a repair under credMu would stall every credential writer. Every
	// row of this listing is keyed the same way.
	key, _ := resolveEndpointFingerprintKey(c.stateDir)
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
		status, err := c.statusLocked(appwire.AuthStatusParams{Provider: id}, key)
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
		out.Providers = append(out.Providers, c.instanceStatusKeyed(key, inst))
	}
	return out, nil
}

// storedCommandExpressionError is the refusal every stored-key surface gives
// a $(command) expression value: the store never expands one, so a command
// stored as a key would be sent as the literal text and fail at the server
// with no local hint. It points at the field that authors expressions
// instead. Only a well-formed command expression refuses: a literal key
// whose odd $ bytes merely look like a mistyped expression is text the
// store may hold, and refusing it would reject a legitimate secret with a
// message about expressions.
func storedCommandExpressionError(value string) error {
	if scan, _ := valueexpr.Scan(value); len(scan.Commands) > 0 {
		return appwire.InvalidParams("a stored key is a literal secret and is never expanded: put a $(command) expression on the instance's credential header instead, as in Authorization=Bearer $(get-gateway-token)")
	}
	return nil
}

func (c *hubAuthController) ApiKeySet(params appwire.AuthApiKeySetParams) (appwire.AuthStatusResponse, error) {
	name := normalizeAuthProvider(params.Provider)
	if strings.TrimSpace(params.Value) == "" {
		return appwire.AuthStatusResponse{}, appwire.InvalidParams("value is required")
	}
	// A stored key is a literal secret the transports send verbatim: the store
	// never expands $(command) expressions, so one stored here would be sent
	// as the literal text and fail at the server with no local hint.
	if err := storedCommandExpressionError(params.Value); err != nil {
		return appwire.AuthStatusResponse{}, err
	}
	// The fingerprint key is resolved once, before the credential lock is taken:
	// resolving it can repair the key file (an inter-process lock and a write),
	// and a repair under that lock would hold every listing and credential op
	// behind it. One key for the whole write, so the assertion below is checked
	// against the key the caller's row was served with.
	key, keyErr := resolveEndpointFingerprintKey(c.stateDir)
	// Both refusals below ask what name authenticates with, and a rename can
	// change the answer: it holds credMu exclusively while it re-keys
	// providers.toml and reloads, so only a check inside that lock describes
	// the instance this write actually lands on.
	applied, err := c.credentialWrite(func() error {
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
		if err := c.verifyEndpointFingerprintWithKey(name, params.ExpectedEndpointFingerprint, key, keyErr); err != nil {
			return err
		}
		return c.setCredential(name, params.Value)
	})
	if err != nil {
		return appwire.AuthStatusResponse{}, err
	}
	return c.statusAfterWrite(name, applied, c.statusByProvider)
}

// skipConditionalSet records a non-writable classification on resp and reports
// that nothing was written: the one shape every skip branch of the conditional
// set returns. Each branch still spells its own Reason, because those strings
// are user-visible; this only collapses the assign-and-return.
func skipConditionalSet(resp *appwire.ApiKeyConditionalSetResponse, reason string) (bool, error) {
	resp.Action = appwire.ApiKeyConditionalSetActionSkipped
	resp.Reason = reason
	return false, nil
}

// ApiKeyConditionalSet is the host-side conditional (compare-and-set) credential
// write the remote credential push calls instead of the read-evenser/auth/status,
// classify, then evener/auth/apiKey/set pair the design rejects (07 "credential
// push"). All of the decision and the write happen inside one
// credentialWriteConditional critical section against the state the host
// resolves right there, so no concurrent change can slip between the check and
// the write, and a skip reloads nothing because it wrote nothing:
//
//   - The instance is re-resolved under the lock (endpointInstanceFor), so a
//     rename or removal that landed since the client's read is seen.
//   - A non-empty ExpectedRevision that no longer equals the instance's
//     effective-configuration revision is refused with a typed Conflict and
//     nothing is written. This is the fence: a credential whose configuration
//     changed underneath the client is never clobbered.
//   - A scheme whose credential a file-layer key must not shadow — Codex
//     OAuth, gcp-adc, auth-none — or a credential that now resolves from
//     providers.toml (api_key/credential_headers), the environment — comes
//     back as a successful typed "skipped" with a reason, matching the
//     design's classification table, so the push report shows a skip, not an
//     error. A non-key-capable scheme is classified before the ExpectedSource
//     fence, which guards a layer such a scheme can never write (see the
//     ordering note in the body). An authored credential header that supplies
//     the instance's auth-header slot is itself the instance's credential —
//     the registry reads that slot case-insensitively and classifies it
//     "credential_headers" — so the providers.toml case covers it.
//   - A non-empty ExpectedSource that no longer equals the instance's resolved
//     source is refused with a typed Conflict, for the schemes and sources the
//     write could actually land in.
//
// Only a source of "store" (updated) or "none" on a key-capable scheme (added)
// reaches c.setCredential; the response's Status is the post-write status read
// the same way every other credential write reports one.
func (c *hubAuthController) ApiKeyConditionalSet(params appwire.ApiKeyConditionalSetParams) (appwire.ApiKeyConditionalSetResponse, error) {
	name := normalizeAuthProvider(params.Provider)
	if strings.TrimSpace(params.Value) == "" {
		return appwire.ApiKeyConditionalSetResponse{}, appwire.InvalidParams("value is required")
	}
	// The same stored-key contract as ApiKeySet, before any fence or
	// classification: a $(command) expression stored as the key would be
	// sent as the literal text, and reporting it added or updated would
	// describe a credential that fails at the first request.
	if err := storedCommandExpressionError(params.Value); err != nil {
		return appwire.ApiKeyConditionalSetResponse{}, err
	}
	resp := appwire.ApiKeyConditionalSetResponse{}
	// The revision fence is keyed with the same hub-held key the endpoint
	// fingerprints use, resolved once here before the credential lock is taken:
	// resolving can repair the key file (an inter-process lock and a write), and
	// a repair while credMu is held would stall every listing and credential op.
	// One key for the whole write, so the fence is checked against the key the
	// caller's row was served with.
	key, keyErr := resolveEndpointFingerprintKey(c.stateDir)
	applied, err := c.credentialWriteConditional(func() (bool, error) {
		// The registry that resolved inst travels with it: the revision fence
		// and the classification both have to describe the same generation of
		// providers.toml as the instance they judge, and asking the controller
		// for its current registry again would let a reload land in between -
		// the revision then computed over one generation and the classification
		// over another. entryFor resolves its row this way for the same reason.
		r, inst, ok := c.endpointInstanceFor(name)
		if !ok {
			return skipConditionalSet(&resp, fmt.Sprintf("%q is not a configured provider or instance on this host", name))
		}
		source := inst.CredentialSource
		// One resolution of the instance answers both questions this section
		// asks about that generation: the revision the fence compares, and
		// whether the instance's own auth header is already supplied by its
		// authored credential_headers (the classification below).
		// resolvedRowInstance is the same single-resolution helper the listing
		// rows use, and CredentialConfigRevisionResolved contributes exactly
		// what CredentialConfigRevision would for this resolution.
		resolved, resolvedOK := resolvedRowInstance(r, inst)
		current := c.credentialConfigRevisionForKey(key, inst.Name, resolved)
		if err := c.verifyConfigRevision(name, params.ExpectedRevision, current, keyErr); err != nil {
			return false, err
		}
		// The scheme is classified before the source fence, because that fence
		// guards the credential layer the write would land in and a scheme that
		// consumes no key has none: the two reads of such an instance's source
		// legitimately differ, so fencing them would refuse a push that the
		// design's table makes a skip. The Codex case is the live one — a
		// corrupt auth/<name>.json is "none" to evener/auth/status, which treats
		// an unreadable record as absent, and "oauth" to registry resolution,
		// which asks only whether the record file exists — and the Conflict's
		// own remedy ("re-read the instance and start the push again") is one
		// re-reading cannot deliver, because every read reproduces the pair.
		switch inst.Auth {
		case registry.AuthOAuthOpenAICodex:
			return skipConditionalSet(&resp, name+" authenticates with an OAuth record; sign in on the host instead of pushing a key")
		case registry.AuthGCPADC:
			return skipConditionalSet(&resp, name+" authenticates with Google application-default credentials, which do not read an API key")
		case registry.AuthNone:
			return skipConditionalSet(&resp, name+" authenticates without a credential; a stored key would be one nothing sends")
		}
		// The fences are checked before the classification below: a client whose
		// observed state no longer describes the instance must be told so, not
		// handed a skip it could mistake for a durable decision.
		if params.ExpectedSource != "" && params.ExpectedSource != source {
			return false, appwire.Conflict(fmt.Sprintf("%s no longer resolves its credential from %q (it is now %q): re-read the instance and start the push again", name, params.ExpectedSource, source))
		}
		switch {
		case source == "api_key" || source == "credential_headers":
			return skipConditionalSet(&resp, fmt.Sprintf("%s resolves its credential from providers.toml (%s), which outranks the file layer", name, source))
		case strings.HasPrefix(source, "env:"):
			return skipConditionalSet(&resp, fmt.Sprintf("the host's environment supplies %s's credential (%s), which a stored key would silently replace", name, source))
		// An authored credential that resolves to nothing is terminal: the
		// registry returns "none" at its layer without consulting the file store
		// or the environment (registry.credential), which is why the source
		// string alone cannot carry this - "none" is also what a writable
		// instance with no credential reports. A stored key here is one
		// nothing reads until the variables are set, so the write is refused
		// rather than reported as a live credential that is dead.
		case resolvedOK && resolved.Credential.AuthoredLayer != "":
			return skipConditionalSet(&resp, fmt.Sprintf("%s authors its %s in providers.toml and it resolves to nothing, which outranks any stored key: a key pushed here would be one nothing sends", name, resolved.Credential.AuthoredLayer))
		case source == "store":
			resp.Action = appwire.ApiKeyConditionalSetActionUpdated
		case source == "none":
			resp.Action = appwire.ApiKeyConditionalSetActionAdded
		default:
			return skipConditionalSet(&resp, fmt.Sprintf("%s resolves its credential from %s, which the credential push does not manage", name, source))
		}
		if err := c.setCredential(name, params.Value); err != nil {
			return false, err
		}
		return true, nil
	})
	if err != nil {
		return appwire.ApiKeyConditionalSetResponse{}, err
	}
	status, err := c.statusAfterWrite(name, applied, c.statusByProvider)
	if err != nil {
		return appwire.ApiKeyConditionalSetResponse{}, err
	}
	resp.Status = status
	return resp, nil
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
	// The fingerprint key is resolved once, before the credential lock is taken:
	// resolving it can repair the key file (an inter-process lock and a write),
	// and a repair under that lock would hold every listing and credential op
	// behind it. One key for the whole write, so the assertion below is checked
	// against the key the caller's row was served with.
	key, keyErr := resolveEndpointFingerprintKey(c.stateDir)
	// The endpoint check and the clear are one step, the way the set is: the
	// clear was confirmed for the row the client listed, and a name another
	// client has re-pointed since must not have its replacement instance's key
	// removed.
	applied, err := c.credentialWrite(func() error {
		if err := c.verifyEndpointFingerprintWithKey(name, params.ExpectedEndpointFingerprint, key, keyErr); err != nil {
			return err
		}
		return c.clearCredential(name)
	})
	if err != nil {
		return appwire.AuthStatusResponse{}, err
	}
	return c.statusAfterWrite(name, applied, c.statusByProvider)
}

func effectiveHubAuthEnv(launchEnv map[string]string) map[string]string {
	out := envToMap(os.Environ())
	maps.Copy(out, launchEnv)
	return out
}

func openAIStateDirFromEnv(env map[string]string) string {
	return openAIStateDirFromEnvMap(env)
}

// openAIStatusFromRecord reports needsLogin only when the user must actually
// sign in again: the access token is expired AND there is no refresh token to
// recover it with. An expired access token backed by a refresh token is
// routine and expected - ResolveRuntimeCredentials refreshes it on the next
// use - so that case reports signedIn/needsRefresh instead of needsLogin
// (issue #2468). A refresh that was attempted and permanently rejected would
// also justify needsLogin, but nothing persists that outcome to the stored
// record today (ResolveRuntimeCredentials's permanent-failure branch returns
// an error without rewriting record.RefreshToken), so that case is not
// distinguishable here yet and falls through to whatever RefreshToken holds.
func openAIStatusFromRecord(now time.Time, record authopenai.AuthRecord) authopenai.AuthStatus {
	expired := !record.Expiry.IsZero() && !record.Expiry.After(now)
	needsLogin := expired && strings.TrimSpace(record.RefreshToken) == ""
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
		return appwire.AuthDeviceStartResponse{}, appwire.EndpointConflict(provider + " cannot be checked against its endpoint: the hub cannot key its endpoint fingerprints right now, so this destination could not be checked when the sign-in completes; review its destination and start the sign-in again")
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
	// Abandoned device flows have no other reaper: expiry is only checked when a
	// flow is polled, so a start the user never polls would keep its device code
	// for the life of the process. Sweep under the same lock, as LoginStart does
	// for c.flows, so the map holds only live flows plus the one being added.
	now := c.now()
	for id, flow := range c.deviceFlows {
		if now.Sub(flow.StartedAt) >= hubAuthFlowTTL {
			delete(c.deviceFlows, id)
		}
	}
	c.deviceFlows[flowID] = deviceFlow{Provider: provider, Code: dc, EndpointFingerprint: endpoint, StartedAt: now}
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
	// The fingerprint key is resolved once, before the credential lock is taken:
	// resolving it can repair the key file (an inter-process lock and a write),
	// and a repair under that lock would hold every listing and credential op
	// behind it. Only the key is threaded into the check below: the flow guard's
	// empty capture is refused on the destination and the state root it reads,
	// not on the resolution error.
	key, _ := resolveEndpointFingerprintKey(c.stateDir)
	// Re-checked under the lock for the same reason LoginComplete re-checks
	// it: the poll's own exchange is the long step an instance mutation can
	// land in.
	applied, err := c.credentialWrite(func() error {
		if err := c.requiresCodex(provider); err != nil {
			return err
		}
		if err := c.verifyFlowEndpointWithKey(provider, flow.EndpointFingerprint, key); err != nil {
			return err
		}
		return c.saveAuth(c.stateDir, provider, record)
	})
	if err != nil {
		return appwire.AuthDevicePollResponse{}, err
	}
	c.mu.Lock()
	delete(c.deviceFlows, flowID)
	c.mu.Unlock()

	// Authorized and applied: the record is saved. The state is what the
	// handler reads to tell this from a pending poll, which writes nothing.
	status, err := c.statusAfterWrite(provider, applied, c.openAIInstanceStatusByName)
	return appwire.AuthDevicePollResponse{State: "authorized", Status: &status}, err
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
		// The gate judges the launch the bare name makes — the default
		// row's merged transport, the same presence resolution the
		// status pane shows — not the provider's model-less shape: a
		// row or glob override must move this gate with the pane it
		// sits behind.
		res, err := r.ResolveInstancePresence(name)
		if err != nil {
			return "", false
		}
		return res.Transport.Auth, true
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

// endpointFingerprintForKey is endpointFingerprintFor over a key the caller
// already resolved (destinationFingerprintKeyed). The credential writes resolve
// the key once, before taking credMu, and check their assertion against that
// key inside the lock: a resolution there can repair the key file (an
// inter-process lock and a write), and a repair must not run while the
// credential lock is held - List resolves its listing's key outside the same
// lock for the same reason. Empty for the same reasons endpointFingerprintFor
// is, plus a key the hub could not resolve.
func (c *hubAuthController) endpointFingerprintForKey(name string, key []byte) string {
	r, inst, ok := c.endpointInstanceFor(name)
	if !ok {
		return ""
	}
	return destinationFingerprintKeyed(key, r, inst)
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
// This form resolves the fingerprint key through the same seam List resolves
// its key with (resolveEndpointFingerprintKey) and delegates to
// verifyEndpointFingerprintWithKey; the credential writes resolve the key
// before taking credMu and call that form, so no resolution - and no repair of
// the key file it can make - runs while that lock is held.
//
// An empty assertion is a client that was shown no endpoint. That is nothing to
// check only while the hub itself has nothing to key with - no state root at
// all, a bare controller - or can key a fingerprint: a state root that exists
// but cannot be read or written is exactly the state that serves empty
// fingerprints to every client, and accepting the assertions that follow would
// let a concurrent endpoint change receive the credential with no verification
// at all. There the write fails closed, with a refusal the user can act on.
func (c *hubAuthController) verifyEndpointFingerprint(name, asserted string) error {
	key, keyErr := resolveEndpointFingerprintKey(c.stateDir)
	return c.verifyEndpointFingerprintWithKey(name, asserted, key, keyErr)
}

// verifyEndpointFingerprintWithKey is verifyEndpointFingerprint over a key the
// caller resolved before taking its credential lock, with keyErr the reason it
// has none. The credential writes resolve the key before that lock and call
// this form, because a resolution here could repair the key file (an
// inter-process lock and a write) while every listing and credential op waited
// on credMu. Threading the result in keeps the answer for every case: keyErr is
// the failed resolution the empty assertion is refused for, and a key the hub
// could not resolve is the empty current value an assertion is refused against.
func (c *hubAuthController) verifyEndpointFingerprintWithKey(name, asserted string, key []byte, keyErr error) error {
	if asserted == "" {
		if keyErr != nil {
			return appwire.EndpointConflict(name + " cannot be checked against the endpoint this form was opened on: the hub cannot key its endpoint fingerprints right now; this destination cannot be verified, so review its destination and enter the credential again")
		}
		return nil
	}
	current := c.endpointFingerprintForKey(name, key)
	// A fingerprint the hub cannot match is a refusal, not a bypass: the client
	// was shown an endpoint, and a hub that can no longer describe where the
	// name points cannot say the credential would land there. Landing it anyway
	// would let a deleted key file or an unreachable state root switch the guard
	// off without a word. A client that asserted nothing is handled above: that
	// is nothing to check only while the hub can key a fingerprint or has no
	// state root at all.
	if current == "" {
		return appwire.EndpointConflict(name + " cannot be checked against the endpoint this form was opened on: the hub cannot resolve it now, so review its destination and enter the credential again")
	}
	if current != asserted {
		return appwire.EndpointConflict(name + " no longer resolves to the endpoint this form was opened on: review its destination and enter the credential again")
	}
	return nil
}

// verifyConfigRevision refuses a conditional credential write whose client
// captured a configuration revision the host no longer matches, and refuses to
// fence at all while the hub cannot key a revision it has a state root for.
//
// An empty assertion is a client that was shown no revision. A hub with no state
// root at all - a bare controller - has nothing to key with, so an empty
// assertion is the only thing it can carry, and it is accepted. A state root
// that exists but cannot be read or written is the state that serves empty
// revisions to every client, and accepting the assertions that follow would let
// the conditional set write with no fence at all: there the write fails closed
// with a refusal the user can act on, exactly as verifyEndpointFingerprintWithKey
// fails an empty endpoint assertion closed. current is the keyed revision
// (credentialConfigRevisionForKey), empty when the hub has no key or the name
// does not resolve; a non-empty assertion against either is refused too. keyErr
// is the failed key resolution that the empty assertion is refused for.
func (c *hubAuthController) verifyConfigRevision(name, asserted, current string, keyErr error) error {
	if asserted == "" {
		if keyErr != nil {
			return appwire.Conflict(name + " cannot be checked against the configuration this credential was prepared for: the hub cannot key its credential-configuration revision right now, so its configuration cannot be verified; re-read the instance and start the push again")
		}
		return nil
	}
	if current == "" {
		return appwire.Conflict(name + " cannot be checked against the configuration this credential was prepared for: the hub cannot resolve it now; re-read the instance and start the push again")
	}
	if current != asserted {
		return appwire.Conflict(name + " changed on the host after this credential was prepared: its configuration revision no longer matches the one this request observed; re-read the instance and start the push again")
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
	key, _ := resolveEndpointFingerprintKey(c.stateDir)
	return c.verifyFlowEndpointWithKey(name, started, key)
}

// verifyFlowEndpointWithKey is verifyFlowEndpoint over a key the caller
// resolved before taking its credential lock (verifyEndpointFingerprintWithKey
// states why the resolution must not happen there). The empty capture's refusal
// reads the destination and the state root rather than the key, so it is
// untouched; a key the hub could not resolve leaves the empty current value the
// comparison below refuses, exactly as the resolving wrapper's own resolution
// produced it.
func (c *hubAuthController) verifyFlowEndpointWithKey(name, started string, key []byte) error {
	if started == "" {
		if c.endpointHasDestination(name) && c.hasEndpointStateRoot() {
			return appwire.EndpointConflict(name + " cannot be checked against the endpoint this sign-in was started on: the hub cannot key its endpoint fingerprints right now, so review its destination and start the sign-in again")
		}
		return nil
	}
	current := c.endpointFingerprintForKey(name, key)
	// As verifyEndpointFingerprint reads it: a flow bound to an endpoint the hub
	// can no longer describe is one whose record nobody can place, so it is
	// refused rather than filed somewhere the user never signed in for.
	if current == "" {
		return appwire.EndpointConflict(name + " cannot be checked against the endpoint this sign-in was started on: the hub cannot resolve it now, so review its destination and start the sign-in again")
	}
	if current != started {
		return appwire.EndpointConflict(name + " no longer resolves to the endpoint this sign-in was started on: review its destination and start the sign-in again")
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
	// The fingerprint key is resolved once, before the credential lock is taken:
	// resolving it can repair the key file (an inter-process lock and a write),
	// and a repair under that lock would hold every listing and credential op
	// behind it. One key for the whole write, so the assertion below is checked
	// against the key the caller's row was served with.
	key, keyErr := resolveEndpointFingerprintKey(c.stateDir)
	applied, err := c.credentialWrite(func() error {
		// Asked again inside the lock, because it is the answer at the moment
		// of the write that matters: a rename holds credMu exclusively while
		// it re-keys providers.toml and reloads, so the check above can
		// describe an instance this name no longer reaches. The one above
		// stays for the caller who pasted for the wrong instance, so the
		// refusal still beats a complaint about the JSON.
		if err := c.requiresGCPADC(name); err != nil {
			return err
		}
		if err := c.verifyEndpointFingerprintWithKey(name, params.ExpectedEndpointFingerprint, key, keyErr); err != nil {
			return err
		}
		return c.setCredential(name, value)
	})
	if err != nil {
		return appwire.AuthStatusResponse{}, err
	}
	return c.statusAfterWrite(name, applied, c.statusByProvider)
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
// layer, and for the Codex transport the OAuth record. A caller that already
// resolved inst (the instances listing, which resolved it for its endpoint
// fingerprint) passes that resolution so the revision is not derived from a
// second ResolveInstance of the same name.
func (c *hubAuthController) instanceStatus(inst registry.Instance, resolved ...registry.Resolved) appwire.AuthStatusResponse {
	key, _ := resolveEndpointFingerprintKey(c.stateDir)
	return c.instanceStatusKeyed(key, inst, resolved...)
}

// instanceStatusKeyed is instanceStatus over a key the caller already resolved,
// for a caller that holds credMu (Status, List, the listing's own rows) and must
// not resolve - and possibly repair - the key file under it.
func (c *hubAuthController) instanceStatusKeyed(key []byte, inst registry.Instance, resolved ...registry.Resolved) appwire.AuthStatusResponse {
	if inst.Auth == registry.AuthOAuthOpenAICodex {
		resp, _ := c.openAIInstanceStatusKeyed(key, inst.Name, resolved...)
		return resp
	}
	_, hasFile := c.storedKey(inst.Name)
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
		// The revision the credential push fences its conditional set against,
		// resolved from the same snapshot instanceStatus answers the source from
		// (see hubcore.CredentialConfigRevision), keyed with the caller's key, or
		// from the caller's own resolution when it has one.
		ConfigRevision: c.credentialConfigRevisionForKey(key, inst.Name, resolved...),
	}
}

// credentialConfigRevisionForKey is the effective credential-configuration
// revision for name, over a key the caller already resolved and a resolution it
// already holds when it has one. It is keyed with the same hub-held secret the
// endpoint fingerprints use (fingerprintWithKey): the listing and the status
// reads resolve one key before taking credMu - a resolution can repair the key
// file, and a repair under the lock would stall every credential op - and
// thread it down here. An empty key yields no revision
// (hubcore.CredentialConfigRevisionResolved); the credential write fences on
// that with verifyConfigRevision rather than reading it as no fence. With no
// resolution it resolves the name itself, which is what the single-instance
// callers (a post-write status read) want.
func (c *hubAuthController) credentialConfigRevisionForKey(key []byte, name string, resolved ...registry.Resolved) string {
	if len(resolved) > 0 {
		return hubcore.CredentialConfigRevisionResolved(key, resolved[0])
	}
	return hubcore.CredentialConfigRevision(key, c.registry(), name)
}

// openAIInstanceStatusByName is openAIInstanceStatus in statusAfterWrite's
// reader shape: a post-write status read has no resolution to reuse, so it
// resolves the name itself.
func (c *hubAuthController) openAIInstanceStatusByName(name string) (appwire.AuthStatusResponse, error) {
	return c.openAIInstanceStatus(name)
}

// openAIInstanceStatus is the credential status of one instance on the Codex
// transport, keyed by instance name: it reads auth/<name>.json and
// credentials[name] (spec §9.5). As on instanceStatus, a caller that already
// resolved name passes that resolution so the revision is not recomputed from a
// second resolution.
func (c *hubAuthController) openAIInstanceStatus(name string, resolved ...registry.Resolved) (appwire.AuthStatusResponse, error) {
	key, _ := resolveEndpointFingerprintKey(c.stateDir)
	return c.openAIInstanceStatusKeyed(key, name, resolved...)
}

// openAIInstanceStatusKeyed is openAIInstanceStatus over a key the caller
// already resolved, for the same credential-lock reason instanceStatusKeyed
// exists.
func (c *hubAuthController) openAIInstanceStatusKeyed(key []byte, name string, resolved ...registry.Resolved) (appwire.AuthStatusResponse, error) {
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
	_, hasFile := c.storedKey(name)

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
		// An OAuth record is this instance's credential configuration; the
		// revision is resolved here as it is for every other scheme, keyed with
		// the caller's key and reusing a resolution the caller already made when
		// it has one.
		ConfigRevision: c.credentialConfigRevisionForKey(key, name, resolved...),
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
