package cmdutil

import (
	"fmt"
	"os"
	"path/filepath"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/internal/credentials"
	"primeradiant.com/serf/internal/providerconfig"
	"primeradiant.com/serf/llm"
)

// LoadClient constructs an LLM client that is always config-driven.
//
// Path resolution: if SERF_PROVIDERS_CONFIG is set, that path is used;
// otherwise filepath.Join(providerconfig.DefaultStateRoot(), "providers.toml").
//
// Behavior:
//   - providers.toml absent → materialized from environment (descriptors only, no secrets written).
//   - providers.toml present and valid → loaded as-is.
//   - providers.toml corrupt/invalid → returns error.
//
// After loading or materializing the descriptors config, credentials are
// resolved from the credentials.toml in the same directory (missing file = empty
// store, not an error) and injected into the in-memory config only. The file on
// disk is never written with secrets.
//
// Always returns (client, cfg, true, nil) on success.
func LoadClient(opts ...llm.EnvOption) (*llm.Client, providerconfig.Config, bool, error) {
	path := os.Getenv("SERF_PROVIDERS_CONFIG")
	if path == "" {
		path = filepath.Join(providerconfig.DefaultStateRoot(), "providers.toml")
	}

	cfg, exists, err := providerconfig.LoadFile(path)
	if err != nil {
		return nil, providerconfig.Config{}, false, fmt.Errorf("providers config: %w", err)
	}

	if !exists {
		cfg, err = materializeProvidersConfig(path, opts...)
		if err != nil {
			return nil, providerconfig.Config{}, false, fmt.Errorf("materialize providers config: %w", err)
		}
	}

	store, err := credentials.LoadStore(filepath.Join(filepath.Dir(path), "credentials.toml"))
	if err != nil {
		return nil, providerconfig.Config{}, false, fmt.Errorf("credentials store: %w", err)
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
		return nil, providerconfig.Config{}, false, fmt.Errorf("LLM client from config: %w", err)
	}
	return client, cfg, true, nil
}

// BuildResolveProfile returns the SessionConfig.ResolveProfile closure.
// When hasConfig is true, instance names are resolved via ResolveProfileFromConfig;
// otherwise the existing SelectProfile (env-based) path is used.
func BuildResolveProfile(cfg providerconfig.Config, hasConfig bool) func(ref string) (agent.ProviderProfile, error) {
	return func(ref string) (agent.ProviderProfile, error) {
		if hasConfig {
			return agent.ResolveProfileFromConfig(cfg, ref)
		}
		mr, err := ParseModelRef(ref)
		if err != nil {
			return nil, err
		}
		return SelectProfile(mr.Provider, mr.Model, "")
	}
}
