package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/appwire"
	authopenai "primeradiant.com/serf/auth/openai"
	"primeradiant.com/serf/auth/openai/oaitest"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/internal/credentials"
)

func TestHubRPCAuthStatusUsesUserScopedOpenAIAuth(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	xdgStateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", xdgStateHome)
	userStateDir := authopenai.DefaultStateDirWithStateHome(xdgStateHome)
	if err := authopenai.SaveAuth(userStateDir, "openai", authopenai.AuthRecord{
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

	hub := newHubRPCTestServer(t, hubcore.WebConfig{Past: hubcore.NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	init, err := client.Initialize(context.Background(), appwire.InitializeParams{})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if !init.Features.Auth {
		t.Fatalf("Initialize features=%+v, want auth advertised", init.Features)
	}

	status, err := client.AuthStatus(context.Background(), appwire.AuthStatusParams{Provider: "openai"})
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

func TestHubRPCAuthStatusPrefersStoredOAuthOverEnv(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	t.Setenv("OPENAI_API_KEY", "env-token")
	xdgStateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", xdgStateHome)
	if err := authopenai.SaveAuth(authopenai.DefaultStateDirWithStateHome(xdgStateHome), "openai", authopenai.AuthRecord{
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

	hub := newHubRPCTestServer(t, hubcore.WebConfig{Past: hubcore.NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	status, err := client.AuthStatus(context.Background(), appwire.AuthStatusParams{Provider: "openai"})
	if err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}
	if !status.SignedIn || status.ActiveSource != authopenai.AuthSourceOAuth || !status.HasStoredOAuth || status.StoredEmail != "stored@example.com" || status.Email != "stored@example.com" {
		t.Fatalf("status=%+v, want stored OAuth to win over env-token", status)
	}
}

func TestHubRPCAuthStatusFallsBackToEnvWhenNoStoredOAuth(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	t.Setenv("OPENAI_API_KEY", "env-token")
	xdgStateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", xdgStateHome)

	hub := newHubRPCTestServer(t, hubcore.WebConfig{Past: hubcore.NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	status, err := client.AuthStatus(context.Background(), appwire.AuthStatusParams{Provider: "openai"})
	if err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}
	if !status.SignedIn || status.ActiveSource != authopenai.AuthSourceEnv || status.HasStoredOAuth {
		t.Fatalf("status=%+v, want env-token to be active with no stored OAuth", status)
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
			ctrl.now = func() time.Time { return now }
			if err := authopenai.SaveAuth(ctrl.stateDir, "openai", authopenai.AuthRecord{
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

			status, err := ctrl.Status(appwire.AuthStatusParams{Provider: "openai"})
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			if status.SignedIn != tc.wantSignedIn || status.NeedsRefresh != tc.wantRefresh || status.NeedsLogin != tc.wantLogin {
				t.Fatalf("status=%+v, want signedIn=%t needsRefresh=%t needsLogin=%t", status, tc.wantSignedIn, tc.wantRefresh, tc.wantLogin)
			}
		})
	}
}

func TestOpenAIStateDirFromEnvUsesWindowsHomePrecedence(t *testing.T) {
	got := openAIStateDirFromLookup("windows", func(key string) (string, bool) {
		env := map[string]string{
			"HOME":        `C:\msys\home\jesse`,
			"USERPROFILE": `C:\Users\Jesse`,
		}
		value, ok := env[key]
		return value, ok
	})
	want := filepath.Join(`C:\Users\Jesse`, ".local", "state", "serf") //nolint:gocritic // filepathJoin: base is a full home path; mirrors the impl under test
	if got != want {
		t.Fatalf("stateDir=%q, want %q", got, want)
	}
}

func TestOpenAIStateDirFromEnvUsesWindowsHomeDrivePath(t *testing.T) {
	got := openAIStateDirFromLookup("windows", func(key string) (string, bool) {
		env := map[string]string{
			"HOME":      `C:\msys\home\jesse`,
			"HOMEDRIVE": `D:`,
			"HOMEPATH":  `\Users\Jesse`,
		}
		value, ok := env[key]
		return value, ok
	})
	want := filepath.Join(`D:\Users\Jesse`, ".local", "state", "serf") //nolint:gocritic // filepathJoin: base is a full home path; mirrors the impl under test
	if got != want {
		t.Fatalf("stateDir=%q, want %q", got, want)
	}
}

func TestOpenAIStateDirFromEnvWindowsIgnoresHomeFallback(t *testing.T) {
	got := openAIStateDirFromLookup("windows", func(key string) (string, bool) {
		env := map[string]string{
			"HOME": `C:\msys\home\jesse`,
		}
		value, ok := env[key]
		return value, ok
	})
	want := filepath.Join(os.TempDir(), ".local", "state", "serf")
	if got != want {
		t.Fatalf("stateDir=%q, want %q", got, want)
	}
}

func TestOpenAIStateDirFromEnvDoesNotFallBackToProcessEnv(t *testing.T) {
	processStateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", processStateHome)

	got := openAIStateDirFromEnv(map[string]string{})
	want := filepath.Join(os.TempDir(), ".local", "state", "serf")
	if got != want {
		t.Fatalf("stateDir=%q, want %q", got, want)
	}
}

func TestHubRPCAuthLogoutRemovesUserScopedOpenAIAuth(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	xdgStateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", xdgStateHome)
	userStateDir := authopenai.DefaultStateDirWithStateHome(xdgStateHome)
	if err := authopenai.SaveAuth(userStateDir, "openai", authopenai.AuthRecord{
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

	hub := newHubRPCTestServer(t, hubcore.WebConfig{Past: hubcore.NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	resp, err := client.AuthLogout(context.Background(), appwire.AuthLogoutParams{Provider: "openai"})
	if err != nil {
		t.Fatalf("AuthLogout: %v", err)
	}
	if !resp.Removed || resp.Status.ActiveSource != authopenai.AuthSourceSignedOut {
		t.Fatalf("logout=%+v, want removed and signed out", resp)
	}
	if _, err := authopenai.LoadAuth(userStateDir, "openai"); !errors.Is(err, authopenai.ErrAuthNotFound) {
		t.Fatalf("LoadAuth() err=%v, want ErrAuthNotFound", err)
	}
}

func TestHubAuthControllerManualPastebackSavesOpenAIAuth(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	ctrl := newHubAuthController()
	ctrl.stateDir = t.TempDir()
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

	start, err := ctrl.LoginStart(appwire.AuthLoginStartParams{Provider: "openai"})
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
		Provider:    "openai",
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
	record, err := authopenai.LoadAuth(ctrl.stateDir, "openai")
	if err != nil {
		t.Fatalf("LoadAuth: %v", err)
	}
	if record.AccessToken != "access-token" || record.Email != "oauth@example.com" {
		t.Fatalf("record=%+v, want saved token and email", record)
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
	got, err := c.ApiKeySet(appwire.AuthApiKeySetParams{Provider: "anthropic", Value: "sk-ant-XXX"})
	if err != nil {
		t.Fatalf("ApiKeySet: %v", err)
	}
	if got.ActiveSource != string(credentials.SourceFile) {
		t.Errorf("ActiveSource = %q, want file", got.ActiveSource)
	}
	// Reload from disk; value should persist.
	store2, _ := credentials.LoadStore(credsPath)
	v, src := store2.Get("anthropic")
	if v != "sk-ant-XXX" || src != credentials.SourceFile {
		t.Errorf("after ApiKeySet: v=%q src=%q", v, src)
	}
}

func TestAuth_Status_AnthropicViaStore(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	stateDir := t.TempDir()
	store, _ := credentials.LoadStore(filepath.Join(stateDir, "credentials.toml"))
	_ = store.Set("anthropic", "key")
	c := newHubAuthControllerWithStore(stateDir, store)
	got, err := c.Status(appwire.AuthStatusParams{Provider: "anthropic"})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !got.SignedIn || got.ActiveSource != string(credentials.SourceFile) {
		t.Errorf("Status anthropic = %+v", got)
	}
	if len(got.AuthModes) == 0 {
		t.Errorf("AuthModes empty: %+v", got)
	}
}

func TestAuth_OpenAI_Status_ReflectsStoredFileKey(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	store, _ := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	c := newHubAuthControllerWithStore(dir, store)
	c.stateDir = t.TempDir() // empty: no OAuth record
	if err := store.Set("openai", "sk-test-123"); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
	got, err := c.Status(appwire.AuthStatusParams{Provider: "openai"})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !got.SignedIn || got.ActiveSource != string(credentials.SourceFile) || !got.HasStoredFile {
		t.Fatalf("status=%+v, want signed-in file with HasStoredFile", got)
	}
	if got.HasStoredOAuth {
		t.Fatalf("status=%+v, want no stored OAuth", got)
	}
}

func TestAuth_OpenAI_Status_OAuthShadowsStoredFileKey(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	store, _ := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	c := newHubAuthControllerWithStore(dir, store)
	c.stateDir = t.TempDir()
	if err := store.Set("openai", "sk-test-123"); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
	if err := authopenai.SaveAuth(c.stateDir, "openai", authopenai.AuthRecord{
		Version: 1, Provider: "openai", Source: authopenai.AuthSourceOAuth,
		ObtainedAt: time.Now().Add(-time.Hour), TokenType: "Bearer",
		AccessToken: "acc", RefreshToken: "ref",
		Expiry: time.Now().Add(time.Hour), Email: "o@example.com",
	}); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}
	got, err := c.Status(appwire.AuthStatusParams{Provider: "openai"})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got.ActiveSource != authopenai.AuthSourceOAuth || !got.HasStoredFile || !got.HasStoredOAuth {
		t.Fatalf("status=%+v, want oauth active with file shadowed", got)
	}
}

func TestAuth_OpenAI_Status_CorruptOAuthFallsBackToFile(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	store, _ := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	c := newHubAuthControllerWithStore(dir, store)
	c.stateDir = t.TempDir()
	if err := store.Set("openai", "sk-test-123"); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
	authPath := authopenai.AuthFilePath(c.stateDir, "openai")
	if err := os.MkdirAll(filepath.Dir(authPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := c.Status(appwire.AuthStatusParams{Provider: "openai"})
	if err != nil {
		t.Fatalf("Status returned error on corrupt record: %v", err)
	}
	if !got.SignedIn || got.ActiveSource != string(credentials.SourceFile) || !got.HasStoredFile {
		t.Fatalf("status=%+v, want signed-in file (corrupt oauth treated as absent)", got)
	}
}

func TestAuth_OpenAI_ApiKeySet_PersistsAndReportsFile(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	credsPath := filepath.Join(dir, "credentials.toml")
	store, _ := credentials.LoadStore(credsPath)
	c := newHubAuthControllerWithStore(dir, store)
	c.stateDir = t.TempDir() // no OAuth record

	got, err := c.ApiKeySet(appwire.AuthApiKeySetParams{Provider: "openai", Value: "sk-openai-XXX"})
	if err != nil {
		t.Fatalf("ApiKeySet(openai): %v", err)
	}
	if got.ActiveSource != string(credentials.SourceFile) || !got.HasStoredFile {
		t.Fatalf("status=%+v, want file active with HasStoredFile", got)
	}
	store2, _ := credentials.LoadStore(credsPath)
	v, src := store2.Get("openai")
	if v != "sk-openai-XXX" || src != credentials.SourceFile {
		t.Errorf("after ApiKeySet: v=%q src=%q, want sk-openai-XXX/file", v, src)
	}
}

func TestAuth_OpenAI_Logout_ClearsStoredFileKey(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	store, _ := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	c := newHubAuthControllerWithStore(dir, store)
	c.stateDir = t.TempDir() // no OAuth record
	if err := store.Set("openai", "sk-test-123"); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
	resp, err := c.Logout(appwire.AuthLogoutParams{Provider: "openai"})
	if err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if !resp.Removed || resp.Status.ActiveSource != authopenai.AuthSourceSignedOut {
		t.Fatalf("resp=%+v, want removed + signed-out", resp)
	}
	if v, _ := store.Get("openai"); v != "" {
		t.Errorf("file key still present: %q", v)
	}
}

func TestAuth_OpenAI_Logout_OAuthRevealsStoredFileKey(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	store, _ := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	c := newHubAuthControllerWithStore(dir, store)
	c.stateDir = t.TempDir()
	if err := store.Set("openai", "sk-test-123"); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
	if err := authopenai.SaveAuth(c.stateDir, "openai", authopenai.AuthRecord{
		Version: 1, Provider: "openai", Source: authopenai.AuthSourceOAuth,
		ObtainedAt: time.Now().Add(-time.Hour), TokenType: "Bearer",
		AccessToken: "acc", RefreshToken: "ref",
		Expiry: time.Now().Add(time.Hour), Email: "o@example.com",
	}); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}
	resp, err := c.Logout(appwire.AuthLogoutParams{Provider: "openai"})
	if err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if !resp.Removed || resp.Status.ActiveSource != string(credentials.SourceFile) {
		t.Fatalf("resp=%+v, want removed OAuth revealing file", resp)
	}
	if resp.Status.HasStoredOAuth {
		t.Errorf("OAuth record still present after logout")
	}
}

func TestAuth_OpenAI_Logout_CorruptOAuthDeletedRevealsFile(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	store, _ := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	c := newHubAuthControllerWithStore(dir, store)
	c.stateDir = t.TempDir()
	if err := store.Set("openai", "sk-test-123"); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
	authPath := authopenai.AuthFilePath(c.stateDir, "openai")
	if err := os.MkdirAll(filepath.Dir(authPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	resp, err := c.Logout(appwire.AuthLogoutParams{Provider: "openai"})
	if err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if !resp.Removed || resp.Status.ActiveSource != string(credentials.SourceFile) {
		t.Fatalf("resp=%+v, want corrupt oauth removed revealing file", resp)
	}
	if _, statErr := os.Stat(authPath); !os.IsNotExist(statErr) {
		t.Errorf("corrupt openai.json still present after logout: %v", statErr)
	}
}

func TestAuth_OpenAI_Status_ExpiredOAuthStillShadowsFileKey(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	store, _ := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	c := newHubAuthControllerWithStore(dir, store)
	c.stateDir = t.TempDir()
	if err := store.Set("openai", "sk-test-123"); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
	// Expired OAuth record (expiry in the past) alongside a stored file key.
	if err := authopenai.SaveAuth(c.stateDir, "openai", authopenai.AuthRecord{
		Version: 1, Provider: "openai", Source: authopenai.AuthSourceOAuth,
		ObtainedAt: time.Now().Add(-2 * time.Hour), TokenType: "Bearer",
		AccessToken: "acc", RefreshToken: "ref",
		Expiry: time.Now().Add(-time.Hour), Email: "o@example.com",
	}); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}
	got, err := c.Status(appwire.AuthStatusParams{Provider: "openai"})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	// Expired OAuth stays the effective source (NeedsLogin); must NOT downgrade to file.
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
	if v, src := reloaded.Get("anthropic"); v != "sk-ant-PERSIST" || src != credentials.SourceFile {
		t.Errorf("nil-store fallback did not persist to %s: v=%q src=%q", credsPath, v, src)
	}
}

func TestAuth_DeviceStart_ReturnsCodeAndStoresFlow(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	store, _ := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	c := newHubAuthControllerWithStore(dir, store)
	c.requestDeviceCode = func(context.Context, *http.Client, authopenai.Config) (authopenai.DeviceCode, error) {
		return authopenai.DeviceCode{UserCode: "USER-1", VerificationURL: "https://auth.openai.com/codex/device", DeviceAuthID: "dev-1", Interval: 5 * time.Second}, nil
	}
	got, err := c.DeviceStart(context.Background(), appwire.AuthDeviceStartParams{Provider: "openai"})
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
	c.requestDeviceCode = func(context.Context, *http.Client, authopenai.Config) (authopenai.DeviceCode, error) {
		return authopenai.DeviceCode{}, authopenai.ErrDeviceCodeNotEnabled
	}
	got, err := c.DeviceStart(context.Background(), appwire.AuthDeviceStartParams{Provider: "openai"})
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
	start, err := c.DeviceStart(context.Background(), appwire.AuthDeviceStartParams{Provider: "openai"})
	if err != nil {
		t.Fatalf("DeviceStart: %v", err)
	}
	p1, err := c.DevicePoll(context.Background(), appwire.AuthDevicePollParams{Provider: "openai", FlowID: start.FlowID})
	if err != nil || p1.State != "pending" {
		t.Fatalf("first poll = %+v err=%v, want pending", p1, err)
	}
	p2, err := c.DevicePoll(context.Background(), appwire.AuthDevicePollParams{Provider: "openai", FlowID: start.FlowID})
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
	got, err := c.DevicePoll(context.Background(), appwire.AuthDevicePollParams{Provider: "openai", FlowID: "nope"})
	if err != nil || got.State != "expired" {
		t.Fatalf("got=%+v err=%v, want expired", got, err)
	}
}

func TestAuth_DevicePoll_ExistingFlowExpiresAfter15Min(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	store, _ := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	c := newHubAuthControllerWithStore(dir, store)
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
	start, err := c.DeviceStart(context.Background(), appwire.AuthDeviceStartParams{Provider: "openai"})
	if err != nil {
		t.Fatalf("DeviceStart: %v", err)
	}
	now = now.Add(16 * time.Minute) // advance the clock past the 15-minute window
	got, err := c.DevicePoll(context.Background(), appwire.AuthDevicePollParams{Provider: "openai", FlowID: start.FlowID})
	if err != nil || got.State != "expired" {
		t.Fatalf("got=%+v err=%v, want expired", got, err)
	}
	if pollCalls != 0 {
		t.Errorf("pollDeviceOnce called %d times, want 0 (expiry checked before polling)", pollCalls)
	}
	// the expired flow should be dropped — a second poll returns expired (unknown flow)
	got2, _ := c.DevicePoll(context.Background(), appwire.AuthDevicePollParams{Provider: "openai", FlowID: start.FlowID})
	if got2.State != "expired" {
		t.Errorf("after expiry the flow should be dropped; got %+v", got2)
	}
}

// TestAuth_InstanceStatus_EnvVarReportedFromType ensures that instanceStatus
// reports the correct EnvVar when the active credential comes from the type's
// env var (e.g. ANTHROPIC_API_KEY) rather than the instance-name's env var.
// This is Fix 2: Layers(name) only checks the name's env vars; InstanceLayers
// also checks the type's env vars, matching ResolveKey resolution order.
func TestAuth_InstanceStatus_EnvVarReportedFromType(t *testing.T) {
	// Set ANTHROPIC_API_KEY in the environment; leave instance name "work" unset.
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	t.Setenv("WORK_API_KEY", "")

	stateDir := t.TempDir()
	store, err := credentials.LoadStore(filepath.Join(stateDir, "credentials.toml"))
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	// No stored file key for "work".
	c := newHubAuthControllerWithStore(stateDir, store)

	status := c.instanceStatus("work", "anthropic")

	if status.ActiveSource != string(credentials.SourceEnv) {
		t.Errorf("ActiveSource=%q, want %q", status.ActiveSource, credentials.SourceEnv)
	}
	if status.EnvVar != "ANTHROPIC_API_KEY" {
		t.Errorf("EnvVar=%q, want %q", status.EnvVar, "ANTHROPIC_API_KEY")
	}
	if !status.SignedIn {
		t.Errorf("SignedIn=false, want true")
	}
}
