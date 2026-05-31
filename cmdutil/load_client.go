package cmdutil

import (
	"fmt"
	"os"
	"path/filepath"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/internal/credentials"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
)

// LoadClient constructs an LLM client that is always config-driven.
//
// Path resolution: if SERF_PROVIDERS_CONFIG is set, that path is used;
// otherwise filepath.Join(providercfg.DefaultStateRoot(), "providers.toml").
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
// Always returns (client, cfg, true, nil) on success.
func LoadClient(opts ...llm.EnvOption) (*llm.Client, providercfg.Config, bool, error) {
	path := os.Getenv("SERF_PROVIDERS_CONFIG")
	if path == "" {
		path = filepath.Join(providercfg.DefaultStateRoot(), "providers.toml")
	}

	cfg, exists, err := providercfg.LoadFile(path)
	if err != nil {
		return nil, providercfg.Config{}, false, fmt.Errorf("providers config: %w", err)
	}

	if !exists {
		// Absent file: seed the config in memory from the environment. We do
		// NOT write here — persisting providers.toml is the hub's job
		// (MaterializeProvidersConfig on startup), so a plain client build never
		// has a write side effect.
		cfg, err = seedConfigFromEnv(opts...)
		if err != nil {
			return nil, providercfg.Config{}, false, fmt.Errorf("seed providers config: %w", err)
		}
	}

	store, err := credentials.LoadStore(filepath.Join(filepath.Dir(path), "credentials.toml"))
	if err != nil {
		return nil, providercfg.Config{}, false, fmt.Errorf("credentials store: %w", err)
	}

	for i := range cfg.Instances {
		if cfg.Instances[i].APIKey == "" {
			if key, _ := store.ResolveKey(cfg.Instances[i].Name, string(cfg.Instances[i].Type)); key != "" {
				cfg.Instances[i].APIKey = key
			}
		}
	}

	client, err := llm.NewFromProviders(cfg, opts...)
	if err != nil {
		return nil, providercfg.Config{}, false, fmt.Errorf("LLM client from config: %w", err)
	}
	return client, cfg, true, nil
}

// BuildResolveProfile returns the SessionConfig.ResolveProfile closure.
// Instance names are always resolved via ResolveProfileFromConfig (config is
// always present after LoadClient). The hasConfig parameter is retained for
// call-site compatibility and is ignored.
func BuildResolveProfile(cfg providercfg.Config, hasConfig bool) func(ref string) (agent.ProviderProfile, error) {
	return func(ref string) (agent.ProviderProfile, error) {
		return agent.ResolveProfileFromConfig(cfg, ref)
	}
}
