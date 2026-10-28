package tokenauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"golang.org/x/oauth2/google"

	authopenai "primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// A credential resolution failure that is not the login sentinel surfaces as
// the instance's own error, wrapped, without pretending the user must sign in.
func TestCodexApplySurfacesACredentialResolutionFailure(t *testing.T) {
	boom := errors.New("credential backend unavailable")
	c := &Codex{StateDir: t.TempDir(), Credentials: func(context.Context, string, string) (authopenai.RuntimeCredentials, error) {
		return authopenai.RuntimeCredentials{}, boom
	}}
	req, _ := http.NewRequest(http.MethodPost, "https://x", nil)
	err := c.Apply(context.Background(), req, codexRes("openai-codex"))
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the resolution failure wrapped", err)
	}
	if errors.Is(err, llm.ErrSignInRequired) {
		t.Fatalf("err = %v, a backend failure must not masquerade as the sign-in sentinel", err)
	}
}

// Without the Credentials seam, Apply resolves through the shared auth
// service, creating it on first use: an empty state dir has no OAuth record,
// so the service's answer is the login requirement.
func TestCodexApplyResolvesThroughTheDefaultService(t *testing.T) {
	c := &Codex{StateDir: t.TempDir()}
	req, _ := http.NewRequest(http.MethodPost, "https://x", nil)
	err := c.Apply(context.Background(), req, codexRes("openai-codex"))
	if !errors.Is(err, llm.ErrSignInRequired) {
		t.Fatalf("err = %v, want the sign-in sentinel from the empty record store", err)
	}
	if c.service == nil {
		t.Fatal("the shared auth service must be created on first use")
	}
}

// A state root that cannot be resolved at all fails the credential lookup
// before any service is built. This test serializes against the process
// environment (t.Setenv) and cannot run in parallel with package tests that
// read the ambient state home.
func TestCodexCredentialLookupWithoutAStateHomeFails(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "")
	c := &Codex{}
	req, _ := http.NewRequest(http.MethodPost, "https://x", nil)
	err := c.Apply(context.Background(), req, codexRes("openai-codex"))
	if err == nil || errors.Is(err, llm.ErrSignInRequired) {
		t.Fatalf("err = %v, want the state-home resolution failure, not a sign-in prompt", err)
	}
}

// The scope read fails when the record store cannot answer: no record, no
// claims to hash into the storage scope.
func TestCodexAuthScopeWithoutARecordFails(t *testing.T) {
	c := &Codex{StateDir: t.TempDir()}
	account, workspace, err := c.AuthScope(context.Background(), codexRes("openai-codex"))
	if err == nil {
		t.Fatal("AuthScope succeeded without any stored record")
	}
	if account != "" || workspace != "" {
		t.Fatalf("a failed scope read = (%q, %q), want empty claims", account, workspace)
	}
}

// A record with a persisted account but no workspace and an unparsable id
// token yields the account it actually has: the claims fallback is best-effort
// and must not drop the one claim that survived.
func TestCodexAuthScopeKeepsTheAccountWhenTheIDTokenIsUnparsable(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	rec := authopenai.AuthRecord{Version: 1, Provider: "openai", Source: authopenai.AuthSourceOAuth, ObtainedAt: now, TokenType: "Bearer", AccessToken: "tok", RefreshToken: "rt", Expiry: now.Add(time.Hour), AccountID: "acct_9", IDToken: "not-a-jwt"}
	if err := authopenai.SaveAuth(dir, "openai-codex", rec); err != nil {
		t.Fatal(err)
	}
	c := &Codex{StateDir: dir}
	account, workspace, err := c.AuthScope(context.Background(), codexRes("openai-codex"))
	if err != nil {
		t.Fatalf("AuthScope: %v", err)
	}
	if account != "acct_9" || workspace != "" {
		t.Fatalf("scope claims = (%q, %q), want the persisted account and no workspace", account, workspace)
	}
}

// A blank ClientVersion falls back to "dev" in the user agent, matching the
// binaries' contract that the header is always set. The version is a
// process-wide package variable, so this test cannot run in parallel with
// anything else reading it.
func TestCodexUserAgentFallsBackToDevWhenTheVersionIsBlank(t *testing.T) {
	prev := ClientVersion
	ClientVersion = ""
	defer func() { ClientVersion = prev }()

	dir := codexState(t, "openai-codex", "acct_1")
	c := &Codex{StateDir: dir, Credentials: func(context.Context, string, string) (authopenai.RuntimeCredentials, error) {
		return authopenai.RuntimeCredentials{BearerToken: "tok", Source: authopenai.AuthSourceOAuth}, nil
	}}
	req, _ := http.NewRequest(http.MethodPost, "https://x", nil)
	if err := c.Apply(context.Background(), req, codexRes("openai-codex")); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("evener/dev (%s %s)", runtime.GOOS, runtime.GOARCH)
	if got := req.Header.Get("User-Agent"); got != want {
		t.Fatalf("User-Agent = %q, want %q", got, want)
	}
}

// gcpadcRes builds a resolved row for a stored Google credential.
func gcpadcRes(instance, value string) registry.Resolved {
	return registry.Resolved{Instance: instance, Credential: registry.Credential{Source: "store", Value: value}}
}

// Without the CredentialsFromJSON seam, a stored credential is parsed by the
// real parser: a well-formed service account builds a token source offline
// (the key is read, never used over the network) and the source is cached.
func TestGCPADCStoredCredentialParsesThroughTheDefaultParser(t *testing.T) {
	a := &GCPADC{}
	res := gcpadcRes("vertex", testServiceAccountJSON(t))
	if _, err := a.tokenSource(context.Background(), res); err != nil {
		t.Fatalf("tokenSource: %v", err)
	}
	if _, err := a.tokenSource(context.Background(), res); err != nil {
		t.Fatalf("second tokenSource: %v", err)
	}
}

// A credential the registry gate accepts but the parser rejects surfaces the
// parser's failure: the gate and the parser are two checks, and the second
// one's verdict must not be swallowed.
func TestGCPADCStoredCredentialParserFailureSurfaces(t *testing.T) {
	boom := errors.New("parser refused the document")
	a := &GCPADC{CredentialsFromJSON: func(context.Context, []byte, ...string) (*google.Credentials, error) {
		return nil, boom
	}}
	_, err := a.tokenSource(context.Background(), gcpadcRes("vertex", testServiceAccountJSON(t)))
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the parser failure surfaced", err)
	}
}

// Without the FindCredentials seam the ADC lookup runs the real finder. A
// GOOGLE_APPLICATION_CREDENTIALS path that does not exist makes the finder
// report absence itself — no network, no metadata-server probe, no ambient
// host state — and that absence must surface rather than a fabricated
// source. Serializes the process environment, so it cannot run in parallel
// with tests that read the ambient home.
func TestGCPADCDefaultCredentialLookupReportsAbsence(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "no-such-credentials.json"))
	a := &GCPADC{}
	_, err := a.tokenSource(context.Background(), registry.Resolved{Instance: "vertex", Credential: registry.Credential{Source: "adc"}})
	if err == nil {
		t.Fatal("the default ADC lookup must report that no credentials were found")
	}
}
