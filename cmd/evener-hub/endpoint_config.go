package hub

import (
	"fmt"
	"os"

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
		return EndpointConfig{}, fmt.Errorf("%s listener: %w", source, err)
	}
	result := EndpointConfig{ListenAddr: listen, ListenSource: source}
	if publicURL != "" {
		origin, err := hubendpoint.ParseOrigin(publicURL)
		if err != nil {
			return EndpointConfig{}, fmt.Errorf("public_url: %w", err)
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
	cfg.Addr = endpoint.ListenAddr.String()
	return cfg, endpoint, nil
}
