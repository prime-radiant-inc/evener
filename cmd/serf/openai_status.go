package main

import (
	"flag"
	"fmt"
	"io"
	"strings"

	authopenai "primeradiant.com/serf/auth/openai"
)

var openAIStatusAction = func(stateDir, instanceName string) (authopenai.AuthStatus, error) {
	return authopenai.NewService(authopenai.DefaultConfig(), nil).Status(stateDir, instanceName)
}

func runOpenAIStatus(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("openai status", flag.ContinueOnError)
	fs.SetOutput(stderr)

	workDir := fs.String("dir", "", "working directory hint")
	stateDir := fs.String("state-dir", "", "override OpenAI auth state directory")
	instance := fs.String("instance", "openai", "instance name (default: openai)")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: serf openai status [flags]\n\n")
		fmt.Fprintf(stderr, "Show the current OpenAI auth status.\n\n")
		fmt.Fprintf(stderr, "Flags:\n")
		fmt.Fprintf(stderr, "  --dir <path>         Working directory hint\n")
		fmt.Fprintf(stderr, "  --state-dir <path>   Override OpenAI auth state directory\n")
		fmt.Fprintf(stderr, "  --instance <name>    Instance name (default: openai)\n")
	}

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}

	resolvedStateDir, err := resolveOpenAIStateDir(*workDir, *stateDir)
	if err != nil {
		return err
	}
	instanceName := strings.TrimSpace(*instance)
	if instanceName == "" {
		instanceName = "openai"
	}

	status, err := openAIStatusAction(resolvedStateDir, instanceName)
	if err != nil {
		return err
	}

	fmt.Fprintln(stdout, formatOpenAIStatus(status))
	return nil
}

func formatOpenAIStatus(status authopenai.AuthStatus) string {
	state := "signed-out"
	if status.SignedIn {
		state = "signed-in"
	}

	source := strings.TrimSpace(status.Source)
	if source == "" {
		source = authopenai.AuthSourceSignedOut
	}

	parts := []string{
		"state=" + state,
		"source=" + source,
	}
	if status.Email != "" {
		parts = append(parts, "email="+status.Email)
	}
	if status.AccountID != "" {
		parts = append(parts, "account_id="+status.AccountID)
	}
	if status.WorkspaceID != "" {
		parts = append(parts, "workspace_id="+status.WorkspaceID)
	}
	if !status.Expiry.IsZero() {
		parts = append(parts, "expiry="+status.Expiry.UTC().Format("2006-01-02T15:04:05Z"))
	}
	if status.Source == authopenai.AuthSourceOAuth {
		parts = append(parts,
			fmt.Sprintf("needs_refresh=%t", status.NeedsRefresh),
			fmt.Sprintf("needs_login=%t", status.NeedsLogin),
		)
	}
	return strings.Join(parts, " ")
}
