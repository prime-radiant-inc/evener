package cmdutil

import (
	"fmt"
	"os"
	"path/filepath"
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

	data, err := providerconfig.Marshal(cfg)
	if err != nil {
		return providerconfig.Config{}, fmt.Errorf("materialize providers config: marshal: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return providerconfig.Config{}, fmt.Errorf("materialize providers config: mkdir: %w", err)
	}

	// Atomic write via a uniquely-named temp file + rename so concurrent
	// writers never clobber a shared temp path.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".providers-*.toml.tmp")
	if err != nil {
		return providerconfig.Config{}, fmt.Errorf("materialize providers config: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { tmp.Close(); os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return providerconfig.Config{}, fmt.Errorf("materialize providers config: write temp file: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		cleanup()
		return providerconfig.Config{}, fmt.Errorf("materialize providers config: chmod: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return providerconfig.Config{}, fmt.Errorf("materialize providers config: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return providerconfig.Config{}, fmt.Errorf("materialize providers config: close: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return providerconfig.Config{}, fmt.Errorf("materialize providers config: rename: %w", err)
	}

	return cfg, nil
}
