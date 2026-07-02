package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	authopenai "primeradiant.com/serf/auth/openai"
	"primeradiant.com/serf/llm"
)

func TestClientHasProvider(t *testing.T) {
	if clientHasProvider(nil, "openai") {
		t.Error("nil client should never have a provider")
	}

	c := llm.NewClient()
	c.Register(&serfHelperAdapter{name: "openai"})
	if !clientHasProvider(c, "openai") {
		t.Error("expected registered provider to be found")
	}
	// Match is case-insensitive.
	if !clientHasProvider(c, "OpenAI") {
		t.Error("provider match should be case-insensitive")
	}
	if clientHasProvider(c, "anthropic") {
		t.Error("unregistered provider should not be found")
	}
}

type serfHelperAdapter struct{ name string }

func (a *serfHelperAdapter) Name() string { return a.name }
func (a *serfHelperAdapter) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("not implemented")
}
func (a *serfHelperAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, errors.New("not implemented")
}

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
