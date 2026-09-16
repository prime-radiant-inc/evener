package hubcore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/afero"
)

type recoveryAuthority struct {
	ExitConfirmed bool   `json:"exit_confirmed"`
	Group         string `json:"group"`
	SessionID     string `json:"session_id"`
}

type recoveryRecord struct {
	Alias string `json:"alias"`
	recoveryAuthority
}

type recoverySnapshot struct {
	Version int              `json:"version"`
	Records []recoveryRecord `json:"records"`
}

type recoveryStoreFaults struct {
	BeforeRename func() error
	AfterRename  func() error
}

// recoveryStore is serialized by ResumeLocks.persistenceMu, independently of
// the admission mutex. Its state follows the visible file even on sync failure.
type recoveryStore struct {
	fs            afero.Fs
	root          string
	directoryBase string
	state         map[string]recoveryAuthority
	faults        recoveryStoreFaults
}

func openRecoveryStore(fs afero.Fs, root string) (*recoveryStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("recovery state root is required")
	}
	root = filepath.Clean(root)
	base, err := existingRecoveryDirectory(fs, root)
	if err != nil {
		return nil, err
	}
	store := &recoveryStore{fs: fs, root: root, directoryBase: base, state: map[string]recoveryAuthority{}}
	data, err := afero.ReadFile(fs, filepath.Join(root, "recovery", "state.json"))
	if os.IsNotExist(err) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read recovery state: %w", err)
	}
	var snapshot recoverySnapshot
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return nil, fmt.Errorf("decode recovery state: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("decode recovery state: trailing data")
	}
	if snapshot.Version != 3 {
		return nil, fmt.Errorf("unsupported recovery state version %d", snapshot.Version)
	}
	targets := make(map[string]recoveryAuthority)
	for _, record := range snapshot.Records {
		if !validRecoveryAlias(record.Alias) || strings.TrimSpace(record.Group) == "" || !validRecoveryAlias(record.SessionID) {
			return nil, errors.New("invalid recovery alias or group")
		}
		if _, exists := store.state[record.Alias]; exists {
			return nil, fmt.Errorf("duplicate recovery alias %q", record.Alias)
		}
		if target, ok := targets[record.Group]; ok && target != record.recoveryAuthority {
			return nil, errors.New("recovery group has conflicting exit or session authority")
		}
		targets[record.Group] = record.recoveryAuthority
		store.state[record.Alias] = record.recoveryAuthority
	}
	return store, nil
}

func validRecoveryAlias(alias string) bool {
	return alias != "" && alias != "." && alias != ".." && strings.TrimSpace(alias) == alias && !strings.ContainsAny(alias, "/\\:\x00")
}

func (s *recoveryStore) commit(next map[string]recoveryAuthority) (bool, error) {
	snapshot := recoverySnapshot{Version: 3, Records: []recoveryRecord{}}
	for _, alias := range slices.Sorted(maps.Keys(next)) {
		snapshot.Records = append(snapshot.Records, recoveryRecord{Alias: alias, recoveryAuthority: next[alias]})
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return false, err
	}
	renamed, err := s.write(data)
	if renamed {
		s.state = next
	}
	if err != nil {
		return renamed, fmt.Errorf("persist recovery state: %w", err)
	}
	return renamed, nil
}

func (s *recoveryStore) write(data []byte) (renamed bool, err error) {
	dir := filepath.Join(s.root, "recovery")
	if err := createRecoveryDirectory(s.fs, dir, s.directoryBase); err != nil {
		return false, err
	}
	temp, err := afero.TempFile(s.fs, dir, "state.json.tmp-*")
	if err != nil {
		return false, err
	}
	tempPath := temp.Name()
	defer func() {
		if temp != nil {
			_ = temp.Close()
		}
		if !renamed {
			_ = s.fs.Remove(tempPath)
		}
	}()
	if _, err := temp.Write(data); err != nil {
		return false, err
	}
	if err := temp.Sync(); err != nil {
		return false, err
	}
	if err := temp.Close(); err != nil {
		return false, err
	}
	temp = nil
	if s.faults.BeforeRename != nil {
		if err := s.faults.BeforeRename(); err != nil {
			return false, err
		}
	}
	if err := s.fs.Rename(tempPath, filepath.Join(dir, "state.json")); err != nil {
		return false, err
	}
	renamed = true
	if s.faults.AfterRename != nil {
		if err := s.faults.AfterRename(); err != nil {
			return true, err
		}
	}
	return true, syncRecoveryDirectory(s.fs, dir)
}

// The existing hierarchy is owned by the caller. Retain its boundary across
// retries so links created by this store are synced even after a failed sync,
// without requiring read access to unrelated traversal-only ancestors.
func existingRecoveryDirectory(fs afero.Fs, dir string) (string, error) {
	for {
		info, err := fs.Stat(dir)
		if err == nil {
			if !info.IsDir() {
				return "", fmt.Errorf("recovery state parent %q is not a directory", dir)
			}
			return dir, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", err
		}
		dir = parent
	}
}

// Every missing directory is linked durably in its parent before intent can
// authorize a signal. Unsupported sync is an error, never claimed as durable.
func createRecoveryDirectory(fs afero.Fs, dir, base string) error {
	if dir == base {
		return nil
	}
	parent := filepath.Dir(dir)
	if parent != dir {
		if err := createRecoveryDirectory(fs, parent, base); err != nil {
			return err
		}
	}
	info, err := fs.Stat(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		if err := fs.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
			return err
		}
	} else if !info.IsDir() {
		return fmt.Errorf("recovery state parent %q is not a directory", dir)
	}
	// Repeat parent syncs even for existing directories: an earlier attempt may
	// have created one and failed before its parent's sync completed.
	if parent != dir {
		return syncRecoveryDirectory(fs, parent)
	}
	return nil
}

func syncRecoveryDirectory(fs afero.Fs, dir string) error {
	directory, err := fs.Open(dir)
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	return errors.Join(syncErr, closeErr)
}
