package tokenauth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	authopenai "primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func codexState(t *testing.T, instance, accountID string) string {
	t.Helper()
	dir := t.TempDir()
	now := time.Now()
	rec := authopenai.AuthRecord{Version: 1, Provider: "openai", Source: authopenai.AuthSourceOAuth, ObtainedAt: now, TokenType: "Bearer", AccessToken: "stale", RefreshToken: "rt", Expiry: now.Add(time.Hour), AccountID: accountID}
	if err := authopenai.SaveAuth(dir, instance, rec); err != nil {
		t.Fatal(err)
	}
	return dir
}

func codexRes(instance string) registry.Resolved {
	caps := registry.Caps{Fields: registry.Baseline(registry.ProtocolOpenAIResponses), ResponsesLite: new(true)}
	return registry.Resolved{Instance: instance, Protocol: registry.ProtocolOpenAIResponses, Credential: registry.Credential{Source: "oauth"}, Transport: registry.Transport{Auth: registry.AuthOAuthOpenAICodex}, Caps: caps}
}

func TestCodexApplySetsEveryRequestHeader(t *testing.T) {
	dir := codexState(t, "openai-codex", "acct_123")
	var gotDir, gotInstance string
	c := &Codex{StateDir: dir, Credentials: func(_ context.Context, stateDir, instance string) (authopenai.RuntimeCredentials, error) {
		gotDir, gotInstance = stateDir, instance
		return authopenai.RuntimeCredentials{BearerToken: "fresh-token", Source: authopenai.AuthSourceOAuth}, nil
	}}
	req, _ := http.NewRequest(http.MethodGet, "https://chatgpt.com/backend-api/codex/models", nil)
	req.Header.Set("User-Agent", "custom/1")
	if err := c.Apply(context.Background(), req, codexRes("openai-codex")); err != nil {
		t.Fatal(err)
	}
	if gotDir != dir || gotInstance != "openai-codex" {
		t.Fatalf("credentials asked for %q/%q", gotDir, gotInstance)
	}
	h := req.Header
	if h.Get("Authorization") != "Bearer fresh-token" || h.Get("ChatGPT-Account-ID") != "acct_123" || h.Get("originator") != "evener" || h.Get("User-Agent") != "custom/1" {
		t.Fatalf("headers = %v", h)
	}
	req2, _ := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
	_ = c.Apply(context.Background(), req2, codexRes("openai-codex"))
	if ua := req2.Header.Get("User-Agent"); !strings.HasPrefix(ua, "evener/dev (") {
		t.Fatalf("default User-Agent = %q", ua)
	}
}

func TestCodexApplyRequiresLogin(t *testing.T) {
	c := &Codex{StateDir: t.TempDir(), Credentials: func(context.Context, string, string) (authopenai.RuntimeCredentials, error) {
		t.Fatal("credentials must not be resolved without an oauth credential source")
		return authopenai.RuntimeCredentials{}, nil
	}}
	req, _ := http.NewRequest(http.MethodPost, "https://x", nil)
	res := codexRes("openai-codex")
	res.Credential = registry.Credential{Source: "none"}
	err := c.Apply(context.Background(), req, res)
	var cfg *llm.ConfigurationError
	if !errors.As(err, &cfg) || !strings.Contains(err.Error(), "evener openai login --instance openai-codex") {
		t.Fatalf("err = %v", err)
	}
	c.Credentials = func(context.Context, string, string) (authopenai.RuntimeCredentials, error) {
		return authopenai.RuntimeCredentials{}, authopenai.ErrLoginRequired
	}
	err = c.Apply(context.Background(), req, codexRes("openai-codex"))
	if !errors.As(err, &cfg) || !errors.Is(err, authopenai.ErrLoginRequired) {
		t.Fatalf("expired login must be a configuration error wrapping ErrLoginRequired: %v", err)
	}

	// The registry's own gate (res.Credential.Source == "oauth") is a
	// file-existence check under a state root that need not match c's; Apply
	// must re-check what c.credentials actually resolved and refuse an
	// env-sourced key rather than send it to the Codex backend.
	c.Credentials = func(context.Context, string, string) (authopenai.RuntimeCredentials, error) {
		return authopenai.RuntimeCredentials{BearerToken: "env-key", Source: authopenai.AuthSourceEnv}, nil
	}
	req3, _ := http.NewRequest(http.MethodPost, "https://x", nil)
	err = c.Apply(context.Background(), req3, codexRes("openai-codex"))
	if !errors.As(err, &cfg) || !strings.Contains(err.Error(), "evener openai login --instance openai-codex") {
		t.Fatalf("env-sourced credential must be rejected: %v", err)
	}
	if req3.Header.Get("Authorization") != "" {
		t.Fatal("no Authorization header when the resolved credential is not oauth-sourced")
	}
}

func TestCodexPrepareRequest(t *testing.T) {
	c := &Codex{}
	res := codexRes("openai-codex")
	res.Caps.Fields["metadata"] = true
	req := llm.Request{SessionID: " sess-1 ", ThreadID: "thread-1", ClientMetadata: map[string]string{"installation_id": "inst-9"}}
	body := map[string]any{"metadata": map[string]string{"trace": "t"}, "input": "x"}
	httpReq, _ := http.NewRequest(http.MethodPost, "https://x", nil)
	if err := c.PrepareRequest(context.Background(), httpReq, body, req, res); err != nil {
		t.Fatal(err)
	}
	h := httpReq.Header
	if h.Get("x-openai-internal-codex-responses-lite") != "true" || h.Get("session-id") != "sess-1" || h.Get("thread-id") != "thread-1" || h.Get("x-client-request-id") != "thread-1" {
		t.Fatalf("headers = %v", h)
	}
	if _, still := body["metadata"]; still {
		t.Fatal("metadata must be deleted")
	}
	if got := body["client_metadata"].(map[string]string); got["trace"] != "t" || got["installation_id"] != "inst-9" {
		t.Fatalf("client_metadata = %v", got)
	}

	off := codexRes("openai-codex")
	off.Caps.Fields["metadata"] = false
	off.Caps.ResponsesLite = nil
	// A ProviderOptions["openai-responses"]["client_metadata"] can reach the
	// body; with the metadata field off, spec §9.5 says neither is sent.
	body = map[string]any{"metadata": map[string]string{"trace": "t"}, "client_metadata": map[string]string{"installation_id": "inst-9"}}
	httpReq, _ = http.NewRequest(http.MethodPost, "https://x", nil)
	_ = c.PrepareRequest(context.Background(), httpReq, body, req, off)
	if _, has := body["client_metadata"]; has || body["metadata"] != nil || httpReq.Header.Get("x-openai-internal-codex-responses-lite") != "" {
		t.Fatalf("metadata off: body = %v headers = %v", body, httpReq.Header)
	}

	empty := map[string]any{}
	_ = c.PrepareRequest(context.Background(), httpReq, empty, llm.Request{}, res)
	if _, has := empty["client_metadata"]; has {
		t.Fatal("an empty merge sends no client_metadata")
	}
	if !c.RequiresStreamingComplete() {
		t.Fatal("Codex answers Complete through Stream")
	}
}
