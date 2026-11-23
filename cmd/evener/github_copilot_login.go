package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"

	"primeradiant.com/evener/auth/githubcopilot"
)

// gitHubCopilotLoginContext lets a behavior test cancel the flow without a signal.
var gitHubCopilotLoginContext = func() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}

func runGitHubCopilotLogin(args []string, stdout, stderr io.Writer) error {
	stateDir, instance, err := parseGitHubCopilotFlags("login", "Sign in to GitHub Copilot with the device-code flow.", args, stderr)
	if err != nil {
		return err
	}
	ctx, cancel := gitHubCopilotLoginContext()
	defer cancel()

	_, _ = fmt.Fprintln(stdout, "auth_mode=device")
	status, err := gitHubCopilotServiceFactory().LoginWithDevice(ctx, stateDir, instance, func(dc githubcopilot.DeviceCode) {
		// Machine-readable fields on stdout; human guidance on stderr.
		_, _ = fmt.Fprintf(stdout, "device_code_url=%s\n", dc.VerificationURL)
		_, _ = fmt.Fprintf(stdout, "device_code=%s\n", dc.UserCode)
		_, _ = fmt.Fprintf(stderr, "\nTo sign in, open this URL on any device:\n  %s\n", dc.VerificationURL)
		_, _ = fmt.Fprintf(stderr, "and enter the code:\n  %s\n\n", dc.UserCode)
		_, _ = fmt.Fprintln(stderr, "Waiting for GitHub authorization (this command will exit automatically)...")
	})
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintln(stdout, formatGitHubCopilotStatus(status))
	return nil
}
