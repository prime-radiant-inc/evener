package llm

import (
	"context"
	"fmt"
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
}

func NewClient() *Client {
	return &Client{providers: map[string]ProviderAdapter{}}
}

func (c *Client) Register(adapter ProviderAdapter) {
	if c.providers == nil {
		c.providers = map[string]ProviderAdapter{}
	}
	c.providers[adapter.Name()] = adapter
	if c.defaultProvider == "" {
		c.defaultProvider = adapter.Name()
	}
}

func (c *Client) SetDefaultProvider(name string) {
	c.defaultProvider = name
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

	base := func(ctx context.Context, req Request) (Response, error) {
		return adapter.Complete(ctx, req)
	}
	handler := applyMiddlewareComplete(base, c.middleware)
	return handler(ctx, req)
}

func (c *Client) Stream(ctx context.Context, req Request) (Stream, error) {
	if err := req.Validate(); err != nil {
		return nil, err
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

	base := func(ctx context.Context, req Request) (Stream, error) {
		return adapter.Stream(ctx, req)
	}
	handler := applyMiddlewareStream(base, c.middleware)
	return handler(ctx, req)
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

func normalizeProviderName(name string) string {
	switch name {
	case "gemini":
		// Serf uses "google" as the canonical provider key for Gemini.
		return "google"
	default:
		return name
	}
}
