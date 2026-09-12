package main

import (
	"strings"
	"testing"
	"time"

	authopenai "primeradiant.com/evener/auth/openai"
)

func TestFormatOpenAIStatus(t *testing.T) {
	// Signed-out with no source falls back to the signed-out source label.
	out := formatOpenAIStatus(authopenai.AuthStatus{})
	if !strings.Contains(out, "state=signed-out") || !strings.Contains(out, "source="+authopenai.AuthSourceSignedOut) {
		t.Fatalf("signed-out status = %q", out)
	}
	// No OAuth-only fields when not an OAuth record.
	if strings.Contains(out, "needs_refresh") || strings.Contains(out, "needs_login") {
		t.Fatalf("non-oauth status should omit refresh/login flags: %q", out)
	}

	// Signed-in OAuth record with full metadata emits every field plus flags.
	oauth := authopenai.AuthStatus{
		SignedIn:     true,
		Source:       authopenai.AuthSourceOAuth,
		Email:        "a@example.com",
		AccountID:    "acct_1",
		WorkspaceID:  "ws_1",
		Expiry:       time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC),
		NeedsRefresh: true,
		NeedsLogin:   false,
	}
	out = formatOpenAIStatus(oauth)
	for _, want := range []string{
		"state=signed-in", "source=oauth", "email=a@example.com",
		"account_id=acct_1", "workspace_id=ws_1",
		"expiry=2030-01-02T03:04:05Z", "needs_refresh=true", "needs_login=false",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("oauth status missing %q; got %q", want, out)
		}
	}
}
