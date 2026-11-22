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

// AuthRecord is the persisted OAuth record for one Copilot instance. It carries
// the GitHub access token and the exchanged Copilot session token. GitHub
// refresh fields are optional: the Pi-compatible identity returns a
// non-expiring GitHub token with no refresh token. Email, TokenType, Scope, and
// RefreshRejected are optional. There is no OAuth source field: the scheme
// names the source.
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
	CopilotToken    string    `json:"copilot_token"`
	CopilotExpiry   time.Time `json:"copilot_expiry"`
	AccountID       string    `json:"account_id"`
	Email           string    `json:"email,omitempty"`
	RefreshRejected bool      `json:"refresh_rejected,omitempty"`
}

// Validate reports whether the record is complete and bound to AuthScheme. A
// GitHub refresh token and refresh expiry are either both present or both
// absent; a non-expiring GitHub access token is valid.
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
	case r.CopilotToken == "":
		return errors.New("copilot session token is required")
	case r.AccountID == "":
		return errors.New("account ID is required")
	case r.ObtainedAt.IsZero():
		return errors.New("obtained_at is required")
	case r.CopilotExpiry.IsZero():
		return errors.New("copilot session expiry is required")
	case (r.RefreshToken == "") != r.RefreshExpiry.IsZero():
		return errors.New("refresh token and refresh expiry must be present together")
	}
	return nil
}

// SameGeneration reports whether other is the same record generation: the same
// refresh token and ObtainedAt. ObtainedAt advances on every login or
// successful refresh even when GitHub does not rotate the refresh token.
func (r AuthRecord) SameGeneration(other AuthRecord) bool {
	return r.RefreshToken == other.RefreshToken && r.ObtainedAt.Equal(other.ObtainedAt)
}

// NeedsLogin reports whether the record cannot be renewed without a fresh
// login: a credential was permanently rejected, the refresh token expired, or
// a non-refreshable GitHub access token expired. A non-expiring GitHub token
// never needs login on time alone.
func (r AuthRecord) NeedsLogin(now time.Time) bool {
	if r.RefreshRejected {
		return true
	}
	if r.RefreshExpiry.IsZero() {
		return !r.AccessExpiry.IsZero() && !r.AccessExpiry.After(now)
	}
	return !r.RefreshExpiry.After(now)
}
