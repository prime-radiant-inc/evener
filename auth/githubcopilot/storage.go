package githubcopilot

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"primeradiant.com/evener/envvars/userdirs"
	"primeradiant.com/evener/execsupport/fsatomic"
)

const (
	authDirName   = "auth"
	schemeDirName = "github-copilot"
	lockSuffix    = ".lock"
	authDirMode   = 0o700
	authFileMode  = 0o600
)

var (
	// ErrAuthNotFound is returned by LoadAuth when no record exists for the
	// instance, so callers can treat the instance as signed out.
	ErrAuthNotFound = errors.New("github copilot auth not found")
	// ErrAuthCorrupt is returned (wrapped) by LoadAuth when the record exists
	// but is not JSON, fails validation, or belongs to another scheme or instance.
	ErrAuthCorrupt = errors.New("github copilot auth is corrupt")
	// ErrAuthInvalid is returned (wrapped) by SaveAuth when the record is
	// incomplete or bound to another scheme or instance.
	ErrAuthInvalid = errors.New("github copilot auth record is invalid")
	// ErrAuthChanged is returned by LoadAuthIfCurrent when a locked re-read
	// found a record generation other than the one the caller read before
	// deciding what to save.
	ErrAuthChanged = errors.New("github copilot auth record changed")

	errNoStateDir = errors.New("a state directory is required for GitHub Copilot auth")
)

// LoadAuthIfCurrent re-reads instance's record under the caller's
// WithRecordLock and returns it only when it is still expected's generation.
// ErrAuthChanged or ErrAuthNotFound means a newer login, refresh, or logout
// won, so the caller must not save over it.
func LoadAuthIfCurrent(stateDir, instance string, expected AuthRecord) (AuthRecord, error) {
	current, err := LoadAuth(stateDir, instance)
	if err != nil {
		return AuthRecord{}, err
	}
	if !expected.SameGeneration(current) {
		return AuthRecord{}, ErrAuthChanged
	}
	return current, nil
}

// AuthFilePath returns the scheme-namespaced record path for instance under
// stateDir: <stateDir>/auth/github-copilot/<instance>.json. The instance is
// reduced to one path component so a name carrying separators or ".." cannot
// escape the scheme directory. An empty stateDir yields "".
func AuthFilePath(stateDir, instance string) string {
	return userdirs.Subdir(stateDir, filepath.Join(authDirName, schemeDirName, filepath.Base(instance)+".json"))
}

// LoadAuth reads and validates the record for instance. A missing record is
// ErrAuthNotFound; a record that cannot be read returns the read error
// unwrapped, while malformed, mis-schemed, mis-instanced, or incomplete
// records wrap ErrAuthCorrupt.
func LoadAuth(stateDir, instance string) (AuthRecord, error) {
	path := AuthFilePath(stateDir, instance)
	if path == "" {
		return AuthRecord{}, errNoStateDir
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return AuthRecord{}, ErrAuthNotFound
	}
	if err != nil {
		return AuthRecord{}, fmt.Errorf("read GitHub Copilot auth record: %w", err)
	}
	var record AuthRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return AuthRecord{}, fmt.Errorf("%w: %w", ErrAuthCorrupt, err)
	}
	if err := record.Validate(); err != nil {
		return AuthRecord{}, fmt.Errorf("%w: %w", ErrAuthCorrupt, err)
	}
	if record.Instance != instance {
		return AuthRecord{}, fmt.Errorf("%w: record instance %q does not match %q", ErrAuthCorrupt, record.Instance, instance)
	}
	return record, nil
}

// SaveAuth validates record and writes it atomically and durably as instance's
// record, creating the scheme directory if needed. The file is mode 0600.
func SaveAuth(stateDir, instance string, record AuthRecord) error {
	path := AuthFilePath(stateDir, instance)
	if path == "" {
		return errNoStateDir
	}
	if record.Instance != instance {
		return fmt.Errorf("%w: record instance %q does not match %q", ErrAuthInvalid, record.Instance, instance)
	}
	if err := record.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrAuthInvalid, err)
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal GitHub Copilot auth record: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), authDirMode); err != nil {
		return fmt.Errorf("create GitHub Copilot auth directory: %w", err)
	}
	return fsatomic.Write(path, append(data, '\n'), authFileMode)
}

// DeleteAuth removes instance's record and makes the removal durable. Deleting
// a missing record is not an error.
func DeleteAuth(stateDir, instance string) error {
	path := AuthFilePath(stateDir, instance)
	if path == "" {
		return errNoStateDir
	}
	return fsatomic.Remove(path)
}

// WithRecordLock runs fn while holding the instance's cross-process lifecycle
// lock, creating the scheme directory if needed. The lock file is left in
// place; closing it releases the lock. fn's error is returned unchanged, so a
// caller's re-read can report ErrAuthNotFound.
//
// Callers that modify a record must re-read it under this lock and compare the
// still-current record (a token, ObtainedAt) with the snapshot they decided
// on. A save without that check can resurrect a record another process
// deleted or overwrite a newer login.
func WithRecordLock(stateDir, instance string, fn func() error) (err error) {
	path := AuthFilePath(stateDir, instance)
	if path == "" {
		return errNoStateDir
	}
	if err := os.MkdirAll(filepath.Dir(path), authDirMode); err != nil {
		return fmt.Errorf("create GitHub Copilot auth directory: %w", err)
	}
	release, err := lockRecordFile(path + lockSuffix)
	if err != nil {
		return fmt.Errorf("lock GitHub Copilot auth record %q: %w", instance, err)
	}
	defer func() {
		if releaseErr := release(); releaseErr != nil && err == nil {
			err = fmt.Errorf("unlock GitHub Copilot auth record %q: %w", instance, releaseErr)
		}
	}()
	return fn()
}
