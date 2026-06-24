package llm

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestContinuationSecretLoadOrCreateCreatesPrivateFile(t *testing.T) {
	stateDir := t.TempDir()

	secret, err := LoadOrCreateContinuationSecret(stateDir)
	if err != nil {
		t.Fatalf("LoadOrCreateContinuationSecret: %v", err)
	}
	if len(secret) != 32 {
		t.Fatalf("secret length = %d, want 32", len(secret))
	}

	path := ContinuationSecretPath(stateDir)
	if path != filepath.Join(stateDir, "continuation", "local_scope_secret") {
		t.Fatalf("ContinuationSecretPath = %q", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat secret: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("secret mode = %o, want 0600", got)
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat secret dir: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got&0o077 != 0 {
		t.Fatalf("secret dir mode = %o, want no group/world bits", got)
	}
}

func TestContinuationSecretLoadOrCreateReusesExistingSecret(t *testing.T) {
	stateDir := t.TempDir()

	first, err := LoadOrCreateContinuationSecret(stateDir)
	if err != nil {
		t.Fatalf("first LoadOrCreateContinuationSecret: %v", err)
	}
	second, err := LoadOrCreateContinuationSecret(stateDir)
	if err != nil {
		t.Fatalf("second LoadOrCreateContinuationSecret: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("secret changed across loads")
	}
}

func TestContinuationSecretRequiresStateDir(t *testing.T) {
	_, err := LoadOrCreateContinuationSecret("")
	if !errors.Is(err, ErrContinuationSecretUnavailable) {
		t.Fatalf("error = %v, want ErrContinuationSecretUnavailable", err)
	}
}

func TestContinuationSecretRejectsWrongPermissions(t *testing.T) {
	stateDir := t.TempDir()
	path := ContinuationSecretPath(stateDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte{1}, 32), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := LoadOrCreateContinuationSecret(stateDir)
	if !errors.Is(err, ErrContinuationSecretUnavailable) {
		t.Fatalf("error = %v, want ErrContinuationSecretUnavailable", err)
	}
}
