package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"primeradiant.com/serf/identifier"
)

// ProviderAdapter is the interface a single LLM provider backend implements.
// It reports its name and serves completion requests in both buffered and
// streaming forms.
type ProviderAdapter interface {
	Name() string
	Complete(ctx context.Context, req Request) (Response, error)
	Stream(ctx context.Context, req Request) (Stream, error)
}

// Client routes LLM requests to registered provider adapters, applies
// middleware, and stamps the resolved provider name and behavior tag onto
// responses and errors.
type Client struct {
	providers       map[string]ProviderAdapter
	defaultProvider string
	middleware      []Middleware
	nameToTag       map[string]string
}

// NewClient returns a Client with no registered providers.
func NewClient() *Client {
	return &Client{providers: map[string]ProviderAdapter{}}
}

// SetNameToTag configures a mapping from provider instance names to behavior
// tags (e.g. {"work": "openai"}). The client stamps the behavior tag onto
// errors so classifiers can key on provider type rather than instance name.
// A nil map means no tag is stamped (behavior tag remains empty).
func (c *Client) SetNameToTag(m map[string]string) {
	c.nameToTag = m
}

// NonDefaultEligible is implemented by adapters that should never be
// auto-elected as the client's default provider. They remain reachable
// by explicit name lookup. Use for providers that are always-registered
// for ergonomic explicit selection (e.g. local Ollama with no API key)
// but where becoming the silent default in a process that didn't
// explicitly opt in would be wrong.
type NonDefaultEligible interface {
	ProviderAdapter
	NonDefaultEligible()
}

// Register adds an adapter to the client, keyed by its Name. If no default
// provider has been set yet and the adapter does not implement
// NonDefaultEligible, it becomes the default. Adapters implementing
// Initializer are initialized immediately with a background context.
func (c *Client) Register(adapter ProviderAdapter) {
	if c.providers == nil {
		c.providers = map[string]ProviderAdapter{}
	}
	c.providers[adapter.Name()] = adapter
	if c.defaultProvider == "" {
		if _, skip := adapter.(NonDefaultEligible); !skip {
			c.defaultProvider = adapter.Name()
		}
	}
	if init, ok := adapter.(Initializer); ok {
		_ = init.Initialize(context.Background())
	}
}

// SetDefaultProvider sets the provider name used when a request does not
// specify one.
func (c *Client) SetDefaultProvider(name string) {
	c.defaultProvider = name
}

// DefaultProvider returns the name of the currently-elected default
// provider, or an empty string if none has been set. The default is
// the first registered ProviderAdapter that does not implement
// NonDefaultEligible.
func (c *Client) DefaultProvider() string {
	return c.defaultProvider
}

// ProviderNames returns the names of all registered providers in unspecified
// order, or nil if none are registered.
func (c *Client) ProviderNames() []string {
	if c == nil || len(c.providers) == 0 {
		return nil
	}
	out := make([]string, 0, len(c.providers))
	for k := range c.providers {
		out = append(out, k)
	}
	return out
}

// Complete validates the request, resolves the target provider (falling back
// to the default when unset), runs the request through the middleware chain
// and the selected adapter, and returns the response. The resolved provider
// name and behavior tag are stamped onto the response and any error.
func (c *Client) Complete(ctx context.Context, req Request) (Response, error) {
	ctx = c.bindAPIAttemptSinkBeforeDispatch(ctx)
	if err := req.Validate(); err != nil {
		return Response{}, err
	}
	// Complete defaults to a bounded network profile covering connection setup and
	// the whole request/response cycle.
	if req.AdapterTimeout == nil {
		at := DefaultAdapterTimeout()
		req.AdapterTimeout = &at
	}
	prov := req.Provider
	if prov == "" {
		prov = c.defaultProvider
	}
	if prov == "" {
		return Response{}, &ConfigurationError{Message: "no provider specified and no default provider configured"}
	}
	prov = normalizeProviderName(prov)
	adapter, ok := c.providers[prov]
	if !ok {
		return Response{}, &ConfigurationError{Message: "unknown provider: " + prov}
	}
	req.Provider = prov

	tag := c.behaviorTagFor(prov)

	base := func(ctx context.Context, req Request) (Response, error) {
		return adapter.Complete(ctx, req)
	}
	handler := applyMiddlewareComplete(base, c.middleware)
	resp, err := handler(ctx, req)
	resp.Provider = prov
	err = RewriteErrorProvider(err, prov)
	err = StampErrorBehaviorTag(err, tag)
	return resp, err
}

// Stream validates the request, resolves the target provider (falling back to
// the default when unset), runs the request through the middleware chain and
// the selected adapter, and returns a Stream. The resolved provider name and
// behavior tag are stamped onto stream events and any error.
func (c *Client) Stream(ctx context.Context, req Request) (Stream, error) {
	ctx = c.bindAPIAttemptSinkBeforeDispatch(ctx)
	if err := req.Validate(); err != nil {
		return nil, err
	}
	// Stream defaults to phase-bounded connection, response-header, and per-line
	// stream-read timeouts without adding an overall stream deadline. Caller-supplied
	// context and HTTP client policies remain authoritative.
	if req.AdapterTimeout == nil {
		at := DefaultAdapterTimeout()
		req.AdapterTimeout = &at
	}
	prov := req.Provider
	if prov == "" {
		prov = c.defaultProvider
	}
	if prov == "" {
		return nil, &ConfigurationError{Message: "no provider specified and no default provider configured"}
	}
	prov = normalizeProviderName(prov)
	adapter, ok := c.providers[prov]
	if !ok {
		return nil, &ConfigurationError{Message: "unknown provider: " + prov}
	}
	req.Provider = prov

	tag := c.behaviorTagFor(prov)

	base := func(ctx context.Context, req Request) (Stream, error) {
		return adapter.Stream(ctx, req)
	}
	handler := applyMiddlewareStream(base, c.middleware)
	st, err := handler(ctx, req)
	if err != nil {
		err = RewriteErrorProvider(err, prov)
		err = StampErrorBehaviorTag(err, tag)
		return nil, err
	}
	return newProviderStampStream(st, prov, tag), nil
}

func (c *Client) PlanResponsesContinuation(ctx context.Context, req Request) (ResponsesContinuationPlan, error) {
	_ = ctx
	prov := req.Provider
	if prov == "" {
		prov = c.defaultProvider
	}
	if prov == "" {
		return ResponsesContinuationPlan{}, &ConfigurationError{Message: "no provider specified and no default provider configured"}
	}
	prov = normalizeProviderName(prov)
	adapter, ok := c.providers[prov]
	if !ok {
		return ResponsesContinuationPlan{}, &ConfigurationError{Message: "unknown provider: " + prov}
	}
	planner, ok := adapter.(ResponsesContinuationPlanner)
	if !ok {
		return ResponsesContinuationPlan{}, &ConfigurationError{Message: "provider does not support responses continuation planning: " + prov}
	}
	req.Provider = prov
	return planner.PlanResponsesContinuation(req)
}

// Use appends middleware to the client. Middleware is applied in registration order
// for the request phase and in reverse order for the response/event phases.
func (c *Client) Use(mw ...Middleware) {
	if c == nil {
		return
	}
	c.middleware = append(c.middleware, mw...)
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
	for _, middleware := range c.middleware {
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
	for _, middleware := range c.middleware {
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
		for _, middleware := range c.middleware {
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

// ToolChoiceSupporter is implemented by adapters that want to declare which
// tool choice modes they support. If not implemented, all modes are assumed supported.
type ToolChoiceSupporter interface {
	SupportsToolChoice(mode string) bool
}

// ModelLister is implemented by adapters that can list available models from
// the provider API.
type ModelLister interface {
	ListModels(ctx context.Context) ([]ModelInfo, error)
}

// ResponsesContinuationPlanner is implemented by adapters that can build a
// ResponsesContinuationPlan for a request.
type ResponsesContinuationPlanner interface {
	PlanResponsesContinuation(req Request) (ResponsesContinuationPlan, error)
}

// ModelCompatibilityValidator is implemented by adapters that enforce a
// static model-support map independent of live enumeration — e.g. the
// OpenAI Codex backend, whose ChatGPT-account model set is narrower than
// the platform API and isn't reliably distinguished by a live models list.
type ModelCompatibilityValidator interface {
	ValidateModel(model string) error
}

// Close closes all registered adapters that implement the Closer interface.
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	var firstErr error
	for _, a := range c.providers {
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
	for _, a := range c.providers {
		if init, ok := a.(Initializer); ok {
			if err := init.Initialize(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

// SupportsToolChoice checks whether the named provider supports the given tool choice mode.
// Returns true if the adapter does not implement ToolChoiceSupporter (assumed supported).
func (c *Client) SupportsToolChoice(provider, mode string) bool {
	if c == nil {
		return false
	}
	provider = normalizeProviderName(provider)
	a, ok := c.providers[provider]
	if !ok {
		return false
	}
	if tc, ok := a.(ToolChoiceSupporter); ok {
		return tc.SupportsToolChoice(mode)
	}
	return true
}

// ValidateModelCompatibility runs an adapter's static compatibility check
// for model, when the adapter implements ModelCompatibilityValidator; nil
// (no opinion) otherwise, including for unknown providers.
func (c *Client) ValidateModelCompatibility(provider, model string) error {
	if c == nil {
		return nil
	}
	provider = normalizeProviderName(provider)
	a, ok := c.providers[provider]
	if !ok {
		return nil
	}
	if v, ok := a.(ModelCompatibilityValidator); ok {
		return v.ValidateModel(model)
	}
	return nil
}

// ListModels returns available models from the named provider. The adapter
// must implement the ModelLister interface.
func (c *Client) ListModels(ctx context.Context, provider string) ([]ModelInfo, error) {
	if c == nil {
		return nil, &ConfigurationError{Message: "client is nil"}
	}
	provider = normalizeProviderName(provider)
	a, ok := c.providers[provider]
	if !ok {
		return nil, &ConfigurationError{Message: "unknown provider: " + provider}
	}
	lister, ok := a.(ModelLister)
	if !ok {
		return nil, &ConfigurationError{Message: fmt.Sprintf("provider %s does not support listing models", provider)}
	}
	ctx, operation := c.beginProviderOperation(ctx)
	models, err := lister.ListModels(ctx)
	operation.settle(ctx, err)
	return models, err
}

// behaviorTagFor returns the behavior tag for a given provider instance name.
// If no nameToTag mapping is configured, or the name is not in the map,
// an empty string is returned (no tag is stamped).
func (c *Client) behaviorTagFor(provName string) string {
	if t, ok := c.nameToTag[provName]; ok {
		return t
	}
	return ""
}

// BehaviorTagOf returns the behavior tag for the given provider instance name.
// When a nameToTag mapping is configured and the name is present, the mapped
// tag is returned. Otherwise the name itself is returned (identity fallback),
// so callers can compare against tag constants without special-casing the
// env path (where instance name == type == tag).
func (c *Client) BehaviorTagOf(name string) string {
	if t, ok := c.nameToTag[name]; ok {
		return t
	}
	return name
}

// providerStampStream wraps a Stream and rewrites the provider field on all
// StreamEventResponse and StreamEventError events so that downstream consumers
// see the instance name (req.Provider) rather than the adapter's hardcoded type.
// It also stamps the behavior tag onto errors when a nameToTag mapping exists.
type providerStampStream struct {
	inner       Stream
	provider    string
	behaviorTag string
	out         chan StreamEvent
	once        sync.Once
	done        chan struct{}
	closing     chan struct{}
}

func newProviderStampStream(inner Stream, provider, behaviorTag string) *providerStampStream {
	s := &providerStampStream{
		inner:       inner,
		provider:    provider,
		behaviorTag: behaviorTag,
		out:         make(chan StreamEvent, 128),
		done:        make(chan struct{}),
		closing:     make(chan struct{}),
	}
	go s.pump()
	return s
}

// pump forwards events from the inner stream, stamping the provider name (and
// behavior tag) onto FINISH responses and errors. It exits when the inner
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
				ev.Err = StampErrorBehaviorTag(ev.Err, s.behaviorTag)
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
