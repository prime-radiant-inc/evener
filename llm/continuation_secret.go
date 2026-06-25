package llm

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrContinuationSecretUnavailable wraps every failure to load, create, or use
// the continuation secret.
var ErrContinuationSecretUnavailable = errors.New("continuation secret unavailable")

// ContinuationScopeHashVersion is the version prefix tagged onto continuation
// scope and storage-scope HMACs.
const ContinuationScopeHashVersion = "cont-scope-v1"

// ContinuationHasher produces stable, redacted HMACs of continuation handles
// and storage-scope values using subkeys derived from a per-state-dir secret.
type ContinuationHasher struct {
	redactionKey []byte
	scopeKey     []byte
}

// NewContinuationHasher returns a ContinuationHasher whose redaction and scope
// subkeys are HMAC-derived from secret.
func NewContinuationHasher(secret []byte) *ContinuationHasher {
	return &ContinuationHasher{
		redactionKey: deriveContinuationSubkey(secret, "serf-continuation-redaction-v1"),
		scopeKey:     deriveContinuationSubkey(secret, "serf-continuation-scope-v1"),
	}
}

// ContinuationHasherForStateDir loads or creates the continuation secret under
// stateDir and returns a ContinuationHasher keyed from it.
func ContinuationHasherForStateDir(stateDir string) (*ContinuationHasher, error) {
	secret, err := LoadOrCreateContinuationSecret(stateDir)
	if err != nil {
		return nil, err
	}
	return NewContinuationHasher(secret), nil
}

func (h *ContinuationHasher) HashContinuationHandle(kind, value string) (string, error) {
	if !validContinuationHandleKind(kind) {
		return "", fmt.Errorf("%w: unknown handle kind %q", ErrContinuationSecretUnavailable, kind)
	}
	return versionedContinuationHMAC("cont-handle-v1", kind, h.redactionKey, value), nil
}

func (h *ContinuationHasher) HashContinuationScopeValue(kind, value string) (string, error) {
	if !validContinuationScopeKind(kind) {
		return "", fmt.Errorf("%w: unknown scope kind %q", ErrContinuationSecretUnavailable, kind)
	}
	return versionedContinuationHMAC(ContinuationScopeHashVersion, kind, h.scopeKey, value), nil
}

func (h *ContinuationHasher) HashContinuationStorageScope(scope ContinuationStorageScope) (string, error) {
	if h == nil {
		return "", fmt.Errorf("%w: missing continuation hasher", ErrContinuationSecretUnavailable)
	}
	input := continuationStorageScopeFingerprintInput{
		Provider:           strings.TrimSpace(scope.Provider),
		EndpointFamily:     strings.TrimSpace(scope.EndpointFamily),
		BaseURL:            strings.TrimSpace(scope.BaseURL),
		Path:               strings.TrimSpace(scope.Path),
		AuthSource:         strings.TrimSpace(scope.AuthSource),
		OrgIDHash:          strings.TrimSpace(scope.OrgIDHash),
		ProjectIDHash:      strings.TrimSpace(scope.ProjectIDHash),
		AccountHash:        strings.TrimSpace(scope.AccountHash),
		WorkspaceHash:      strings.TrimSpace(scope.WorkspaceHash),
		CredentialHash:     strings.TrimSpace(scope.CredentialHash),
		ConversationIDHash: strings.TrimSpace(scope.ConversationIDHash),
		StoragePolicy:      strings.TrimSpace(scope.StoragePolicy),
	}
	b, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("%w: marshal storage scope: %w", ErrContinuationSecretUnavailable, err)
	}
	return versionedContinuationHMAC(ContinuationScopeHashVersion, "storage_scope", h.scopeKey, string(b)), nil
}

// ContinuationSecretPath returns the file path of the continuation scope secret
// under stateDir, or "" when stateDir is empty.
func ContinuationSecretPath(stateDir string) string {
	if stateDir == "" {
		return ""
	}
	return filepath.Join(stateDir, "continuation", "local_scope_secret")
}

// LoadOrCreateContinuationSecret reads the 32-byte continuation secret under
// stateDir, creating it (mode 0600) if absent, and returns
// ErrContinuationSecretUnavailable on failure.
func LoadOrCreateContinuationSecret(stateDir string) ([]byte, error) {
	path := ContinuationSecretPath(stateDir)
	if path == "" {
		return nil, fmt.Errorf("%w: missing state dir", ErrContinuationSecretUnavailable)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("%w: create secret dir: %w", ErrContinuationSecretUnavailable, err)
	}

	secret, err := readContinuationSecret(path)
	if err == nil {
		return secret, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	secret = make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("%w: generate secret: %w", ErrContinuationSecretUnavailable, err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return readContinuationSecret(path)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: create secret file: %w", ErrContinuationSecretUnavailable, err)
	}
	defer f.Close() //nolint:errcheck
	if _, err := f.Write(secret); err != nil {
		return nil, fmt.Errorf("%w: write secret file: %w", ErrContinuationSecretUnavailable, err)
	}
	if err := f.Sync(); err != nil {
		return nil, fmt.Errorf("%w: sync secret file: %w", ErrContinuationSecretUnavailable, err)
	}
	return secret, nil
}

func readContinuationSecret(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("%w: secret file mode %o", ErrContinuationSecretUnavailable, info.Mode().Perm())
	}
	secret, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: read secret file: %w", ErrContinuationSecretUnavailable, err)
	}
	if len(secret) != 32 {
		return nil, fmt.Errorf("%w: secret length %d", ErrContinuationSecretUnavailable, len(secret))
	}
	return secret, nil
}

func deriveContinuationSubkey(secret []byte, label string) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(label)) //nolint:errcheck
	return mac.Sum(nil)
}

type continuationStorageScopeFingerprintInput struct {
	Provider           string `json:"provider,omitempty"`
	EndpointFamily     string `json:"endpoint_family,omitempty"`
	BaseURL            string `json:"base_url,omitempty"`
	Path               string `json:"path,omitempty"`
	AuthSource         string `json:"auth_source,omitempty"`
	OrgIDHash          string `json:"org_id_hash,omitempty"`
	ProjectIDHash      string `json:"project_id_hash,omitempty"`
	AccountHash        string `json:"account_hash,omitempty"`
	WorkspaceHash      string `json:"workspace_hash,omitempty"`
	CredentialHash     string `json:"credential_hash,omitempty"`
	ConversationIDHash string `json:"conversation_id_hash,omitempty"`
	StoragePolicy      string `json:"storage_policy,omitempty"`
}

func versionedContinuationHMAC(prefix, kind string, key []byte, value string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(strings.TrimSpace(value))) //nolint:errcheck
	sum := mac.Sum(nil)
	return prefix + ":" + kind + ":" + base64.RawURLEncoding.EncodeToString(sum)
}

func validContinuationHandleKind(kind string) bool {
	switch kind {
	case "response_id", "previous_response_id", "conversation_id":
		return true
	default:
		return false
	}
}

func validContinuationScopeKind(kind string) bool {
	switch kind {
	case "credential", "account", "workspace", "org_id", "project_id", "conversation_id":
		return true
	default:
		return false
	}
}
