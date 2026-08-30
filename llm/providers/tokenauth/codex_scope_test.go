package tokenauth

import (
	"context"
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
