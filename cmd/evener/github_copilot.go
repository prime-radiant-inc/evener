package main

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"primeradiant.com/evener/auth/githubcopilot"
	"primeradiant.com/evener/envvars/userdirs"
)

// gitHubCopilotInstance is the instance managed when no --instance names another.
const gitHubCopilotInstance = "github-copilot"

var gitHubCopilotServiceFactory = func() *githubcopilot.Service {
	return githubcopilot.NewService(githubcopilot.DefaultConfig(), nil)
}

func runGitHubCopilot(args []string, _ io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printGitHubCopilotUsage(stderr)
		return nil
	}

	switch args[0] {
	case "login":
		return runGitHubCopilotLogin(args[1:], stdout, stderr)
	case "logout":
		return runGitHubCopilotLogout(args[1:], stdout, stderr)
	case "status":
		return runGitHubCopilotStatus(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printGitHubCopilotUsage(stderr)
		return nil
	default:
		printGitHubCopilotUsage(stderr)
		return &unknownSubcommandError{group: "github-copilot", name: args[0]}
	}
}

// parseGitHubCopilotFlags parses the shared flags and resolves the target.
func parseGitHubCopilotFlags(command, summary string, args []string, stderr io.Writer) (string, string, error) {
	fs := flag.NewFlagSet("github-copilot "+command, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.String("dir", "", "working directory hint")
	stateDir := fs.String("state-dir", "", "override GitHub Copilot auth state directory")
	instance := fs.String("instance", gitHubCopilotInstance, "instance name (default: "+gitHubCopilotInstance+")")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage: evener github-copilot %s [flags]\n\n%s\n\nFlags:\n", command, summary)
		_, _ = fmt.Fprintf(stderr, "  --dir <path>         Working directory hint\n  --state-dir <path>   Override GitHub Copilot auth state directory\n")
		_, _ = fmt.Fprintf(stderr, "  --instance <name>    Instance name (default: %s)\n", gitHubCopilotInstance)
	}

	if err := fs.Parse(args); err != nil {
		return "", "", err
	}
	if fs.NArg() != 0 {
		return "", "", unexpectedArguments(fs)
	}
	resolvedStateDir := strings.TrimSpace(*stateDir)
	if resolvedStateDir == "" {
		var err error
		if resolvedStateDir, err = userdirs.DefaultStateRoot(); err != nil {
			return "", "", err
		}
	}
	name := ""
	if name = strings.TrimSpace(*instance); name == "" {
		name = gitHubCopilotInstance
	}
	return resolvedStateDir, name, nil
}

func printGitHubCopilotUsage(w io.Writer) {
	_, _ = fmt.Fprintf(w, "Usage: evener github-copilot <command> [flags]\n\nManage Evener's GitHub Copilot subscription auth state.\n\n")
	_, _ = fmt.Fprintf(w, "Commands:\n")
	_, _ = fmt.Fprintf(w, "  login    Sign in with the GitHub device-code flow\n")
	_, _ = fmt.Fprintf(w, "  logout   Delete locally stored GitHub Copilot auth state\n")
	_, _ = fmt.Fprintf(w, "  status   Show current GitHub Copilot auth status\n")
}
