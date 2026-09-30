package hubcore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/afero"

	"primeradiant.com/evener/cmd/evener-hub/internal/fsdurability"
)

// This file holds the shared writer/reader for the hub's JSON state stores
// (keybindings, transcript display, deletions). Each of those persists one
// snapshot with the same sequence: create the state directory 0o700, write a
// temp file in that directory, fsync it, atomically rename it over the target,
// then fsync the directory. writeStateFileAtomic reports renamed only after
// the rename publishes the new contents, so a caller can tell a pre-rename
// failure (the old state stays authoritative) apart from a post-rename one
// (the new state is authoritative but a follow-up sync or hook failed).
//
// The recovery store deliberately does not use this helper: its writer's
// durability contract differs (see recoveryStore.write) and its reader reports
// its own trailing-data diagnostic.

// writeStateFileAtomic durably publishes data at path. label names the store in
// wrapped errors ("keybindings", "transcript display", "deletion"). beforeRename
// and afterRename, when non-nil, fire around the rename; an afterRename fault is
// reported with renamed=true because the rename has already published the new
// contents. An unsupported sync (deletionSyncUnsupported) is tolerated.
func writeStateFileAtomic(fs afero.Fs, path, label string, data []byte, beforeRename, afterRename func() error) (renamed bool, err error) {
	dir := filepath.Dir(path)
	if err := fs.MkdirAll(dir, 0o700); err != nil {
		return false, fmt.Errorf("create %s state directory: %w", label, err)
	}
	temp, err := afero.TempFile(fs, dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return false, fmt.Errorf("create temp %s state: %w", label, err)
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
		return false, fmt.Errorf("write temp %s state: %w", label, err)
	}
	if err := temp.Sync(); err != nil && !deletionSyncUnsupported(err) {
		return false, fmt.Errorf("sync temp %s state: %w", label, err)
	}
	if err := temp.Close(); err != nil {
		return false, fmt.Errorf("close temp %s state: %w", label, err)
	}
	temp = nil
	if beforeRename != nil {
		if err := beforeRename(); err != nil {
			return false, err
		}
	}
	if err := fs.Rename(tempPath, path); err != nil {
		return false, fmt.Errorf("rename %s state: %w", label, err)
	}
	renamed = true
	directory, err := fs.Open(dir)
	if err != nil {
		return true, fmt.Errorf("open %s state directory: %w", label, err)
	}
	if err := directory.Sync(); err != nil && !deletionSyncUnsupported(err) {
		_ = directory.Close()
		return true, fmt.Errorf("sync %s state directory: %w", label, err)
	}
	if err := directory.Close(); err != nil {
		return true, fmt.Errorf("close %s state directory: %w", label, err)
	}
	if afterRename != nil {
		if err := afterRename(); err != nil {
			return true, err
		}
	}
	return true, nil
}

// readStateFile reads the raw bytes of a state file. ok is false when the file
// does not exist, so the caller can fall back to its shipped default; a real
// read error is wrapped as "read <label> state".
func readStateFile(fs afero.Fs, path, label string) (data []byte, ok bool, err error) {
	data, err = afero.ReadFile(fs, path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read %s state: %w", label, err)
	}
	return data, true, nil
}

// decodeStateFileStrict decodes data into out, rejecting unknown fields and any
// trailing JSON value so a corrupt or partially overwritten file is reported
// instead of silently truncated. Errors are wrapped with label for the store's
// diagnostics.
func decodeStateFileStrict(data []byte, label string, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("decode %s state: %w", label, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("decode %s state: trailing JSON value", label)
		}
		return fmt.Errorf("decode %s state trailing data: %w", label, err)
	}
	return nil
}

// deletionSyncUnsupported is the name the deletion store's tests use for the
// hub's shared sync-tolerance predicate, which lives in fsdurability.
func deletionSyncUnsupported(err error) bool {
	return fsdurability.SyncUnsupported(err)
}
