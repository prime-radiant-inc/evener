package protocolhttp

import (
	"context"
	"net/http"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// codexOverrideAuthenticator records one wire preparation and writes the
// credential a Codex override carries.
type codexOverrideAuthenticator struct{ applied int }

func (a *codexOverrideAuthenticator) Apply(_ context.Context, req *http.Request, _ registry.Resolved) error {
	a.applied++
	req.Header.Set("Authorization", "Bearer codex-override")
	return nil
}

// TestAuthenticatorOverrideAuthenticatesCodexCall proves the library's
// single-authenticator override reaches a real request: Prepare must select it
// for the Codex scheme and send its header, while the same context answers
// nothing for another record-backed scheme.
func TestAuthenticatorOverrideAuthenticatesCodexCall(t *testing.T) {
	t.Parallel()
	auth := &codexOverrideAuthenticator{}
	ctx := llm.WithAuthenticatorOverride(context.Background(), auth)
	res := registry.Resolved{
		Instance:  "work",
		Protocol:  registry.ProtocolOpenAIResponses,
		Transport: registry.Transport{Auth: registry.AuthOAuthOpenAICodex, BaseURL: "https://example.test", Endpoint: "/responses"},
	}
	prepared, err := Prepare(ctx, &Call{
		Operation: "responses.create", Method: http.MethodPost,
		URL:  URL(res, res.Transport.Endpoint),
		Body: map[string]any{"input": "hi"},
		Res:  res,
	})
	if err != nil {
		t.Fatalf("Prepare with a Codex override: %v", err)
	}
	if auth.applied != 1 {
		t.Fatalf("override applications = %d, want 1", auth.applied)
	}
	if got, want := prepared.Request.Header.Get("Authorization"), "Bearer codex-override"; got != want {
		t.Fatalf("Authorization = %q, want %q", got, want)
	}

	copilot := res
	copilot.Transport.Auth = registry.AuthOAuthGitHubCopilot
	if _, err := Prepare(ctx, &Call{Method: http.MethodGet, URL: URL(copilot, "/models"), Res: copilot}); err == nil {
		t.Fatal("a Codex-bound override authenticated a Copilot request")
	}
}
