// Package credentials owns ~/.serf/credentials.toml. Provider API keys
// are stored verbatim with chmod 600; encryption-at-rest is deliberately
// not provided (see spec §5.5 non-goals).
package credentials

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/spf13/afero"
	"primeradiant.com/serf/envvars"
)

// Source describes where a provider's effective value came from.
type Source string

const (
	SourceFile   Source = "file"
	SourceEnv    Source = "env"
	SourceOAuth  Source = "oauth"
	SourceAbsent Source = "absent"
	SourceNone   Source = "none"
)

// Provider is one row in List().
type Provider struct {
	Name      string
	AuthModes []string
	Source    Source
}

// EnvVars returns the accepted environment variable names for provider.
func EnvVars(provider string) []string {
	vars := envvars.APIKeyVars(strings.ToLower(provider))
	out := make([]string, 0, len(vars))
	for _, v := range vars {
		out = append(out, v.Name)
	}
	return out
}

type fileShape struct {
	Schema    int                        `toml:"schema"`
	Providers map[string]providerSection `toml:"providers"`
}

type providerSection struct {
	APIKey string `toml:"api_key,omitempty"`
}

// Store is the in-memory + on-disk credentials.toml.
type Store struct {
	path string
	data fileShape
	fs   afero.Fs
}

// LoadStore reads path. Missing returns an empty Store. Non-missing files
// must have mode 0600 (group/world bits unset).
func LoadStore(path string) (*Store, error) {
	return loadStoreFS(afero.NewOsFs(), path)
}

// loadStoreFS is the construction seam beneath LoadStore: it builds a Store over
// an injected afero.Fs. Production passes afero.NewOsFs(), whose methods forward
// straight to the os package, so behavior is byte-identical to direct os calls.
// Tests and fuzzers inject an in-memory or sandboxed filesystem to drive
// persistence off real disk.
func loadStoreFS(fs afero.Fs, path string) (*Store, error) {
	s := &Store{path: path, data: fileShape{Schema: 1, Providers: map[string]providerSection{}}, fs: fs}
	info, err := fs.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return nil, fmt.Errorf("credentials: stat %s: %w", path, err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("credentials: %s has mode %o; require 0600", path, info.Mode().Perm())
	}
	raw, err := afero.ReadFile(fs, path)
	if err != nil {
		return nil, fmt.Errorf("credentials: read %s: %w", path, err)
	}
	if _, err := toml.Decode(string(raw), &s.data); err != nil {
		return nil, fmt.Errorf("credentials: parse %s: %w", path, err)
	}
	if s.data.Providers == nil {
		s.data.Providers = map[string]providerSection{}
	}
	return s, nil
}

// Get returns the effective API key for provider and its Source.
// Lookup order: file → env → empty.
func (s *Store) Get(provider string) (string, Source) {
	provider = strings.ToLower(provider)
	if p, ok := s.data.Providers[provider]; ok && strings.TrimSpace(p.APIKey) != "" {
		return p.APIKey, SourceFile
	}
	for _, env := range envvars.APIKeyVars(provider) {
		if v := env.Trimmed(); v != "" {
			return v, SourceEnv
		}
	}
	return "", SourceAbsent
}

// Layers returns the individual file and env sources for a provider,
// independently of priority. Used to display all active sources to the user.
func (s *Store) Layers(provider string) (hasFile bool, envVar string) {
	provider = strings.ToLower(provider)
	if p, ok := s.data.Providers[provider]; ok && strings.TrimSpace(p.APIKey) != "" {
		hasFile = true
	}
	for _, env := range envvars.APIKeyVars(provider) {
		if v := env.Trimmed(); v != "" {
			envVar = env.Name
			break
		}
	}
	return hasFile, envVar
}

// InstanceLayers returns the individual file and env sources for a provider
// instance, mirroring the resolution order of ResolveKey: the instance name's
// env vars are checked first, then the type's env vars. This ensures the
// reported EnvVar matches what ResolveKey actually resolved.
func (s *Store) InstanceLayers(name, typ string) (hasFile bool, envVar string) {
	name = strings.ToLower(name)
	typ = strings.ToLower(typ)
	if p, ok := s.data.Providers[name]; ok && strings.TrimSpace(p.APIKey) != "" {
		hasFile = true
	}
	var candidates []envvars.Var
	candidates = append(candidates, envvars.APIKeyVars(name)...)
	if typ != name {
		candidates = append(candidates, envvars.APIKeyVars(typ)...)
	}
	for _, env := range candidates {
		if v := env.Trimmed(); v != "" {
			envVar = env.Name
			break
		}
	}
	return hasFile, envVar
}

// Set writes a provider API key into the in-memory store and persists.
func (s *Store) Set(provider, value string) error {
	provider = strings.ToLower(provider)
	if s.data.Providers == nil {
		s.data.Providers = map[string]providerSection{}
	}
	s.data.Providers[provider] = providerSection{APIKey: strings.TrimSpace(value)}
	return s.save()
}

// Clear removes the provider entry. No error if absent.
func (s *Store) Clear(provider string) error {
	provider = strings.ToLower(provider)
	delete(s.data.Providers, provider)
	return s.save()
}

// List returns one Provider entry per supported provider.
func (s *Store) List() []Provider {
	out := []Provider{}
	for _, provider := range envvars.Providers() {
		name := provider.Name
		modes := append([]string(nil), provider.AuthModes...)
		_, src := s.Get(name)
		// Ollama needs no creds — report SourceNone.
		if len(modes) == 1 && modes[0] == "none" {
			src = SourceNone
		}
		out = append(out, Provider{Name: name, AuthModes: modes, Source: src})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ResolveKey returns the effective API key for a provider instance given its
// unique name and provider type. Lookup order:
//  1. File entry keyed by instance name.
//  2. Env var(s) for the instance name, then the provider type (first non-empty
//     wins) — so openai-compatible resolves OPENAI_COMPATIBLE_API_KEY before the
//     type's OPENAI_API_KEY.
//  3. Empty string with SourceAbsent.
func (s *Store) ResolveKey(name, typ string) (string, Source) {
	name = strings.ToLower(name)
	typ = strings.ToLower(typ)
	if p, ok := s.data.Providers[name]; ok && strings.TrimSpace(p.APIKey) != "" {
		return p.APIKey, SourceFile
	}
	// Env fallback: the instance name's var(s) first (covers openai-compatible,
	// whose key is OPENAI_COMPATIBLE_API_KEY though its type is openai), then the
	// type's var(s) (covers custom-named instances that fall back to their type key).
	var candidates []envvars.Var
	candidates = append(candidates, envvars.APIKeyVars(name)...)
	if typ != name {
		candidates = append(candidates, envvars.APIKeyVars(typ)...)
	}
	for _, env := range candidates {
		if v := env.Trimmed(); v != "" {
			return v, SourceEnv
		}
	}
	return "", SourceAbsent
}

// APIKeyFor implements launchconfig.CredentialResolver.
// Returns the API key value and the source label (e.g. "file", "env", "absent").
func (s *Store) APIKeyFor(provider string) (string, string) {
	v, src := s.Get(provider)
	return v, string(src)
}

func (s *Store) save() error {
	if s.path == "" {
		return nil
	}
	if err := s.fs.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("credentials: mkdir: %w", err)
	}
	tmp := s.path + ".tmp"
	f, err := s.fs.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("credentials: open: %w", err)
	}
	if err := toml.NewEncoder(f).Encode(s.data); err != nil {
		_ = f.Close()
		_ = s.fs.Remove(tmp)
		return fmt.Errorf("credentials: encode: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = s.fs.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = s.fs.Remove(tmp)
		return err
	}
	return s.fs.Rename(tmp, s.path)
}
