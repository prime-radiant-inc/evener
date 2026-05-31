package llm

import (
	"fmt"
	"os"
	"sync"
)

// EnvConfig holds configuration derived from environment variables and options
// that is passed to env adapter factories.
type EnvConfig struct {
	StateDir  string
	StateHome string
}

// EnvOption configures an EnvConfig.
type EnvOption func(*EnvConfig)

// WithStateDir returns an EnvOption that sets the StateDir on the EnvConfig.
func WithStateDir(stateDir string) EnvOption {
	return func(cfg *EnvConfig) {
		cfg.StateDir = stateDir
	}
}

// EnvAdapterFactory constructs a ProviderAdapter from an EnvConfig. It reports
// whether the adapter was configured and returns any error encountered.
type EnvAdapterFactory func(cfg EnvConfig) (adapter ProviderAdapter, configured bool, err error)

var (
	envFactoriesMu sync.Mutex
	envFactories   []EnvAdapterFactory
)

// RegisterEnvAdapterFactory registers a factory that can construct a ProviderAdapter
// from environment variables. Provider packages should call this from init().
func RegisterEnvAdapterFactory(factory EnvAdapterFactory) {
	if factory == nil {
		return
	}
	envFactoriesMu.Lock()
	envFactories = append(envFactories, factory)
	envFactoriesMu.Unlock()
}

// NewFromEnv constructs a Client by registering any provider adapters that can be
// constructed from environment variables. The first successfully registered provider
// becomes the default provider.
//
// Note: providers are discovered via factories registered with RegisterEnvAdapterFactory.
func NewFromEnv(opts ...EnvOption) (*Client, error) {
	envFactoriesMu.Lock()
	factories := append([]EnvAdapterFactory{}, envFactories...)
	envFactoriesMu.Unlock()

	cfg := EnvConfig{
		StateDir:  os.Getenv("SERF_STATE_DIR"),
		StateHome: os.Getenv("XDG_STATE_HOME"),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	c := NewClient()
	for _, f := range factories {
		a, ok, err := f(cfg)
		if err != nil {
			return nil, err
		}
		if ok && a != nil {
			c.Register(a)
		}
	}
	if len(c.ProviderNames()) == 0 {
		return nil, fmt.Errorf("no LLM providers configured via environment variables")
	}
	return c, nil
}

var (
	defaultClientMu   sync.Mutex
	defaultClientInit bool
	defaultClient     *Client
	defaultClientErr  error
)

// SetDefaultClient sets the module-level default client and disables lazy env initialization.
func SetDefaultClient(c *Client) {
	defaultClientMu.Lock()
	defer defaultClientMu.Unlock()
	defaultClient = c
	defaultClientErr = nil
	defaultClientInit = true
}

// DefaultClient returns the module-level default client, lazily constructing it
// from environment variables on first use.
func DefaultClient() (*Client, error) {
	defaultClientMu.Lock()
	if defaultClientInit {
		c, err := defaultClient, defaultClientErr
		defaultClientMu.Unlock()
		return c, err
	}
	defaultClientMu.Unlock()

	c, err := NewFromEnv()

	defaultClientMu.Lock()
	defaultClient = c
	defaultClientErr = err
	defaultClientInit = true
	defaultClientMu.Unlock()
	return c, err
}
