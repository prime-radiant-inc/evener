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

// providerEnvVars maps provider name -> env var(s) checked for fallback.
// Order matters: first non-empty wins.
var providerEnvVars = map[string][]string{
	"openai":               {"OPENAI_API_KEY"},
	"anthropic":            {"ANTHROPIC_API_KEY"},
	"google":               {"GEMINI_API_KEY", "GOOGLE_API_KEY"},
	"gemini":               {"GEMINI_API_KEY", "GOOGLE_API_KEY"},
	"minimax":              {"MINIMAX_API_KEY"},
	"openrouter":           {"OPENROUTER_API_KEY"},
	"openrouter-anthropic": {"OPENROUTER_API_KEY"},
	"kimi":                 {"KIMI_API_KEY"},
	"kimi-anthropic":       {"KIMI_CODING_API_KEY"},
	"glm":                  {"GLM_API_KEY"},
	"openai-compatible":    {"OPENAI_COMPATIBLE_API_KEY"},
	"ollama":               nil,
}

// EnvVars returns the accepted environment variable names for provider.
func EnvVars(provider string) []string {
	vars := providerEnvVars[strings.ToLower(provider)]
	return append([]string(nil), vars...)
}

// providerAuthModes lists supported auth flows per provider.
var providerAuthModes = map[string][]string{
	"openai":               {"apiKey", "oauth"},
	"anthropic":            {"apiKey"},
	"google":               {"apiKey"},
	"gemini":               {"apiKey"},
	"minimax":              {"apiKey"},
	"openrouter":           {"apiKey"},
	"openrouter-anthropic": {"apiKey"},
	"kimi":                 {"apiKey"},
	"kimi-anthropic":       {"apiKey"},
	"glm":                  {"apiKey"},
	"openai-compatible":    {"apiKey"},
	"ollama":               {"none"},
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
}

// LoadStore reads path. Missing returns an empty Store. Non-missing files
// must have mode 0600 (group/world bits unset).
func LoadStore(path string) (*Store, error) {
	s := &Store{path: path, data: fileShape{Schema: 1, Providers: map[string]providerSection{}}}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return nil, fmt.Errorf("credentials: stat %s: %w", path, err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("credentials: %s has mode %o; require 0600", path, info.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
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
	for _, env := range providerEnvVars[provider] {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
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
	for _, env := range providerEnvVars[provider] {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			envVar = env
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
	var candidates []string
	candidates = append(candidates, providerEnvVars[name]...)
	if typ != name {
		candidates = append(candidates, providerEnvVars[typ]...)
	}
	for _, env := range candidates {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			envVar = env
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
	for name, modes := range providerAuthModes {
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
	var candidates []string
	candidates = append(candidates, providerEnvVars[name]...)
	if typ != name {
		candidates = append(candidates, providerEnvVars[typ]...)
	}
	for _, env := range candidates {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
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
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("credentials: mkdir: %w", err)
	}
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("credentials: open: %w", err)
	}
	if err := toml.NewEncoder(f).Encode(s.data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("credentials: encode: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, s.path)
}
