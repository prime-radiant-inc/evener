package openai

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
)

func TestTokenClaimsParsesEmailFromIDToken(t *testing.T) {
	claims, err := ParseIDTokenClaims(testJWT(t, map[string]any{
		"email": "user@example.com",
	}))
	if err != nil {
		t.Fatalf("ParseIDTokenClaims() error = %v", err)
	}
	if claims.Email != "user@example.com" {
		t.Fatalf("Email = %q, want %q", claims.Email, "user@example.com")
	}
}

func TestTokenClaimsParsesAccountAndWorkspaceMetadata(t *testing.T) {
	claims, err := ParseIDTokenClaims(testJWT(t, map[string]any{
		"account_id":   "acct_123",
		"workspace_id": "ws_123",
	}))
	if err != nil {
		t.Fatalf("ParseIDTokenClaims() error = %v", err)
	}
	if claims.AccountID != "acct_123" {
		t.Fatalf("AccountID = %q, want %q", claims.AccountID, "acct_123")
	}
	if claims.WorkspaceID != "ws_123" {
		t.Fatalf("WorkspaceID = %q, want %q", claims.WorkspaceID, "ws_123")
	}
}

func TestTokenClaimsParsesChatGPTAccountID(t *testing.T) {
	claims, err := ParseIDTokenClaims(testJWT(t, map[string]any{
		"chatgpt_account_id": "acct_chatgpt",
	}))
	if err != nil {
		t.Fatalf("ParseIDTokenClaims() error = %v", err)
	}
	if claims.AccountID != "acct_chatgpt" {
		t.Fatalf("AccountID = %q, want %q", claims.AccountID, "acct_chatgpt")
	}
}

func TestTokenClaimsEmptyIDTokenIsBestEffort(t *testing.T) {
	claims, err := ParseIDTokenClaims("")
	if err != nil {
		t.Fatalf("ParseIDTokenClaims() error = %v, want nil", err)
	}
	if claims != (TokenClaims{}) {
		t.Fatalf("claims = %+v, want zero value", claims)
	}
}

func TestTokenClaimsMalformedIDTokenReturnsSentinel(t *testing.T) {
	_, err := ParseIDTokenClaims("not-a-jwt")
	if !errors.Is(err, ErrInvalidIDToken) {
		t.Fatalf("ParseIDTokenClaims() error = %v, want ErrInvalidIDToken", err)
	}
}

func testJWT(t *testing.T, payload map[string]any) string {
	t.Helper()

	headerBytes, err := json.Marshal(map[string]any{
		"alg": "none",
		"typ": "JWT",
	})
	if err != nil {
		t.Fatalf("Marshal(header) error = %v", err)
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal(payload) error = %v", err)
	}

	return base64.RawURLEncoding.EncodeToString(headerBytes) + "." +
		base64.RawURLEncoding.EncodeToString(payloadBytes) + "."
}
