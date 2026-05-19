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

	"primeradiant.com/serf/internal/appwire"
	authopenai "primeradiant.com/serf/internal/auth/openai"
	"primeradiant.com/serf/internal/auth/openai/oaitest"
	"primeradiant.com/serf/internal/credentials"
)

func TestHubRPCAuthStatusUsesUserScopedOpenAIAuth(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	xdgStateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", xdgStateHome)
	userStateDir := authopenai.DefaultStateDirWithStateHome(xdgStateHome)
	if err := authopenai.SaveAuth(userStateDir, authopenai.AuthRecord{
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

	hub := newHubRPCTestServer(t, WebConfig{Past: NewPastIndex("")})
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
	if err := authopenai.SaveAuth(authopenai.DefaultStateDirWithStateHome(xdgStateHome), authopenai.AuthRecord{
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

	hub := newHubRPCTestServer(t, WebConfig{Past: NewPastIndex("")})
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

	hub := newHubRPCTestServer(t, WebConfig{Past: NewPastIndex("")})
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
			if err := authopenai.SaveAuth(ctrl.stateDir, authopenai.AuthRecord{
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
	want := filepath.Join(`C:\Users\Jesse`, ".local", "state", "serf")
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
	want := filepath.Join(`D:\Users\Jesse`, ".local", "state", "serf")
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
	if err := authopenai.SaveAuth(userStateDir, authopenai.AuthRecord{
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

	hub := newHubRPCTestServer(t, WebConfig{Past: NewPastIndex("")})
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
	if _, err := authopenai.LoadAuth(userStateDir); !errors.Is(err, authopenai.ErrAuthNotFound) {
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
	record, err := authopenai.LoadAuth(ctrl.stateDir)
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
