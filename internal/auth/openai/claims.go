package openai

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

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
		Email:       claimString(raw, "email"),
		AccountID:   claimString(raw, "account_id", "account"),
		WorkspaceID: claimString(raw, "workspace_id", "workspace"),
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
