package main

import (
	"context"
	"testing"
	"time"

	authopenai "primeradiant.com/serf/internal/auth/openai"
)

func TestOpenAIAuthHelperStatusSignedOut(t *testing.T) {
	helper := newOpenAIAuthHelper(t.TempDir())

	status, err := helper.Status()
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

func TestOpenAIAuthHelperStatusOAuthWithEmail(t *testing.T) {
	stateDir := t.TempDir()
	if err := authopenai.SaveAuth(stateDir, testOpenAIAuthRecord("bot@example.com")); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}

	helper := newOpenAIAuthHelper(stateDir)
	status, err := helper.Status()
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

func TestOpenAIAuthHelperStatusDistinguishesEnvAndStoredOAuth(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-env")
	stateDir := t.TempDir()
	if err := authopenai.SaveAuth(stateDir, testOpenAIAuthRecord("bot@example.com")); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}

	helper := newOpenAIAuthHelper(stateDir)
	status, err := helper.Status()
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

func TestOpenAIAuthHelperSummaryFormatting(t *testing.T) {
	tests := []struct {
		name   string
		status openAIAuthStatus
		want   string
	}{
		{
			name:   "signed out",
			status: openAIAuthStatus{ActiveSource: authopenai.AuthSourceSignedOut},
			want:   "OpenAI auth: signed out",
		},
		{
			name:   "oauth email",
			status: openAIAuthStatus{ActiveSource: authopenai.AuthSourceOAuth, SignedIn: true, HasStoredOAuth: true, Email: "bot@example.com"},
			want:   "OpenAI auth: oauth (bot@example.com)",
		},
		{
			name:   "env and oauth",
			status: openAIAuthStatus{ActiveSource: authopenai.AuthSourceEnv, SignedIn: true, HasStoredOAuth: true, StoredEmail: "bot@example.com"},
			want:   "OpenAI auth: env+oauth (bot@example.com)",
		},
		{
			name:   "env only",
			status: openAIAuthStatus{ActiveSource: authopenai.AuthSourceEnv, SignedIn: true},
			want:   "OpenAI auth: env",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatOpenAIAuthStatusSummary(tc.status); got != tc.want {
				t.Fatalf("formatOpenAIAuthStatusSummary() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOpenAIAuthHelperLogoutWhenAlreadySignedOut(t *testing.T) {
	helper := newOpenAIAuthHelper(t.TempDir())

	loggedOut, err := helper.Logout()
	if err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if loggedOut {
		t.Fatal("Logout() = true, want false")
	}
}

func TestOpenAIAuthHelperLoginPassesURLHooksToService(t *testing.T) {
	helper := newOpenAIAuthHelper(t.TempDir())
	wantStatus := openAIAuthStatus{
		SignedIn:       true,
		ActiveSource:   authopenai.AuthSourceOAuth,
		HasStoredOAuth: true,
		Email:          "bot@example.com",
		StoredEmail:    "bot@example.com",
	}

	var gotURL string
	var waited bool
	helper.newService = func() openAIService {
		return stubOpenAIService{
			loginFunc: func(ctx context.Context, stateDir string, hooks openAILoginHooks) (openAIAuthStatus, error) {
				if stateDir != helper.stateDir {
					t.Fatalf("stateDir = %q, want %q", stateDir, helper.stateDir)
				}
				hooks.OnAuthURL("https://auth.openai.com/authorize?state=abc")
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

	status, err := helper.Login(context.Background(), openAILoginHooks{
		OnAuthURL: func(rawURL string) {
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
		t.Fatal("OnAuthURL was not called")
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

type stubOpenAIService struct {
	statusFunc func(string) (openAIAuthStatus, error)
	loginFunc  func(context.Context, string, openAILoginHooks) (openAIAuthStatus, error)
	logoutFunc func(string) (bool, error)
}

func (s stubOpenAIService) Status(stateDir string) (openAIAuthStatus, error) {
	return s.statusFunc(stateDir)
}

func (s stubOpenAIService) Login(ctx context.Context, stateDir string, hooks openAILoginHooks) (openAIAuthStatus, error) {
	return s.loginFunc(ctx, stateDir, hooks)
}

func (s stubOpenAIService) Logout(stateDir string) (bool, error) {
	return s.logoutFunc(stateDir)
}
