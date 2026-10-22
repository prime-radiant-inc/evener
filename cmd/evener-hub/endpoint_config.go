package hub

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/internal/hubendpoint"
)

// EndpointInput distinguishes an omitted value from one explicitly set empty.
type EndpointInput struct {
	Value   string
	Present bool
}

// EndpointConfig records the chosen listener and advertised origin, not the
// address a listener may subsequently acquire when binding port zero.
type EndpointConfig struct {
	ListenAddr   hubendpoint.ListenAddr
	ListenSource string
	PublicURL    *hubendpoint.Origin
	ConfigPath   string
	ConfigExists bool
}

// EndpointMigrationError names a setting that no longer has meaning. It never
// holds the supplied value, which could contain a credential.
type EndpointMigrationError struct {
	Retired     string
	Replacement string
}

func (e *EndpointMigrationError) Error() string {
	return fmt.Sprintf("%s is retired; use %s instead (HTTP(S) origin for a client URL)", e.Retired, e.Replacement)
}

// EndpointValidationError identifies the setting layer that supplied an invalid
// endpoint while leaving the parser's typed validation error accessible.
type EndpointValidationError struct {
	Source string
	Err    error
}

func (e *EndpointValidationError) Error() string { return e.Source + " " + e.Err.Error() }
func (e *EndpointValidationError) Unwrap() error { return e.Err }

// resolveEndpointConfig chooses by presence before parsing the winning value.
func resolveEndpointConfig(flagValue, envValue, configValue EndpointInput, publicURL string) (EndpointConfig, error) {
	selected, source := EndpointInput{Value: DefaultConfig().Addr, Present: true}, "default"
	if configValue.Present {
		selected, source = configValue, "config"
	}
	if envValue.Present {
		selected, source = envValue, "env"
	}
	if flagValue.Present {
		selected, source = flagValue, "flag"
	}
	listen, err := hubendpoint.ParseListenAddr(selected.Value)
	if err != nil {
		return EndpointConfig{}, &EndpointValidationError{Source: source, Err: fmt.Errorf("listener: %w", err)}
	}
	result := EndpointConfig{ListenAddr: listen, ListenSource: source}
	if publicURL != "" {
		origin, err := hubendpoint.ParseOrigin(publicURL)
		if err != nil {
			return EndpointConfig{}, &EndpointValidationError{Source: "config", Err: fmt.Errorf("public_url: %w", err)}
		}
		result.PublicURL = &origin
	}
	return result, nil
}

// loadResolvedConfig uses the startup read/resolve path with the real loader;
// the read-only endpoint command can reuse it. Config keeps the requested address.
func loadResolvedConfig(path string, explicit bool, flagValue EndpointInput) (Config, EndpointConfig, error) {
	cfg, err := loadConfigForCommandLine(path, explicit)
	if err != nil {
		return cfg, EndpointConfig{}, err
	}
	return resolveLoadedEndpointConfig(cfg, path, flagValue)
}

// resolveLoadedEndpointConfig also accepts an injected config loader in startup
// tests without bypassing the common endpoint selection and validation.
func resolveLoadedEndpointConfig(cfg Config, path string, flagValue EndpointInput) (Config, EndpointConfig, error) {
	if _, exists := envvars.EVENERHubAddr.LookupEnv(); exists {
		return cfg, EndpointConfig{}, &EndpointMigrationError{Retired: envvars.EVENERHubAddr.Name, Replacement: envvars.EVENERHubURL.Name}
	}
	env, envPresent := envvars.EVENERHubListenAddr.LookupEnv()
	configPresent := cfg.addrPresent || cfg.Addr != DefaultConfig().Addr
	endpoint, err := resolveEndpointConfig(flagValue, EndpointInput{env, envPresent}, EndpointInput{cfg.Addr, configPresent}, cfg.PublicURL)
	if err != nil {
		return cfg, EndpointConfig{}, err
	}
	endpoint.ConfigPath = path
	if _, err := os.Stat(path); err == nil {
		endpoint.ConfigExists = true
	}
	if cfg.HubStateRoot != "" && !filepath.IsAbs(cfg.HubStateRoot) {
		cwd, err := os.Getwd()
		if err != nil {
			return cfg, EndpointConfig{}, err
		}
		// Do not clean before filesystem lookup: link/.. follows the link's
		// physical parent, not the lexical parent of the configured spelling.
		root, err := resolveRelativeHubStateRoot(cwd + string(os.PathSeparator) + cfg.HubStateRoot)
		if err != nil {
			return cfg, EndpointConfig{}, fmt.Errorf("resolve hub_state_root %q: %w", cfg.HubStateRoot, err)
		}
		cfg.HubStateRoot = root
	}
	cfg.Addr = endpoint.ListenAddr.String()
	return cfg, endpoint, nil
}

// Resolve the existing prefix without creating anything. A fresh root can have
// missing directory components, but a dangling symlink or unresolved .. cannot
// be normalized into a different directory. Absolute configured roots bypass
// this helper and keep their existing spelling and behavior.
func resolveRelativeHubStateRoot(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil || !os.IsNotExist(err) {
		return resolved, err
	}
	if _, linkErr := os.Lstat(path); !os.IsNotExist(linkErr) {
		return "", err
	}
	trimmed := strings.TrimRight(path, string(os.PathSeparator))
	i := strings.LastIndexByte(trimmed, os.PathSeparator)
	name := trimmed[i+1:]
	if i < 0 || name == "." || name == ".." {
		return "", err
	}
	parent := trimmed[:i]
	if parent == "" {
		parent = string(os.PathSeparator)
	}
	resolved, err = resolveRelativeHubStateRoot(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, name), nil
}
