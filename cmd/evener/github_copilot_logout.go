package main

import (
	"fmt"
	"io"
)

func runGitHubCopilotLogout(args []string, stdout, stderr io.Writer) error {
	stateDir, instance, err := parseGitHubCopilotFlags("logout", "Delete Evener's locally stored GitHub Copilot auth state.", args, stderr)
	if err != nil {
		return err
	}
	service := gitHubCopilotServiceFactory()
	deleted, err := service.Logout(stateDir, instance)
	if err != nil {
		return err
	}
	status, err := service.Status(stateDir, instance)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "%s deleted=%t\n", formatGitHubCopilotStatus(status), deleted)
	return nil
}
