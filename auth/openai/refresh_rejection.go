package openai

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// refreshRejectionSuffix names the note beside an instance's auth record that
// says the issuer permanently refused the record's refresh token (#2479). It
// is appended to the record's own path, so the note does not end in ".json"
// and can never be another instance's record.
const refreshRejectionSuffix = ".refresh-rejected"

// refreshRejection is the note's content: which refresh token the issuer
// refused, by its SHA-256 (never the token itself), and when.
type refreshRejection struct {
	RefreshTokenSHA256 string    `json:"refresh_token_sha256"`
	RejectedAt         time.Time `json:"rejected_at"`
}

func refreshRejectionPath(stateDir, instanceName string) string {
	return AuthFilePath(stateDir, instanceName) + refreshRejectionSuffix
}

func refreshTokenDigest(refreshToken string) string {
	sum := sha256.Sum256([]byte(refreshToken))
	return hex.EncodeToString(sum[:])
}

// RecordRefreshRejection notes that the issuer permanently refused
// refreshToken for instanceName, so status can say signing in again is needed
// (#2479). It never rewrites the auth record: another process refreshing the
// same record may be saving the token that replaced this one, and rewriting
// the record could put the refused token back over it. The note names the
// refused token, so it stops mattering once the record holds another.
func RecordRefreshRejection(stateDir, instanceName, refreshToken string, at time.Time) error {
	data, err := json.Marshal(refreshRejection{RefreshTokenSHA256: refreshTokenDigest(refreshToken), RejectedAt: at.UTC()})
	if err != nil {
		return fmt.Errorf("marshal refresh rejection: %w", err)
	}
	path := refreshRejectionPath(stateDir, instanceName)
	if err := authMkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create auth directory: %w", err)
	}
	return WriteAuthFile(path, append(data, '\n'))
}

// RefreshRejected reports whether the issuer permanently refused record's
// refresh token, as RecordRefreshRejection noted it. No note, an unreadable
// one, or a note about any other token is false.
func RefreshRejected(stateDir, instanceName string, record AuthRecord) bool {
	data, err := os.ReadFile(refreshRejectionPath(stateDir, instanceName))
	if err != nil {
		return false
	}
	var rejection refreshRejection
	if err := json.Unmarshal(data, &rejection); err != nil {
		return false
	}
	return rejection.RefreshTokenSHA256 == refreshTokenDigest(record.RefreshToken)
}

// clearRefreshRejection removes instanceName's note, if there is one. SaveAuth
// and DeleteAuth call it best effort: a note left behind names a token that
// only an issuer that never rotates refresh tokens keeps on the record, and
// the next save clears it again.
func clearRefreshRejection(stateDir, instanceName string) error {
	if err := authRemove(refreshRejectionPath(stateDir, instanceName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clear refresh rejection: %w", err)
	}
	return nil
}
