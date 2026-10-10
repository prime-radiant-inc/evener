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
// concurrent read+write) and serializes each mutation's persist, so two racing
// writers can't drop each other's update. path and fs are set once at
// construction (loadStoreFS) and never mutated again, so reading them needs no
// lock.
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
// A failed save puts the entry back the way it was: the hub answers auth
// status from this map and reloads the registry from the file, so memory
// that leads the file is a credential the pane reports and no launch can
// resolve.
func (s *Store) Set(name, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	name = strings.ToLower(name)
	if s.data.Providers == nil {
		s.data.Providers = map[string]providerSection{}
	}
	prev, had := s.data.Providers[name]
	s.data.Providers[name] = providerSection{APIKey: strings.TrimSpace(value)}
	if err := s.save(); err != nil {
		restoreEntry(s.data.Providers, name, prev, had)
		return err
	}
	return nil
}

// Clear removes the entry, restoring it if the save fails (see Set). No error
// if absent.
func (s *Store) Clear(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	name = strings.ToLower(name)
	prev, had := s.data.Providers[name]
	delete(s.data.Providers, name)
	if err := s.save(); err != nil {
		restoreEntry(s.data.Providers, name, prev, had)
		return err
	}
	return nil
}

// Move files the entry under newName instead of oldName in one persist, which
// is what a renamed instance carries its stored key with: a Set-then-Clear
// pair whose second half failed would leave the key under both names, and the
// reverse order would lose it outright. A name with no entry is nothing to
// move, not a failure. A failed save leaves both names exactly as they were,
// for the reason Set gives.
func (s *Store) Move(oldName, newName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	oldName, newName = strings.ToLower(oldName), strings.ToLower(newName)
	entry, ok := s.data.Providers[oldName]
	// Names that differ only in case are one entry here, and moving it onto
	// itself would delete the copy that was just made.
	if !ok || oldName == newName {
		return nil
	}
	prev, had := s.data.Providers[newName]
	s.data.Providers[newName] = entry
	delete(s.data.Providers, oldName)
	if err := s.save(); err != nil {
		s.data.Providers[oldName] = entry
		restoreEntry(s.data.Providers, newName, prev, had)
		return err
	}
	return nil
}

// restoreEntry puts one name back the way a mutation found it, after a save
// that did not happen.
func restoreEntry(providers map[string]providerSection, name string, prev providerSection, had bool) {
	if had {
		providers[name] = prev
		return
	}
	delete(providers, name)
}

// save persists s.data. Callers must hold mu (Lock, not RLock): it reads data
// for encoding, and holding the lock across the temp-write-then-rename keeps a
// racing pair from dropping one update. The temp file is created exclusively
// under a random name (never a fixed <path>.tmp): a planted symlink at a
// predictable temp name would otherwise redirect the saved credentials into a
// file of the attacker's choosing, the same class #1040 fixed for the target
// itself.
// Because the temp name is random, a save killed between the exclusive create
// and the rename (crash, SIGKILL, power loss) leaves a unique
// credentials.toml.tmp-* file behind that nothing reclaims; that is the
// accepted cost of never reusing a name an attacker could plant.
func (s *Store) save() error {
	if s.path == "" {
		return nil
	}
	dir := filepath.Dir(s.path)
	if err := s.fs.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("credentials: mkdir: %w", err)
	}
	f, err := afero.TempFile(s.fs, dir, filepath.Base(s.path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("credentials: open: %w", err)
	}
	tmp := f.Name()
	// A save that dies partway has already created the temp file, so clear it
	// too: the rename takes the name with it, so this is a no-op once the save
	// has landed.
	defer func() { _ = s.fs.Remove(tmp) }()
	if err := toml.NewEncoder(f).Encode(s.data); err != nil {
		_ = f.Close()
		return fmt.Errorf("credentials: encode: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return s.fs.Rename(tmp, s.path)
}
