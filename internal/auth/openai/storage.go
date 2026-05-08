package openai

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	authDirName  = "auth"
	authFileName = "openai.json"
)

var (
	ErrAuthNotFound = errors.New("openai auth not found")
	ErrAuthCorrupt  = errors.New("openai auth is corrupt")
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

func AuthFilePath(stateDir string) string {
	return filepath.Join(stateDir, authDirName, authFileName)
}

func LoadAuth(stateDir string) (AuthRecord, error) {
	path := AuthFilePath(stateDir)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return AuthRecord{}, ErrAuthNotFound
		}
		return AuthRecord{}, fmt.Errorf("read auth file: %w", err)
	}

	var record AuthRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return AuthRecord{}, fmt.Errorf("%w: %v", ErrAuthCorrupt, err)
	}
	if err := record.Validate(); err != nil {
		return AuthRecord{}, fmt.Errorf("%w: %v", ErrAuthCorrupt, err)
	}
	return record, nil
}

func SaveAuth(stateDir string, record AuthRecord) error {
	path := AuthFilePath(stateDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create auth directory: %w", err)
	}

	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal auth record: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(path), authFileName+".*")
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

func DeleteAuth(stateDir string) (bool, error) {
	path := AuthFilePath(stateDir)
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
	case r.Provider != "openai":
		return fmt.Errorf("unexpected auth provider %q", r.Provider)
	case r.Source == "":
		return fmt.Errorf("auth source is required")
	case r.AccessToken == "":
		return fmt.Errorf("access token is required")
	case r.RefreshToken == "":
		return fmt.Errorf("refresh token is required")
	case r.TokenType == "":
		return fmt.Errorf("token type is required")
	case r.Expiry.IsZero():
		return fmt.Errorf("expiry is required")
	case r.ObtainedAt.IsZero():
		return fmt.Errorf("obtained_at is required")
	}
	return nil
}
