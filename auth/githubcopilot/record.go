// Package githubcopilot stores Evener's GitHub Copilot subscription credentials
// in a scheme-namespaced record shared by login, refresh, the hub, and the CLI.
package githubcopilot

import (
	"errors"
	"fmt"
	"time"
)

// AuthScheme is the auth scheme of a Copilot record. Validation rejects any
// other scheme, so another provider's record cannot be used as credentials.
const AuthScheme = "oauth-github-copilot"

// AuthRecord is the persisted OAuth record for one Copilot instance. Email and
// RefreshRejected are optional; every other field is required. There is no
// OAuth source field: the scheme names the source.
type AuthRecord struct {
	Instance        string    `json:"instance"`
	Scheme          string    `json:"auth_scheme"`
	Version         int       `json:"version"`
	AccessToken     string    `json:"access_token"`
	RefreshToken    string    `json:"refresh_token"`
	TokenType       string    `json:"token_type"`
	Scope           string    `json:"scope"`
	ObtainedAt      time.Time `json:"obtained_at"`
	AccessExpiry    time.Time `json:"access_expiry"`
	RefreshExpiry   time.Time `json:"refresh_expiry"`
	AccountID       string    `json:"account_id"`
	Email           string    `json:"email,omitempty"`
	RefreshRejected bool      `json:"refresh_rejected,omitempty"`
}

// Validate reports whether the record is complete and bound to AuthScheme.
func (r AuthRecord) Validate() error {
	switch {
	case r.Scheme != AuthScheme:
		return fmt.Errorf("auth scheme %q is not %q", r.Scheme, AuthScheme)
	case r.Version != 1:
		return fmt.Errorf("unsupported auth record version %d", r.Version)
	case r.Instance == "":
		return errors.New("instance is required")
	case r.AccessToken == "":
		return errors.New("access token is required")
	case r.RefreshToken == "":
		return errors.New("refresh token is required")
	case r.TokenType == "":
		return errors.New("token type is required")
	case r.Scope == "":
		return errors.New("scope is required")
	case r.AccountID == "":
		return errors.New("account ID is required")
	case r.ObtainedAt.IsZero():
		return errors.New("obtained_at is required")
	case r.AccessExpiry.IsZero():
		return errors.New("access expiry is required")
	case r.RefreshExpiry.IsZero():
		return errors.New("refresh expiry is required")
	}
	return nil
}

// SameGeneration reports whether other is the same record generation: the same
// refresh token and ObtainedAt. ObtainedAt advances on every login or
// successful refresh even when GitHub does not rotate the refresh token.
func (r AuthRecord) SameGeneration(other AuthRecord) bool {
	return r.RefreshToken == other.RefreshToken && r.ObtainedAt.Equal(other.ObtainedAt)
}

// NeedsLogin reports whether the record cannot be refreshed without a fresh
// login: GitHub refused its refresh token or the refresh token expired.
func (r AuthRecord) NeedsLogin(now time.Time) bool {
	return r.RefreshRejected || r.RefreshExpiry.IsZero() || !r.RefreshExpiry.After(now)
}
