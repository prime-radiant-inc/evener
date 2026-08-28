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
			req, err = shapeAndApplyTokenBudget(req, t.res)
			if err != nil {
				return Response{}, err
			}
		}
		if t.override != nil {
			return t.override.Complete(ctx, req)
		}
		return t.protocol.Complete(c.withHasher(ctx), req, t.res)
	}
	handler := applyMiddlewareComplete(base, c.middlewareSnapshot())
	resp, err := handler(ctx, req)
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
			req, err = shapeAndApplyTokenBudget(req, t.res)
			if err != nil {
				return nil, err
			}
		}
		if t.override != nil {
			return t.override.Stream(ctx, req)
		}
		return t.protocol.Stream(c.withHasher(ctx), req, t.res)
	}
	handler := applyMiddlewareStream(base, c.middlewareSnapshot())
	st, err := handler(ctx, req)
	if err != nil {
		return nil, RewriteErrorProvider(err, t.name)
	}
	return newProviderStampStream(st, t.name), nil
}

func shapeAndApplyTokenBudget(req Request, res registry.Resolved) (Request, error) {
	req = ShapeRequest(req, res)
	budgeted, _, err := ApplyTokenBudget(req, res)
	return budgeted, err
}

// ModelListing is what Models returns: the instance's visible rows, after a
// live listing from its transport was applied to the registry.
type ModelListing struct {
	// Live is true when a live listing was fetched; false means registry-only
	// (spec §8.1: an unsupported models endpoint is not a failure). A client
	// with no registry of its own reports Live over snapshot rows: it
	// fetches the listing but does not write it into the registry it shares
	// with every other such client, so the rows below are the snapshot's.
	Live bool
	// Models are the visible rows — hidden rows and rows whose live layer
	// says Tools = false are dropped (spec §5) — sorted by model id.
	Models []registry.Resolved
}

// LiveModelLister is the optional override interface for adapters that serve
// a live model listing. An override that does not implement it cannot list
// models under its instance name, even when the registry knows that name.
type LiveModelLister interface {
	LiveModels(ctx context.Context) ([]registry.Model, error)
}

// Models lists an instance's models. An override lists through its own
// LiveModels seam and its rows are returned as they came; otherwise the
// protocol's listing is applied to the registry so later Resolve calls see
// it, and every id the registry then knows for the instance is resolved and
// filtered by the §5 visibility rule.
func (c *Client) Models(ctx context.Context, instance string) (ModelListing, error) {
	instance = normalizeProviderName(instance)
	r := c.Registry()
	c.overridesMu.RLock()
	override := c.overrides[instance]
	c.overridesMu.RUnlock()
	if override != nil {
		// An override owns its instance name: its listing seam is the only
		// way it can list, so no registry-only fallback stands in for it.
		// Its rows stay out of the registry — a client without WithRegistry
		// shares EmbeddedRegistry with every other client in the process,
		// and only an instance's own transport may speak for it.
		lister, ok := override.(LiveModelLister)
		if !ok {
			return ModelListing{}, &ConfigurationError{Message: fmt.Sprintf("provider %s does not support listing models", instance)}
		}
		opCtx, op := c.beginProviderOperation(ctx)
		rows, err := lister.LiveModels(opCtx)
		op.settle(opCtx, err)
		if err != nil {
			return ModelListing{}, RewriteErrorProvider(err, instance)
		}
		return ModelListing{Live: true, Models: standaloneRows(instance, rows)}, nil
	}
	res, err := r.ResolveInstance(instance)
	if err != nil {
		return ModelListing{}, &ConfigurationError{Message: err.Error()}
	}
	p, ok := ProtocolFor(res.Protocol)
	if !ok {
		return ModelListing{}, &ConfigurationError{Message: fmt.Sprintf("%s: protocol %q is not registered", instance, res.Protocol)}
	}
	opCtx, op := c.beginProviderOperation(ctx)
	rows, err := p.ListModels(opCtx, res)
	op.settle(opCtx, err)
	live := false
	switch {
	case errors.Is(err, ErrModelListingUnsupported):
	case err != nil:
		return ModelListing{}, RewriteErrorProvider(err, instance)
	default:
		live = true
		if c.hasRegistry {
			// Only a client that owns its registry records what an
			// instance's transport said. A client without WithRegistry
			// resolves against the process-wide EmbeddedRegistry, which it
			// shares with every other such client (spec §5.1, §8.1).
			r.ApplyLive(instance, rows)
		}
	}
	ids, err := r.ModelIDs(instance)
	if err != nil {
		return ModelListing{}, &ConfigurationError{Message: err.Error()}
	}
	out := make([]registry.Resolved, 0, len(ids))
	for _, id := range ids {
		row, err := r.Resolve(instance + "/" + id)
		if err != nil || row.Model.Hidden || liveSaysNoTools(row) {
			continue
		}
		out = append(out, row)
	}
	return ModelListing{Live: live, Models: out}, nil
}

// liveSaysNoTools reports the §5 visibility rule: a row is hidden when the
// live layer is the one that set Tools = false. A catalog row that declares
// no tool support stays listed — the registry, not the provider's current
// listing, is what said so.
func liveSaysNoTools(row registry.Resolved) bool {
	return row.Caps.Tools != nil && !*row.Caps.Tools && row.Provenance["Tools"] == registry.LayerLive
}

// standaloneRows turns a listing that has no registry record behind it into
// Resolved rows, sorted by model id. An override's listing is its own live
// layer, so the §5 visibility rule applies to it directly: a hidden row and a
// row that says Tools = false are both dropped.
func standaloneRows(instance string, rows []registry.Model) []registry.Resolved {
	out := make([]registry.Resolved, 0, len(rows))
	for _, m := range rows {
		if m.Hidden || (m.Caps.Tools != nil && !*m.Caps.Tools) {
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
	if c == nil {
		return nil
	}
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
