package llm

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm/registry"
)

// ProviderAdapter is the interface a single LLM provider backend implements.
// It reports its name and serves completion requests in both buffered and
// streaming forms.
type ProviderAdapter interface {
	Name() string
	Complete(ctx context.Context, req Request) (Response, error)
	Stream(ctx context.Context, req Request) (Stream, error)
}

// Client routes LLM requests (spec §8.1): the instance half of a request
// resolves through the registry to a Resolved record whose Protocol names
// the wire implementation. Adapters registered with Register form an
// override map consulted by instance name first — when the name also
// resolves, the override receives the shaped request; when it does not,
// the request passes through untouched. Middleware, API-attempt logging,
// and provider stamping apply to both paths.
type Client struct {
	registry *registry.Registry
	// hasRegistry records that WithRegistry supplied the registry above.
	// A client without one still resolves, against EmbeddedRegistry, but
	// that snapshot's instances are neither listed nor eligible as the
	// default (spec §5.1, §8.1).
	hasRegistry bool
	stateDir    string
	hasherOnce  sync.Once
	hasher      *ContinuationHasher
	hasherErr   error

	// overridesMu guards overrides, pinnedDefault, and firstOverride. Register
	// and SetDefaultProvider are meant to keep working after a client is
	// shared, same as Use — and overrides is a plain map, so an unguarded
	// Register racing any reader is worse than middlewareMu's slice case: a
	// concurrent map write fatals the process outright, guard or not.
	overridesMu   sync.RWMutex
	overrides     map[string]ProviderAdapter
	pinnedDefault string
	firstOverride string

	// middlewareMu guards middleware. One client is legitimately shared by
	// concurrent callers — two sessions on the same injected client each
	// attach their own API logger — so registration races dispatch. An
	// RWMutex is the whole cost: one RLock per dispatch is noise next to the
	// network call it precedes.
	middlewareMu sync.RWMutex
	middleware   []Middleware
}

// middlewareSnapshot returns the registered middleware for one read. The
// returned header is safe to iterate without the lock: a later Use appends at
// or past this snapshot's length, which it never reads.
func (c *Client) middlewareSnapshot() []Middleware {
	if c == nil {
		return nil
	}
	c.middlewareMu.RLock()
	defer c.middlewareMu.RUnlock()
	return c.middleware
}

// ClientOption configures NewClient.
type ClientOption func(*Client)

// WithRegistry supplies the registry the client resolves instances against.
// Only a client given one lists the registry's instances in ProviderNames
// and takes its default instance as DefaultProvider: the fallback registry
// a bare client loads is a hermetic snapshot for resolution alone, and its
// credential-less implicit instances are nobody's default.
func WithRegistry(r *registry.Registry) ClientOption {
	return func(c *Client) {
		c.registry = r
		c.hasRegistry = r != nil
	}
}

// WithClientStateDir names the session state directory that holds the
// continuation secret (spec §7.6: the ContinuationHasher stays on the
// client, keyed by state dir).
func WithClientStateDir(dir string) ClientOption {
	return func(c *Client) { c.stateDir = dir }
}

// NewClient returns a client with no overrides. Without WithRegistry it
// resolves against EmbeddedRegistry (spec §8.1).
func NewClient(opts ...ClientOption) *Client {
	c := &Client{overrides: map[string]ProviderAdapter{}}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	return c
}

var (
	embeddedRegistryOnce sync.Once
	embeddedRegistry     *registry.Registry
)

// EmbeddedRegistry is the process-wide fallback registry: the embedded
// models.dev snapshot and curated overlay, loaded offline with no user layer,
// no cache, and no environment. Every client built without WithRegistry
// resolves against this one instance, so the catalog is parsed once per
// process rather than once per client, and a bare client resolves the same
// records on every machine without ever reading a developer's keys.
//
// It panics when the load fails: the snapshot and the overlay are compiled
// into the binary, so a failure here is a build defect, not a condition a
// caller can handle.
func EmbeddedRegistry() *registry.Registry {
	embeddedRegistryOnce.Do(func() {
		r, err := registry.Load(
			registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
			registry.WithEnv(func(string) (string, bool) { return "", false }),
		)
		if err != nil {
			panic("llm: the embedded provider registry failed to load: " + err.Error())
		}
		embeddedRegistry = r
	})
	return embeddedRegistry
}

// Registry returns the registry WithRegistry supplied, or EmbeddedRegistry.
func (c *Client) Registry() *registry.Registry {
	if c.registry != nil {
		return c.registry
	}
	return EmbeddedRegistry()
}

// Resolve resolves an instance/model reference through the client's registry.
func (c *Client) Resolve(ref string) (registry.Resolved, error) {
	return c.Registry().Resolve(ref)
}

// withRegistryAuthenticator binds the resolved scheme's registered
// authenticator to the client registry's state root, unless the caller already
// scoped one for that scheme. A record-backed authenticator reads one root, so
// without the binding a client whose registry resolved an instance under its
// own root would authenticate against whatever root the process-global default
// last named. Schemes that are not record-backed, a caller's explicit scope,
// and a registry without a root all keep their current behavior.
func (c *Client) withRegistryAuthenticator(ctx context.Context, res registry.Resolved) context.Context {
	scheme := res.Transport.Auth
	if scheme == "" || AuthenticatorOverrideFor(ctx, scheme) != nil {
		return ctx
	}
	root := c.Registry().StateRoot()
	if root == "" {
		return ctx
	}
	registered, ok := AuthenticatorFor(scheme)
	if !ok {
		return ctx
	}
	binder, ok := registered.(StateRootAuthenticator)
	if !ok {
		return ctx
	}
	return WithScopedAuthenticator(ctx, scheme, binder.WithStateRoot(root))
}

// CanServe reports whether provider/model would dispatch: an override is
// registered under the name, or the reference resolves (spec §7.3: every
// model resolves except an id off the Codex allowlist).
func (c *Client) CanServe(provider, model string) bool {
	provider = normalizeProviderName(provider)
	c.overridesMu.RLock()
	_, ok := c.overrides[provider]
	c.overridesMu.RUnlock()
	if ok {
		return true
	}
	_, err := c.Resolve(provider + "/" + model)
	return err == nil
}

// ContinuationHasher returns the hasher for the client's state directory,
// creating the secret on first use; ErrContinuationSecretUnavailable when
// the client has no state directory.
func (c *Client) ContinuationHasher() (*ContinuationHasher, error) {
	c.hasherOnce.Do(func() {
		if strings.TrimSpace(c.stateDir) == "" {
			c.hasherErr = fmt.Errorf("%w: client has no state directory", ErrContinuationSecretUnavailable)
			return
		}
		c.hasher, c.hasherErr = ContinuationHasherForStateDir(c.stateDir)
	})
	return c.hasher, c.hasherErr
}

// withHasher attaches the client's continuation hasher to ctx so the
// protocols, which are process singletons, can stamp the response-id hash.
func (c *Client) withHasher(ctx context.Context) context.Context {
	if h, err := c.ContinuationHasher(); err == nil {
		return ContextWithContinuationHasher(ctx, h)
	}
	return ctx
}

// Register adds an override adapter keyed by its Name. The first override
// becomes the default when nothing was pinned and the registry names no
// default instance. Adapters implementing Initializer are initialized
// immediately with a background context, before the adapter is published:
// a concurrent Complete or ProviderNames must never reach a name whose
// adapter is still initializing.
func (c *Client) Register(adapter ProviderAdapter) {
	if init, ok := adapter.(Initializer); ok {
		_ = init.Initialize(context.Background())
	}
	name := normalizeProviderName(adapter.Name())
	c.overridesMu.Lock()
	if c.overrides == nil {
		c.overrides = map[string]ProviderAdapter{}
	}
	c.overrides[name] = adapter
	if c.firstOverride == "" {
		c.firstOverride = name
	}
	c.overridesMu.Unlock()
}

// SetDefaultProvider pins the default instance name for requests that
// name none. A pin outranks both the registry and registration order.
func (c *Client) SetDefaultProvider(name string) {
	c.overridesMu.Lock()
	c.pinnedDefault = normalizeProviderName(name)
	c.overridesMu.Unlock()
}

// DefaultProvider is the pinned name, else the default instance of a
// registry the client was given (spec §5.1), else the first registered
// override, else "".
func (c *Client) DefaultProvider() string {
	c.overridesMu.RLock()
	pinned, first := c.pinnedDefault, c.firstOverride
	c.overridesMu.RUnlock()
	if pinned != "" {
		return pinned
	}
	if c.hasRegistry {
		if name, _, err := c.registry.DefaultInstance(); err == nil && name != "" {
			return name
		}
	}
	return first
}

// ProviderNames lists every override, plus every instance of a registry the
// client was given, sorted.
func (c *Client) ProviderNames() []string {
	if c == nil {
		return nil
	}
	seen := map[string]bool{}
	c.overridesMu.RLock()
	for name := range c.overrides {
		seen[name] = true
	}
	c.overridesMu.RUnlock()
	if c.hasRegistry {
		for _, inst := range c.registry.Instances() {
			seen[inst.Name] = true
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// HasProvider reports whether name matches a provider ProviderNames lists,
// case-insensitively. It is the shared check behind every "is this provider
// configured (and credentialed)" validation — --fast-cheap-model,
// --vision-model, and the session-side vision-model equivalent.
func (c *Client) HasProvider(name string) bool {
	if c == nil {
		return false
	}
	for _, p := range c.ProviderNames() {
		if strings.EqualFold(p, name) {
			return true
		}
	}
	return false
}

// dispatchTarget is where one request goes: an override, a resolved record,
// or both (spec §8.1).
type dispatchTarget struct {
	name     string
	override ProviderAdapter
	res      registry.Resolved
	resolved bool
	protocol Protocol
}

// dispatchTarget names the instance a request goes to and picks what serves
// it: an override registered under that name wins, and the registry record
// behind the name — when there is one — shapes the request either way.
func (c *Client) dispatchTarget(req Request) (dispatchTarget, error) {
	name := normalizeProviderName(req.Provider)
	if name == "" {
		name = c.DefaultProvider()
	}
	if name == "" {
		return dispatchTarget{}, &ConfigurationError{Message: "no provider specified and no default provider configured"}
	}
	c.overridesMu.RLock()
	override := c.overrides[name]
	c.overridesMu.RUnlock()
	t := dispatchTarget{name: name, override: override}
	res, err := c.Resolve(name + "/" + req.Model)
	switch {
	case err == nil:
		t.res, t.resolved = res, true
	case t.override == nil:
		return dispatchTarget{}, &ConfigurationError{Message: err.Error()}
	}
	if t.override == nil {
		p, ok := ProtocolFor(t.res.Protocol)
		if !ok {
			return dispatchTarget{}, &ConfigurationError{Message: fmt.Sprintf("%s: protocol %q is not registered (import primeradiant.com/evener/llm/providers/all)", name, t.res.Protocol)}
		}
		t.protocol = p
	}
	return t, nil
}

// Complete validates the request, resolves its target, shapes it for the
// resolved row, runs it through the middleware chain and the override or
// protocol, and stamps the instance name onto the response and any error.
func (c *Client) Complete(ctx context.Context, req Request) (Response, error) {
	ctx = c.bindAPIAttemptSinkBeforeDispatch(ctx)
	if err := req.Validate(); err != nil {
		return Response{}, err
	}
	// Complete defaults to a bounded network profile covering connection setup and
	// the whole request/response cycle.
	if req.AdapterTimeout == nil {
		req.AdapterTimeout = new(DefaultAdapterTimeout())
	}
	t, err := c.dispatchTarget(req)
	if err != nil {
		return Response{}, err
	}
	req.Provider = t.name
	if t.resolved {
		req = ShapeRequest(req, t.res)
	}
	base := func(ctx context.Context, req Request) (Response, error) {
		if t.resolved {
			var err error
			req, err = shapeAndApplyTokenBudget(req, t.res, t.override != nil)
			if err != nil {
				return Response{}, err
			}
		}
		observeRequestSent(ctx)
		if t.override != nil {
			return t.override.Complete(ctx, req)
		}
		return t.protocol.Complete(c.withHasher(ctx), req, t.res)
	}
	handler := applyMiddlewareComplete(base, c.middlewareSnapshot())
	var rotation authRotation
	if t.resolved && t.override == nil {
		ctx = c.withRegistryAuthenticator(ctx, t.res)
		rotation = authRotationFor(ctx, t.res)
	}
	resp, err := retryOnAuthRotation(ctx, rotation, t.res, func() (Response, error) {
		return handler(ctx, req)
	}, nil)
	resp.Provider = t.name
	err = RewriteErrorProvider(err, t.name)
	return resp, err
}

// Stream is Complete's streaming twin; stream events carry the instance
// name through providerStampStream.
func (c *Client) Stream(ctx context.Context, req Request) (Stream, error) {
	ctx = c.bindAPIAttemptSinkBeforeDispatch(ctx)
	if err := req.Validate(); err != nil {
		return nil, err
	}
	// Stream defaults to a bounded connection, HTTP-attempt lifetime, and per-line
	// stream-read profile. The request lifetime includes response headers and body
	// consumption; caller-supplied context and HTTP client policies remain authoritative.
	if req.AdapterTimeout == nil {
		req.AdapterTimeout = new(DefaultAdapterTimeout())
	}
	t, err := c.dispatchTarget(req)
	if err != nil {
		return nil, err
	}
	req.Provider = t.name
	if t.resolved {
		req = ShapeRequest(req, t.res)
	}
	base := func(ctx context.Context, req Request) (Stream, error) {
		if t.resolved {
			var err error
			req, err = shapeAndApplyTokenBudget(req, t.res, t.override != nil)
			if err != nil {
				return nil, err
			}
		}
		observeRequestSent(ctx)
		var (
			st  Stream
			err error
		)
		if t.override != nil {
			st, err = t.override.Stream(ctx, req)
		} else {
			st, err = t.protocol.Stream(c.withHasher(ctx), req, t.res)
		}
		// An adapter that reports success with no stream is a broken contract:
		// the stamping wrapper reads the inner channel on every iteration, so a
		// nil inner dereferences it and kills the process. Reject it here, at
		// the innermost layer, so middleware settles a real error rather than
		// logging a successful call that produced nothing.
		if err == nil && st == nil {
			err = fmt.Errorf("%w: provider %q returned no stream", ErrStreamUnsupported, t.name)
		}
		return st, err
	}
	handler := applyMiddlewareStream(base, c.middlewareSnapshot())
	var rotation authRotation
	if t.resolved && t.override == nil {
		ctx = c.withRegistryAuthenticator(ctx, t.res)
		rotation = authRotationFor(ctx, t.res)
	}
	st, err := retryOnAuthRotation(ctx, rotation, t.res, func() (Stream, error) {
		return handler(ctx, req)
	}, func(st Stream) {
		if st != nil {
			_ = st.Close()
		}
	})
	if err != nil {
		return nil, RewriteErrorProvider(err, t.name)
	}
	return newProviderStampStream(st, t.name), nil
}

// shapeAndApplyTokenBudget shapes req and admits it against res's budget.
//
// override says whether an adapter override serves the request. A protocol
// route's builder replaces a tool schema before sending it, so the count is
// taken over the list that builder emits rather than over the one the request
// arrived with. An override marshals the definition it was handed, so the list
// it received is its wire list and the catalog's own count is the price.
func shapeAndApplyTokenBudget(req Request, res registry.Resolved, override bool) (Request, error) {
	req = ShapeRequest(req, res)
	budgeted, _, err := ApplyTokenBudgetForRoute(req, res, override)
	return budgeted, err
}

// ApplyTokenBudgetForRoute runs ApplyTokenBudget over the tool list res's route
// emits (EffectiveToolsJSONBytes) without writing that count into the request:
// the returned request carries the stamp the caller set, so a later price by
// another route starts from the catalog rather than from this route's emission.
//
// override says an adapter override serves the request, so the catalog it is
// handed is the list it dispatches.
func ApplyTokenBudgetForRoute(req Request, res registry.Resolved, override bool) (Request, TokenBudget, error) {
	return ApplyTokenBudgetWithToolBytes(req, res, EffectiveToolsJSONBytes(req, res, override))
}

// ModelListing is what Models returns: the instance's visible live-authoritative
// or static-fallback rows.
type ModelListing struct {
	// Live is true when the instance's models endpoint answered successfully;
	// false means the endpoint is unsupported or failed (spec §8.1). A client
	// with no registry of its own reports Live over snapshot rows: it
	// fetches the listing but does not write it into the registry it shares
	// with every other such client, so the rows below are the snapshot's.
	Live bool
	// Usable is true when the live response contains a visible chat model and
	// Models therefore contains the live-authoritative rows. Live can be true
	// while Usable is false; Models then contains the static fallback.
	Usable bool
	// Models are the visible rows — hidden rows, disabled rows (registry
	// ErrModelDisabled), and rows whose live layer says Tools = false are
	// dropped (spec §5) — sorted by model id.
	Models []registry.Resolved
}

// ModelNotAvailableError reports a launch naming a model the instance's
// authoritative roster does not list. Available names every model the roster
// offers, so the caller can tell the user what to choose instead.
type ModelNotAvailableError struct {
	Instance  string
	Model     string
	Available []string
}

func (e *ModelNotAvailableError) Error() string {
	if len(e.Available) == 0 {
		return fmt.Sprintf("%s does not list %q and offers no models", e.Instance, e.Model)
	}
	return fmt.Sprintf("%s does not list %q; available models: %s", e.Instance, e.Model, strings.Join(e.Available, ", "))
}

// LiveModelLister is the optional override interface for adapters that serve
// a live model listing. An override that does not implement it cannot list
// models under its instance name, even when the registry knows that name.
type LiveModelLister interface {
	// Implementations should use ModelListingTimeout(ctx) for their HTTP requests.
	// Client.Models supplies the policy without imposing a total deadline by default.
	LiveModels(ctx context.Context) ([]registry.Model, error)
}

// Models lists an instance's models. An override lists through its own
// LiveModels seam and its rows are returned as they came. Otherwise a usable
// protocol listing is applied to the registry; an unusable result returns the
// static fallback, alongside the original fetch error when there is one.
func (c *Client) Models(ctx context.Context, instance string) (ModelListing, error) {
	// Normalized once up front so the fetch, the apply, and the resolve
	// all key the same instance: listLive normalizes internally too,
	// but ApplyLive and resolveListing below must not run on the raw
	// string (upper case, surrounding whitespace), or the listing is
	// fetched and then silently dropped under the wrong key.
	instance = normalizeProviderName(instance)
	authoritative := !c.isOverride(instance) && c.Registry().LiveListingAuthoritative(instance)
	rows, live, err := c.listLive(ctx, instance)
	if err != nil {
		if c.isOverride(instance) {
			return ModelListing{}, err
		}
		listing, fallbackErr := c.resolveFallbackListing(instance, false)
		if fallbackErr != nil {
			return ModelListing{}, errors.Join(err, fallbackErr)
		}
		return listing, err
	}
	if c.isOverride(instance) {
		// An override has no registry record behind it: its rows are
		// returned as they came, never written anywhere.
		models := standaloneRows(instance, rows)
		return ModelListing{Live: true, Usable: len(models) > 0, Models: models}, nil
	}
	if !live {
		return c.resolveFallbackListing(instance, false)
	}
	if !c.LiveListingUsable(instance, rows) {
		if c.hasRegistry {
			c.Registry().ApplyLiveListing(instance, registry.LiveListing{Authoritative: authoritative})
		}
		if authoritative {
			// The account answered and offers nothing Evener can dispatch:
			// the bundled rows are not this subscription's catalog.
			return ModelListing{Live: true}, nil
		}
		return c.resolveFallbackListing(instance, true)
	}
	if !c.hasRegistry {
		if authoritative {
			return ModelListing{Live: true, Usable: true, Models: standaloneRows(instance, rows)}, nil
		}
		return c.resolveFallbackListing(instance, true)
	}
	// Only a client that owns its registry records what an instance's
	// transport said. A client without WithRegistry resolves against the
	// process-wide EmbeddedRegistry shared by every bare client.
	c.Registry().ApplyLiveListing(instance, registry.LiveListing{Rows: rows, Authoritative: authoritative})
	return c.resolveListing(instance)
}

// LiveListingUsable reports whether rows contain a chat model that survives
// static hidden/disabled policy and the §5 live Tools=false filter. It does not
// read or change cached live state, so the hub can decide what to publish before
// its token-validated registry write.
func (c *Client) LiveListingUsable(instance string, rows []registry.Model) bool {
	instance = normalizeProviderName(instance)
	for _, listed := range rows {
		if !registry.IsChatModelID(listed.ID) || listed.Hidden {
			continue
		}
		static, err := c.Registry().ResolveListingRow(instance+"/"+listed.ID, false, true)
		if err != nil {
			if errors.Is(err, registry.ErrModelDisabled) {
				continue
			}
			// A live-only row has no static record to resolve. Its advertised
			// facts alone decide whether it is usable.
			if listed.Caps.Tools == nil || *listed.Caps.Tools {
				return true
			}
			continue
		}
		if static.Model.Hidden {
			continue
		}
		if listed.Caps.Tools == nil || *listed.Caps.Tools || strings.HasPrefix(static.Provenance["Tools"], registry.LayerConfig+"/") {
			return true
		}
	}
	return false
}

// ListingProvesUnavailable reports whether listing is proof that the instance
// serves no model: its account-owned roster answered successfully and offered
// no usable row. A failed fetch (Live false) and a non-authoritative fallback
// prove nothing about availability.
func (c *Client) ListingProvesUnavailable(instance string, listing ModelListing) bool {
	if c == nil || !listing.Live || listing.Usable {
		return false
	}
	instance = normalizeProviderName(instance)
	if c.isOverride(instance) {
		return false
	}
	return c.Registry().LiveListingAuthoritative(instance)
}

// isOverride reports whether name is a registered adapter override.
func (c *Client) isOverride(name string) bool {
	c.overridesMu.RLock()
	defer c.overridesMu.RUnlock()
	return c.overrides[name] != nil
}

// ServesOverride reports whether an adapter override serves requests for the
// named provider, resolving an empty name through the default provider the way
// a dispatch does.
//
// A caller that prices a request before dispatch needs this because the two
// halves of a target do not bill the same list: a protocol route's builder
// rewrites tool schemas before sending them, and an override marshals the
// definition it was handed. A price taken over the protocol's emitted list and
// then applied to a request an override serves bills schemas that never reach
// the wire.
func (c *Client) ServesOverride(provider string) bool {
	if c == nil {
		return false
	}
	name := normalizeProviderName(provider)
	if name == "" {
		name = c.DefaultProvider()
	}
	return c.isOverride(name)
}

// ListLive fetches one instance's raw live rows without touching the
// registry: the hub's live prefetch resolves the visible rows itself and
// applies the raw snapshot only through the holder's token-validated
// path, so a superseded fetch never writes. ok=false means the endpoint
// does not support listing (spec §8.1: not a failure); the caller then
// applies nothing.
func (c *Client) ListLive(ctx context.Context, instance string) (rows []registry.Model, ok bool, err error) {
	return c.listLive(ctx, instance)
}

// listLive is the fetch half of Models: timeout, override seam, and the
// protocol request. It never writes the registry.
func (c *Client) listLive(ctx context.Context, instance string) ([]registry.Model, bool, error) {
	timeout := ModelListingTimeout(ctx)
	ctx = WithModelListingTimeout(ctx, *timeout)
	instance = normalizeProviderName(instance)
	r := c.Registry()
	c.overridesMu.RLock()
	override := c.overrides[instance]
	c.overridesMu.RUnlock()
	if override != nil {
		// An override owns its instance name: its listing seam is the only
		// way it can list, so no registry-only fallback stands in for it.
		// Its rows stay out of the registry -- a client without WithRegistry
		// shares EmbeddedRegistry with every other such client (spec §5.1,
		// §8.1) -- and only an instance's own transport may speak for it.
		lister, ok := override.(LiveModelLister)
		if !ok {
			return nil, false, &ConfigurationError{Message: fmt.Sprintf("provider %s does not support listing models", instance), Cause: ErrModelListingUnsupported}
		}
		// Overrides have no protocol layer to apply their request deadline.
		// Built-in protocols own it themselves, preserving the original caller
		// context for canonical attempt timeout/cancellation attribution.
		ctx, cancel := ApplyAdapterTimeout(ctx, timeout, false)
		defer cancel()
		opCtx, op := c.beginProviderOperation(ctx)
		rows, err := lister.LiveModels(opCtx)
		op.settle(opCtx, err)
		if err != nil {
			return nil, false, RewriteErrorProvider(err, instance)
		}
		return rows, true, nil
	}
	res, err := r.ResolveInstanceListing(instance)
	if err != nil {
		return nil, false, &ConfigurationError{Message: err.Error()}
	}
	ctx = c.withRegistryAuthenticator(ctx, res)
	// A Copilot instance's roster is parsed by the Chat Completions listing
	// parser whatever generation protocol the user configured: the transport's
	// auth scheme owns the account's picker/policy filter and per-row routing,
	// and the parser reads the resolved transport's own models URL.
	listingProtocol := res.Protocol
	if res.Transport.Auth == registry.AuthOAuthGitHubCopilot {
		listingProtocol = registry.ProtocolOpenAIChat
	}
	p, ok := ProtocolFor(listingProtocol)
	if !ok {
		return nil, false, &ConfigurationError{Message: fmt.Sprintf("%s: protocol %q is not registered", instance, listingProtocol)}
	}
	opCtx, op := c.beginProviderOperation(ctx)
	rows, err := p.ListModels(opCtx, res)
	op.settle(opCtx, err)
	switch {
	case errors.Is(err, ErrModelListingUnsupported):
		return nil, false, nil
	case err != nil:
		return nil, false, RewriteErrorProvider(err, instance)
	default:
		return rows, true, nil
	}
}

// resolveListing resolves every visible id the registry knows for instance --
// after the caller applied the live rows -- and filters by the §5
// visibility rule. A lookup failure is an error, not an empty
// listing: swallowing it would silently drop a fetched live listing
// under a mismatched key. Rows resolve at facts depth, so displaying the roster
// runs no credential command (spec §10.1).
func (c *Client) resolveListing(instance string) (ModelListing, error) {
	ids, err := c.Registry().ModelIDs(instance)
	return c.resolveListingIDs(instance, true, true, ids, err, func(ref string) (registry.Resolved, error) {
		return c.Registry().ResolveListingRow(ref, true, false)
	})
}

func (c *Client) resolveFallbackListing(instance string, live bool) (ModelListing, error) {
	ids, err := c.Registry().FallbackModelIDs(instance)
	return c.resolveListingIDs(instance, live, false, ids, err, func(ref string) (registry.Resolved, error) {
		return c.Registry().ResolveListingRow(ref, false, false)
	})
}

func (c *Client) resolveListingIDs(instance string, live, usable bool, ids []string, err error, resolveModel func(string) (registry.Resolved, error)) (ModelListing, error) {
	if err != nil {
		return ModelListing{}, &ConfigurationError{Message: err.Error()}
	}
	out := make([]registry.Resolved, 0, len(ids))
	for _, id := range ids {
		row, err := resolveModel(instance + "/" + id)
		if err != nil || row.Model.Hidden || LiveSaysNoTools(row) {
			continue
		}
		out = append(out, row)
	}
	return ModelListing{Live: live, Usable: usable, Models: out}, nil
}

// LiveSaysNoTools reports the §5 visibility rule: a row is hidden when the
// live layer is the one that set Tools = false. A catalog row that declares
// no tool support stays listed — the registry, not the provider's current
// listing, is what said so. Exported for the hub picker, whose
// command-credentialed fallback resolves registry rows through the same
// filter the child's own listing applies.
func LiveSaysNoTools(row registry.Resolved) bool {
	return row.Caps.Tools != nil && !*row.Caps.Tools && row.Provenance["Tools"] == registry.LayerLive
}

// standaloneRows turns a listing that has no registry record behind it into
// Resolved rows, sorted by model id. An override's listing is its own live
// layer, so the §5 visibility rule applies to it directly: a hidden row and a
// row that says Tools = false are both dropped.
func standaloneRows(instance string, rows []registry.Model) []registry.Resolved {
	out := make([]registry.Resolved, 0, len(rows))
	for _, m := range rows {
		if !registry.IsChatModelID(m.ID) || m.Hidden || (m.Caps.Tools != nil && !*m.Caps.Tools) {
			continue
		}
		out = append(out, registry.Resolved{Instance: instance, ModelID: m.ID, WireID: m.ID, Model: m, Caps: m.Caps})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModelID < out[j].ModelID })
	return out
}

// PlanResponsesContinuation returns the continuation plan for req's
// instance (spec §7.6): an override's own planner when one is registered
// under the name, else the plan computed from Resolved and the built body.
func (c *Client) PlanResponsesContinuation(ctx context.Context, req Request) (ResponsesContinuationPlan, error) {
	t, err := c.dispatchTarget(req)
	if err != nil {
		return ResponsesContinuationPlan{}, err
	}
	req.Provider = t.name
	if t.resolved {
		req = ShapeRequest(req, t.res)
	}
	if t.override != nil {
		planner, ok := t.override.(ResponsesContinuationPlanner)
		if !ok {
			return ResponsesContinuationPlan{}, &ConfigurationError{Message: "provider does not support responses continuation planning: " + t.name}
		}
		return planner.PlanResponsesContinuation(req)
	}
	return c.planContinuation(ctx, req, t.res, t.protocol)
}

// Use appends middleware to the client. Middleware is applied in registration order
// for the request phase and in reverse order for the response/event phases.
func (c *Client) Use(mw ...Middleware) {
	if c == nil {
		return
	}
	c.middlewareMu.Lock()
	c.middleware = append(c.middleware, mw...)
	c.middlewareMu.Unlock()
}

type sessionAPILogReleaser interface {
	ReleaseSession(string) error
}

// ReleaseSessionAPILog releases any routed API-log ownership held by the
// client's middleware for sessionID.
func (c *Client) ReleaseSessionAPILog(sessionID string) error {
	var result error
	for _, middleware := range c.middlewareSnapshot() {
		if releaser, ok := middleware.(sessionAPILogReleaser); ok {
			result = errors.Join(result, releaser.ReleaseSession(sessionID))
		}
	}
	return result
}

// bindAPIAttemptSinkBeforeDispatch gives a caller-owned logical attempt group
// its canonical destination before request validation and provider resolution.
// Provider middleware still owns implicit groups for ordinary direct calls.
func (c *Client) bindAPIAttemptSinkBeforeDispatch(ctx context.Context) context.Context {
	group := apiAttemptGroupFromContext(ctx)
	if group == nil {
		return ctx
	}
	var sink APIAttemptSink
	for _, middleware := range c.middlewareSnapshot() {
		if candidate, ok := middleware.(APIAttemptSink); ok {
			sink = candidate
		}
	}
	if sink == nil {
		return ctx
	}
	group.mu.Lock()
	group.bindSinkLocked(apiAttemptSinkContext{sink: sink})
	group.mu.Unlock()
	return WithAPIAttemptSink(ctx, sink)
}

// providerOperation coordinates canonical attempt evidence for provider API
// operations that do not pass through completion middleware, such as model
// listing and exact input-token counting.
type providerOperation struct {
	group     *APIAttemptGroup
	ownsGroup bool
}

func (c *Client) beginProviderOperation(ctx context.Context) (context.Context, *providerOperation) {
	if ctx == nil {
		ctx = context.Background()
	}
	group := apiAttemptGroupFromContext(ctx)
	state, _ := ctx.Value(apiAttemptSinkContextKey{}).(apiAttemptSinkContext)
	sink := state.sink
	if sink == nil {
		for _, middleware := range c.middlewareSnapshot() {
			if candidate, ok := middleware.(APIAttemptSink); ok {
				sink = candidate
			}
		}
	}
	if sink == nil {
		return ctx, nil
	}

	operation := &providerOperation{group: group}
	if operation.group == nil {
		operation.group = NewAPIAttemptGroup(identifier.MustNewAgentCallID())
		operation.ownsGroup = true
	}
	ctx = WithAPIAttemptGroup(ctx, operation.group)
	ctx = WithAPIAttemptSink(ctx, sink)
	return ctx, operation
}

func (o *providerOperation) settle(ctx context.Context, err error) {
	if o == nil || !o.ownsGroup {
		return
	}
	o.group.mu.Lock()
	hasBegunAttempt := o.group.finalAttemptCount != 0
	o.group.mu.Unlock()
	if !hasBegunAttempt {
		return
	}
	o.group.SettleResult(ctx, err)
}

// Optional adapter interfaces. Adapters may implement these for additional lifecycle
// and capability management.

// Closer is implemented by adapters that hold resources requiring cleanup.
type Closer interface {
	Close() error
}

// Initializer is implemented by adapters that need explicit initialization.
type Initializer interface {
	Initialize(ctx context.Context) error
}

// ResponsesContinuationPlanner is implemented by adapters that can build a
// ResponsesContinuationPlan for a request.
type ResponsesContinuationPlanner interface {
	PlanResponsesContinuation(req Request) (ResponsesContinuationPlan, error)
}

// Close closes all registered adapters that implement the Closer interface.
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	var firstErr error
	for _, a := range c.overridesSnapshot() {
		if cl, ok := a.(Closer); ok {
			if err := cl.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// Initialize calls Initialize on all registered adapters that implement the Initializer interface.
func (c *Client) Initialize(ctx context.Context) error {
	if c == nil {
		return nil
	}
	for _, a := range c.overridesSnapshot() {
		if init, ok := a.(Initializer); ok {
			if err := init.Initialize(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

// overridesSnapshot returns the registered override adapters for one read,
// safe to range over and call into without overridesMu held — Close and
// Initialize both call adapter methods that must not run under the lock.
func (c *Client) overridesSnapshot() []ProviderAdapter {
	c.overridesMu.RLock()
	defer c.overridesMu.RUnlock()
	out := make([]ProviderAdapter, 0, len(c.overrides))
	for _, a := range c.overrides {
		out = append(out, a)
	}
	return out
}

// providerStampStream wraps a Stream and rewrites the provider field on all
// StreamEventResponse and StreamEventError events so that downstream consumers
// see the instance name (req.Provider) rather than the adapter's own name.
type providerStampStream struct {
	inner    Stream
	provider string
	out      chan StreamEvent
	once     sync.Once
	done     chan struct{}
	closing  chan struct{}
}

func newProviderStampStream(inner Stream, provider string) *providerStampStream {
	s := &providerStampStream{
		inner:    inner,
		provider: provider,
		out:      make(chan StreamEvent, 128),
		done:     make(chan struct{}),
		closing:  make(chan struct{}),
	}
	go s.pump()
	return s
}

// pump forwards events from the inner stream, stamping the provider name onto
// FINISH responses and errors. It exits when the inner
// stream closes its events channel OR when Close signals s.closing. The closing
// signal is selected on both the inner receive and the out send so that Close
// returns promptly even if the consumer has stopped draining and the inner
// stream never closes its channel — otherwise this goroutine would leak.
func (s *providerStampStream) pump() {
	defer close(s.done)
	defer close(s.out)
	for {
		select {
		case <-s.closing:
			return
		case ev, ok := <-s.inner.Events():
			if !ok {
				return
			}
			if ev.Type == StreamEventError && ev.Err != nil {
				ev.Err = RewriteErrorProvider(ev.Err, s.provider)
			}
			if ev.Type == StreamEventFinish && ev.Response != nil {
				ev.Response.Provider = s.provider
			}
			select {
			case s.out <- ev:
			case <-s.closing:
				return
			}
		}
	}
}

func (s *providerStampStream) Events() <-chan StreamEvent { return s.out }

func (s *providerStampStream) Close() error {
	var err error
	s.once.Do(func() {
		close(s.closing)
		err = s.inner.Close()
	})
	<-s.done
	return err
}

func normalizeProviderName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}
