package cmdutil

import (
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// LoadClient loads the registry and builds the session client. stateDir is
// the session's state directory (the continuation secret lives there); the
// registry's own state root is DefaultStateRoot.
func LoadClient(stateDir string) (*llm.Client, error) {
	r, _, err := LoadRegistry()
	if err != nil {
		return nil, err
	}
	return NewRegistryClient(r, stateDir), nil
}

// LoadClientAt is LoadClient for an explicit providers.toml path (the hub's
// credential probes inspect the same file the spawn path will).
func LoadClientAt(path, stateDir string) (*llm.Client, error) {
	r, _, err := LoadRegistry(registry.WithConfigPath(path))
	if err != nil {
		return nil, err
	}
	return NewRegistryClient(r, stateDir), nil
}

// ResolveProfile resolves an instance/model reference on the client's registry.
func ResolveProfile(client *llm.Client, ref string) (*provider.Profile, error) {
	return provider.Resolve(client.Registry(), ref)
}

// BuildResolveProfile is SessionConfig.ResolveProfile: cross-instance
// switches resolve on the same registry the session's client dispatches on.
func BuildResolveProfile(client *llm.Client) func(ref string) (*provider.Profile, error) {
	return func(ref string) (*provider.Profile, error) { return ResolveProfile(client, ref) }
}
