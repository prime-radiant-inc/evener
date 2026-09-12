package hubcore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sync"

	"github.com/spf13/afero"

	"primeradiant.com/evener/appwire"
)

const (
	keybindingsSnapshotVersion uint64 = 1
	keybindingsStateSubdir            = "keybindings"
	keybindingsStateFilename          = "state.json"
)

type keybindingsSnapshot struct {
	Version  uint64                    `json:"version"`
	Revision uint64                    `json:"revision"`
	Config   appwire.KeybindingsConfig `json:"config"`
}

type keybindingsStoreFaults struct {
	BeforeRename func() error
	AfterRename  func() error
}

// KeybindingsPostRenameError wraps a durable failure that happened AFTER the
// rename published the new snapshot: the stored overrides advanced to the new
// revision, but a follow-up directory sync or hook failed. Callers must treat
// the patch as APPLIED - in particular the RPC layer must still broadcast the
// new canonical state, or every other client stays on the pre-patch revision.
type KeybindingsPostRenameError struct{ Err error }

func (e *KeybindingsPostRenameError) Error() string { return e.Err.Error() }
func (e *KeybindingsPostRenameError) Unwrap() error { return e.Err }

// KeybindingsStore is the hub-authoritative store for user keybinding
// overrides. One mutex serializes each durable update.
type KeybindingsStore struct {
	mu      sync.Mutex
	fs      afero.Fs
	root    string
	state   keybindingsSnapshot
	loadErr error
	faults  keybindingsStoreFaults
}

// NewKeybindingsStore opens the keybindings state below stateRoot. A malformed
// snapshot returns a usable shipped-default fallback together with the load
// diagnostic; callers must retain both values and report the error.
func NewKeybindingsStore(stateRoot string) (*KeybindingsStore, error) {
	return newKeybindingsStoreFS(afero.NewOsFs(), stateRoot, keybindingsStoreFaults{})
}

// NewKeybindingsStoreForTest is NewKeybindingsStore plus a fault hook fired
// after each rename publishes a patch, so tests outside this package can
// exercise post-rename failure handling (KeybindingsPostRenameError).
func NewKeybindingsStoreForTest(stateRoot string, afterRenameFault func() error) (*KeybindingsStore, error) {
	return newKeybindingsStoreFS(afero.NewOsFs(), stateRoot, keybindingsStoreFaults{AfterRename: afterRenameFault})
}

func newKeybindingsStoreFS(fs afero.Fs, stateRoot string, faults keybindingsStoreFaults) (*KeybindingsStore, error) {
	state, err := loadKeybindingsSnapshotFS(fs, stateRoot)
	if err != nil {
		state = keybindingsShippedSnapshot()
	}
	return &KeybindingsStore{
		fs:      fs,
		root:    stateRoot,
		state:   state,
		loadErr: err,
		faults:  faults,
	}, err
}

// Snapshot returns a deep value copy of the canonical overrides. The returned
// rules slice is never shared with the store.
func (s *KeybindingsStore) Snapshot() appwire.KeybindingsOverrides {
	if s == nil {
		return appwire.KeybindingsShippedDefaults()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return keybindingsSnapshotOverrides(s.state)
}

// LoadErr reports the diagnostic captured when the persisted state failed to
// load at construction (Snapshot serves the shipped-default fallback in that
// case, and Patch rejects). Nil when the state loaded cleanly.
func (s *KeybindingsStore) LoadErr() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadErr
}

// Patch validates and durably replaces the overrides using optimistic
// revision checking. A durable error before rename leaves the old state
// published; an error after rename publishes the new canonical state in
// memory and is returned wrapped in *KeybindingsPostRenameError so callers
// can tell the applied-but-unfinished case apart from a rejected patch.
func (s *KeybindingsStore) Patch(params appwire.KeybindingsPatchParams) (appwire.KeybindingsOverrides, error) {
	if s == nil {
		return appwire.KeybindingsOverrides{}, errors.New("keybindings store is not configured")
	}
	if err := appwire.ValidateKeybindingsConfig(params.Config); err != nil {
		return appwire.KeybindingsOverrides{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return appwire.KeybindingsOverrides{}, fmt.Errorf("keybindings state is unavailable: %w", s.loadErr)
	}

	if s.state.Revision != params.ExpectedRevision {
		return appwire.KeybindingsOverrides{}, keybindingsConflict(s.state, params.ExpectedRevision)
	}
	if reflect.DeepEqual(s.state.Config, params.Config) {
		return keybindingsSnapshotOverrides(s.state), nil
	}
	if s.state.Revision == math.MaxUint64 {
		return appwire.KeybindingsOverrides{}, errors.New("keybindings revision overflow")
	}

	next := keybindingsSnapshot{
		Version:  s.state.Version,
		Revision: s.state.Revision + 1,
		Config:   cloneKeybindingsConfig(params.Config),
	}
	// A nil Rules slice marshals as "rules":null, which the loader's shape
	// check rejects on restart (it would silently fall back to defaults) -
	// normalize to the empty slice the shipped defaults use so a direct Go
	// caller's nil round-trips.
	if next.Config.Rules == nil {
		next.Config.Rules = []appwire.KeybindingsRule{}
	}
	renamed, err := saveKeybindingsSnapshotFS(s.fs, s.root, next, s.faults)
	if renamed {
		s.state = next
	}
	if err != nil {
		if renamed {
			return appwire.KeybindingsOverrides{}, &KeybindingsPostRenameError{Err: err}
		}
		return appwire.KeybindingsOverrides{}, err
	}
	return keybindingsSnapshotOverrides(s.state), nil
}

func keybindingsConflict(state keybindingsSnapshot, expected uint64) appwire.WireError {
	return appwire.WireError{
		Code:    appwire.CodeConflict,
		Message: fmt.Sprintf("keybindings revision conflict: expected %d, current %d", expected, state.Revision),
		Data: appwire.KeybindingsConflictData{
			EvenerErrorInfo: appwire.ErrorConflict,
			Current:         keybindingsSnapshotOverrides(state),
		},
	}
}

func keybindingsStatePath(stateRoot string) string {
	return filepath.Join(stateRoot, keybindingsStateSubdir, keybindingsStateFilename)
}

func loadKeybindingsSnapshotFS(fs afero.Fs, stateRoot string) (keybindingsSnapshot, error) {
	empty := keybindingsShippedSnapshot()
	if stateRoot == "" {
		return empty, nil
	}
	data, err := afero.ReadFile(fs, keybindingsStatePath(stateRoot))
	if os.IsNotExist(err) {
		return empty, nil
	}
	if err != nil {
		return empty, fmt.Errorf("read keybindings state: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return empty, nil
	}

	var state keybindingsSnapshot
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return empty, fmt.Errorf("decode keybindings state: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return empty, errors.New("decode keybindings state: trailing JSON value")
		}
		return empty, fmt.Errorf("decode keybindings state trailing data: %w", err)
	}
	if err := validateKeybindingsSnapshotShape(data); err != nil {
		return empty, fmt.Errorf("validate keybindings state shape: %w", err)
	}
	if err := validateKeybindingsSnapshot(state); err != nil {
		return empty, fmt.Errorf("validate keybindings state: %w", err)
	}
	return state, nil
}

func saveKeybindingsSnapshotFS(fs afero.Fs, stateRoot string, state keybindingsSnapshot, faults keybindingsStoreFaults) (renamed bool, err error) {
	if err := validateKeybindingsSnapshot(state); err != nil {
		return false, err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return false, fmt.Errorf("marshal keybindings state: %w", err)
	}
	if stateRoot == "" {
		return true, nil
	}

	path := keybindingsStatePath(stateRoot)
	dir := filepath.Dir(path)
	if err := fs.MkdirAll(dir, 0o700); err != nil {
		return false, fmt.Errorf("create keybindings state directory: %w", err)
	}
	temp, err := afero.TempFile(fs, dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return false, fmt.Errorf("create temp keybindings state: %w", err)
	}
	tempPath := temp.Name()
	defer func() {
		if temp != nil {
			_ = temp.Close()
		}
		if !renamed {
			_ = fs.Remove(tempPath)
		}
	}()
	if _, err := temp.Write(data); err != nil {
		return false, fmt.Errorf("write temp keybindings state: %w", err)
	}
	if err := temp.Sync(); err != nil && !deletionSyncUnsupported(err) {
		return false, fmt.Errorf("sync temp keybindings state: %w", err)
	}
	if err := temp.Close(); err != nil {
		return false, fmt.Errorf("close temp keybindings state: %w", err)
	}
	temp = nil
	if faults.BeforeRename != nil {
		if err := faults.BeforeRename(); err != nil {
			return false, err
		}
	}
	if err := fs.Rename(tempPath, path); err != nil {
		return false, fmt.Errorf("rename keybindings state: %w", err)
	}
	renamed = true
	directory, err := fs.Open(dir)
	if err != nil {
		return true, fmt.Errorf("open keybindings state directory: %w", err)
	}
	if err := directory.Sync(); err != nil && !deletionSyncUnsupported(err) {
		_ = directory.Close()
		return true, fmt.Errorf("sync keybindings state directory: %w", err)
	}
	if err := directory.Close(); err != nil {
		return true, fmt.Errorf("close keybindings state directory: %w", err)
	}
	if faults.AfterRename != nil {
		if err := faults.AfterRename(); err != nil {
			return true, err
		}
	}
	return true, nil
}

func validateKeybindingsSnapshot(state keybindingsSnapshot) error {
	if state.Version != keybindingsSnapshotVersion {
		return fmt.Errorf("unsupported keybindings state version %d", state.Version)
	}
	return appwire.ValidateKeybindingsConfig(state.Config)
}

func validateKeybindingsSnapshotShape(raw []byte) error {
	top, err := keybindingsSnapshotObjectFields(raw)
	if err != nil {
		return err
	}
	if err := requireKeybindingsSnapshotFields(top, "version", "revision", "config"); err != nil {
		return err
	}
	if bytes.Equal(bytes.TrimSpace(top["revision"]), []byte("null")) {
		return errors.New("revision must be an unsigned integer")
	}
	configFields, err := keybindingsSnapshotObjectFields(top["config"])
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if err := requireKeybindingsSnapshotFields(configFields, "version", "rules"); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if bytes.Equal(bytes.TrimSpace(configFields["rules"]), []byte("null")) {
		return errors.New("config: rules must be an array")
	}
	var rules []json.RawMessage
	if err := json.Unmarshal(configFields["rules"], &rules); err != nil {
		return fmt.Errorf("config: rules: %w", err)
	}
	for i, rawRule := range rules {
		ruleFields, err := keybindingsSnapshotObjectFields(rawRule)
		if err != nil {
			return fmt.Errorf("rule %d: %w", i, err)
		}
		if err := requireKeybindingsSnapshotFields(ruleFields, "action", "chord"); err != nil {
			return fmt.Errorf("rule %d: %w", i, err)
		}
	}
	return nil
}

func keybindingsSnapshotObjectFields(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		return nil, errors.New("expected JSON object")
	}
	return fields, nil
}

func requireKeybindingsSnapshotFields(fields map[string]json.RawMessage, names ...string) error {
	for _, name := range names {
		if _, ok := fields[name]; !ok {
			return fmt.Errorf("missing required field %q", name)
		}
	}
	return nil
}

func keybindingsShippedSnapshot() keybindingsSnapshot {
	defaults := appwire.KeybindingsShippedDefaults()
	return keybindingsSnapshot{
		Version:  keybindingsSnapshotVersion,
		Revision: defaults.Revision,
		Config:   appwire.KeybindingsConfig{Version: defaults.Version, Rules: cloneKeybindingsRules(defaults.Rules)},
	}
}

func keybindingsSnapshotOverrides(state keybindingsSnapshot) appwire.KeybindingsOverrides {
	return appwire.KeybindingsOverrides{
		Version:  state.Config.Version,
		Revision: state.Revision,
		Rules:    cloneKeybindingsRules(state.Config.Rules),
	}
}

func cloneKeybindingsConfig(config appwire.KeybindingsConfig) appwire.KeybindingsConfig {
	return appwire.KeybindingsConfig{Version: config.Version, Rules: cloneKeybindingsRules(config.Rules)}
}

func cloneKeybindingsRules(rules []appwire.KeybindingsRule) []appwire.KeybindingsRule {
	if rules == nil {
		return nil
	}
	cloned := make([]appwire.KeybindingsRule, len(rules))
	for i, rule := range rules {
		cloned[i] = rule
		if rule.Chord != nil {
			chord := *rule.Chord
			cloned[i].Chord = &chord
		}
	}
	return cloned
}
