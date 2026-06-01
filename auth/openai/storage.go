package openai

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
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

// AuthRecord is the persisted Serf-owned OpenAI auth record.
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
func AuthFilePath(stateDir, instanceName string) string {
	return filepath.Join(stateDir, authDirName, instanceName+".json")
}

// DefaultStateDir returns the default Serf state directory, resolving the state
// home from XDG_STATE_HOME (falling back to ~/.local/state). It is equivalent
// to DefaultStateDirWithStateHome("").
func DefaultStateDir() string {
	return DefaultStateDirWithStateHome("")
}

// DefaultStateDirWithStateHome returns the Serf state directory rooted at the
// given state home. When stateHome is empty it falls back to XDG_STATE_HOME,
// then to ~/.local/state (or the OS temp dir if the home directory cannot be
// determined). The result is that base joined with "serf".
func DefaultStateDirWithStateHome(stateHome string) string {
	base := strings.TrimSpace(stateHome)
	if base == "" {
		base = strings.TrimSpace(os.Getenv("XDG_STATE_HOME"))
	}
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = os.TempDir()
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "serf")
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
// creating the auth directory if needed. The write is atomic (a 0600 temp file
// is synced and renamed into place) so a reader never observes a partially
// written record.
func SaveAuth(stateDir, instanceName string, record AuthRecord) error {
	path := AuthFilePath(stateDir, instanceName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create auth directory: %w", err)
	}

	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal auth record: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(path), instanceName+".json.*")
	if err != nil {
		return fmt.Errorf("create temp auth file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		_ = tmp.Close()
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("chmod temp auth file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write temp auth file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temp auth file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp auth file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace auth file: %w", err)
	}
	cleanup = false
	return nil
}

// DeleteAuth removes the stored auth file for instanceName under stateDir. It
// reports whether a file was actually deleted; a missing file returns
// (false, nil).
func DeleteAuth(stateDir, instanceName string) (bool, error) {
	path := AuthFilePath(stateDir, instanceName)
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("delete auth file: %w", err)
	}
	return true, nil
}

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
