package hub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	authopenai "primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/auth/openai/oaitest"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/internal/credentials"
)

func TestHubRPCAuthStatusUsesUserScopedOpenAIAuth(t *testing.T) {
	stateRoot := oaitest.IsolateOpenAIAuth(t)
	if err := authopenai.SaveAuth(stateRoot, "openai-codex", authopenai.AuthRecord{
		Version:      1,
		Provider:     "openai",
		Source:       authopenai.AuthSourceOAuth,
		ObtainedAt:   time.Now().Add(-time.Hour),
		TokenType:    "Bearer",
		Scope:        "openid profile email",
		AccessToken:  "stored-access-token",
		RefreshToken: "stored-refresh-token",
		Expiry:       time.Now().Add(time.Hour),
		Email:        "j@example.com",
	}); err != nil {
		t.Fatal(err)
	}

	hub := newHubRPCTestServer(t, stateRootAuthConfig(t, stateRoot, nil))
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	init, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if !init.Features.Auth {
		t.Fatalf("Initialize features=%+v, want auth advertised", init.Features)
	}

	status, err := client.AuthStatus(context.Background(), appwire.AuthStatusParams{Provider: "openai-codex"})
	if err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}
	if !status.SignedIn || status.ActiveSource != authopenai.AuthSourceOAuth || status.Email != "j@example.com" {
		t.Fatalf("status=%+v, want signed-in oauth j@example.com", status)
	}
	if !status.HasStoredOAuth {
		t.Fatalf("status=%+v, want stored OAuth marker", status)
	}
}

// stateRootAuthConfig is a hub config whose registry and auth controller
// both keep OAuth records under stateRoot, and whose registry reads env. A
// test that seeds auth/<instance>.json itself passes the root it seeded; the
// default fixture would mint a fresh one. The default fixture registry is
// also hermetic - it reads no environment at all - so a test about
// env-vs-OAuth precedence passes the variables it is about.
func stateRootAuthConfig(t *testing.T, stateRoot string, env map[string]string) hubcore.WebConfig {
	t.Helper()
	store := newTestCredentialsStore(t)
	return hubcore.WebConfig{
		Past:         hubcore.NewPastIndex(""),
		HubStateRoot: stateRoot,
		Registry:     newTestRegistry(t, stateRoot, "", store, env),
		CredsStore:   store,
	}
}

func TestHubRPCAuthStatusPrefersStoredOAuthOverEnv(t *testing.T) {
	stateRoot := oaitest.IsolateOpenAIAuth(t)
	if err := authopenai.SaveAuth(stateRoot, "openai-codex", authopenai.AuthRecord{
		Version:      1,
		Provider:     "openai",
		Source:       authopenai.AuthSourceOAuth,
		ObtainedAt:   time.Now().Add(-time.Hour),
		TokenType:    "Bearer",
		Scope:        "openid profile email",
		AccessToken:  "stored-access-token",
		RefreshToken: "stored-refresh-token",
		Expiry:       time.Now().Add(time.Hour),
		Email:        "stored@example.com",
	}); err != nil {
		t.Fatal(err)
	}

	hub := newHubRPCTestServer(t, stateRootAuthConfig(t, stateRoot, map[string]string{"OPENAI_API_KEY": "env-token"}))
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	status, err := client.AuthStatus(context.Background(), appwire.AuthStatusParams{Provider: "openai-codex"})
	if err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}
	if !status.SignedIn || status.ActiveSource != authopenai.AuthSourceOAuth || !status.HasStoredOAuth || status.StoredEmail != "stored@example.com" || status.Email != "stored@example.com" {
		t.Fatalf("status=%+v, want stored OAuth to win over env-token", status)
	}

	// Positive control: the plain API-key provider is the one that does read
	// the environment, so an env layer that never reaches the registry fails
	// here instead of making the Codex assertion above vacuously true.
	envStatus, err := client.AuthStatus(context.Background(), appwire.AuthStatusParams{Provider: "openai"})
	if err != nil {
		t.Fatalf("AuthStatus(openai): %v", err)
	}
	if !envStatus.SignedIn || envStatus.ActiveSource != "env:OPENAI_API_KEY" || envStatus.EnvVar != "OPENAI_API_KEY" {
		t.Fatalf("status(openai)=%+v, want OPENAI_API_KEY to resolve from the environment", envStatus)
	}
}

// TestHubRPCAuthStatusIgnoresAPIKeyForTheCodexInstance is spec §5.1: the
// Codex transport authenticates with its OAuth record and nothing else, so an
// OPENAI_API_KEY in the environment leaves it signed out rather than claiming
// a sign-in the child could never use.
func TestHubRPCAuthStatusIgnoresAPIKeyForTheCodexInstance(t *testing.T) {
	stateRoot := oaitest.IsolateOpenAIAuth(t)

	hub := newHubRPCTestServer(t, stateRootAuthConfig(t, stateRoot, map[string]string{"OPENAI_API_KEY": "env-token"}))
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	status, err := client.AuthStatus(context.Background(), appwire.AuthStatusParams{Provider: "openai-codex"})
	if err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}
	if status.SignedIn || status.ActiveSource != "none" || status.HasStoredOAuth || status.EnvVar != "" {
		t.Fatalf("status=%+v, want signed out with source none: an API key is not a Codex credential", status)
	}

	// Positive control: the plain API-key provider is the one that does read
	// the environment, so an env layer that never reaches the registry fails
	// here instead of making the Codex assertion above vacuously true.
	envStatus, err := client.AuthStatus(context.Background(), appwire.AuthStatusParams{Provider: "openai"})
	if err != nil {
		t.Fatalf("AuthStatus(openai): %v", err)
	}
	if !envStatus.SignedIn || envStatus.ActiveSource != "env:OPENAI_API_KEY" || envStatus.EnvVar != "OPENAI_API_KEY" {
		t.Fatalf("status(openai)=%+v, want OPENAI_API_KEY to resolve from the environment", envStatus)
	}
}

func TestHubRPCAuthStatusReportsOAuthRefreshAndLoginStates(t *testing.T) {
	now := time.Date(2026, 5, 14, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		expiry       time.Time
		wantSignedIn bool
		wantRefresh  bool
		wantLogin    bool
	}{
		{
			name:         "refreshable",
			expiry:       now.Add(2 * time.Minute),
			wantSignedIn: true,
			wantRefresh:  true,
		},
		{
			name:      "expired",
			expiry:    now.Add(-time.Minute),
			wantLogin: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := newHubAuthController(map[string]string{"OPENAI_API_KEY": ""})
			ctrl.stateDir = t.TempDir()
			attachTestRegistry(t, ctrl)
			ctrl.now = func() time.Time { return now }
			if err := authopenai.SaveAuth(ctrl.stateDir, "openai-codex", authopenai.AuthRecord{
				Version:      1,
				Provider:     "openai",
				Source:       authopenai.AuthSourceOAuth,
				ObtainedAt:   now.Add(-time.Hour),
				TokenType:    "Bearer",
				Scope:        "openid profile email",
				AccessToken:  "stored-access-token",
				RefreshToken: "stored-refresh-token",
				Expiry:       tc.expiry,
				Email:        "stored@example.com",
			}); err != nil {
				t.Fatal(err)
			}

			status, err := ctrl.Status(appwire.AuthStatusParams{Provider: "openai-codex"})
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			if status.SignedIn != tc.wantSignedIn || status.NeedsRefresh != tc.wantRefresh || status.NeedsLogin != tc.wantLogin {
				t.Fatalf("status=%+v, want signedIn=%t needsRefresh=%t needsLogin=%t", status, tc.wantSignedIn, tc.wantRefresh, tc.wantLogin)
			}
		})
	}
}

// TestOpenAIStateDirFromEnvUsesLaunchEnvStateHome pins the one thing the hub
// auth controller still resolves for itself: XDG_STATE_HOME out of the launch
// env it is handed, which is not the process env cmdutil.DefaultStateRoot reads.
func TestOpenAIStateDirFromEnvUsesLaunchEnvStateHome(t *testing.T) {
	t.Setenv(envvars.XDGStateHome.Name, t.TempDir())
	launchStateHome := t.TempDir()

	got := openAIStateDirFromEnv(map[string]string{envvars.XDGStateHome.Name: launchStateHome})
	if want := filepath.Join(launchStateHome, "evener"); got != want {
		t.Fatalf("stateDir=%q, want %q", got, want)
	}
}

// TestOpenAIStateDirFromEnvFallsBackToDefaultStateRoot pins the fallback on
// cmdutil.DefaultStateRoot rather than on a second resolution of evener's state
// root. The two used to disagree on Windows, where this one read
// USERPROFILE/HOMEDRIVE+HOMEPATH out of the supplied env instead of letting
// os.UserHomeDir find the home directory, and with no home resolvable at all,
// where it landed in os.TempDir() instead of "." (#1012).
func TestOpenAIStateDirFromEnvFallsBackToDefaultStateRoot(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv(envvars.XDGStateHome.Name, stateHome)

	got := openAIStateDirFromEnv(map[string]string{})
	if want := filepath.Join(stateHome, "evener"); got != want {
		t.Fatalf("stateDir=%q, want %q", got, want)
	}
	if want := cmdutil.DefaultStateRoot(); got != want {
		t.Fatalf("stateDir=%q, want cmdutil.DefaultStateRoot() %q", got, want)
	}

	// And the home arm below XDG_STATE_HOME, the one that diverged.
	t.Setenv(envvars.XDGStateHome.Name, "")
	if got, want := openAIStateDirFromEnv(map[string]string{}), cmdutil.DefaultStateRoot(); got != want {
		t.Fatalf("stateDir=%q, want cmdutil.DefaultStateRoot() %q", got, want)
	}
}

func TestHubRPCAuthLogoutRemovesUserScopedOpenAIAuth(t *testing.T) {
	stateRoot := oaitest.IsolateOpenAIAuth(t)
	if err := authopenai.SaveAuth(stateRoot, "openai-codex", authopenai.AuthRecord{
		Version:      1,
		Provider:     "openai",
		Source:       authopenai.AuthSourceOAuth,
		ObtainedAt:   time.Now().Add(-time.Hour),
		TokenType:    "Bearer",
		Scope:        "openid profile email",
		AccessToken:  "stored-access-token",
		RefreshToken: "stored-refresh-token",
		Expiry:       time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	hub := newHubRPCTestServer(t, stateRootAuthConfig(t, stateRoot, nil))
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	resp, err := client.AuthLogout(context.Background(), appwire.AuthLogoutParams{Provider: "openai-codex"})
	if err != nil {
		t.Fatalf("AuthLogout: %v", err)
	}
	if !resp.Removed || resp.Status.ActiveSource != "none" {
		t.Fatalf("logout=%+v, want removed and source none", resp)
	}
	if _, err := authopenai.LoadAuth(stateRoot, "openai-codex"); !errors.Is(err, authopenai.ErrAuthNotFound) {
		t.Fatalf("LoadAuth(%s) err=%v, want ErrAuthNotFound after logout", stateRoot, err)
	}
}

func TestHubAuthControllerManualPastebackSavesOpenAIAuth(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	ctrl := newHubAuthController()
	ctrl.stateDir = t.TempDir()
	attachTestRegistry(t, ctrl)
	ctrl.cfg = authopenai.Config{IssuerBaseURL: "https://auth.example.test"}
	ctrl.client = &http.Client{}
	var exchangeReq authopenai.TokenExchangeRequest
	ctrl.exchangeCode = func(_ context.Context, _ *http.Client, _ authopenai.Config, req authopenai.TokenExchangeRequest) (authopenai.TokenSet, error) {
		exchangeReq = req
		return authopenai.TokenSet{
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
			IDToken:      hubAuthTestJWT(t, map[string]any{"email": "oauth@example.com"}),
			TokenType:    "Bearer",
			Scope:        "openid profile email",
			Expiry:       time.Now().Add(time.Hour),
		}, nil
	}

	start, err := ctrl.LoginStart(appwire.AuthLoginStartParams{Provider: "openai-codex"})
	if err != nil {
		t.Fatalf("LoginStart: %v", err)
	}
	authorizeURL, err := url.Parse(start.URL)
	if err != nil {
		t.Fatalf("parse authorize URL: %v", err)
	}
	state := authorizeURL.Query().Get("state")
	if state == "" || start.FlowID != state {
		t.Fatalf("flow=%q state=%q, want matching non-empty values", start.FlowID, state)
	}

	resp, err := ctrl.LoginComplete(context.Background(), appwire.AuthLoginCompleteParams{
		Provider:    "openai-codex",
		FlowID:      start.FlowID,
		RedirectURL: "http://localhost:1455/auth/callback?code=auth-code&state=" + url.QueryEscape(state),
	})
	if err != nil {
		t.Fatalf("LoginComplete: %v", err)
	}
	if exchangeReq.Code != "auth-code" || exchangeReq.RedirectURI == "" || exchangeReq.CodeVerifier == "" {
		t.Fatalf("exchange request=%+v, want code, redirect URI, and verifier", exchangeReq)
	}
	if !resp.Status.SignedIn || resp.Status.ActiveSource != authopenai.AuthSourceOAuth || resp.Status.Email != "oauth@example.com" {
		t.Fatalf("status=%+v, want oauth status with email", resp.Status)
	}
	record, err := authopenai.LoadAuth(ctrl.stateDir, "openai-codex")
	if err != nil {
		t.Fatalf("LoadAuth: %v", err)
	}
	if record.AccessToken != "access-token" || record.Email != "oauth@example.com" {
		t.Fatalf("record=%+v, want saved token and email", record)
	}
}

// TestAuth_LoginComplete_WritesUnderTheConfiguredStateRoot pins the state
// root the controller is handed as the root its OAuth records land in. A
// fixture with a private root must not read or write the shared auth/*.json
// under the ambient XDG_STATE_HOME, or parallel fixtures overwrite each other.
func TestAuth_LoginComplete_WritesUnderTheConfiguredStateRoot(t *testing.T) {
	envStateDir := oaitest.IsolateOpenAIAuth(t)
	configuredRoot := t.TempDir()
	store, err := credentials.LoadStore(filepath.Join(configuredRoot, "credentials.toml"))
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	c := newHubAuthControllerWithStore(configuredRoot, store)
	attachTestRegistry(t, c)
	c.cfg = authopenai.Config{IssuerBaseURL: "https://auth.example.test"}
	c.client = &http.Client{}
	c.exchangeCode = func(context.Context, *http.Client, authopenai.Config, authopenai.TokenExchangeRequest) (authopenai.TokenSet, error) {
		return authopenai.TokenSet{
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
			IDToken:      hubAuthTestJWT(t, map[string]any{"email": "oauth@example.com"}),
			TokenType:    "Bearer",
			Scope:        "openid profile email",
			Expiry:       time.Now().Add(time.Hour),
		}, nil
	}

	start, err := c.LoginStart(appwire.AuthLoginStartParams{Provider: "openai-codex"})
	if err != nil {
		t.Fatalf("LoginStart: %v", err)
	}
	authorizeURL, err := url.Parse(start.URL)
	if err != nil {
		t.Fatalf("parse authorize URL: %v", err)
	}
	state := authorizeURL.Query().Get("state")
	if _, err := c.LoginComplete(context.Background(), appwire.AuthLoginCompleteParams{
		Provider:    "openai-codex",
		FlowID:      start.FlowID,
		RedirectURL: "http://localhost:1455/auth/callback?code=auth-code&state=" + url.QueryEscape(state),
	}); err != nil {
		t.Fatalf("LoginComplete: %v", err)
	}

	record, err := authopenai.LoadAuth(configuredRoot, "openai-codex")
	if err != nil {
		t.Fatalf("LoadAuth(%s): %v; the record must land under the configured hub state root", configuredRoot, err)
	}
	if record.AccessToken != "access-token" {
		t.Fatalf("record=%+v, want the saved access token", record)
	}
	if _, err := authopenai.LoadAuth(envStateDir, "openai-codex"); !errors.Is(err, authopenai.ErrAuthNotFound) {
		t.Fatalf("LoadAuth(%s) err=%v, want ErrAuthNotFound: nothing may land under the env state root", envStateDir, err)
	}
}

// TestHubRPCAuthStoresOAuthWhereTheRegistryReads pins how the hub wires the
// auth controller: its OAuth records live in the registry's state root, the
// directory registry credential resolution reads auth/<instance>.json from,
// not under HubStateRoot. The hub loads its registry at
// cmdutil.DefaultStateRoot() whatever hub_state_root says, so a record kept
// under HubStateRoot would be a login the registry, the credential probe and
// every spawned child never see.
func TestHubRPCAuthStoresOAuthWhereTheRegistryReads(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	registryRoot := t.TempDir()
	hubStateRoot := t.TempDir()
	store, err := credentials.LoadStore(filepath.Join(t.TempDir(), "credentials.toml"))
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	if err := authopenai.SaveAuth(registryRoot, "openai-codex", authopenai.AuthRecord{
		Version:      1,
		Provider:     "openai",
		Source:       authopenai.AuthSourceOAuth,
		ObtainedAt:   time.Now().Add(-time.Hour),
		TokenType:    "Bearer",
		Scope:        "openid profile email",
		AccessToken:  "stored-access-token",
		RefreshToken: "stored-refresh-token",
		Expiry:       time.Now().Add(time.Hour),
		Email:        "stored@example.com",
	}); err != nil {
		t.Fatal(err)
	}

	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		Past:         hubcore.NewPastIndex(""),
		HubStateRoot: hubStateRoot,
		Registry:     newTestRegistry(t, registryRoot, "", store, nil),
		CredsStore:   store,
	})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	status, err := client.AuthStatus(context.Background(), appwire.AuthStatusParams{Provider: "openai-codex"})
	if err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}
	if !status.SignedIn || status.ActiveSource != authopenai.AuthSourceOAuth {
		t.Fatalf("status=%+v, want the record under the registry state root %s to sign the instance in", status, registryRoot)
	}
	logout, err := client.AuthLogout(context.Background(), appwire.AuthLogoutParams{Provider: "openai-codex"})
	if err != nil {
		t.Fatalf("AuthLogout: %v", err)
	}
	if !logout.Removed {
		t.Fatalf("logout=%+v, want the record under the registry state root removed", logout)
	}
	if _, err := authopenai.LoadAuth(registryRoot, "openai-codex"); !errors.Is(err, authopenai.ErrAuthNotFound) {
		t.Fatalf("LoadAuth(%s) err=%v, want ErrAuthNotFound after logout", registryRoot, err)
	}
}

func hubAuthTestJWT(t *testing.T, payload map[string]any) string {
	t.Helper()
	headerBytes, err := json.Marshal(map[string]any{"alg": "none", "typ": "JWT"})
	if err != nil {
		t.Fatal(err)
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(headerBytes) + "." +
		base64.RawURLEncoding.EncodeToString(payloadBytes) + "."
}

func TestAuth_List_IncludesAllProviders(t *testing.T) {
	stateDir := t.TempDir()
	credsPath := filepath.Join(stateDir, "credentials.toml")
	store, _ := credentials.LoadStore(credsPath)
	c := newHubAuthControllerWithStore(stateDir, store)
	attachTestRegistry(t, c)
	got, err := c.List(appwire.EmptyParams{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	names := map[string]bool{}
	for _, p := range got.Providers {
		names[p.Provider] = true
	}
	for _, want := range []string{"openai", "anthropic", "ollama"} {
		if !names[want] {
			t.Errorf("List missing %q; got %v", want, names)
		}
	}
}

func TestAuth_ApiKeySet_WritesAndReports(t *testing.T) {
	stateDir := t.TempDir()
	credsPath := filepath.Join(stateDir, "credentials.toml")
	store, _ := credentials.LoadStore(credsPath)
	c := newHubAuthControllerWithStore(stateDir, store)
	attachTestRegistry(t, c)
	got, err := c.ApiKeySet(appwire.AuthApiKeySetParams{Provider: "anthropic", Value: "sk-ant-XXX"})
	if err != nil {
		t.Fatalf("ApiKeySet: %v", err)
	}
	if got.ActiveSource != "store" {
		t.Errorf("ActiveSource = %q, want store", got.ActiveSource)
	}
	// Reload from disk; value should persist.
	store2, _ := credentials.LoadStore(credsPath)
	v, ok := store2.Get("anthropic")
	if v != "sk-ant-XXX" || !ok {
		t.Errorf("after ApiKeySet: v=%q ok=%v", v, ok)
	}
}

func TestAuth_Status_AnthropicViaStore(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	stateDir := t.TempDir()
	store, _ := credentials.LoadStore(filepath.Join(stateDir, "credentials.toml"))
	_ = store.Set("anthropic", "key")
	c := newHubAuthControllerWithStore(stateDir, store)
	attachTestRegistry(t, c)
	got, err := c.Status(appwire.AuthStatusParams{Provider: "anthropic"})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !got.SignedIn || got.ActiveSource != "store" {
		t.Errorf("Status anthropic = %+v", got)
	}
	if len(got.AuthModes) == 0 {
		t.Errorf("AuthModes empty: %+v", got)
	}
}

// TestAuth_Codex_StoredKeyIsNotACredential is kata z1gm on the Codex
// transport: the registry resolves an oauth-openai-codex instance from its
// OAuth record and nothing else (spec §5.1, §10), so a credentials.toml entry
// under that name is not a credential. The pane must say so, because the spawn
// gate reading the same registry refuses the launch.
func TestAuth_Codex_StoredKeyIsNotACredential(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	store, _ := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	c := newHubAuthControllerWithStore(dir, store)
	c.stateDir = t.TempDir()
	attachTestRegistry(t, c) // empty: no OAuth record
	if err := store.Set("openai-codex", "sk-test-123"); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
	if err := c.reg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	got, err := c.Status(appwire.AuthStatusParams{Provider: "openai-codex"})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got.SignedIn || got.ActiveSource != "none" {
		t.Fatalf("status=%+v, want signed out with source none: a stored key is not a Codex credential", got)
	}
	if !got.HasStoredFile {
		t.Errorf("status=%+v, want HasStoredFile as a diagnostic", got)
	}
	if got.HasStoredOAuth {
		t.Fatalf("status=%+v, want no stored OAuth", got)
	}
	// And the gate in front of the launch agrees.
	if err := validateProviderCredentials("openai-codex", c.reg); err == nil {
		t.Fatal("the spawn gate accepted a Codex instance whose only key is in the store")
	}
}

func TestAuth_OpenAI_Status_OAuthShadowsStoredFileKey(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	store, _ := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	c := newHubAuthControllerWithStore(dir, store)
	c.stateDir = t.TempDir()
	attachTestRegistry(t, c)
	if err := store.Set("openai-codex", "sk-test-123"); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
	if err := authopenai.SaveAuth(c.stateDir, "openai-codex", authopenai.AuthRecord{
		Version: 1, Provider: "openai", Source: authopenai.AuthSourceOAuth,
		ObtainedAt: time.Now().Add(-time.Hour), TokenType: "Bearer",
		AccessToken: "acc", RefreshToken: "ref",
		Expiry: time.Now().Add(time.Hour), Email: "o@example.com",
	}); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}
	got, err := c.Status(appwire.AuthStatusParams{Provider: "openai-codex"})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got.ActiveSource != authopenai.AuthSourceOAuth || !got.HasStoredFile || !got.HasStoredOAuth {
		t.Fatalf("status=%+v, want oauth active with file shadowed", got)
	}
}

func TestAuth_Codex_Status_CorruptOAuthIsNoCredential(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	store, _ := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	c := newHubAuthControllerWithStore(dir, store)
	c.stateDir = t.TempDir()
	attachTestRegistry(t, c)
	if err := store.Set("openai-codex", "sk-test-123"); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
	authPath := authopenai.AuthFilePath(c.stateDir, "openai-codex")
	if err := os.MkdirAll(filepath.Dir(authPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := c.Status(appwire.AuthStatusParams{Provider: "openai-codex"})
	if err != nil {
		t.Fatalf("Status returned error on corrupt record: %v", err)
	}
	if got.SignedIn || got.ActiveSource != "none" {
		t.Fatalf("status=%+v, want signed out with source none (a corrupt record is absent, and the store is not a Codex credential)", got)
	}
	if !got.HasStoredFile {
		t.Errorf("status=%+v, want HasStoredFile as a diagnostic", got)
	}
}

// TestAuth_Codex_ApiKeySetIsRefused: storing a key nothing can use and then
// reporting success is how the pane came to claim a sign-in the spawn gate
// refuses. The Codex transport authenticates with its OAuth record alone
// (spec §5.1), so the pane says so instead of writing the key.
func TestAuth_Codex_ApiKeySetIsRefused(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	credsPath := filepath.Join(dir, "credentials.toml")
	store, _ := credentials.LoadStore(credsPath)
	c := newHubAuthControllerWithStore(dir, store)
	c.stateDir = t.TempDir()
	attachTestRegistry(t, c) // no OAuth record

	_, err := c.ApiKeySet(appwire.AuthApiKeySetParams{Provider: "openai-codex", Value: "sk-openai-XXX"})
	if err == nil {
		t.Fatal("ApiKeySet(openai-codex) succeeded; a key the Codex transport never reads must be refused")
	}
	if !strings.Contains(err.Error(), "evener openai login") {
		t.Errorf("the refusal names the way in: %v", err)
	}
	store2, _ := credentials.LoadStore(credsPath)
	if v, _ := store2.Get("openai-codex"); v != "" {
		t.Errorf("the refused key reached credentials.toml: %q", v)
	}
}

func TestAuth_OpenAI_Logout_ClearsStoredFileKey(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	store, _ := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	c := newHubAuthControllerWithStore(dir, store)
	c.stateDir = t.TempDir()
	attachTestRegistry(t, c) // no OAuth record
	if err := store.Set("openai-codex", "sk-test-123"); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
	resp, err := c.Logout(appwire.AuthLogoutParams{Provider: "openai-codex"})
	if err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if !resp.Removed || resp.Status.ActiveSource != "none" {
		t.Fatalf("resp=%+v, want removed + source none", resp)
	}
	if v, _ := store.Get("openai"); v != "" {
		t.Errorf("file key still present: %q", v)
	}
}

func TestAuth_Codex_Logout_OAuthRemovalLeavesNoCredential(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	store, _ := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	c := newHubAuthControllerWithStore(dir, store)
	c.stateDir = t.TempDir()
	attachTestRegistry(t, c)
	if err := store.Set("openai-codex", "sk-test-123"); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
	if err := authopenai.SaveAuth(c.stateDir, "openai-codex", authopenai.AuthRecord{
		Version: 1, Provider: "openai", Source: authopenai.AuthSourceOAuth,
		ObtainedAt: time.Now().Add(-time.Hour), TokenType: "Bearer",
		AccessToken: "acc", RefreshToken: "ref",
		Expiry: time.Now().Add(time.Hour), Email: "o@example.com",
	}); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}
	resp, err := c.Logout(appwire.AuthLogoutParams{Provider: "openai-codex"})
	if err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if !resp.Removed || resp.Status.ActiveSource != "none" {
		t.Fatalf("resp=%+v, want the OAuth record removed and no credential left (the store is not a Codex credential)", resp)
	}
	if resp.Status.HasStoredOAuth {
		t.Errorf("OAuth record still present after logout")
	}
}

func TestAuth_Codex_Logout_CorruptOAuthDeletedLeavesNoCredential(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	store, _ := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	c := newHubAuthControllerWithStore(dir, store)
	c.stateDir = t.TempDir()
	attachTestRegistry(t, c)
	if err := store.Set("openai-codex", "sk-test-123"); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
	authPath := authopenai.AuthFilePath(c.stateDir, "openai-codex")
	if err := os.MkdirAll(filepath.Dir(authPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	resp, err := c.Logout(appwire.AuthLogoutParams{Provider: "openai-codex"})
	if err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if !resp.Removed || resp.Status.ActiveSource != "none" {
		t.Fatalf("resp=%+v, want the corrupt record removed and no credential left", resp)
	}
	if _, statErr := os.Stat(authPath); !os.IsNotExist(statErr) {
		t.Errorf("corrupt openai.json still present after logout: %v", statErr)
	}
}

func TestAuth_Codex_Status_ExpiredOAuthStaysTheSource(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	store, _ := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	c := newHubAuthControllerWithStore(dir, store)
	c.stateDir = t.TempDir()
	attachTestRegistry(t, c)
	if err := store.Set("openai-codex", "sk-test-123"); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
	// Expired OAuth record (expiry in the past) alongside a stored file key.
	if err := authopenai.SaveAuth(c.stateDir, "openai-codex", authopenai.AuthRecord{
		Version: 1, Provider: "openai", Source: authopenai.AuthSourceOAuth,
		ObtainedAt: time.Now().Add(-2 * time.Hour), TokenType: "Bearer",
		AccessToken: "acc", RefreshToken: "ref",
		Expiry: time.Now().Add(-time.Hour), Email: "o@example.com",
	}); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}
	got, err := c.Status(appwire.AuthStatusParams{Provider: "openai-codex"})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	// Expired OAuth stays the source the registry resolves (the record exists),
	// with NeedsLogin carrying the "sign in again" signal.
	if got.ActiveSource != authopenai.AuthSourceOAuth || !got.NeedsLogin {
		t.Fatalf("status=%+v, want oauth active + NeedsLogin (expired record must not fall back to file)", got)
	}
	if !got.HasStoredFile {
		t.Errorf("status=%+v, want HasStoredFile true (file shadowed beneath expired oauth)", got)
	}
}

func TestAuth_NewControllerWithNilStore_PersistsWritesToDefaultPath(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	c := newHubAuthControllerWithStore("", nil)
	if _, err := c.ApiKeySet(appwire.AuthApiKeySetParams{Provider: "anthropic", Value: "sk-ant-PERSIST"}); err != nil {
		t.Fatalf("ApiKeySet: %v", err)
	}
	// A nil store must fall back to the on-disk default, not a path-less store
	// whose writes silently no-op. Verify the key actually reached disk.
	credsPath := filepath.Join(filepath.Dir(c.stateDir), "credentials.toml")
	reloaded, err := credentials.LoadStore(credsPath)
	if err != nil {
		t.Fatalf("LoadStore(%s): %v", credsPath, err)
	}
	if v, ok := reloaded.Get("anthropic"); v != "sk-ant-PERSIST" || !ok {
		t.Errorf("nil-store fallback did not persist to %s: v=%q ok=%v", credsPath, v, ok)
	}
}

func TestAuth_DeviceStart_ReturnsCodeAndStoresFlow(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	store, _ := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	c := newHubAuthControllerWithStore(dir, store)
	attachTestRegistry(t, c)
	c.requestDeviceCode = func(context.Context, *http.Client, authopenai.Config) (authopenai.DeviceCode, error) {
		return authopenai.DeviceCode{UserCode: "USER-1", VerificationURL: "https://auth.openai.com/codex/device", DeviceAuthID: "dev-1", Interval: 5 * time.Second}, nil
	}
	got, err := c.DeviceStart(context.Background(), appwire.AuthDeviceStartParams{Provider: "openai-codex"})
	if err != nil {
		t.Fatalf("DeviceStart: %v", err)
	}
	if got.Fallback || got.UserCode != "USER-1" || got.VerificationURL == "" || got.IntervalSeconds != 5 || got.FlowID == "" {
		t.Fatalf("resp=%+v, want code fields + interval 5 + flowId", got)
	}
}

func TestAuth_DeviceStart_FallbackWhenNotEnabled(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	store, _ := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	c := newHubAuthControllerWithStore(dir, store)
	attachTestRegistry(t, c)
	c.requestDeviceCode = func(context.Context, *http.Client, authopenai.Config) (authopenai.DeviceCode, error) {
		return authopenai.DeviceCode{}, authopenai.ErrDeviceCodeNotEnabled
	}
	got, err := c.DeviceStart(context.Background(), appwire.AuthDeviceStartParams{Provider: "openai-codex"})
	if err != nil {
		t.Fatalf("DeviceStart: %v", err)
	}
	if !got.Fallback {
		t.Fatalf("resp=%+v, want Fallback=true", got)
	}
}

func TestAuth_DevicePoll_PendingThenAuthorized(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	store, _ := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	c := newHubAuthControllerWithStore(dir, store)
	c.stateDir = t.TempDir()
	attachTestRegistry(t, c)
	c.requestDeviceCode = func(context.Context, *http.Client, authopenai.Config) (authopenai.DeviceCode, error) {
		return authopenai.DeviceCode{UserCode: "U", VerificationURL: "https://x", DeviceAuthID: "d", Interval: time.Second}, nil
	}
	pending := true
	c.pollDeviceOnce = func(context.Context, *http.Client, authopenai.Config, authopenai.DeviceCode) (authopenai.DeviceCodeSuccess, bool, error) {
		if pending {
			pending = false
			return authopenai.DeviceCodeSuccess{}, true, nil
		}
		return authopenai.DeviceCodeSuccess{AuthorizationCode: "ac", CodeVerifier: "cv"}, false, nil
	}
	c.exchangeDevice = func(context.Context, *http.Client, authopenai.Config, string, string) (authopenai.TokenSet, error) {
		return authopenai.TokenSet{AccessToken: "at", RefreshToken: "rt", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}, nil
	}
	start, err := c.DeviceStart(context.Background(), appwire.AuthDeviceStartParams{Provider: "openai-codex"})
	if err != nil {
		t.Fatalf("DeviceStart: %v", err)
	}
	p1, err := c.DevicePoll(context.Background(), appwire.AuthDevicePollParams{Provider: "openai-codex", FlowID: start.FlowID})
	if err != nil || p1.State != "pending" {
		t.Fatalf("first poll = %+v err=%v, want pending", p1, err)
	}
	p2, err := c.DevicePoll(context.Background(), appwire.AuthDevicePollParams{Provider: "openai-codex", FlowID: start.FlowID})
	if err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if p2.State != "authorized" || p2.Status.ActiveSource != authopenai.AuthSourceOAuth {
		t.Fatalf("second poll = %+v, want authorized + oauth", p2)
	}
}

func TestAuth_DevicePoll_UnknownFlowExpired(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	store, _ := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	c := newHubAuthControllerWithStore(dir, store)
	attachTestRegistry(t, c)
	got, err := c.DevicePoll(context.Background(), appwire.AuthDevicePollParams{Provider: "openai-codex", FlowID: "nope"})
	if err != nil || got.State != "expired" {
		t.Fatalf("got=%+v err=%v, want expired", got, err)
	}
}

func TestAuth_DevicePoll_ExistingFlowExpiresAfter15Min(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	store, _ := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	c := newHubAuthControllerWithStore(dir, store)
	attachTestRegistry(t, c)
	now := time.Now()
	c.now = func() time.Time { return now }
	c.requestDeviceCode = func(context.Context, *http.Client, authopenai.Config) (authopenai.DeviceCode, error) {
		return authopenai.DeviceCode{UserCode: "U", VerificationURL: "https://x", DeviceAuthID: "d", Interval: time.Second}, nil
	}
	pollCalls := 0
	c.pollDeviceOnce = func(context.Context, *http.Client, authopenai.Config, authopenai.DeviceCode) (authopenai.DeviceCodeSuccess, bool, error) {
		pollCalls++
		return authopenai.DeviceCodeSuccess{}, true, nil
	}
	start, err := c.DeviceStart(context.Background(), appwire.AuthDeviceStartParams{Provider: "openai-codex"})
	if err != nil {
		t.Fatalf("DeviceStart: %v", err)
	}
	now = now.Add(16 * time.Minute) // advance the clock past the 15-minute window
	got, err := c.DevicePoll(context.Background(), appwire.AuthDevicePollParams{Provider: "openai-codex", FlowID: start.FlowID})
	if err != nil || got.State != "expired" {
		t.Fatalf("got=%+v err=%v, want expired", got, err)
	}
	if pollCalls != 0 {
		t.Errorf("pollDeviceOnce called %d times, want 0 (expiry checked before polling)", pollCalls)
	}
	// the expired flow should be dropped — a second poll returns expired (unknown flow)
	got2, _ := c.DevicePoll(context.Background(), appwire.AuthDevicePollParams{Provider: "openai-codex", FlowID: start.FlowID})
	if got2.State != "expired" {
		t.Errorf("after expiry the flow should be dropped; got %+v", got2)
	}
}

// TestAuth_InstanceStatus_EnvVarReportedFromRegistrySource ensures the pane
// names the variable that actually resolved: the registry reports the
// credential source as env:<VAR>, and instanceStatus reads the variable name
// straight out of it rather than guessing from the instance name.
func TestAuth_InstanceStatus_EnvVarReportedFromRegistrySource(t *testing.T) {
	dir := t.TempDir()
	store, err := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	c := newHubAuthControllerWithStore(dir, store)
	c.stateDir = t.TempDir()
	c.reg = newTestRegistry(t, c.stateDir, "", store, map[string]string{"ANTHROPIC_API_KEY": "sk-ant-test"})

	inst, ok := c.reg.Get().Instance("anthropic")
	if !ok {
		t.Fatal("ANTHROPIC_API_KEY makes anthropic an implicit instance")
	}
	status := c.instanceStatus(inst)
	if status.ActiveSource != "env:ANTHROPIC_API_KEY" {
		t.Errorf("ActiveSource=%q, want env:ANTHROPIC_API_KEY", status.ActiveSource)
	}
	if status.EnvVar != "ANTHROPIC_API_KEY" {
		t.Errorf("EnvVar=%q, want ANTHROPIC_API_KEY", status.EnvVar)
	}
	if !status.SignedIn {
		t.Errorf("SignedIn=false, want true")
	}
}

// The three tests below pin one rule: the instance a credential write was
// checked against is the instance it lands on. A rename holds credMu
// exclusively while it re-keys providers.toml and reloads the registry, so a
// check made outside that lock describes an instance the write no longer
// reaches.

// TestAuth_ApiKeySetRechecksTheInstanceUnderTheCredentialLock: the fixture's
// entry shadows the curated Codex provider with a bearer instance, so a key
// is exactly what it reads. Once a rename moves that entry away, the name
// resolves to the curated provider again, which authenticates with an OAuth
// record alone — and a key stored under it is enough to make it reappear as
// an implicit instance.
func TestAuth_ApiKeySetRechecksTheInstanceUnderTheCredentialLock(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	tomlPath := writeProvidersToml(t, dir, `[providers.openai-codex]
base = "openai"
`)
	ctrl := newTestAuthController(t, dir, t.TempDir(), tomlPath)
	if ctrl.instanceIsCodex("openai-codex") {
		t.Fatal("the fixture's entry must be the bearer shadow a key belongs under")
	}

	ctrl.credMu.Lock()
	done := make(chan error, 1)
	go func() {
		_, err := ctrl.ApiKeySet(appwire.AuthApiKeySetParams{Provider: "openai-codex", Value: "sk-x"})
		done <- err
	}()
	// Only so a check made outside the lock has run by the time the rename
	// lands: what the test asserts does not depend on the wait.
	time.Sleep(100 * time.Millisecond)
	renameProvidersEntry(t, ctrl, tomlPath, `[providers.work]
base = "openai"
`)
	ctrl.credMu.Unlock()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "evener openai login") {
			t.Fatalf("ApiKeySet = %v, want the Codex refusal for the name the rename left behind", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ApiKeySet never returned after the rename released the lock")
	}
	if v, ok := ctrl.creds.Get("openai-codex"); ok {
		t.Fatalf("the key landed under openai-codex (%q), which reads OAuth records alone", v)
	}
}

// TestAuth_LoginCompleteRechecksTheInstanceBeforeItSavesTheRecord: the token
// exchange is where a browser round trip's worth of time passes, so the
// rename happens inside it. The record must not land under a name that no
// longer authenticates through OAuth.
func TestAuth_LoginCompleteRechecksTheInstanceBeforeItSavesTheRecord(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	tomlPath := writeProvidersToml(t, dir, codexInstanceToml)
	ctrl := newTestAuthController(t, dir, stateDir, tomlPath)
	ctrl.cfg = authopenai.Config{IssuerBaseURL: "https://auth.example.test"}
	ctrl.client = &http.Client{}
	ctrl.exchangeCode = func(context.Context, *http.Client, authopenai.Config, authopenai.TokenExchangeRequest) (authopenai.TokenSet, error) {
		renameProvidersEntry(t, ctrl, tomlPath, `[providers.work2]
base = "openai-codex"
`)
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
	state := authorizeURL.Query().Get("state")

	_, err = ctrl.LoginComplete(context.Background(), appwire.AuthLoginCompleteParams{
		Provider:    "work",
		FlowID:      start.FlowID,
		RedirectURL: "http://localhost:1455/auth/callback?code=auth-code&state=" + url.QueryEscape(state),
	})
	if err == nil || !strings.Contains(err.Error(), "OAuth is not supported") {
		t.Fatalf("LoginComplete = %v, want the refusal for the name the rename left behind", err)
	}
	if _, err := authopenai.LoadAuth(stateDir, "work"); !errors.Is(err, authopenai.ErrAuthNotFound) {
		t.Fatalf("LoadAuth(work) err = %v, want ErrAuthNotFound: the record must not land under the stale name", err)
	}
}

// TestAuth_DevicePollRechecksTheInstanceBeforeItSavesTheRecord is the same
// property on the device flow, whose own exchange is the long step.
func TestAuth_DevicePollRechecksTheInstanceBeforeItSavesTheRecord(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	tomlPath := writeProvidersToml(t, dir, codexInstanceToml)
	ctrl := newTestAuthController(t, dir, stateDir, tomlPath)
	ctrl.requestDeviceCode = func(context.Context, *http.Client, authopenai.Config) (authopenai.DeviceCode, error) {
		return authopenai.DeviceCode{UserCode: "U", VerificationURL: "https://x", DeviceAuthID: "d", Interval: time.Second}, nil
	}
	ctrl.pollDeviceOnce = func(context.Context, *http.Client, authopenai.Config, authopenai.DeviceCode) (authopenai.DeviceCodeSuccess, bool, error) {
		return authopenai.DeviceCodeSuccess{AuthorizationCode: "ac", CodeVerifier: "cv"}, false, nil
	}
	ctrl.exchangeDevice = func(context.Context, *http.Client, authopenai.Config, string, string) (authopenai.TokenSet, error) {
		renameProvidersEntry(t, ctrl, tomlPath, `[providers.work2]
base = "openai-codex"
`)
		return authopenai.TokenSet{AccessToken: "at", RefreshToken: "rt", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}, nil
	}

	start, err := ctrl.DeviceStart(context.Background(), appwire.AuthDeviceStartParams{Provider: "work"})
	if err != nil {
		t.Fatalf("DeviceStart: %v", err)
	}
	_, err = ctrl.DevicePoll(context.Background(), appwire.AuthDevicePollParams{Provider: "work", FlowID: start.FlowID})
	if err == nil || !strings.Contains(err.Error(), "OAuth is not supported") {
		t.Fatalf("DevicePoll = %v, want the refusal for the name the rename left behind", err)
	}
	if _, err := authopenai.LoadAuth(stateDir, "work"); !errors.Is(err, authopenai.ErrAuthNotFound) {
		t.Fatalf("LoadAuth(work) err = %v, want ErrAuthNotFound: the record must not land under the stale name", err)
	}
}

// TestAuth_LogoutClearsTheStoreTheSchemeUnderTheLockNames: which layer a
// logout clears is decided by the instance's auth scheme, so that decision
// belongs in the same locked write as the removal. The fixture's bearer
// shadow says "clear the stored key"; once the rename moves it away the name
// is the curated Codex provider, whose logout clears the OAuth record and
// leaves the stored key alone.
func TestAuth_LogoutClearsTheStoreTheSchemeUnderTheLockNames(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	if err := authopenai.SaveAuth(stateDir, "openai-codex", makeOAuthRecord("openai-codex", "codex@example.com")); err != nil {
		t.Fatalf("SaveAuth(openai-codex): %v", err)
	}
	tomlPath := writeProvidersToml(t, dir, `[providers.openai-codex]
base = "openai"
`)
	ctrl := newTestAuthController(t, dir, stateDir, tomlPath)
	if err := ctrl.creds.Set("openai-codex", "sk-x"); err != nil {
		t.Fatalf("seed the stored key: %v", err)
	}
	if ctrl.instanceIsCodex("openai-codex") {
		t.Fatal("the fixture's entry must be the bearer shadow whose logout clears the stored key")
	}

	ctrl.credMu.Lock()
	done := make(chan error, 1)
	go func() {
		_, err := ctrl.Logout(appwire.AuthLogoutParams{Provider: "openai-codex"})
		done <- err
	}()
	// Only so a decision made outside the lock has run by the time the rename
	// lands: what the test asserts does not depend on the wait.
	time.Sleep(100 * time.Millisecond)
	renameProvidersEntry(t, ctrl, tomlPath, `[providers.work]
base = "openai"
`)
	ctrl.credMu.Unlock()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Logout: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Logout never returned after the rename released the lock")
	}
	if _, err := authopenai.LoadAuth(stateDir, "openai-codex"); !errors.Is(err, authopenai.ErrAuthNotFound) {
		t.Fatalf("LoadAuth(openai-codex) err = %v, want ErrAuthNotFound: the OAuth record is the layer the name's scheme clears", err)
	}
	if v, ok := ctrl.creds.Get("openai-codex"); !ok || v != "sk-x" {
		t.Fatalf("stored key = %q/%v, want sk-x/true: a Codex logout clears the record alone", v, ok)
	}
}

// renameProvidersEntry is what an instance rename leaves behind for a
// credential write already in flight: providers.toml re-keyed and the
// registry reloaded onto it.
func renameProvidersEntry(t *testing.T, c *hubAuthController, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("rewrite providers.toml: %v", err)
	}
	if err := c.reg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
}

// attachTestRegistry gives a controller a hermetic registry rooted at its own
// state dir and credentials store, so instance resolution sees exactly the
// credential state the test set up and nothing from the machine.
func attachTestRegistry(t *testing.T, c *hubAuthController) {
	t.Helper()
	c.reg = newTestRegistry(t, c.stateDir, "", c.creds, nil)
}
