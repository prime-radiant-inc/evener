package openai

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/envvars/userdirs"
	"primeradiant.com/evener/execsupport/fsatomic"
)

var (
	authMkdirAll = os.MkdirAll
	authMarshal  = json.MarshalIndent
	authRemove   = os.Remove
)

const (
	authDirName = "auth"
)

var (
	// ErrAuthNotFound is returned by LoadAuth when no auth file exists for the
	// instance. Callers branch on it (via errors.Is) to fall back to
	// OPENAI_API_KEY or to treat the instance as signed out.
	ErrAuthNotFound = errors.New("openai auth not found")
	// ErrAuthCorrupt is returned (wrapped) by LoadAuth when the auth file
	// exists but cannot be parsed as JSON or fails AuthRecord.Validate.
	ErrAuthCorrupt = errors.New("openai auth is corrupt")
)

// AuthRecord is the persisted Evener-owned OpenAI auth record.
type AuthRecord struct {
	Version      int       `json:"version"`
	Provider     string    `json:"provider"`
	Source       string    `json:"source"`
	ObtainedAt   time.Time `json:"obtained_at"`
	TokenType    string    `json:"token_type"`
	Scope        string    `json:"scope"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	IDToken      string    `json:"id_token,omitempty"`
	Expiry       time.Time `json:"expiry"`
	Email        string    `json:"email,omitempty"`
	AccountID    string    `json:"account_id,omitempty"`
	WorkspaceID  string    `json:"workspace_id,omitempty"`
}

// AuthFilePath returns the path to the OAuth record for the given instance.
// instanceName is the provider instance name (e.g. "openai", "work"); it
// maps directly to the filename: auth/<instanceName>.json.
//
// instanceName is contained to a single path component (filepath.Base) so a
// name carrying path separators or ".." can never escape the auth dir and have
// a caller read/write/delete an arbitrary file. Callers validate names upstream;
// this is the last-line guard at the filesystem sink (a missing controller-level
// check let evener/instance/remove delete arbitrary .json files — see the
// ValidateInstanceName fix in cmd/evener-hub/app_instances.go).
func AuthFilePath(stateDir, instanceName string) string {
	return filepath.Join(stateDir, authDirName, filepath.Base(instanceName)+".json")
}

// DefaultStateDir returns the default Evener state directory, resolving the state
// home from XDG_STATE_HOME (falling back to ~/.local/state). It is equivalent
// to DefaultStateDirWithStateHome("").
func DefaultStateDir() (string, error) {
	return DefaultStateDirWithStateHome("")
}

// DefaultStateDirWithStateHome returns the Evener state directory rooted at the
// given state home (userdirs.StateRoot). When stateHome is empty it falls back
// to XDG_STATE_HOME, then to ~/.local/state. It returns userdirs.ErrNoStateHome
// when none of those resolve.
func DefaultStateDirWithStateHome(stateHome string) (string, error) {
	base := strings.TrimSpace(stateHome)
	if base == "" {
		base = envvars.XDGStateHome.Trimmed()
	}
	return userdirs.StateRoot(base, os.UserHomeDir)
}

// LoadAuth reads and validates the stored auth record for instanceName under
// stateDir. It returns ErrAuthNotFound if no file exists and a wrapped
// ErrAuthCorrupt if the file cannot be parsed or fails validation.
func LoadAuth(stateDir, instanceName string) (AuthRecord, error) {
	path := AuthFilePath(stateDir, instanceName)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return AuthRecord{}, ErrAuthNotFound
		}
		return AuthRecord{}, fmt.Errorf("read auth file: %w", err)
	}

	var record AuthRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return AuthRecord{}, fmt.Errorf("%w: %w", ErrAuthCorrupt, err)
	}
	if err := record.Validate(); err != nil {
		return AuthRecord{}, fmt.Errorf("%w: %w", ErrAuthCorrupt, err)
	}
	return record, nil
}

// SaveAuth writes record as the auth file for instanceName under stateDir,
// creating the auth directory if needed. The write is atomic and durable (a
// 0600 temp file is synced and renamed into place, then the containing
// directory is synced) so a reader never observes a partially written record
// and the replacement survives a crash once SaveAuth returns.
func SaveAuth(stateDir, instanceName string, record AuthRecord) error {
	path := AuthFilePath(stateDir, instanceName)
	if err := authMkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create auth directory: %w", err)
	}

	data, err := authMarshal(record, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal auth record: %w", err)
	}
	data = append(data, '\n')

	if err := WriteAuthFile(path, data); err != nil {
		return err
	}
	// A saved record is a login or a refresh that worked, so a refusal noted
	// for the record it replaced no longer applies (#2479).
	_ = clearRefreshRejection(stateDir, instanceName)
	return nil
}

// WriteAuthFile writes data to path as an auth file, replacing whatever was
// there atomically and durably (fsatomic.Write, mode 0600): a reader never
// observes a partially written file, a crash inside the write leaves the
// previous contents instead of a truncated credential, and a crash after the
// rename cannot revert to the previous record. A directory sync that fails
// after the rename is reported (fsatomic.ErrPublished) with the new file left
// in place. SaveAuth is its marshalling caller; a caller restoring captured
// bytes shares these guarantees rather than approximating them.
func WriteAuthFile(path string, data []byte) error {
	return fsatomic.Write(path, data, 0o600)
}

// DeleteAuth removes the stored auth file for instanceName under stateDir. It
// reports whether a file was actually deleted; a missing file returns
// (false, nil).
func DeleteAuth(stateDir, instanceName string) (bool, error) {
	path := AuthFilePath(stateDir, instanceName)
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Already signed out: no refresh token remains for a refusal to
			// be about.
			_ = clearRefreshRejection(stateDir, instanceName)
			return false, nil
		}
		return false, fmt.Errorf("delete auth file: %w", err)
	}
	// A signed-out instance has no refresh token for a refusal to be about.
	// Cleared only after the record is actually gone, so a failed remove
	// never loses the note while the refused record is still on disk.
	_ = clearRefreshRejection(stateDir, instanceName)
	return true, nil
}

// Validate reports whether the record is a complete, supported auth record.
func (r AuthRecord) Validate() error {
	switch {
	case r.Version != 1:
		return fmt.Errorf("unsupported auth record version %d", r.Version)
	case r.Source == "":
		return errors.New("auth source is required")
	case r.AccessToken == "":
		return errors.New("access token is required")
	case r.RefreshToken == "":
		return errors.New("refresh token is required")
	case r.TokenType == "":
		return errors.New("token type is required")
	case r.Expiry.IsZero():
		return errors.New("expiry is required")
	case r.ObtainedAt.IsZero():
		return errors.New("obtained_at is required")
	}
	return nil
}
