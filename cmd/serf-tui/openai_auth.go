package main

import (
	"context"
	"errors"
	"strings"

	authopenai "primeradiant.com/serf/internal/auth/openai"
)

type openAIAuthHelper struct {
	stateDir   string
	newService func() openAIService
}

type openAIAuthStatus struct {
	SignedIn       bool
	ActiveSource   string
	HasStoredOAuth bool
	Email          string
	StoredEmail    string
	AccountID      string
	WorkspaceID    string
}

type openAILoginHooks struct {
	OnAuthURL       func(string)
	OpenBrowser     func(string) error
	WaitForRedirect func(context.Context) (string, error)
}

type openAIService interface {
	Status(stateDir string) (openAIAuthStatus, error)
	Login(ctx context.Context, stateDir string, hooks openAILoginHooks) (openAIAuthStatus, error)
	Logout(stateDir string) (bool, error)
}

func newOpenAIAuthHelper(stateDir string) *openAIAuthHelper {
	return &openAIAuthHelper{
		stateDir: stateDir,
		newService: func() openAIService {
			return defaultOpenAIService{}
		},
	}
}

func (h *openAIAuthHelper) Status() (openAIAuthStatus, error) {
	return h.newService().Status(h.stateDir)
}

func (h *openAIAuthHelper) Login(ctx context.Context, hooks openAILoginHooks) (openAIAuthStatus, error) {
	return h.newService().Login(ctx, h.stateDir, hooks)
}

func (h *openAIAuthHelper) Logout() (bool, error) {
	return h.newService().Logout(h.stateDir)
}

func formatOpenAIAuthStatusSummary(status openAIAuthStatus) string {
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
	}

	email := strings.TrimSpace(status.Email)
	if email == "" {
		email = strings.TrimSpace(status.StoredEmail)
	}
	if email != "" && label != "signed out" {
		return "OpenAI auth: " + label + " (" + email + ")"
	}
	return "OpenAI auth: " + label
}

type defaultOpenAIService struct{}

func (defaultOpenAIService) Status(stateDir string) (openAIAuthStatus, error) {
	svc := authopenai.NewService(authopenai.DefaultConfig(), nil)
	active, err := svc.Status(stateDir)
	if err != nil {
		return openAIAuthStatus{}, err
	}

	status := openAIAuthStatus{
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
		return openAIAuthStatus{}, err
	}

	return status, nil
}

func (defaultOpenAIService) Login(ctx context.Context, stateDir string, hooks openAILoginHooks) (openAIAuthStatus, error) {
	svc := authopenai.NewService(authopenai.DefaultConfig(), nil).
		WithBrowserOpener(func(rawURL string) error {
			if hooks.OnAuthURL != nil {
				hooks.OnAuthURL(rawURL)
			}
			if hooks.OpenBrowser != nil {
				return hooks.OpenBrowser(rawURL)
			}
			return nil
		}).
		WithManualRedirectReader(hooks.WaitForRedirect)

	if _, err := svc.Login(ctx, stateDir); err != nil {
		return openAIAuthStatus{}, err
	}
	return defaultOpenAIService{}.Status(stateDir)
}

func (defaultOpenAIService) Logout(stateDir string) (bool, error) {
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
