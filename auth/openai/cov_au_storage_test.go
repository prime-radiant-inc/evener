package openai

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/envvars"
)

// TestDefaultStateDirWithStateHomeFallsBackToUserHome covers the arm where
// neither an explicit state home nor XDG_STATE_HOME is set, so the state dir is
// rooted at the user's home (~/.local/state).
func TestDefaultStateDirWithStateHomeFallsBackToUserHome(t *testing.T) {
	t.Setenv(envvars.XDGStateHome.Name, "")

	home, err := os.UserHomeDir()
	if err != nil {
		home = os.TempDir()
	}
	want := filepath.Join(home, ".local", "state", "serf")

	if got := DefaultStateDirWithStateHome(""); got != want {
		t.Fatalf("DefaultStateDirWithStateHome(\"\") = %q, want %q", got, want)
	}
}

func TestSaveAuthMkdirFailureSurfaces(t *testing.T) {
	// A regular file standing where the state dir belongs makes MkdirAll fail
	// with ENOTDIR when it tries to create the auth subdirectory beneath it.
	stateFile := filepath.Join(t.TempDir(), "state-is-a-file")
	if err := os.WriteFile(stateFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	err := SaveAuth(stateFile, "openai", sampleAuthRecord())
	if err == nil {
		t.Fatal("SaveAuth() error = nil, want create-directory failure")
	}
	if !strings.Contains(err.Error(), "create auth directory") {
		t.Fatalf("SaveAuth() error = %v, want create-directory failure", err)
	}
}

func TestDeleteAuthSurfacesUnexpectedRemoveError(t *testing.T) {
	stateDir := t.TempDir()
	// Make the auth "file" a non-empty directory so os.Remove fails with
	// something other than ErrNotExist.
	path := AuthFilePath(stateDir, "openai")
	if err := os.MkdirAll(filepath.Join(path, "child"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	_, err := DeleteAuth(stateDir, "openai")
	if err == nil {
		t.Fatal("DeleteAuth() error = nil, want remove failure")
	}
	if !strings.Contains(err.Error(), "delete auth file") {
		t.Fatalf("DeleteAuth() error = %v, want delete-auth-file failure", err)
	}
}

func TestAuthRecordValidateRejectsMissingFields(t *testing.T) {
	base := sampleAuthRecord()

	tests := []struct {
		name   string
		mutate func(*AuthRecord)
		want   string
	}{
		{"bad version", func(r *AuthRecord) { r.Version = 2 }, "unsupported auth record version"},
		{"empty source", func(r *AuthRecord) { r.Source = "" }, "auth source is required"},
		{"empty access token", func(r *AuthRecord) { r.AccessToken = "" }, "access token is required"},
		{"empty refresh token", func(r *AuthRecord) { r.RefreshToken = "" }, "refresh token is required"},
		{"empty token type", func(r *AuthRecord) { r.TokenType = "" }, "token type is required"},
		{"zero expiry", func(r *AuthRecord) { r.Expiry = time.Time{} }, "expiry is required"},
		{"zero obtained_at", func(r *AuthRecord) { r.ObtainedAt = time.Time{} }, "obtained_at is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record := base
			tt.mutate(&record)
			err := record.Validate()
			if err == nil {
				t.Fatalf("Validate() error = nil, want %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() error = %q, want to contain %q", err, tt.want)
			}
		})
	}

	if err := base.Validate(); err != nil {
		t.Fatalf("Validate() on a complete record error = %v, want nil", err)
	}
}

func TestLoadAuthUnexpectedReadError(t *testing.T) {
	// A directory at the auth-file path makes os.ReadFile fail with an error
	// that is neither ErrNotExist nor a JSON/validation problem.
	stateDir := t.TempDir()
	path := AuthFilePath(stateDir, "openai")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	_, err := LoadAuth(stateDir, "openai")
	if err == nil {
		t.Fatal("LoadAuth() error = nil, want read failure")
	}
	if errors.Is(err, ErrAuthNotFound) || errors.Is(err, ErrAuthCorrupt) {
		t.Fatalf("LoadAuth() error = %v, want a raw read error", err)
	}
}
