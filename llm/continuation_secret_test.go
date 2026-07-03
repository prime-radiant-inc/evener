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
	writeContinuationSecretForMode(t, path, bytes.Repeat([]byte{1}, 32), 0o644)

	_, err := LoadOrCreateContinuationSecret(stateDir)
	if !errors.Is(err, ErrContinuationSecretUnavailable) {
		t.Fatalf("error = %v, want ErrContinuationSecretUnavailable", err)
	}
}

func writeContinuationSecretForMode(t testing.TB, path string, secret []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, secret, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("Chmod: %v", err)
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

func TestContinuationHasher_StorageScopeFingerprintFormatStabilityAndSecret(t *testing.T) {
	hasher := NewContinuationHasher(bytes.Repeat([]byte{8}, 32))
	scope := continuationStorageScopeForTest()

	first, err := hasher.HashContinuationStorageScope(scope)
	if err != nil {
		t.Fatalf("HashContinuationStorageScope: %v", err)
	}
	second, err := hasher.HashContinuationStorageScope(scope)
	if err != nil {
		t.Fatalf("second HashContinuationStorageScope: %v", err)
	}
	if first != second {
		t.Fatalf("storage scope fingerprint changed: %q vs %q", first, second)
	}
	if !strings.HasPrefix(first, "cont-scope-v1:storage_scope:") {
		t.Fatalf("storage scope fingerprint = %q, want cont-scope-v1:storage_scope prefix", first)
	}

	handleHash, err := hasher.HashContinuationHandle("conversation_id", "conversation-hash")
	if err != nil {
		t.Fatalf("HashContinuationHandle: %v", err)
	}
	if first == handleHash {
		t.Fatalf("storage scope fingerprint must not use provider-handle redaction hash")
	}

	otherHasher := NewContinuationHasher(bytes.Repeat([]byte{9}, 32))
	other, err := otherHasher.HashContinuationStorageScope(scope)
	if err != nil {
		t.Fatalf("other HashContinuationStorageScope: %v", err)
	}
	if other == first {
		t.Fatalf("storage scope fingerprint did not change after root secret changed: %s", first)
	}
}

func TestContinuationHasher_StorageScopeFingerprintChangesForScopeFields(t *testing.T) {
	hasher := NewContinuationHasher(bytes.Repeat([]byte{8}, 32))
	base := continuationStorageScopeForTest()
	wantDifferentFrom, err := hasher.HashContinuationStorageScope(base)
	if err != nil {
		t.Fatalf("HashContinuationStorageScope: %v", err)
	}

	cases := []struct {
		name string
		edit func(*ContinuationStorageScope)
	}{
		{name: "provider", edit: func(s *ContinuationStorageScope) { s.Provider = "other" }},
		{name: "endpoint family", edit: func(s *ContinuationStorageScope) { s.EndpointFamily = "openai_codex" }},
		{name: "base url", edit: func(s *ContinuationStorageScope) { s.BaseURL = "https://proxy.example.com" }},
		{name: "path", edit: func(s *ContinuationStorageScope) { s.Path = "/other/responses" }},
		{name: "auth source", edit: func(s *ContinuationStorageScope) { s.AuthSource = "oauth" }},
		{name: "org hash", edit: func(s *ContinuationStorageScope) { s.OrgIDHash = "cont-scope-v1:org_id:other" }},
		{name: "project hash", edit: func(s *ContinuationStorageScope) { s.ProjectIDHash = "cont-scope-v1:project_id:other" }},
		{name: "account hash", edit: func(s *ContinuationStorageScope) { s.AccountHash = "cont-scope-v1:account:other" }},
		{name: "workspace hash", edit: func(s *ContinuationStorageScope) { s.WorkspaceHash = "cont-scope-v1:workspace:other" }},
		{name: "credential hash", edit: func(s *ContinuationStorageScope) { s.CredentialHash = "cont-scope-v1:credential:other" }},
		{name: "conversation hash", edit: func(s *ContinuationStorageScope) { s.ConversationIDHash = "cont-scope-v1:conversation_id:other" }},
		{name: "storage policy", edit: func(s *ContinuationStorageScope) { s.StoragePolicy = "public-openai-no-store" }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scope := continuationStorageScopeForTest()
			tc.edit(&scope)
			got, err := hasher.HashContinuationStorageScope(scope)
			if err != nil {
				t.Fatalf("HashContinuationStorageScope: %v", err)
			}
			if got == wantDifferentFrom {
				t.Fatalf("storage scope fingerprint did not change for %s: %s", tc.name, got)
			}
		})
	}
}

func TestContinuationHasher_StorageScopeFingerprintRequiresHasher(t *testing.T) {
	var hasher *ContinuationHasher
	_, err := hasher.HashContinuationStorageScope(continuationStorageScopeForTest())
	if !errors.Is(err, ErrContinuationSecretUnavailable) {
		t.Fatalf("error = %v, want ErrContinuationSecretUnavailable", err)
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

func continuationStorageScopeForTest() ContinuationStorageScope {
	return ContinuationStorageScope{
		HashVersion:        "cont-scope-v1",
		Provider:           "openai",
		EndpointFamily:     "openai_public",
		BaseURL:            "https://api.openai.com",
		Path:               "/v1/responses",
		AuthSource:         "api_key",
		OrgIDHash:          "cont-scope-v1:org_id:abc",
		ProjectIDHash:      "cont-scope-v1:project_id:def",
		AccountHash:        "cont-scope-v1:account:ghi",
		WorkspaceHash:      "cont-scope-v1:workspace:jkl",
		CredentialHash:     "cont-scope-v1:credential:mno",
		ConversationIDHash: "cont-scope-v1:conversation_id:pqr",
		StoragePolicy:      "public-openai-store",
	}
}
