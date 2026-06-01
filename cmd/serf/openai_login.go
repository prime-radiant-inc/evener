package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"

	authopenai "primeradiant.com/serf/auth/openai"
)

var openAILoginAction = func(ctx context.Context, stateDir, instanceName string, openBrowser func(string) error, readRedirectURL func(context.Context) (string, error)) (authopenai.AuthStatus, error) {
	service := authopenai.NewService(authopenai.DefaultConfig(), nil).
		WithBrowserOpener(openBrowser).
		WithManualRedirectReader(readRedirectURL)
	return service.Login(ctx, stateDir, instanceName)
}

var openAIDeviceLoginAction = func(ctx context.Context, stateDir, instanceName string, showPrompt func(authopenai.DeviceCode), notifyConcurrentLogin func()) (authopenai.AuthStatus, error) {
	service := authopenai.NewService(authopenai.DefaultConfig(), nil).
		WithConcurrentLoginNotifier(notifyConcurrentLogin)
	return service.LoginWithDevice(ctx, stateDir, instanceName, showPrompt)
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

	workDir := fs.String("dir", "", "working directory hint")
	stateDir := fs.String("state-dir", "", "override OpenAI auth state directory")
	instance := fs.String("instance", "openai", "instance name (default: openai)")
	device := fs.Bool("device", false, "force device-code flow")
	noDevice := fs.Bool("no-device", false, "force browser flow")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: serf openai login [flags]\n\n")
		fmt.Fprintf(stderr, "Start the OpenAI OAuth login flow.\n\n")
		fmt.Fprintf(stderr, "By default, serf picks between the browser flow and the device-code flow\n")
		fmt.Fprintf(stderr, "automatically. It uses device-code when it looks like there is no graphical\n")
		fmt.Fprintf(stderr, "session: when $SSH_CONNECTION or $SSH_TTY is set, or on Linux/BSD when\n")
		fmt.Fprintf(stderr, "neither $DISPLAY nor $WAYLAND_DISPLAY is set. macOS and Windows default to\n")
		fmt.Fprintf(stderr, "the browser flow unless an SSH session is detected. Setting\n")
		fmt.Fprintf(stderr, "SERF_LOGIN_HEADLESS=1 (or 0) overrides the detection.\n\n")
		fmt.Fprintf(stderr, "Flags:\n")
		fmt.Fprintf(stderr, "  --dir <path>         Working directory hint\n")
		fmt.Fprintf(stderr, "  --state-dir <path>   Override OpenAI auth state directory\n")
		fmt.Fprintf(stderr, "  --instance <name>    Instance name (default: openai)\n")
		fmt.Fprintf(stderr, "  --device             Force device-code flow (headless / remote sessions)\n")
		fmt.Fprintf(stderr, "  --no-device          Force browser flow (overrides auto-detection)\n")
	}

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if *device && *noDevice {
		return errors.New("conflicting flags: --device and --no-device cannot both be set")
	}

	resolvedStateDir, err := resolveOpenAIStateDir(*workDir, *stateDir)
	if err != nil {
		return err
	}
	instanceName := strings.TrimSpace(*instance)
	if instanceName == "" {
		instanceName = "openai"
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	mode, reason := chooseLoginMode(*device, *noDevice)
	fmt.Fprintf(stdout, "auth_mode=%s", mode)
	if reason != "" {
		fmt.Fprintf(stdout, " auth_mode_reason=%s", reason)
	}
	fmt.Fprintln(stdout)

	if mode == "device" {
		return runOpenAIDeviceLogin(ctx, resolvedStateDir, instanceName, stdout, stderr)
	}

	openBrowser := func(rawURL string) error {
		fmt.Fprintf(stdout, "url=%s\n", rawURL)
		if err := openAIBrowserOpener(rawURL); err != nil {
			fmt.Fprintf(stderr, "browser_open_error=%v\n", err)
		}
		return nil
	}

	status, err := openAILoginAction(ctx, resolvedStateDir, instanceName, openBrowser, makeRedirectURLReader(stdin, stderr))
	if err != nil {
		return err
	}

	fmt.Fprintln(stdout, formatOpenAIStatus(status))
	return nil
}

// chooseLoginMode picks "device" or "browser" given the flag state. The
// returned reason is script-friendly key/value content for auth_mode_reason
// (empty when no extra reason should be printed).
func chooseLoginMode(forceDevice, forceBrowser bool) (mode, reason string) {
	switch {
	case forceDevice:
		return "device", "forced"
	case forceBrowser:
		return "browser", "forced"
	}
	if isHeadlessLogin() {
		return "device", "auto_no_display"
	}
	return "browser", "auto"
}

// isHeadlessLogin reports whether the current session looks unable to open
// a browser. It is best-effort: env-var inspection only.
func isHeadlessLogin() bool {
	return isHeadlessLoginFor(runtime.GOOS, os.Getenv)
}

// isHeadlessLoginFor is the testable core of isHeadlessLogin. Pass a goos
// string ("linux", "darwin", "windows", ...) and an env lookup function.
func isHeadlessLoginFor(goos string, getenv func(string) string) bool {
	if v := strings.TrimSpace(getenv("SERF_LOGIN_HEADLESS")); v != "" {
		switch strings.ToLower(v) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	}
	if strings.TrimSpace(getenv("SSH_CONNECTION")) != "" || strings.TrimSpace(getenv("SSH_TTY")) != "" {
		return true
	}
	switch goos {
	case "darwin", "windows":
		return false
	default:
		// Linux, BSDs, and anything else unix-y: need a display server.
		if strings.TrimSpace(getenv("DISPLAY")) == "" && strings.TrimSpace(getenv("WAYLAND_DISPLAY")) == "" {
			return true
		}
		return false
	}
}

func runOpenAIDeviceLogin(ctx context.Context, stateDir, instanceName string, stdout, stderr io.Writer) error {
	prompt := func(dc authopenai.DeviceCode) {
		// Machine-readable lines first, mirroring the browser flow's
		// `url=` convention so scripts can parse stdout uniformly.
		fmt.Fprintf(stdout, "device_code_url=%s\n", dc.VerificationURL)
		fmt.Fprintf(stdout, "device_code=%s\n", dc.UserCode)
		// Human-readable guidance on stderr — keeps stdout pristine for
		// pipelines that only want the key=value pairs and the final
		// status line.
		fmt.Fprintf(stderr, "\nTo sign in, open this URL on any device:\n  %s\n", dc.VerificationURL)
		fmt.Fprintf(stderr, "and enter the code:\n  %s\n", dc.UserCode)
		fmt.Fprintln(stderr, "\nDevice codes are a common phishing target. Never share this code.")
		fmt.Fprintln(stderr, "Waiting for authorization (this command will exit automatically)...")
	}

	notifyConcurrentLogin := func() {
		// Machine-readable signal first so scripts can branch on it,
		// then a human-readable explanation on stderr.
		fmt.Fprintln(stdout, "concurrent_login=detected")
		fmt.Fprintln(stderr, "Detected concurrent login; using existing OAuth state.")
	}

	status, err := openAIDeviceLoginAction(ctx, stateDir, instanceName, prompt, notifyConcurrentLogin)
	if err != nil {
		return err
	}

	fmt.Fprintln(stdout, formatOpenAIStatus(status))
	return nil
}

func resolveOpenAIStateDir(workDir, override string) (string, error) {
	_ = workDir
	if strings.TrimSpace(override) != "" {
		return override, nil
	}
	return authopenai.DefaultStateDir(), nil
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
			return "", errors.New("redirect URL is required")
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
