package cmdutil

import (
	"fmt"
	"path/filepath"
	"strings"

	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm"
	_ "primeradiant.com/evener/llm/providers/all" // register every protocol and authenticator
	"primeradiant.com/evener/llm/providers/tokenauth"
	"primeradiant.com/evener/llm/registry"
)

// ProvidersConfigPath is the user layer's location under the tri-state
// rule (spec §10): unset → <config-root>/providers.toml; present and empty
// → no user layer at all; set → that path.
func ProvidersConfigPath() (path string, noUserLayer bool) {
	v, ok := envvars.EVENERProvidersConfig.LookupEnv()
	switch {
	case ok && strings.TrimSpace(v) == "":
		return "", true
	case ok:
		return v, false
	}
	return filepath.Join(DefaultConfigRoot(), "providers.toml"), false
}

// CredentialsPath is credentials.toml's location: EVENER_CREDENTIALS_CONFIG
// when set, else the sibling of the providers path, else
// <config-root>/credentials.toml (spec §10).
func CredentialsPath() string {
	if v, ok := envvars.EVENERCredentialsConfig.LookupEnv(); ok && strings.TrimSpace(v) != "" {
		return v
	}
	if path, none := ProvidersConfigPath(); !none {
		return filepath.Join(filepath.Dir(path), "credentials.toml")
	}
	return filepath.Join(DefaultConfigRoot(), "credentials.toml")
}

// StoreCredentialSource exposes credentials.toml's file layer to the
// registry: entries are looked up by instance name only (spec §10).
type StoreCredentialSource struct{ Store *credentials.Store }

// Lookup implements registry.CredentialSource: the store is credentials.toml
// and nothing else, so a hit is a file entry keyed by instance name.
// Environment lookups are the registry's own job (see
// registry.CredentialSource) and never reach here.
func (s StoreCredentialSource) Lookup(name string) (string, bool) {
	if s.Store == nil {
		return "", false
	}
	return s.Store.Get(name)
}

// LoadRegistry loads the registry the way every binary does: the
// credentials store from CredentialsPath, the state root from
// DefaultStateRoot, the user layer per the tri-state, then the caller's
// options. An old-schema providers.toml comes back as registry.ErrOldSchema
// (wrapped); the CLI exits with it and the hub degrades (spec §10, §14.1).
func LoadRegistry(opts ...registry.Option) (*registry.Registry, *credentials.Store, error) {
	store, err := credentials.LoadStore(CredentialsPath())
	if err != nil {
		return nil, nil, fmt.Errorf("credentials: %w", err)
	}
	all := append([]registry.Option{registry.WithCredentials(StoreCredentialSource{Store: store}), registry.WithStateRoot(DefaultStateRoot())}, opts...)
	r, err := registry.Load(all...)
	if err != nil {
		return nil, store, err
	}
	return r, store, nil
}

// NewRegistryClient builds the client every binary uses and wires the two
// process-wide seams from one state root and one build version (spec §9.5):
// the Codex authenticator reads OAuth records under the registry's state
// root and announces the build in its User-Agent.
func NewRegistryClient(r *registry.Registry, stateDir string) *llm.Client {
	tokenauth.DefaultCodex.StateDir = r.StateRoot()
	tokenauth.ClientVersion = buildinfo.Version()
	return llm.NewClient(llm.WithRegistry(r), llm.WithClientStateDir(stateDir))
}
