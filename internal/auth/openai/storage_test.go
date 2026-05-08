package openai

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func sampleAuthRecord() AuthRecord {
	return AuthRecord{
		Version:      1,
		Provider:     "openai",
		Source:       "oauth",
		ObtainedAt:   time.Date(2026, 5, 7, 23, 0, 0, 0, time.UTC),
		TokenType:    "Bearer",
		Scope:        "openid profile email offline_access",
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		IDToken:      "id-token",
		Expiry:       time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC),
		Email:        "user@example.com",
		AccountID:    "acct_123",
		WorkspaceID:  "ws_123",
	}
}

func TestAuthStorageSaveWritesExpectedPath(t *testing.T) {
	stateDir := t.TempDir()
	record := sampleAuthRecord()

	if err := SaveAuth(stateDir, record); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}

	authPath := AuthFilePath(stateDir)
	if _, err := os.Stat(authPath); err != nil {
		t.Fatalf("Stat(%q) error = %v", authPath, err)
	}
	if want := filepath.Join(stateDir, "auth", "openai.json"); authPath != want {
		t.Fatalf("AuthFilePath() = %q, want %q", authPath, want)
	}
}

func TestAuthStorageSaveIsAtomic(t *testing.T) {
	stateDir := t.TempDir()
	record := sampleAuthRecord()

	if err := SaveAuth(stateDir, record); err != nil {
		t.Fatalf("SaveAuth() first save error = %v", err)
	}

	record.AccessToken = "new-access-token"
	if err := SaveAuth(stateDir, record); err != nil {
		t.Fatalf("SaveAuth() second save error = %v", err)
	}

	loaded, err := LoadAuth(stateDir)
	if err != nil {
		t.Fatalf("LoadAuth() error = %v", err)
	}
	if loaded.AccessToken != "new-access-token" {
		t.Fatalf("loaded.AccessToken = %q, want %q", loaded.AccessToken, "new-access-token")
	}

	matches, err := filepath.Glob(filepath.Join(stateDir, "auth", "openai.json.*"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary files left behind: %v", matches)
	}
}

func TestAuthStorageLoadReturnsNotFoundCleanly(t *testing.T) {
	_, err := LoadAuth(t.TempDir())
	if !errors.Is(err, ErrAuthNotFound) {
		t.Fatalf("LoadAuth() error = %v, want ErrAuthNotFound", err)
	}
}

func TestAuthStorageDeleteRemovesRecord(t *testing.T) {
	stateDir := t.TempDir()
	record := sampleAuthRecord()

	if err := SaveAuth(stateDir, record); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}

	deleted, err := DeleteAuth(stateDir)
	if err != nil {
		t.Fatalf("DeleteAuth() error = %v", err)
	}
	if !deleted {
		t.Fatal("DeleteAuth() deleted = false, want true")
	}
	if _, err := LoadAuth(stateDir); !errors.Is(err, ErrAuthNotFound) {
		t.Fatalf("LoadAuth() after delete error = %v, want ErrAuthNotFound", err)
	}
}

func TestAuthStorageLoadReturnsCorruptionErrorForMalformedJSON(t *testing.T) {
	stateDir := t.TempDir()
	authPath := AuthFilePath(stateDir)
	if err := os.MkdirAll(filepath.Dir(authPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(authPath, []byte("{not-json"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := LoadAuth(stateDir)
	if !errors.Is(err, ErrAuthCorrupt) {
		t.Fatalf("LoadAuth() error = %v, want ErrAuthCorrupt", err)
	}
}

func TestAuthStorageLoadRejectsUnsupportedVersion(t *testing.T) {
	stateDir := t.TempDir()
	record := sampleAuthRecord()
	record.Version = 99
	writeAuthFixture(t, stateDir, `{
  "version": 99,
  "provider": "openai",
  "source": "oauth",
  "obtained_at": "2026-05-07T23:00:00Z",
  "token_type": "Bearer",
  "scope": "openid profile email offline_access",
  "access_token": "access-token",
  "refresh_token": "refresh-token",
  "expiry": "2026-05-08T00:00:00Z"
}`)

	_, err := LoadAuth(stateDir)
	if !errors.Is(err, ErrAuthCorrupt) {
		t.Fatalf("LoadAuth() error = %v, want ErrAuthCorrupt", err)
	}
}

func TestAuthStorageLoadRejectsWrongProvider(t *testing.T) {
	stateDir := t.TempDir()
	writeAuthFixture(t, stateDir, `{
  "version": 1,
  "provider": "anthropic",
  "source": "oauth",
  "obtained_at": "2026-05-07T23:00:00Z",
  "token_type": "Bearer",
  "scope": "openid profile email offline_access",
  "access_token": "access-token",
  "refresh_token": "refresh-token",
  "expiry": "2026-05-08T00:00:00Z"
}`)

	_, err := LoadAuth(stateDir)
	if !errors.Is(err, ErrAuthCorrupt) {
		t.Fatalf("LoadAuth() error = %v, want ErrAuthCorrupt", err)
	}
}

func TestAuthStorageLoadRejectsMissingAccessToken(t *testing.T) {
	stateDir := t.TempDir()
	writeAuthFixture(t, stateDir, `{
  "version": 1,
  "provider": "openai",
  "source": "oauth",
  "obtained_at": "2026-05-07T23:00:00Z",
  "token_type": "Bearer",
  "scope": "openid profile email offline_access",
  "refresh_token": "refresh-token",
  "expiry": "2026-05-08T00:00:00Z"
}`)

	_, err := LoadAuth(stateDir)
	if !errors.Is(err, ErrAuthCorrupt) {
		t.Fatalf("LoadAuth() error = %v, want ErrAuthCorrupt", err)
	}
}

func TestAuthStorageSaveUsesOwnerOnlyPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not portable on windows")
	}

	stateDir := t.TempDir()
	record := sampleAuthRecord()
	if err := SaveAuth(stateDir, record); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}

	info, err := os.Stat(AuthFilePath(stateDir))
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("file permissions = %#o, want %#o", got, 0o600)
	}
}

func writeAuthFixture(t *testing.T, stateDir, contents string) {
	t.Helper()

	authPath := AuthFilePath(stateDir)
	if err := os.MkdirAll(filepath.Dir(authPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(authPath, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}
