package openai

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/envvars"
)

// TestDefaultStateDirWithStateHomeFallsBackToTempDir covers the arm where the
// home directory cannot be resolved (HOME unset), so the state dir is rooted at
// the OS temp dir.
func TestDefaultStateDirWithStateHomeFallsBackToTempDir(t *testing.T) {
	t.Setenv(envvars.XDGStateHome.Name, "")
	t.Setenv("HOME", "")

	if _, err := os.UserHomeDir(); err == nil {
		t.Skip("os.UserHomeDir still resolves with HOME unset on this platform")
	}

	want := filepath.Join(os.TempDir(), ".local", "state", "serf")
	if got := DefaultStateDirWithStateHome(""); got != want {
		t.Fatalf("DefaultStateDirWithStateHome(\"\") = %q, want %q", got, want)
	}
}

// TestSaveAuthTempFileCreationFailure covers the create-temp-file error arm: a
// read-only auth directory allows MkdirAll (the dir already exists) but denies
// the temp-file creation.
func TestSaveAuthTempFileCreationFailure(t *testing.T) {
	requireNonRoot(t)
	stateDir := t.TempDir()
	authDir := filepath.Join(stateDir, authDirName)
	if err := os.MkdirAll(authDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.Chmod(authDir, 0o500); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(authDir, 0o755) })

	err := SaveAuth(stateDir, "openai", sampleAuthRecord())
	if err == nil {
		t.Fatal("SaveAuth() error = nil, want temp-file creation failure")
	}
}

// TestResolveRuntimeCredentialsSaveFailureAfterRefresh covers the persist arm
// after a successful refresh: the record loads fine (read allowed) but the
// refreshed record cannot be written back to a read-only auth directory.
func TestResolveRuntimeCredentialsSaveFailureAfterRefresh(t *testing.T) {
	requireNonRoot(t)
	stateDir := t.TempDir()
	now := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)

	record := sampleAuthRecord()
	record.Expiry = now.Add(time.Minute) // near expiry so a refresh runs
	if err := SaveAuth(stateDir, "openai", record); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}

	authDir := filepath.Join(stateDir, authDirName)
	if err := os.Chmod(authDir, 0o500); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(authDir, 0o755) })

	svc := newTestService(now)
	svc.refreshToken = func(context.Context, *http.Client, Config, RefreshTokenRequest) (TokenSet, error) {
		return TokenSet{
			AccessToken:  "fresh-access",
			RefreshToken: "fresh-refresh",
			TokenType:    "Bearer",
			Scope:        "openid",
			Expiry:       now.Add(time.Hour),
		}, nil
	}

	_, err := svc.ResolveRuntimeCredentials(context.Background(), stateDir, "openai")
	if err == nil {
		t.Fatal("ResolveRuntimeCredentials() error = nil, want save failure after refresh")
	}
	if errors.Is(err, ErrLoginRequired) {
		t.Fatalf("save failure should not be reported as login-required: %v", err)
	}
}

func requireNonRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses filesystem permission checks")
	}
}
