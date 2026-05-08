package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/cmdutil"
	authopenai "primeradiant.com/serf/internal/auth/openai"
)

var openAILoginAction = func(ctx context.Context, stateDir string, openBrowser func(string) error, readRedirectURL func(context.Context) (string, error)) (authopenai.AuthStatus, error) {
	service := authopenai.NewService(authopenai.DefaultConfig(), nil).
		WithBrowserOpener(openBrowser).
		WithManualRedirectReader(readRedirectURL)
	return service.Login(ctx, stateDir)
}

var openAIBrowserOpener = func(rawURL string) error {
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

func runOpenAI(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printOpenAIUsage(stderr)
		return nil
	}

	switch args[0] {
	case "login":
		return runOpenAILogin(args[1:], stdin, stdout, stderr)
	case "logout":
		return runOpenAILogout(args[1:], stdout, stderr)
	case "status":
		return runOpenAIStatus(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printOpenAIUsage(stderr)
		return nil
	default:
		printOpenAIUsage(stderr)
		return fmt.Errorf("unknown openai command %q", args[0])
	}
}

func runOpenAILogin(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("openai login", flag.ContinueOnError)
	fs.SetOutput(stderr)

	workDir := fs.String("dir", "", "working directory (default: current directory)")
	stateDir := fs.String("state-dir", "", "override runtime state directory")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: serf openai login [flags]\n\n")
		fmt.Fprintf(stderr, "Start the OpenAI OAuth login flow.\n\n")
		fmt.Fprintf(stderr, "Flags:\n")
		fmt.Fprintf(stderr, "  --dir <path>         Working directory (default: current directory)\n")
		fmt.Fprintf(stderr, "  --state-dir <path>   Override runtime state directory\n")
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

	openBrowser := func(rawURL string) error {
		fmt.Fprintf(stdout, "url=%s\n", rawURL)
		if err := openAIBrowserOpener(rawURL); err != nil {
			fmt.Fprintf(stderr, "browser_open_error=%v\n", err)
		}
		return nil
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	status, err := openAILoginAction(ctx, resolvedStateDir, openBrowser, makeRedirectURLReader(stdin, stderr))
	if err != nil {
		return err
	}

	fmt.Fprintln(stdout, formatOpenAIStatus(status))
	return nil
}

func resolveOpenAIStateDir(workDir, override string) (string, error) {
	if override != "" {
		return override, nil
	}
	if stateDir := os.Getenv("SERF_STATE_DIR"); stateDir != "" {
		return stateDir, nil
	}
	if workDir == "" {
		var err error
		workDir, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("cannot determine working directory: %w", err)
		}
	}
	originURL := cmdutil.GitOriginURLFromDir(workDir)
	return agent.RuntimeDir(originURL, workDir, ""), nil
}

func makeRedirectURLReader(stdin io.Reader, stderr io.Writer) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		fmt.Fprintln(stderr, "Paste the full redirect URL and press Enter:")

		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && err != io.EOF {
			return "", err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			return "", fmt.Errorf("redirect URL is required")
		}
		return line, nil
	}
}

func printOpenAIUsage(w io.Writer) {
	fmt.Fprintf(w, "Usage: serf openai <command> [flags]\n\n")
	fmt.Fprintf(w, "Manage Serf's OpenAI OAuth state.\n\n")
	fmt.Fprintf(w, "Commands:\n")
	fmt.Fprintf(w, "  login    Sign in with OpenAI OAuth\n")
	fmt.Fprintf(w, "  logout   Delete locally stored OpenAI OAuth state\n")
	fmt.Fprintf(w, "  status   Show current OpenAI auth status\n")
}
