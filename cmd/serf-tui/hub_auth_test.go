package main

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/internal/appserver"
)

func TestHubModelAuthCommandsUseAppWire(t *testing.T) {
	var methods []string
	var completed appwire.AuthLoginCompleteParams
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodSerfAuthStatus, func(_ context.Context, params appwire.AuthStatusParams) (appwire.AuthStatusResponse, error) {
			methods = append(methods, appwire.MethodSerfAuthStatus+":"+params.Provider)
			return appwire.AuthStatusResponse{Provider: "openai", Supported: true, ActiveSource: "signed-out"}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodSerfAuthLoginStart, func(_ context.Context, params appwire.AuthLoginStartParams) (appwire.AuthLoginStartResponse, error) {
			methods = append(methods, appwire.MethodSerfAuthLoginStart+":"+params.Provider)
			return appwire.AuthLoginStartResponse{Provider: "openai", FlowID: "flow-1", URL: "https://auth.example/authorize"}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodSerfAuthLoginComplete, func(_ context.Context, params appwire.AuthLoginCompleteParams) (appwire.AuthLoginCompleteResponse, error) {
			methods = append(methods, appwire.MethodSerfAuthLoginComplete+":"+params.Provider)
			completed = params
			return appwire.AuthLoginCompleteResponse{Status: appwire.AuthStatusResponse{Provider: "openai", Supported: true, SignedIn: true, ActiveSource: "oauth", Email: "j@example.com"}}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodSerfAuthLogout, func(_ context.Context, params appwire.AuthLogoutParams) (appwire.AuthLogoutResponse, error) {
			methods = append(methods, appwire.MethodSerfAuthLogout+":"+params.Provider)
			return appwire.AuthLogoutResponse{Removed: true, Status: appwire.AuthStatusResponse{Provider: "openai", Supported: true, ActiveSource: "signed-out"}}, nil
		})
	})
	defer cleanup()

	m := newSessionHubModel(client)
	m.session.setInputValue("/auth openai")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("/auth openai should call Hub auth status")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	m = updated.(hubModel)
	if got := m.View(); !strings.Contains(got, "OpenAI auth: signed out") {
		t.Fatalf("auth status missing:\n%s", got)
	}

	m.session.setInputValue("/login openai")
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("/login openai should start Hub auth login")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	m = updated.(hubModel)
	if got := m.View(); !strings.Contains(got, "OpenAI sign-in URL:") || !strings.Contains(got, "https://auth.example/authorize") {
		t.Fatalf("login URL missing:\n%s", got)
	}

	m.session.setInputValue("http://localhost:1455/auth/callback?code=abc&state=flow")
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("pasted redirect should complete Hub auth login")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	m = updated.(hubModel)
	if completed.FlowID != "flow-1" || completed.RedirectURL == "" {
		t.Fatalf("complete params=%+v", completed)
	}
	if got := m.View(); !strings.Contains(got, "OpenAI login complete. OpenAI auth: oauth (j@example.com)") {
		t.Fatalf("login completion missing:\n%s", got)
	}

	m.session.setInputValue("/logout openai")
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("/logout openai should call Hub auth logout")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	if got := updated.(hubModel).View(); !strings.Contains(got, "OpenAI sign-out complete.") {
		t.Fatalf("logout completion missing:\n%s", got)
	}

	want := strings.Join([]string{
		appwire.MethodSerfAuthStatus + ":openai",
		appwire.MethodSerfAuthLoginStart + ":openai",
		appwire.MethodSerfAuthLoginComplete + ":openai",
		appwire.MethodSerfAuthLogout + ":openai",
	}, ",")
	if strings.Join(methods, ",") != want {
		t.Fatalf("methods=%v, want %s", methods, want)
	}
}

func TestHubModelAuthCommandsAppearInHelp(t *testing.T) {
	m := newSessionHubModel(nil)
	m.session.setInputValue("/help")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("/help should not need an async command")
	}
	got := updated.(hubModel).View()
	for _, want := range []string{"/auth", "/login", "/logout"} {
		if !strings.Contains(got, want) {
			t.Fatalf("help missing %q:\n%s", want, got)
		}
	}
}

func TestHubAuthStatusSummaryDistinguishesRefreshAndExpiredOAuth(t *testing.T) {
	tests := []struct {
		name   string
		status authStatus
		want   string
	}{
		{
			name: "refreshable oauth",
			status: authStatus{
				Provider:       "openai",
				ActiveSource:   "oauth",
				SignedIn:       true,
				HasStoredOAuth: true,
				Email:          "bot@example.com",
				NeedsRefresh:   true,
			},
			want: "OpenAI auth: oauth refreshable (bot@example.com)",
		},
		{
			name: "expired oauth",
			status: authStatus{
				Provider:       "openai",
				ActiveSource:   "oauth",
				HasStoredOAuth: true,
				StoredEmail:    "bot@example.com",
				NeedsLogin:     true,
			},
			want: "OpenAI auth: oauth expired (bot@example.com)",
		},
		{
			name: "refresh failed",
			status: authStatus{
				Provider:       "openai",
				ActiveSource:   "signed-out",
				HasStoredOAuth: true,
				StoredEmail:    "bot@example.com",
				Error:          "refresh token rejected",
			},
			want: "OpenAI auth: login required (bot@example.com): refresh token rejected",
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
