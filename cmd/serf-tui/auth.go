package main

import (
	"fmt"
	"strings"

	authopenai "primeradiant.com/serf/internal/auth/openai"
)

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
