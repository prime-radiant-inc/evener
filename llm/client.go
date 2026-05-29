package llm

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

type ProviderAdapter interface {
	Name() string
	Complete(ctx context.Context, req Request) (Response, error)
	Stream(ctx context.Context, req Request) (Stream, error)
}

type Client struct {
	providers       map[string]ProviderAdapter
	defaultProvider string
	middleware      []Middleware
	nameToTag       map[string]string
}

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

func (c *Client) Complete(ctx context.Context, req Request) (Response, error) {
	if err := req.Validate(); err != nil {
		return Response{}, err
	}
	// Default to a bounded network profile. Adapters use request context deadlines and
	// connect timeouts, so leaving this nil can hang indefinitely on stalled networks.
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
		return Response{}, &ConfigurationError{Message: fmt.Sprintf("unknown provider: %s", prov)}
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

func (c *Client) Stream(ctx context.Context, req Request) (Stream, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	// Default to a bounded network profile. For streaming requests this primarily
	// enforces connect + stream read timeouts.
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
		return nil, &ConfigurationError{Message: fmt.Sprintf("unknown provider: %s", prov)}
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

// Use appends middleware to the client. Middleware is applied in registration order
// for the request phase and in reverse order for the response/event phases.
func (c *Client) Use(mw ...Middleware) {
	if c == nil {
		return
	}
	c.middleware = append(c.middleware, mw...)
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

// ListModels returns available models from the named provider. The adapter
// must implement the ModelLister interface.
func (c *Client) ListModels(ctx context.Context, provider string) ([]ModelInfo, error) {
	if c == nil {
		return nil, &ConfigurationError{Message: "client is nil"}
	}
	provider = normalizeProviderName(provider)
	a, ok := c.providers[provider]
	if !ok {
		return nil, &ConfigurationError{Message: fmt.Sprintf("unknown provider: %s", provider)}
	}
	lister, ok := a.(ModelLister)
	if !ok {
		return nil, &ConfigurationError{Message: fmt.Sprintf("provider %s does not support listing models", provider)}
	}
	return lister.ListModels(ctx)
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
}

func newProviderStampStream(inner Stream, provider, behaviorTag string) *providerStampStream {
	s := &providerStampStream{
		inner:       inner,
		provider:    provider,
		behaviorTag: behaviorTag,
		out:         make(chan StreamEvent, 128),
		done:        make(chan struct{}),
	}
	go s.pump()
	return s
}

func (s *providerStampStream) pump() {
	defer close(s.done)
	defer close(s.out)
	for ev := range s.inner.Events() {
		if ev.Type == StreamEventError && ev.Err != nil {
			ev.Err = RewriteErrorProvider(ev.Err, s.provider)
			ev.Err = StampErrorBehaviorTag(ev.Err, s.behaviorTag)
		}
		if ev.Type == StreamEventFinish && ev.Response != nil {
			ev.Response.Provider = s.provider
		}
		s.out <- ev
	}
}

func (s *providerStampStream) Events() <-chan StreamEvent { return s.out }

func (s *providerStampStream) Close() error {
	var err error
	s.once.Do(func() { err = s.inner.Close() })
	<-s.done
	return err
}

func normalizeProviderName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	switch name {
	case "gemini":
		// Serf uses "google" as the canonical provider key for Gemini.
		return "google"
	default:
		return name
	}
}
