package hub

import (
	"fmt"
	"path/filepath"
	"strings"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/envvars"
)

// childProviderConfig keeps a retained user layer available for exactly the
// process that uses it. The hub owns cleanup until a daemon starts; afterward
// its sole process waiter owns cleanup, even if rendezvous fails.
type childProviderConfig struct {
	path        string
	noUserLayer bool
	sourcePath  string
	dir         string
	childOwned  bool
}

func prepareChildProviderConfig(path string, noUserLayer bool, reg *hubcore.ProviderRegistry, projectEnv map[string]string) (*childProviderConfig, error) {
	config := &childProviderConfig{path: path, noUserLayer: noUserLayer, sourcePath: path}
	_, overridden := projectEnv[envvars.EVENERProvidersConfig.Name]
	if noUserLayer || overridden || reg == nil {
		return config, nil
	}
	source, raw, retained := reg.RetainedUserConfig()
	if !retained {
		return config, nil
	}
	if raw == nil {
		config.noUserLayer = true
		return config, nil
	}
	// A detached child can have a different working directory from the hub.
	source, err := filepath.Abs(source)
	if err != nil {
		return nil, fmt.Errorf("locate retained provider configuration: %w", err)
	}
	// Keep the snapshot under the original config parent so its existing
	// directory masks also cover any credential-bearing bytes in the copy.
	dir, err := spawnMkdirTemp(filepath.Dir(source), "retained-providers-")
	if err != nil {
		return nil, fmt.Errorf("create retained provider configuration: %w", err)
	}
	config.sourcePath = source
	config.dir = dir
	config.path = filepath.Join(dir, "providers.toml")
	if err := spawnWriteFile(config.path, raw, 0o600); err != nil {
		config.cleanup()
		return nil, fmt.Errorf("write retained provider configuration: %w", err)
	}
	return config, nil
}

func (c *childProviderConfig) cleanup() {
	if c != nil && c.dir != "" {
		_ = spawnRemoveAll(c.dir)
	}
}

func (c *childProviderConfig) cleanupUnowned() {
	if c != nil && !c.childOwned {
		c.cleanup()
	}
}

// credentialsPath preserves the original sibling fallback when the retained
// provider file moves into its private directory. Explicit hub/project values
// and a nonempty inherited override retain their existing precedence.
func (c *childProviderConfig) credentialsPath(configured string, parentEnv []string) string {
	if c.dir == "" || configured != "" {
		return configured
	}
	for _, entry := range parentEnv {
		if value, ok := strings.CutPrefix(entry, envvars.EVENERCredentialsConfig.Name+"="); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return filepath.Join(filepath.Dir(c.sourcePath), "credentials.toml")
}

// transferToChild is called only after Start succeeds. The returned cleanup
// belongs to the sole waiter and must run after that exact child is reaped.
func (c *childProviderConfig) transferToChild() func() {
	if c == nil {
		return func() {}
	}
	c.childOwned = true
	return c.cleanup
}
