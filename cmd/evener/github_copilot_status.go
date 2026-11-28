package main

import (
	"fmt"
	"io"
	"strings"

	"primeradiant.com/evener/auth/githubcopilot"
)

func runGitHubCopilotStatus(args []string, stdout, stderr io.Writer) error {
	stateDir, instance, err := parseGitHubCopilotFlags("status", "Show the current GitHub Copilot auth status.", args, stderr)
	if err != nil {
		return err
	}
	status, err := gitHubCopilotServiceFactory().Status(stateDir, instance)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(stdout, formatGitHubCopilotStatus(status))
	return nil
}

// formatGitHubCopilotStatus renders the compact key=value status line.
func formatGitHubCopilotStatus(status githubcopilot.AuthStatus) string {
	state := "signed-out"
	if status.SignedIn {
		state = "signed-in"
	}
	source := status.Source
	parts := []string{"state=" + state, "source=" + source}
	if status.Email != "" {
		parts = append(parts, "email="+status.Email)
	}
	if status.AccountID != "" {
		parts = append(parts, "account_id="+status.AccountID)
	}
	if !status.Expiry.IsZero() {
		parts = append(parts, "expiry="+status.Expiry.UTC().Format("2006-01-02T15:04:05Z"))
	}
	if status.Source == githubcopilot.AuthSourceOAuth {
		parts = append(parts,
			fmt.Sprintf("needs_refresh=%t", status.NeedsRefresh),
			fmt.Sprintf("needs_login=%t", status.NeedsLogin),
		)
	}
	return strings.Join(parts, " ")
}
