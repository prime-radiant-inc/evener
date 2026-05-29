package cmdutil

import (
	"fmt"
	"os"
	"path/filepath"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/internal/providerconfig"
	"primeradiant.com/serf/llm"
)

// LoadClient constructs an LLM client using providers.toml when available,
// or falling back to environment-variable discovery when the file is absent.
//
// Path resolution: if SERF_PROVIDERS_CONFIG is set, that path is used;
// otherwise filepath.Join(providerconfig.DefaultStateRoot(), "providers.toml").
//
//   - File exists and valid → llm.NewFromProviders, returns (client, cfg, true, nil).
//   - File absent          → llm.NewFromEnv,         returns (client, Config{}, false, nil).
//   - File exists but corrupt/invalid → returns (nil, Config{}, false, err).
func LoadClient(opts ...llm.EnvOption) (*llm.Client, providerconfig.Config, bool, error) {
	path := os.Getenv("SERF_PROVIDERS_CONFIG")
	if path == "" {
		path = filepath.Join(providerconfig.DefaultStateRoot(), "providers.toml")
	}

	cfg, exists, err := providerconfig.LoadFile(path)
	if err != nil {
		return nil, providerconfig.Config{}, false, fmt.Errorf("providers config: %w", err)
	}

	if exists {
		client, err := llm.NewFromProviders(cfg, opts...)
		if err != nil {
			return nil, providerconfig.Config{}, false, fmt.Errorf("LLM client from config: %w", err)
		}
		return client, cfg, true, nil
	}

	client, err := llm.NewFromEnv(opts...)
	if err != nil {
		return nil, providerconfig.Config{}, false, fmt.Errorf("LLM client from env: %w", err)
	}
	return client, providerconfig.Config{}, false, nil
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
