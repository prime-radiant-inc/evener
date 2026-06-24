package llm

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func TestContinuationHandleHashFormatAndStability(t *testing.T) {
	hasher := NewContinuationHasher(bytes.Repeat([]byte{7}, 32))

	first, err := hasher.HashContinuationHandle("response_id", " resp_123 ")
	if err != nil {
		t.Fatalf("HashContinuationHandle: %v", err)
	}
	second, err := hasher.HashContinuationHandle("response_id", "resp_123")
	if err != nil {
		t.Fatalf("second HashContinuationHandle: %v", err)
	}
	if first != second {
		t.Fatalf("hash changed for normalized input: %q vs %q", first, second)
	}
	if !strings.HasPrefix(first, "cont-handle-v1:response_id:") {
		t.Fatalf("hash = %q, want cont-handle-v1 prefix", first)
	}
}

func TestContinuationScopeHashUsesSeparateSubkey(t *testing.T) {
	hasher := NewContinuationHasher(bytes.Repeat([]byte{8}, 32))

	handleHash, err := hasher.HashContinuationHandle("conversation_id", "shared-value")
	if err != nil {
		t.Fatalf("HashContinuationHandle: %v", err)
	}
	scopeHash, err := hasher.HashContinuationScopeValue("conversation_id", "shared-value")
	if err != nil {
		t.Fatalf("HashContinuationScopeValue: %v", err)
	}
	if handleHash == scopeHash {
		t.Fatalf("handle and scope hashes must use distinct subkeys")
	}
	if !strings.HasPrefix(scopeHash, "cont-scope-v1:conversation_id:") {
		t.Fatalf("scope hash = %q, want cont-scope-v1 prefix", scopeHash)
	}
}

func TestContinuationHashDoesNotLeakRawValue(t *testing.T) {
	hasher := NewContinuationHasher(bytes.Repeat([]byte{9}, 32))
	raw := "resp_sensitive_handle"

	hash, err := hasher.HashContinuationHandle("previous_response_id", raw)
	if err != nil {
		t.Fatalf("HashContinuationHandle: %v", err)
	}
	if strings.Contains(hash, raw) || strings.Contains(hash, "sensitive") {
		t.Fatalf("hash leaked raw value: %q", hash)
	}
}

func TestContinuationHashRejectsUnknownKind(t *testing.T) {
	hasher := NewContinuationHasher(bytes.Repeat([]byte{10}, 32))

	if _, err := hasher.HashContinuationHandle("api_key", "secret"); !errors.Is(err, ErrContinuationSecretUnavailable) {
		t.Fatalf("handle error = %v, want ErrContinuationSecretUnavailable", err)
	}
	if _, err := hasher.HashContinuationScopeValue("api_key", "secret"); !errors.Is(err, ErrContinuationSecretUnavailable) {
		t.Fatalf("scope error = %v, want ErrContinuationSecretUnavailable", err)
	}
}

func TestContinuationHasherForStateDirUnavailableWithoutState(t *testing.T) {
	_, err := ContinuationHasherForStateDir("")
	if !errors.Is(err, ErrContinuationSecretUnavailable) {
		t.Fatalf("error = %v, want ErrContinuationSecretUnavailable", err)
	}
}

func TestContinuationHasherForStateDirLoadsSecret(t *testing.T) {
	stateDir := t.TempDir()

	hasher, err := ContinuationHasherForStateDir(stateDir)
	if err != nil {
		t.Fatalf("ContinuationHasherForStateDir: %v", err)
	}
	hash, err := hasher.HashContinuationHandle("response_id", "resp_123")
	if err != nil {
		t.Fatalf("HashContinuationHandle: %v", err)
	}
	if !strings.HasPrefix(hash, "cont-handle-v1:response_id:") {
		t.Fatalf("hash = %q, want handle prefix", hash)
	}
	if _, err := os.Stat(ContinuationSecretPath(stateDir)); err != nil {
		t.Fatalf("secret was not persisted: %v", err)
	}
}
