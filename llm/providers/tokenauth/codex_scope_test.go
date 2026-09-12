package tokenauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	authopenai "primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/llm/registry"
)

func TestCodexAuthScopeReadsTheInstanceRecord(t *testing.T) {
	stateDir := t.TempDir()
	record := authopenai.AuthRecord{Version: 1, Provider: "openai-codex", Source: authopenai.AuthSourceOAuth, TokenType: "Bearer", AccessToken: "tok", RefreshToken: "refresh", AccountID: "acct_1", WorkspaceID: "ws_1", ObtainedAt: time.Now(), Expiry: time.Now().Add(time.Hour)}
	if err := authopenai.SaveAuth(stateDir, "openai-codex", record); err != nil {
		t.Fatal(err)
	}
	c := &Codex{StateDir: stateDir}
	account, workspace, err := c.AuthScope(context.Background(), registry.Resolved{Instance: "openai-codex", Credential: registry.Credential{Source: "oauth"}})
	if err != nil || account != "acct_1" || workspace != "ws_1" {
		t.Fatalf("AuthScope: %q %q %v", account, workspace, err)
	}
	if _, _, err := c.AuthScope(context.Background(), registry.Resolved{Instance: "work", Credential: registry.Credential{Source: "none"}}); err == nil {
		t.Fatal("no record, no scope")
	}
}

// TestCodexAuthScopeFallsBackToTheIDTokenClaims covers a record written
// before the claims were persisted as their own fields: Apply's account
// lookup has always read them back off the id token, and the scope the
// continuation hashes must agree with it.
func TestCodexAuthScopeFallsBackToTheIDTokenClaims(t *testing.T) {
	stateDir := t.TempDir()
	record := authopenai.AuthRecord{Version: 1, Provider: "openai-codex", Source: authopenai.AuthSourceOAuth, TokenType: "Bearer", AccessToken: "tok", RefreshToken: "refresh", ObtainedAt: time.Now(), Expiry: time.Now().Add(time.Hour),
		IDToken: idTokenWithClaims(t, map[string]any{"chatgpt_account_id": "acct_claims", "workspace_id": "ws_claims"})}
	if err := authopenai.SaveAuth(stateDir, "openai-codex", record); err != nil {
		t.Fatal(err)
	}
	c := &Codex{StateDir: stateDir}
	res := registry.Resolved{Instance: "openai-codex", Credential: registry.Credential{Source: "oauth"}}
	account, workspace, err := c.AuthScope(context.Background(), res)
	if err != nil || account != "acct_claims" || workspace != "ws_claims" {
		t.Fatalf("AuthScope: %q %q %v", account, workspace, err)
	}
	if id := c.accountID("openai-codex"); id != account {
		t.Fatalf("the account Apply sends and the account the scope hashes must agree: %q vs %q", id, account)
	}
}

// idTokenWithClaims builds a JWT whose payload carries claims; only the
// payload segment is read back.
func idTokenWithClaims(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}
