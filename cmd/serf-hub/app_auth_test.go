package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	"primeradiant.com/serf/internal/appwire"
	authopenai "primeradiant.com/serf/internal/auth/openai"
)

func TestHubRPCAuthStatusUsesUserScopedOpenAIAuth(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
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

func TestHubRPCAuthStatusReportsEnvAndStoredOAuth(t *testing.T) {
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
	if !status.SignedIn || status.ActiveSource != authopenai.AuthSourceEnv || !status.HasStoredOAuth || status.StoredEmail != "stored@example.com" {
		t.Fatalf("status=%+v, want env with stored oauth metadata", status)
	}
}

func TestHubRPCAuthLogoutRemovesUserScopedOpenAIAuth(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
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
