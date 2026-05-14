package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	authopenai "primeradiant.com/serf/internal/auth/openai"
)

type authController struct {
	stateDir          string
	newOpenAIProvider func() openAIAuthProvider
}

type authStatus struct {
	Provider       string
	Supported      bool
	SignedIn       bool
	ActiveSource   string
	HasStoredOAuth bool
	Email          string
	StoredEmail    string
	AccountID      string
	WorkspaceID    string
	NeedsRefresh   bool
	NeedsLogin     bool
	Error          string
}

type authLoginHooks struct {
	OnURL           func(string)
	OpenBrowser     func(string) error
	WaitForRedirect func(context.Context) (string, error)
}

type authStatusMsg struct {
	status authStatus
	err    error
}

type authURLMsg struct {
	flowID int
	url    string
}

type authBrowserErrorMsg struct {
	flowID int
	err    error
}

type authLoginMsg struct {
	flowID int
	status authStatus
	err    error
}

type authLogoutMsg struct {
	removed bool
	status  authStatus
	err     error
}

type openAIAuthProvider interface {
	Status(stateDir string) (authStatus, error)
	Login(ctx context.Context, stateDir string, hooks authLoginHooks) (authStatus, error)
	Logout(stateDir string) (bool, error)
}

func newAuthController(stateDir string) *authController {
	return &authController{
		stateDir: stateDir,
		newOpenAIProvider: func() openAIAuthProvider {
			return defaultOpenAIAuthProvider{}
		},
	}
}

func (c *authController) Status(provider string) (authStatus, error) {
	switch strings.TrimSpace(provider) {
	case "openai":
		return c.newOpenAIProvider().Status(c.stateDir)
	default:
		return authStatus{Provider: provider}, nil
	}
}

func (c *authController) Login(ctx context.Context, provider string, hooks authLoginHooks) (authStatus, error) {
	switch strings.TrimSpace(provider) {
	case "openai":
		return c.newOpenAIProvider().Login(ctx, c.stateDir, hooks)
	default:
		return authStatus{Provider: provider}, fmt.Errorf("auth is not supported for provider %q", provider)
	}
}

func (c *authController) Logout(provider string) (bool, error) {
	switch strings.TrimSpace(provider) {
	case "openai":
		return c.newOpenAIProvider().Logout(c.stateDir)
	default:
		return false, fmt.Errorf("auth is not supported for provider %q", provider)
	}
}

func formatAuthStatusSummary(status authStatus) string {
	switch strings.TrimSpace(status.Provider) {
	case "openai":
		label := "signed out"
		switch status.ActiveSource {
		case authopenai.AuthSourceEnv:
			if status.HasStoredOAuth {
				label = "env+oauth"
			} else {
				label = "env"
			}
		case authopenai.AuthSourceOAuth:
			label = "oauth"
			if status.NeedsLogin {
				label = "oauth expired"
			} else if status.NeedsRefresh {
				label = "oauth refreshable"
			}
		}
		if status.ActiveSource == authopenai.AuthSourceSignedOut && status.HasStoredOAuth && strings.TrimSpace(status.Error) != "" {
			label = "login required"
		}

		email := strings.TrimSpace(status.Email)
		if email == "" {
			email = strings.TrimSpace(status.StoredEmail)
		}
		if detail := strings.TrimSpace(status.Error); detail != "" {
			if email != "" && label != "signed out" {
				return "OpenAI auth: " + label + " (" + email + "): " + detail
			}
			return "OpenAI auth: " + label + ": " + detail
		}
		if email != "" && label != "signed out" {
			return "OpenAI auth: " + label + " (" + email + ")"
		}
		return "OpenAI auth: " + label
	default:
		if strings.TrimSpace(status.Provider) == "" {
			return "Auth is not available until a provider is selected."
		}
		return fmt.Sprintf("Auth is not supported for provider %q.", status.Provider)
	}
}

type defaultOpenAIAuthProvider struct{}

func (defaultOpenAIAuthProvider) Status(stateDir string) (authStatus, error) {
	svc := authopenai.NewService(authopenai.DefaultConfig(), nil)
	active, err := svc.Status(stateDir)
	if err != nil {
		return authStatus{}, err
	}

	status := authStatus{
		Provider:     "openai",
		Supported:    true,
		SignedIn:     active.SignedIn,
		ActiveSource: active.Source,
		Email:        active.Email,
		AccountID:    active.AccountID,
		WorkspaceID:  active.WorkspaceID,
	}

	record, err := authopenai.LoadAuth(stateDir)
	switch {
	case err == nil:
		status.HasStoredOAuth = true
		status.StoredEmail = record.Email
		if status.ActiveSource == authopenai.AuthSourceOAuth {
			status.Email = firstNonEmptyString(status.Email, record.Email)
			status.AccountID = firstNonEmptyString(status.AccountID, record.AccountID)
			status.WorkspaceID = firstNonEmptyString(status.WorkspaceID, record.WorkspaceID)
		}
	case errors.Is(err, authopenai.ErrAuthNotFound):
		return status, nil
	default:
		return authStatus{}, err
	}

	return status, nil
}

func (defaultOpenAIAuthProvider) Login(ctx context.Context, stateDir string, hooks authLoginHooks) (authStatus, error) {
	svc := authopenai.NewService(authopenai.DefaultConfig(), nil).
		WithBrowserOpener(func(rawURL string) error {
			if hooks.OnURL != nil {
				hooks.OnURL(rawURL)
			}
			if hooks.OpenBrowser != nil {
				return hooks.OpenBrowser(rawURL)
			}
			return nil
		}).
		WithManualRedirectReader(hooks.WaitForRedirect)

	if _, err := svc.Login(ctx, stateDir); err != nil {
		return authStatus{}, err
	}
	return defaultOpenAIAuthProvider{}.Status(stateDir)
}

func (defaultOpenAIAuthProvider) Logout(stateDir string) (bool, error) {
	svc := authopenai.NewService(authopenai.DefaultConfig(), nil)
	return svc.Logout(stateDir)
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func authHint(status authStatus) string {
	if strings.TrimSpace(status.Provider) != "openai" {
		return ""
	}
	switch status.ActiveSource {
	case authopenai.AuthSourceEnv:
		if status.HasStoredOAuth {
			return "oa: env+oauth"
		}
		return "oa: env"
	case authopenai.AuthSourceOAuth:
		return "oa: oauth"
	default:
		return "oa: login"
	}
}

func loadAuthStatusCmd(controller *authController, provider string) tea.Cmd {
	return func() tea.Msg {
		status, err := controller.Status(provider)
		return authStatusMsg{status: status, err: err}
	}
}

func authLoginCmd(ctx context.Context, flowID int, controller *authController, provider string, asyncCh chan<- tea.Msg, openBrowser func(string) error, redirectCh <-chan string) tea.Cmd {
	return func() tea.Msg {
		status, err := controller.Login(ctx, provider, authLoginHooks{
			OnURL: func(rawURL string) {
				if asyncCh != nil {
					asyncCh <- authURLMsg{flowID: flowID, url: rawURL}
				}
			},
			OpenBrowser: func(rawURL string) error {
				if openBrowser == nil {
					return nil
				}
				if err := openBrowser(rawURL); err != nil {
					if asyncCh != nil {
						asyncCh <- authBrowserErrorMsg{flowID: flowID, err: err}
					}
				}
				return nil
			},
			WaitForRedirect: func(ctx context.Context) (string, error) {
				if redirectCh == nil {
					return "", fmt.Errorf("redirect channel is required")
				}
				select {
				case <-ctx.Done():
					return "", ctx.Err()
				case rawURL := <-redirectCh:
					return rawURL, nil
				}
			},
		})
		return authLoginMsg{flowID: flowID, status: status, err: err}
	}
}

func authLogoutCmd(controller *authController, provider string) tea.Cmd {
	return func() tea.Msg {
		removed, err := controller.Logout(provider)
		if err != nil {
			return authLogoutMsg{err: err}
		}
		status, statusErr := controller.Status(provider)
		return authLogoutMsg{removed: removed, status: status, err: statusErr}
	}
}

func openURLInBrowser(rawURL string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", rawURL)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL)
	default:
		cmd = exec.Command("xdg-open", rawURL)
	}
	return cmd.Start()
}
