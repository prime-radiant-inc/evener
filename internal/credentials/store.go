// Package credentials owns credentials.toml, a sibling of providers.toml
// under the evener config root (cmdutil.DefaultConfigRoot,
// ~/.config/evener by default). Provider API keys are stored verbatim with
// chmod 600; encryption-at-rest is deliberately not provided (see spec
// §5.5 non-goals).
package credentials

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
	"github.com/spf13/afero"
)

type fileShape struct {
	Schema    int                        `toml:"schema"`
	Providers map[string]providerSection `toml:"providers"`
}

type providerSection struct {
	APIKey string `toml:"api_key,omitempty"`
}

// Store is the in-memory + on-disk credentials.toml. One Store is shared by
// every evener/auth/* RPC handler plus evener/instance/remove on the hub's
// single hubAuthController, and AppWire serializes requests only within a
// single connection - two browser tabs, or a browser and the TUI, can call
// Set/Clear/Get concurrently. mu guards data (a plain map, unsafe for
// concurrent read+write) and serializes save()'s temp-file-then-rename pair,
// so two racing writers can't interleave on the same .tmp path and corrupt
// or drop each other's update. path and fs are set once at construction
// (loadStoreFS) and never mutated again, so reading them needs no lock.
type Store struct {
	path string
	fs   afero.Fs

	mu   sync.RWMutex
	data fileShape
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
	s := &Store{path: path, data: fileShape{Schema: 1}, fs: fs}
	info, err := fs.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.data.Providers = map[string]providerSection{}
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

// Get returns the file-layer key stored under name (an instance name, spec
// §10). The environment is the registry's business, not the store's.
func (s *Store) Get(name string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.data.Providers[strings.ToLower(name)]
	if !ok || strings.TrimSpace(p.APIKey) == "" {
		return "", false
	}
	return p.APIKey, true
}

// Names lists every entry, sorted, so a caller can report entries that
// name no instance (spec §14.1).
func (s *Store) Names() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.data.Providers))
	for name := range s.data.Providers {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Path is the file the store reads and writes.
func (s *Store) Path() string { return s.path }

// Set writes an instance's API key into the in-memory store and persists.
func (s *Store) Set(name, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	name = strings.ToLower(name)
	if s.data.Providers == nil {
		s.data.Providers = map[string]providerSection{}
	}
	s.data.Providers[name] = providerSection{APIKey: strings.TrimSpace(value)}
	return s.save()
}

// Clear removes the entry. No error if absent.
func (s *Store) Clear(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	name = strings.ToLower(name)
	delete(s.data.Providers, name)
	return s.save()
}

// save persists s.data. Callers must hold mu (Lock, not RLock): it both
// reads data for encoding and is the only place two concurrent writers could
// otherwise interleave on the same .tmp path.
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
