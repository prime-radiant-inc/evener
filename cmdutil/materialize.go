package cmdutil

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"primeradiant.com/serf/internal/providerconfig"
	"primeradiant.com/serf/llm"
)

// materializeProvidersConfig detects providers from the environment and writes
// a descriptors-only providers.toml to path. It never writes api_key values.
// The parent directory is created if needed. The write is atomic (temp file +
// rename). The caller is responsible for deciding whether to call this (e.g.
// only when the file is absent).
func materializeProvidersConfig(path string, opts ...llm.EnvOption) (providerconfig.Config, error) {
	client, err := llm.NewFromEnv(opts...)
	if err != nil {
		return providerconfig.Config{}, fmt.Errorf("materialize providers config: detect providers: %w", err)
	}

	names := client.ProviderNames()
	def := client.DefaultProvider()

	getBaseURL := func(typ string) string {
		if typ == "ollama" {
			// Match ollama adapter's resolution order:
			// OLLAMA_BASE_URL takes precedence over OLLAMA_HOST.
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

	cfg := providerconfig.Seed(names, def, getBaseURL)

	data, err := providerconfig.Marshal(cfg)
	if err != nil {
		return providerconfig.Config{}, fmt.Errorf("materialize providers config: marshal: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return providerconfig.Config{}, fmt.Errorf("materialize providers config: mkdir: %w", err)
	}

	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return providerconfig.Config{}, fmt.Errorf("materialize providers config: create temp file: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return providerconfig.Config{}, fmt.Errorf("materialize providers config: write temp file: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return providerconfig.Config{}, fmt.Errorf("materialize providers config: sync: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return providerconfig.Config{}, fmt.Errorf("materialize providers config: close: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return providerconfig.Config{}, fmt.Errorf("materialize providers config: rename: %w", err)
	}

	return cfg, nil
}
