package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-tui/internal/launchconfig"
	"primeradiant.com/evener/internal/appserver"
)

func TestHubModelCredentialTestActionUsesAppWire(t *testing.T) {
	var testedProvider string
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerAuthTest, func(_ context.Context, params appwire.AuthTestParams) (appwire.AuthTestResponse, error) {
			testedProvider = params.Provider
			return appwire.AuthTestResponse{
				Provider: params.Provider,
				Status:   appwire.AuthTestStatusSuccess,
				Message:  "provider-secret-must-not-render",
			}, nil
		})
	})
	defer cleanup()

	m := newSessionHubModel(client)
	panel := launchconfig.NewCredentialsPanel()
	loaded, _ := panel.Update(launchconfig.InstanceListResultMsg{List: appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{
		{Name: "custom / team-east", ProviderID: "openai"},
	}}})
	pending, actionCmd := loaded.(launchconfig.CredentialsPanel).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	if actionCmd == nil {
		t.Fatal("t should produce a credential test action")
	}
	panel = pending.(launchconfig.CredentialsPanel)
	m.credentialsPanel = &panel

	action := actionCmd().(launchconfig.CredentialsActionMsg)
	updated, cmd := m.handleCredentialsAction(action)
	if cmd == nil {
		t.Fatal("credential test action should produce an appwire command")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	result := updated.(hubModel)
	if testedProvider != "custom / team-east" {
		t.Fatalf("tested provider=%q, want custom / team-east", testedProvider)
	}
	view := result.credentialsPanel.View()
	if !strings.Contains(view, "Credentials verified.") {
		t.Fatalf("credential result missing from panel:\n%s", view)
	}
	if strings.Contains(view, "provider-secret-must-not-render") {
		t.Fatalf("credential result leaked provider message:\n%s", view)
	}
}

func TestHubModelAuthCommandsUseAppWire(t *testing.T) {
	var methods []string
	var completed appwire.AuthLoginCompleteParams
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerAuthStatus, func(_ context.Context, params appwire.AuthStatusParams) (appwire.AuthStatusResponse, error) {
			methods = append(methods, appwire.MethodEvenerAuthStatus+":"+params.Provider)
			return appwire.AuthStatusResponse{Provider: "openai-codex", Supported: true, ActiveSource: "none", AuthModes: []string{"oauth"}}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerAuthLoginStart, func(_ context.Context, params appwire.AuthLoginStartParams) (appwire.AuthLoginStartResponse, error) {
			methods = append(methods, appwire.MethodEvenerAuthLoginStart+":"+params.Provider)
			return appwire.AuthLoginStartResponse{Provider: "openai-codex", FlowID: "flow-1", URL: "https://auth.example/authorize"}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerAuthLoginComplete, func(_ context.Context, params appwire.AuthLoginCompleteParams) (appwire.AuthLoginCompleteResponse, error) {
			methods = append(methods, appwire.MethodEvenerAuthLoginComplete+":"+params.Provider)
			completed = params
			return appwire.AuthLoginCompleteResponse{Status: appwire.AuthStatusResponse{Provider: "openai-codex", Supported: true, SignedIn: true, ActiveSource: "oauth", AuthModes: []string{"oauth"}, Email: "j@example.com"}}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerAuthLogout, func(_ context.Context, params appwire.AuthLogoutParams) (appwire.AuthLogoutResponse, error) {
			methods = append(methods, appwire.MethodEvenerAuthLogout+":"+params.Provider)
			return appwire.AuthLogoutResponse{Removed: true, Status: appwire.AuthStatusResponse{Provider: "openai-codex", Supported: true, ActiveSource: "none", AuthModes: []string{"oauth"}}}, nil
		})
	})
	defer cleanup()

	m := newSessionHubModel(client)
	m.session.setInputValue("/auth openai-codex")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("/auth openai-codex should call Hub auth status")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	m = updated.(hubModel)
	if got := m.View(); !strings.Contains(got, "openai-codex auth: not configured") {
		t.Fatalf("auth status missing:\n%s", got)
	}

	m.session.setInputValue("/login openai-codex")
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("/login openai-codex should start Hub auth login")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	m = updated.(hubModel)
	if got := m.View(); !strings.Contains(got, "Sign-in URL for openai-codex:") || !strings.Contains(got, "https://auth.example/authorize") {
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
	if got := m.View(); !strings.Contains(got, "Sign-in complete for openai-codex. openai-codex auth: OAuth (j@example.com)") {
		t.Fatalf("login completion missing:\n%s", got)
	}

	m.session.setInputValue("/logout openai-codex")
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("/logout openai-codex should call Hub auth logout")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	if got := updated.(hubModel).View(); !strings.Contains(got, "Removed the stored credential for openai-codex.") {
		t.Fatalf("logout completion missing:\n%s", got)
	}

	want := strings.Join([]string{
		appwire.MethodEvenerAuthStatus + ":openai-codex",
		appwire.MethodEvenerAuthLoginStart + ":openai-codex",
		appwire.MethodEvenerAuthLoginComplete + ":openai-codex",
		appwire.MethodEvenerAuthLogout + ":openai-codex",
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
