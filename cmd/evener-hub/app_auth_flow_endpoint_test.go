package hub

// A sign-in flow's instance can move while the flow is open: the token
// exchange is a browser round trip long, and an edit that re-points the
// instance at another endpoint lands inside it. Only the auth scheme is asked
// again under the credential lock, and an edit to base_url keeps the scheme -
// so without the endpoint being bound too, the record the user just authorized
// is filed against a destination they never signed in for, and the transport
// sends it there.
//
// These tests pin that binding on both flows: the login flow's authorize round
// trip and the device flow's poll.

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	authopenai "primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/auth/openai/oaitest"
)

// One Codex instance at two endpoints: base_url is what the record's transport
// talks to, so an edit to it moves the whole destination the credential is
// used against.
const (
	codexEndpointToml      = "[providers.work]\nbase = \"openai-codex\"\nbase_url = \"https://codex-a.example.test/v1\"\n"
	codexMovedEndpointToml = "[providers.work]\nbase = \"openai-codex\"\nbase_url = \"https://codex-b.example.test/v1\"\n"
)

// moveEndpointDuring is what an edit to providers.toml leaves behind inside a
// flow's long step: the file re-pointed and the registry reloaded onto it.
func moveEndpointDuring(t *testing.T, c *hubAuthController, path string) {
	t.Helper()
	renameProvidersEntry(t, c, path, codexMovedEndpointToml)
}

// loginCallbackURL builds the redirect a browser would hand back, carrying the
// flow's own state.
func loginCallbackURL(t *testing.T, authorizeURL, state string) string {
	t.Helper()
	if authorizeURL == "" {
		t.Fatal("LoginStart returned no authorize URL")
	}
	parsed, err := url.Parse(authorizeURL)
	if err != nil {
		t.Fatalf("parse authorize URL: %v", err)
	}
	if got := parsed.Query().Get("state"); got != state {
		t.Fatalf("authorize URL state = %q, want %q", got, state)
	}
	return "http://localhost:1455/auth/callback?code=auth-code&state=" + url.QueryEscape(state)
}

func TestAuth_LoginCompleteRefusesARecordForAnEndpointThatMoved(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	tomlPath := writeProvidersToml(t, dir, codexEndpointToml)
	ctrl := newTestAuthController(t, dir, stateDir, tomlPath)
	ctrl.cfg = authopenai.Config{IssuerBaseURL: "https://auth.example.test"}
	ctrl.client = &http.Client{}
	ctrl.exchangeCode = func(context.Context, *http.Client, authopenai.Config, authopenai.TokenExchangeRequest) (authopenai.TokenSet, error) {
		moveEndpointDuring(t, ctrl, tomlPath)
		return authopenai.TokenSet{
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
			TokenType:    "Bearer",
			Scope:        "openid profile email",
			Expiry:       time.Now().Add(time.Hour),
		}, nil
	}

	start, err := ctrl.LoginStart(appwire.AuthLoginStartParams{Provider: "work"})
	if err != nil {
		t.Fatalf("LoginStart: %v", err)
	}
	authorizeURL, err := url.Parse(start.URL)
	if err != nil {
		t.Fatalf("parse authorize URL: %v", err)
	}

	_, err = ctrl.LoginComplete(context.Background(), appwire.AuthLoginCompleteParams{
		Provider:    "work",
		FlowID:      start.FlowID,
		RedirectURL: loginCallbackURL(t, start.URL, authorizeURL.Query().Get("state")),
	})
	if err == nil || !strings.Contains(err.Error(), "endpoint") {
		t.Fatalf("LoginComplete = %v, want the refusal for the endpoint the flow was not started on", err)
	}
	if _, err := authopenai.LoadAuth(stateDir, "work"); !errors.Is(err, authopenai.ErrAuthNotFound) {
		t.Fatalf("LoadAuth(work) err = %v, want ErrAuthNotFound: the record must not land on the moved endpoint", err)
	}
}

// TestAuth_LoginCompleteSavesForTheEndpointTheFlowStartedOn is the control:
// the same flow, unchanged, still saves the record - the binding refuses a
// moved destination, not a sign-in.
func TestAuth_LoginCompleteSavesForTheEndpointTheFlowStartedOn(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	tomlPath := writeProvidersToml(t, dir, codexEndpointToml)
	ctrl := newTestAuthController(t, dir, stateDir, tomlPath)
	ctrl.cfg = authopenai.Config{IssuerBaseURL: "https://auth.example.test"}
	ctrl.client = &http.Client{}
	ctrl.exchangeCode = func(context.Context, *http.Client, authopenai.Config, authopenai.TokenExchangeRequest) (authopenai.TokenSet, error) {
		return authopenai.TokenSet{
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
			TokenType:    "Bearer",
			Scope:        "openid profile email",
			Expiry:       time.Now().Add(time.Hour),
		}, nil
	}

	start, err := ctrl.LoginStart(appwire.AuthLoginStartParams{Provider: "work"})
	if err != nil {
		t.Fatalf("LoginStart: %v", err)
	}
	authorizeURL, err := url.Parse(start.URL)
	if err != nil {
		t.Fatalf("parse authorize URL: %v", err)
	}

	if _, err := ctrl.LoginComplete(context.Background(), appwire.AuthLoginCompleteParams{
		Provider:    "work",
		FlowID:      start.FlowID,
		RedirectURL: loginCallbackURL(t, start.URL, authorizeURL.Query().Get("state")),
	}); err != nil {
		t.Fatalf("LoginComplete: %v", err)
	}
	if _, err := authopenai.LoadAuth(stateDir, "work"); err != nil {
		t.Fatalf("LoadAuth(work) err = %v, want the freshly saved record", err)
	}
}

// TestAuth_DevicePollRefusesARecordForAnEndpointThatMoved is the same binding
// on the device flow, whose own exchange is the long step.
func TestAuth_DevicePollRefusesARecordForAnEndpointThatMoved(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	tomlPath := writeProvidersToml(t, dir, codexEndpointToml)
	ctrl := newTestAuthController(t, dir, stateDir, tomlPath)
	ctrl.requestDeviceCode = func(context.Context, *http.Client, authopenai.Config) (authopenai.DeviceCode, error) {
		return authopenai.DeviceCode{UserCode: "U", VerificationURL: "https://x", DeviceAuthID: "d", Interval: time.Second}, nil
	}
	ctrl.pollDeviceOnce = func(context.Context, *http.Client, authopenai.Config, authopenai.DeviceCode) (authopenai.DeviceCodeSuccess, bool, error) {
		return authopenai.DeviceCodeSuccess{AuthorizationCode: "ac", CodeVerifier: "cv"}, false, nil
	}
	ctrl.exchangeDevice = func(context.Context, *http.Client, authopenai.Config, string, string) (authopenai.TokenSet, error) {
		moveEndpointDuring(t, ctrl, tomlPath)
		return authopenai.TokenSet{AccessToken: "at", RefreshToken: "rt", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}, nil
	}

	start, err := ctrl.DeviceStart(context.Background(), appwire.AuthDeviceStartParams{Provider: "work"})
	if err != nil {
		t.Fatalf("DeviceStart: %v", err)
	}
	_, err = ctrl.DevicePoll(context.Background(), appwire.AuthDevicePollParams{Provider: "work", FlowID: start.FlowID})
	if err == nil || !strings.Contains(err.Error(), "endpoint") {
		t.Fatalf("DevicePoll = %v, want the refusal for the endpoint the flow was not started on", err)
	}
	if _, err := authopenai.LoadAuth(stateDir, "work"); !errors.Is(err, authopenai.ErrAuthNotFound) {
		t.Fatalf("LoadAuth(work) err = %v, want ErrAuthNotFound: the record must not land on the moved endpoint", err)
	}
}
