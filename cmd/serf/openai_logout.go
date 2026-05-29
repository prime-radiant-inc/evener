package main

import (
	"flag"
	"fmt"
	"io"
	"strings"

	authopenai "primeradiant.com/serf/internal/auth/openai"
)

var openAILogoutAction = func(stateDir, instanceName string) (bool, error) {
	return authopenai.NewService(authopenai.DefaultConfig(), nil).Logout(stateDir, instanceName)
}

func runOpenAILogout(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("openai logout", flag.ContinueOnError)
	fs.SetOutput(stderr)

	workDir := fs.String("dir", "", "working directory hint")
	stateDir := fs.String("state-dir", "", "override OpenAI auth state directory")
	instance := fs.String("instance", "openai", "instance name (default: openai)")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: serf openai logout [flags]\n\n")
		fmt.Fprintf(stderr, "Delete Serf's locally stored OpenAI OAuth state.\n\n")
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

	deleted, err := openAILogoutAction(resolvedStateDir, instanceName)
	if err != nil {
		return err
	}

	status, err := openAIStatusAction(resolvedStateDir, instanceName)
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "%s deleted=%t\n", formatOpenAIStatus(status), deleted)
	return nil
}
