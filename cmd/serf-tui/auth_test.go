package main

import (
	"context"
	"testing"
	"time"

	authopenai "primeradiant.com/serf/internal/auth/openai"
)

func TestAuthControllerStatusSignedOut(t *testing.T) {
	controller := newAuthController(t.TempDir())

	status, err := controller.Status("openai")
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.ActiveSource != authopenai.AuthSourceSignedOut {
		t.Fatalf("ActiveSource = %q, want %q", status.ActiveSource, authopenai.AuthSourceSignedOut)
	}
	if status.HasStoredOAuth {
		t.Fatal("HasStoredOAuth = true, want false")
	}
	if status.SignedIn {
		t.Fatal("SignedIn = true, want false")
	}
}

func TestAuthControllerStatusOAuthWithEmail(t *testing.T) {
	stateDir := t.TempDir()
	if err := authopenai.SaveAuth(stateDir, testOpenAIAuthRecord("bot@example.com")); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}

	controller := newAuthController(stateDir)
	status, err := controller.Status("openai")
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.ActiveSource != authopenai.AuthSourceOAuth {
		t.Fatalf("ActiveSource = %q, want %q", status.ActiveSource, authopenai.AuthSourceOAuth)
	}
	if !status.HasStoredOAuth {
		t.Fatal("HasStoredOAuth = false, want true")
	}
	if status.Email != "bot@example.com" {
		t.Fatalf("Email = %q, want %q", status.Email, "bot@example.com")
	}
}

func TestAuthControllerStatusDistinguishesEnvAndStoredOAuth(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-env")
	stateDir := t.TempDir()
	if err := authopenai.SaveAuth(stateDir, testOpenAIAuthRecord("bot@example.com")); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}

	controller := newAuthController(stateDir)
	status, err := controller.Status("openai")
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.ActiveSource != authopenai.AuthSourceEnv {
		t.Fatalf("ActiveSource = %q, want %q", status.ActiveSource, authopenai.AuthSourceEnv)
	}
	if !status.HasStoredOAuth {
		t.Fatal("HasStoredOAuth = false, want true")
	}
	if status.StoredEmail != "bot@example.com" {
		t.Fatalf("StoredEmail = %q, want %q", status.StoredEmail, "bot@example.com")
	}
}

func TestAuthControllerStatusUnsupportedProvider(t *testing.T) {
	controller := newAuthController(t.TempDir())
	status, err := controller.Status("anthropic")
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.Supported {
		t.Fatal("Supported = true, want false")
	}
	if status.Provider != "anthropic" {
		t.Fatalf("Provider = %q, want anthropic", status.Provider)
	}
}

func TestAuthSummaryFormatting(t *testing.T) {
	tests := []struct {
		name   string
		status authStatus
		want   string
	}{
		{
			name:   "signed out",
			status: authStatus{Provider: "openai", ActiveSource: authopenai.AuthSourceSignedOut},
			want:   "OpenAI auth: signed out",
		},
		{
			name:   "oauth email",
			status: authStatus{Provider: "openai", ActiveSource: authopenai.AuthSourceOAuth, SignedIn: true, HasStoredOAuth: true, Email: "bot@example.com"},
			want:   "OpenAI auth: oauth (bot@example.com)",
		},
		{
			name:   "env and oauth",
			status: authStatus{Provider: "openai", ActiveSource: authopenai.AuthSourceEnv, SignedIn: true, HasStoredOAuth: true, StoredEmail: "bot@example.com"},
			want:   "OpenAI auth: env+oauth (bot@example.com)",
		},
		{
			name:   "env only",
			status: authStatus{Provider: "openai", ActiveSource: authopenai.AuthSourceEnv, SignedIn: true},
			want:   "OpenAI auth: env",
		},
		{
			name:   "unsupported provider",
			status: authStatus{Provider: "anthropic"},
			want:   `Auth is not supported for provider "anthropic".`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatAuthStatusSummary(tc.status); got != tc.want {
				t.Fatalf("formatAuthStatusSummary() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAuthControllerLogoutWhenAlreadySignedOut(t *testing.T) {
	controller := newAuthController(t.TempDir())

	loggedOut, err := controller.Logout("openai")
	if err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if loggedOut {
		t.Fatal("Logout() = true, want false")
	}
}

func TestAuthControllerLoginPassesURLHooksToProvider(t *testing.T) {
	controller := newAuthController(t.TempDir())
	wantStatus := authStatus{
		Provider:       "openai",
		Supported:      true,
		SignedIn:       true,
		ActiveSource:   authopenai.AuthSourceOAuth,
		HasStoredOAuth: true,
		Email:          "bot@example.com",
		StoredEmail:    "bot@example.com",
	}

	var gotURL string
	var waited bool
	controller.newOpenAIProvider = func() openAIAuthProvider {
		return stubOpenAIAuthProvider{
			loginFunc: func(ctx context.Context, stateDir string, hooks authLoginHooks) (authStatus, error) {
				if stateDir != controller.stateDir {
					t.Fatalf("stateDir = %q, want %q", stateDir, controller.stateDir)
				}
				hooks.OnURL("https://auth.openai.com/authorize?state=abc")
				got, err := hooks.WaitForRedirect(ctx)
				if err != nil {
					t.Fatalf("WaitForRedirect() error = %v", err)
				}
				if got != "http://localhost:1455/auth/callback?code=abc&state=abc" {
					t.Fatalf("WaitForRedirect() = %q", got)
				}
				return wantStatus, nil
			},
		}
	}

	status, err := controller.Login(context.Background(), "openai", authLoginHooks{
		OnURL: func(rawURL string) {
			gotURL = rawURL
		},
		WaitForRedirect: func(context.Context) (string, error) {
			waited = true
			return "http://localhost:1455/auth/callback?code=abc&state=abc", nil
		},
	})
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if gotURL == "" {
		t.Fatal("OnURL was not called")
	}
	if !waited {
		t.Fatal("WaitForRedirect was not called")
	}
	if status.Email != wantStatus.Email {
		t.Fatalf("Email = %q, want %q", status.Email, wantStatus.Email)
	}
}

func testOpenAIAuthRecord(email string) authopenai.AuthRecord {
	return authopenai.AuthRecord{
		Version:      1,
		Provider:     "openai",
		Source:       authopenai.AuthSourceOAuth,
		ObtainedAt:   time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC),
		TokenType:    "Bearer",
		Scope:        "openid profile email offline_access",
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		Expiry:       time.Date(2026, 5, 8, 13, 0, 0, 0, time.UTC),
		Email:        email,
		AccountID:    "acct_123",
		WorkspaceID:  "ws_123",
	}
}

type stubOpenAIAuthProvider struct {
	statusFunc func(string) (authStatus, error)
	loginFunc  func(context.Context, string, authLoginHooks) (authStatus, error)
	logoutFunc func(string) (bool, error)
}

func (s stubOpenAIAuthProvider) Status(stateDir string) (authStatus, error) {
	return s.statusFunc(stateDir)
}

func (s stubOpenAIAuthProvider) Login(ctx context.Context, stateDir string, hooks authLoginHooks) (authStatus, error) {
	return s.loginFunc(ctx, stateDir, hooks)
}

func (s stubOpenAIAuthProvider) Logout(stateDir string) (bool, error) {
	return s.logoutFunc(stateDir)
}
