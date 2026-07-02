package cmdutil

import (
	"fmt"
	"path/filepath"
	"strings"

	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/internal/credentials"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
)

// LoadProviderConfig resolves the providers config using the same path and
// credential injection rules as LoadClient, but does not construct adapters.
//
// Path resolution: if SERF_PROVIDERS_CONFIG is set, that path is used;
// otherwise filepath.Join(DefaultStateRoot(), "providers.toml").
//
// Behavior:
//   - providers.toml present and valid → loaded as-is.
//   - providers.toml absent → the config is seeded in memory from the
//     environment (descriptors only); nothing is written to disk. Persisting the
//     file is the hub's responsibility (MaterializeProvidersConfig on startup).
//   - providers.toml corrupt/invalid → returns error.
//
// After loading or seeding the descriptors config, credentials are resolved from
// credentials.toml in the same directory (missing file = empty store, not an
// error) and injected into the in-memory config only — never written to disk.
//
// Always returns (cfg, true, nil) on success.
func LoadProviderConfig(opts ...llm.EnvOption) (providercfg.Config, bool, error) {
	path := envvars.SERFProvidersConfig.Getenv()
	if path == "" {
		path = filepath.Join(DefaultStateRoot(), "providers.toml")
	}

	cfg, exists, err := providercfg.LoadFile(path)
	if err != nil {
		return providercfg.Config{}, false, fmt.Errorf("providers config: %w", err)
	}

	if !exists {
		// Absent file: seed the config in memory from the environment. We do
		// NOT write here — persisting providers.toml is the hub's job
		// (MaterializeProvidersConfig on startup), so a plain client build never
		// has a write side effect.
		cfg, err = seedConfigFromEnv(opts...)
		if err != nil {
			return providercfg.Config{}, false, fmt.Errorf("seed providers config: %w", err)
		}
	}

	store, err := credentials.LoadStore(filepath.Join(filepath.Dir(path), "credentials.toml"))
	if err != nil {
		return providercfg.Config{}, false, fmt.Errorf("credentials store: %w", err)
	}

	for i := range cfg.Instances {
		if cfg.Instances[i].APIKey != "" || hasAuthorizationHeader(cfg.Instances[i].Headers) {
			// A configured Authorization header IS the instance's
			// authentication: injecting a type-level fallback key would make
			// the adapter's bearer clobber that header on every request,
			// sending an unrelated secret to the gateway.
			continue
		}
		if key, _ := store.ResolveKey(cfg.Instances[i].Name, string(cfg.Instances[i].Type)); key != "" {
			cfg.Instances[i].APIKey = key
		}
	}

	return cfg, true, nil
}

// hasAuthorizationHeader reports whether the instance's configured headers
// carry an Authorization header (case-insensitive, per HTTP semantics).
func hasAuthorizationHeader(headers map[string]string) bool {
	for k := range headers {
		if strings.EqualFold(k, "Authorization") {
			return true
		}
	}
	return false
}

// LoadClient constructs an LLM client that is always config-driven.
// Always returns (client, cfg, true, nil) on success.
func LoadClient(opts ...llm.EnvOption) (*llm.Client, providercfg.Config, bool, error) {
	cfg, hasConfig, err := LoadProviderConfig(opts...)
	if err != nil {
		return nil, providercfg.Config{}, false, err
	}
	client, _, err := llm.NewFromAvailableProviders(cfg, opts...)
	if err != nil {
		return nil, providercfg.Config{}, false, fmt.Errorf("LLM client from config: %w", err)
	}
	return client, cfg, hasConfig, nil
}

// BuildResolveProfile returns the SessionConfig.ResolveProfile closure.
// Instance names are always resolved via ResolveProfileWithLiveWindow (config
// is always present after LoadClient), so cross-provider switches via
// Session.SetModel pick up the provider's live context window for openai-compat
// providers, falling back to the catalog window when the lookup is unavailable.
// The hasConfig parameter is retained for call-site compatibility and is
// ignored.
func BuildResolveProfile(cfg providercfg.Config, hasConfig bool) func(ref string) (*provider.Profile, error) {
	return func(ref string) (*provider.Profile, error) {
		return ResolveProfileWithLiveWindow(cfg, ref)
	}
}
