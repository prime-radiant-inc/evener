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

// refreshRejection is the note's content: which record the issuer's refusal
// was about — its refresh token, by SHA-256 (never the token itself), and its
// ObtainedAt — and when. ObtainedAt is a second key beside the token digest
// because an issuer that does not rotate refresh tokens can leave the token
// unchanged across a save: a concurrent refresh that raced the one being
// refused, and won, still moves ObtainedAt forward, so the note keyed on both
// stops matching once a newer record replaces the one it was about.
type refreshRejection struct {
	RefreshTokenSHA256 string    `json:"refresh_token_sha256"`
	RecordObtainedAt   time.Time `json:"record_obtained_at"`
	RejectedAt         time.Time `json:"rejected_at"`
}

func refreshRejectionPath(stateDir, instanceName string) string {
	return AuthFilePath(stateDir, instanceName) + refreshRejectionSuffix
}

func refreshTokenDigest(refreshToken string) string {
	sum := sha256.Sum256([]byte(refreshToken))
	return hex.EncodeToString(sum[:])
}

// RecordRefreshRejection notes that the issuer permanently refused record's
// refresh token for instanceName, so status can say signing in again is
// needed (#2479). It never rewrites the auth record: another process
// refreshing the same record may be saving the token that replaced this one,
// and rewriting the record could put the refused token back over it. The note
// names the record it was about (its refresh token and ObtainedAt), so it
// stops mattering once the record moves on to another.
func RecordRefreshRejection(stateDir, instanceName string, record AuthRecord, at time.Time) error {
	data, err := json.Marshal(refreshRejection{
		RefreshTokenSHA256: refreshTokenDigest(record.RefreshToken),
		RecordObtainedAt:   record.ObtainedAt.UTC(),
		RejectedAt:         at.UTC(),
	})
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
// one, or a note about a different record (another refresh token, or the same
// token on a record a concurrent successful refresh already moved past) is
// false.
func RefreshRejected(stateDir, instanceName string, record AuthRecord) bool {
	data, err := os.ReadFile(refreshRejectionPath(stateDir, instanceName))
	if err != nil {
		return false
	}
	var rejection refreshRejection
	if err := json.Unmarshal(data, &rejection); err != nil {
		return false
	}
	return rejection.RefreshTokenSHA256 == refreshTokenDigest(record.RefreshToken) &&
		rejection.RecordObtainedAt.Equal(record.ObtainedAt.UTC())
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
