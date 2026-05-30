package cmdutil

import (
	"fmt"
	"os"
	"strings"

	"primeradiant.com/serf/internal/providerconfig"
	"primeradiant.com/serf/llm"
)

// seedConfigFromEnv detects providers from the environment and returns a
// descriptors-only Config. It does NOT write anything to disk. LoadClient uses
// this for the absent-file case so a read operation never has a write side
// effect; callers that want to persist the result (the hub) use
// MaterializeProvidersConfig.
func seedConfigFromEnv(opts ...llm.EnvOption) (providerconfig.Config, error) {
	client, err := llm.NewFromEnv(opts...)
	if err != nil {
		return providerconfig.Config{}, fmt.Errorf("seed providers config: detect providers: %w", err)
	}

	names := client.ProviderNames()
	def := client.DefaultProvider()

	getBaseURL := func(typ string) string {
		if typ == "ollama" {
			// Match the ollama adapter's resolution order: OLLAMA_BASE_URL
			// takes precedence over OLLAMA_HOST.
			if v := strings.TrimSpace(os.Getenv("OLLAMA_BASE_URL")); v != "" {
				return v
			}
			if v := strings.TrimSpace(os.Getenv("OLLAMA_HOST")); v != "" {
				return v
			}
			return ""
		}
		v := providerconfig.BaseURLEnvVar(typ)
		if v == "" {
			return ""
		}
		return strings.TrimSpace(os.Getenv(v))
	}

	return providerconfig.Seed(names, def, getBaseURL), nil
}

// MaterializeProvidersConfig seeds a descriptors-only config from the environment
// (see seedConfigFromEnv) and writes it to path atomically (temp file + rename),
// mode 0644, creating the parent directory if needed. It never writes api_key
// values. The hub calls this on startup to persist a single source of truth;
// LoadClient itself only seeds in memory and never writes.
func MaterializeProvidersConfig(path string, opts ...llm.EnvOption) (providerconfig.Config, error) {
	cfg, err := seedConfigFromEnv(opts...)
	if err != nil {
		return providerconfig.Config{}, err
	}

	if err := providerconfig.WriteFile(path, cfg); err != nil {
		return providerconfig.Config{}, fmt.Errorf("materialize providers config: %w", err)
	}

	return cfg, nil
}
