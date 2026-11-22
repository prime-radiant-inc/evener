// Package githubcopilot stores Evener's GitHub Copilot subscription
// credentials in a scheme-namespaced record that device login, refresh, the
// hub, and the CLI share.
package githubcopilot

import (
	"errors"
	"fmt"
	"time"
)

// AuthScheme is the auth scheme of a GitHub Copilot subscription record. Every
// record carries it and validation rejects any other scheme, so a record
// written for another provider can never be used as Copilot credentials.
const AuthScheme = "oauth-github-copilot"

const recordVersion = 1

// AuthRecord is the persisted OAuth record for one Copilot instance. Email and
// RefreshRejected are optional state; every other field is required. The
// record deliberately has no OAuth source field: the scheme names the source.
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
	case r.Instance == "":
		return errors.New("instance is required")
	case r.Scheme != AuthScheme:
		return fmt.Errorf("auth scheme %q is not %q", r.Scheme, AuthScheme)
	case r.Version != recordVersion:
		return fmt.Errorf("unsupported auth record version %d", r.Version)
	case r.AccessToken == "":
		return errors.New("access token is required")
	case r.RefreshToken == "":
		return errors.New("refresh token is required")
	case r.TokenType == "":
		return errors.New("token type is required")
	case r.Scope == "":
		return errors.New("scope is required")
	case r.ObtainedAt.IsZero():
		return errors.New("obtained_at is required")
	case r.AccessExpiry.IsZero():
		return errors.New("access expiry is required")
	case r.RefreshExpiry.IsZero():
		return errors.New("refresh expiry is required")
	case r.AccountID == "":
		return errors.New("account ID is required")
	}
	return nil
}
