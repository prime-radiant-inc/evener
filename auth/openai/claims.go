package openai

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidIDToken is returned (wrapped) by ParseIDTokenClaims when the ID
// token is malformed: it lacks a JWT payload segment, or the payload is not
// valid base64url-encoded JSON. An empty token is not an error.
var ErrInvalidIDToken = errors.New("invalid id token")

// TokenClaims contains display-oriented metadata parsed from an ID token.
type TokenClaims struct {
	Email       string
	AccountID   string
	WorkspaceID string
}

// ParseIDTokenClaims decodes a JWT payload for status metadata only.
func ParseIDTokenClaims(idToken string) (TokenClaims, error) {
	if strings.TrimSpace(idToken) == "" {
		return TokenClaims{}, nil
	}

	parts := strings.Split(idToken, ".")
	if len(parts) < 2 || parts[1] == "" {
		return TokenClaims{}, ErrInvalidIDToken
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return TokenClaims{}, fmt.Errorf("%w: decode payload: %v", ErrInvalidIDToken, err)
	}

	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return TokenClaims{}, fmt.Errorf("%w: decode claims: %v", ErrInvalidIDToken, err)
	}

	return TokenClaims{
		Email: firstNonEmpty(
			claimString(raw, "email"),
			claimNestedString(raw, "https://api.openai.com/profile", "email"),
		),
		AccountID: firstNonEmpty(
			claimString(raw, "chatgpt_account_id", "account_id", "account"),
			claimNestedString(raw, "https://api.openai.com/auth", "chatgpt_account_id", "account_id"),
		),
		WorkspaceID: firstNonEmpty(
			claimString(raw, "workspace_id", "workspace"),
			claimNestedString(raw, "https://api.openai.com/auth", "workspace_id"),
		),
	}, nil
}

func claimString(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := raw[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case string:
			return typed
		case map[string]any:
			if id, ok := typed["id"].(string); ok {
				return id
			}
		}
	}
	return ""
}

func claimNestedString(raw map[string]any, parent string, keys ...string) string {
	value, ok := raw[parent]
	if !ok {
		return ""
	}
	nested, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	return claimString(nested, keys...)
}
