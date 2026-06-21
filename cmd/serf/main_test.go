package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/provider"
	authopenai "primeradiant.com/serf/auth/openai"
	"primeradiant.com/serf/auth/openai/oaitest"
	"primeradiant.com/serf/llm"
	_ "primeradiant.com/serf/llm/providers/openai"
)

// TestNewSessionFromEnv verifies that we can create a working session
// from environment variables. This is the core wiring test.
func TestNewSessionFromEnv(t *testing.T) {
	requireLiveOpenAI(t)

	client, err := llm.NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}

	profile := provider.NewOpenAIProfile("gpt-5.4-mini")
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())

	sess, err := agent.NewSession(client, profile, env, agent.SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Session should emit SESSION_START on creation.
	select {
	case ev := <-sess.Events():
		if ev.Kind != events.EventSessionStart {
			t.Fatalf("expected SESSION_START, got %s", ev.Kind)
		}
	default:
		t.Fatal("expected SESSION_START event")
	}
}

// TestProcessInputSimplePrompt sends a simple prompt to the model and verifies
// that the session returns a non-empty text response.
func TestProcessInputSimplePrompt(t *testing.T) {
	requireLiveOpenAI(t)

	client, err := llm.NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}

	profile := provider.NewOpenAIProfile("gpt-5.4-mini")
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())

	sess, err := agent.NewSession(client, profile, env, agent.SessionConfig{
		MaxToolRoundsPerInput: 5,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	// Drain SESSION_START.
	<-sess.Events()

	ctx := context.Background()
	result, err := sess.ProcessInput(ctx, "Reply with exactly: HELLO SERF", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if !strings.Contains(strings.ToUpper(result), "HELLO SERF") {
		t.Fatalf("expected response to contain 'HELLO SERF', got: %q", result)
	}
}

// TestProcessInputWithToolUse sends a prompt that requires the model to use a tool
// (write a file), then verifies the file was created.
func TestProcessInputWithToolUse(t *testing.T) {
	requireLiveOpenAI(t)

	client, err := llm.NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}

	tmpDir := t.TempDir()
	profile := provider.NewOpenAIProfile("gpt-5.4-mini")
	env := execenv.NewLocalExecutionEnvironment(tmpDir)

	sess, err := agent.NewSession(client, profile, env, agent.SessionConfig{
		MaxToolRoundsPerInput: 10,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	// Drain SESSION_START.
	<-sess.Events()

	ctx := context.Background()
	_, err = sess.ProcessInput(ctx, "Create a file called hello.txt in the working directory "+tmpDir+" containing exactly the text 'Hello from serf'. Use the write_file tool. Do not explain, just create the file.", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	content, err := os.ReadFile(tmpDir + "/hello.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(content), "Hello from serf") {
		t.Fatalf("expected file to contain 'Hello from serf', got: %q", string(content))
	}
}

func TestOpenAIDispatchesFromTopLevel(t *testing.T) {
	t.Setenv("SERF_STATE_DIR", t.TempDir())

	origStatus := openAIStatusAction
	t.Cleanup(func() { openAIStatusAction = origStatus })

	openAIStatusAction = func(string, string) (authopenai.AuthStatus, error) {
		return authopenai.AuthStatus{
			SignedIn: true,
			Source:   authopenai.AuthSourceEnv,
		}, nil
	}

	var stdout, stderr bytes.Buffer
	handled, label, err := dispatchCLICommand([]string{"openai", "status"}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("dispatchCLICommand() error = %v", err)
	}
	if !handled {
		t.Fatal("dispatchCLICommand() handled = false, want true")
	}
	if label != "serf openai" {
		t.Fatalf("dispatchCLICommand() label = %q, want %q", label, "serf openai")
	}
	if got := strings.TrimSpace(stdout.String()); got != "state=signed-in source=env" {
		t.Fatalf("stdout = %q, want %q", got, "state=signed-in source=env")
	}
}

func TestOpenAIHelpShowsCommands(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := runOpenAI(nil, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("runOpenAI() error = %v", err)
	}

	usage := stderr.String()
	if !strings.Contains(usage, "Usage: serf openai <command>") {
		t.Fatalf("usage = %q, want openai usage header", usage)
	}
	if !strings.Contains(usage, "login") || !strings.Contains(usage, "logout") || !strings.Contains(usage, "status") {
		t.Fatalf("usage = %q, want listed commands", usage)
	}
}

// TestOpenAISubcommandHelpReturnsErrHelp verifies that each openai subcommand
// prints its own usage and returns flag.ErrHelp when invoked with --help, so
// main can detect it and exit 0 without a "flag: help requested" error line.
func TestOpenAISubcommandHelpReturnsErrHelp(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		usagePrefix string
	}{
		{name: "login", args: []string{"login", "--help"}, usagePrefix: "Usage: serf openai login"},
		{name: "logout", args: []string{"logout", "--help"}, usagePrefix: "Usage: serf openai logout"},
		{name: "status", args: []string{"status", "--help"}, usagePrefix: "Usage: serf openai status"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := runOpenAI(tc.args, strings.NewReader(""), &stdout, &stderr)
			if !errors.Is(err, flag.ErrHelp) {
				t.Fatalf("runOpenAI(%v) err = %v, want flag.ErrHelp", tc.args, err)
			}
			if !strings.Contains(stderr.String(), tc.usagePrefix) {
				t.Fatalf("stderr = %q, want prefix %q", stderr.String(), tc.usagePrefix)
			}
		})
	}
}

func TestTopLevelHelpShowsReasoningEffort(t *testing.T) {
	cmd := exec.Command("go", "run", ".", "--help")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("serf --help failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "--reasoning-effort") {
		t.Fatalf("serf --help missing --reasoning-effort:\n%s", out)
	}
}

func TestTopLevelHelpListsSubcommands(t *testing.T) {
	var stderr bytes.Buffer
	fs, _ := newRunFlagSet(&stderr)
	fs.Usage()
	usage := stderr.String()

	for _, cmd := range []string{"openai", "serve", "launch-check"} {
		if !strings.Contains(usage, cmd) {
			t.Errorf("usage missing subcommand %q:\n%s", cmd, usage)
		}
	}
	if !strings.Contains(usage, "Commands:") {
		t.Errorf("usage missing Commands section:\n%s", usage)
	}
}

func TestTopLevelHelpListsEveryRegisteredFlag(t *testing.T) {
	var stderr bytes.Buffer
	fs, _ := newRunFlagSet(&stderr)
	fs.Usage()
	usage := stderr.String()

	fs.VisitAll(func(f *flag.Flag) {
		want := "--" + f.Name
		if !strings.Contains(usage, want) {
			t.Errorf("usage missing registered flag %s:\n%s", want, usage)
		}
	})
}

func TestOpenAIStateDirDefaultIsUserScoped(t *testing.T) {
	xdgStateHome := t.TempDir()
	projectStateDir := filepath.Join(t.TempDir(), "project-state")
	t.Setenv("XDG_STATE_HOME", xdgStateHome)
	t.Setenv("SERF_STATE_DIR", projectStateDir)

	workDirA := filepath.Join(t.TempDir(), "repo-a")
	workDirB := filepath.Join(t.TempDir(), "repo-b")
	if err := os.MkdirAll(workDirA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workDirB, 0o755); err != nil {
		t.Fatal(err)
	}

	gotA, err := resolveOpenAIStateDir(workDirA, "")
	if err != nil {
		t.Fatalf("resolveOpenAIStateDir() error = %v", err)
	}
	gotB, err := resolveOpenAIStateDir(workDirB, "")
	if err != nil {
		t.Fatalf("resolveOpenAIStateDir() error = %v", err)
	}
	want := filepath.Join(xdgStateHome, "serf")
	if gotA != want || gotB != want {
		t.Fatalf("state dirs = %q, %q; want both %q", gotA, gotB, want)
	}
}

func TestOpenAILoginPrintsURLAndSupportsManualFallback(t *testing.T) {
	// Force browser mode so this test is deterministic regardless of the
	// host's $DISPLAY / SSH env. The auto-detection path is covered by
	// dedicated tests below.
	t.Setenv("SERF_LOGIN_HEADLESS", "0")

	stateDir := t.TempDir()
	redirectURL := "http://127.0.0.1:1455/auth/callback?code=manual-code&state=expected-state"

	origLogin := openAILoginAction
	origBrowser := openAIBrowserOpener
	t.Cleanup(func() {
		openAILoginAction = origLogin
		openAIBrowserOpener = origBrowser
	})

	openAIBrowserOpener = func(url string) error {
		if !strings.Contains(url, "oauth/authorize") {
			t.Fatalf("browser URL = %q, want authorize endpoint", url)
		}
		return nil
	}
	openAILoginAction = func(ctx context.Context, gotStateDir string, _ string, openBrowser func(string) error, readRedirectURL func(context.Context) (string, error)) (authopenai.AuthStatus, error) {
		if gotStateDir != stateDir {
			t.Fatalf("stateDir = %q, want %q", gotStateDir, stateDir)
		}
		if err := openBrowser("https://auth.openai.com/oauth/authorize?client_id=test"); err != nil {
			t.Fatalf("openBrowser() error = %v", err)
		}
		gotRedirectURL, err := readRedirectURL(ctx)
		if err != nil {
			t.Fatalf("readRedirectURL() error = %v", err)
		}
		if gotRedirectURL != redirectURL {
			t.Fatalf("redirect URL = %q, want %q", gotRedirectURL, redirectURL)
		}
		return authopenai.AuthStatus{
			SignedIn: true,
			Source:   authopenai.AuthSourceOAuth,
			Email:    "user@example.com",
		}, nil
	}

	var stdout, stderr bytes.Buffer
	if err := runOpenAI([]string{"login", "--state-dir", stateDir}, strings.NewReader(redirectURL+"\n"), &stdout, &stderr); err != nil {
		t.Fatalf("runOpenAI() error = %v", err)
	}

	if got := stdout.String(); !strings.Contains(got, "url=https://auth.openai.com/oauth/authorize?client_id=test") {
		t.Fatalf("stdout = %q, want printed authorize URL", got)
	}
	if got := strings.TrimSpace(stdout.String()); !strings.Contains(got, "state=signed-in source=oauth email=user@example.com") {
		t.Fatalf("stdout = %q, want signed-in status line", got)
	}
	if got := stderr.String(); !strings.Contains(got, "Paste the full redirect URL") {
		t.Fatalf("stderr = %q, want manual redirect prompt", got)
	}
}

func TestOpenAIDeviceLoginPrintsCodeAndStatus(t *testing.T) {
	stateDir := t.TempDir()

	origDevice := openAIDeviceLoginAction
	t.Cleanup(func() { openAIDeviceLoginAction = origDevice })

	openAIDeviceLoginAction = func(ctx context.Context, gotStateDir string, _ string, showPrompt func(authopenai.DeviceCode), notifyConcurrentLogin func()) (authopenai.AuthStatus, error) {
		if gotStateDir != stateDir {
			t.Fatalf("stateDir = %q, want %q", gotStateDir, stateDir)
		}
		if showPrompt == nil {
			t.Fatal("showPrompt = nil, want CLI prompt callback")
		}
		if notifyConcurrentLogin == nil {
			t.Fatal("notifyConcurrentLogin = nil, want CLI concurrent-login callback")
		}
		showPrompt(authopenai.DeviceCode{
			VerificationURL: "https://auth.openai.com/codex/device",
			UserCode:        "ABC-1234",
		})
		return authopenai.AuthStatus{
			SignedIn: true,
			Source:   authopenai.AuthSourceOAuth,
			Email:    "headless@example.com",
		}, nil
	}

	var stdout, stderr bytes.Buffer
	if err := runOpenAI([]string{"login", "--device", "--state-dir", stateDir}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("runOpenAI() error = %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "device_code_url=https://auth.openai.com/codex/device") {
		t.Fatalf("stdout = %q, want device_code_url line", out)
	}
	if !strings.Contains(out, "device_code=ABC-1234") {
		t.Fatalf("stdout = %q, want device_code line", out)
	}
	if !strings.Contains(out, "state=signed-in source=oauth email=headless@example.com") {
		t.Fatalf("stdout = %q, want signed-in status line", out)
	}
	if !strings.Contains(stderr.String(), "ABC-1234") {
		t.Fatalf("stderr = %q, want human-readable code", stderr.String())
	}
	if !strings.Contains(stdout.String(), "auth_mode=device") {
		t.Fatalf("stdout = %q, want auth_mode=device line", stdout.String())
	}
}

func TestIsHeadlessLoginForDecisionTable(t *testing.T) {
	cases := []struct {
		name string
		goos string
		env  map[string]string
		want bool
	}{
		{
			name: "SERF_LOGIN_HEADLESS=1 forces headless",
			goos: "darwin",
			env:  map[string]string{"SERF_LOGIN_HEADLESS": "1"},
			want: true,
		},
		{
			name: "SERF_LOGIN_HEADLESS=true forces headless",
			goos: "linux",
			env:  map[string]string{"SERF_LOGIN_HEADLESS": "true", "DISPLAY": ":0"},
			want: true,
		},
		{
			name: "SERF_LOGIN_HEADLESS=0 forces not headless even without DISPLAY",
			goos: "linux",
			env:  map[string]string{"SERF_LOGIN_HEADLESS": "0"},
			want: false,
		},
		{
			name: "SERF_LOGIN_HEADLESS=false beats SSH_CONNECTION",
			goos: "linux",
			env:  map[string]string{"SERF_LOGIN_HEADLESS": "false", "SSH_CONNECTION": "1.2.3.4 22 5.6.7.8 22"},
			want: false,
		},
		{
			name: "SSH_CONNECTION set → headless (mac)",
			goos: "darwin",
			env:  map[string]string{"SSH_CONNECTION": "1.2.3.4 22 5.6.7.8 22"},
			want: true,
		},
		{
			name: "SSH_TTY set → headless (windows)",
			goos: "windows",
			env:  map[string]string{"SSH_TTY": "/dev/pts/0"},
			want: true,
		},
		{
			name: "linux with no DISPLAY and no WAYLAND and no SSH → headless",
			goos: "linux",
			env:  map[string]string{},
			want: true,
		},
		{
			name: "linux with DISPLAY=:0 and no SSH → not headless",
			goos: "linux",
			env:  map[string]string{"DISPLAY": ":0"},
			want: false,
		},
		{
			name: "linux with WAYLAND_DISPLAY set → not headless",
			goos: "linux",
			env:  map[string]string{"WAYLAND_DISPLAY": "wayland-0"},
			want: false,
		},
		{
			name: "darwin with no DISPLAY → not headless",
			goos: "darwin",
			env:  map[string]string{},
			want: false,
		},
		{
			name: "windows with no DISPLAY → not headless",
			goos: "windows",
			env:  map[string]string{},
			want: false,
		},
		{
			name: "freebsd treated like linux: no DISPLAY → headless",
			goos: "freebsd",
			env:  map[string]string{},
			want: true,
		},
	}

	for _, tc := range cases {

		t.Run(tc.name, func(t *testing.T) {
			getenv := func(key string) string { return tc.env[key] }
			if got := isHeadlessLoginFor(tc.goos, getenv); got != tc.want {
				t.Fatalf("isHeadlessLoginFor(%q, env=%v) = %v, want %v", tc.goos, tc.env, got, tc.want)
			}
		})
	}
}

func TestChooseLoginModeRespectsExplicitFlags(t *testing.T) {
	// Force the auto-detection result to "device" so we can verify that
	// explicit flags win over auto-detection.
	t.Setenv("SERF_LOGIN_HEADLESS", "1")

	mode, _ := chooseLoginMode(true, false)
	if mode != "device" {
		t.Fatalf("--device forced: mode = %q, want device", mode)
	}
	mode, _ = chooseLoginMode(false, true)
	if mode != "browser" {
		t.Fatalf("--no-device forced: mode = %q, want browser", mode)
	}
	mode, reason := chooseLoginMode(false, false)
	if mode != "device" {
		t.Fatalf("auto + headless env: mode = %q, want device", mode)
	}
	if !strings.Contains(reason, "auto") {
		t.Fatalf("auto reason = %q, want it to mention auto", reason)
	}

	t.Setenv("SERF_LOGIN_HEADLESS", "0")
	mode, reason = chooseLoginMode(false, false)
	if mode != "browser" {
		t.Fatalf("auto + not-headless env: mode = %q, want browser", mode)
	}
	if !strings.Contains(reason, "auto") {
		t.Fatalf("auto reason = %q, want it to mention auto", reason)
	}
}

func TestOpenAILoginRejectsConflictingFlags(t *testing.T) {
	stateDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := runOpenAI([]string{"login", "--device", "--no-device", "--state-dir", stateDir}, strings.NewReader(""), &stdout, &stderr)
	if err == nil {
		t.Fatal("runOpenAI(--device --no-device) error = nil, want conflicting flags error")
	}
	if !strings.Contains(err.Error(), "conflicting flags") {
		t.Fatalf("error = %v, want 'conflicting flags' message", err)
	}
}

// TestOpenAILoginAutoSelectsDeviceWhenHeadless verifies that with no
// explicit --device flag, an environment that looks headless routes us
// through the device-code action.
func TestOpenAILoginAutoSelectsDeviceWhenHeadless(t *testing.T) {
	t.Setenv("SERF_LOGIN_HEADLESS", "1")
	stateDir := t.TempDir()

	origDevice := openAIDeviceLoginAction
	origLogin := openAILoginAction
	t.Cleanup(func() {
		openAIDeviceLoginAction = origDevice
		openAILoginAction = origLogin
	})

	deviceCalled := false
	openAIDeviceLoginAction = func(ctx context.Context, gotStateDir string, _ string, showPrompt func(authopenai.DeviceCode), _ func()) (authopenai.AuthStatus, error) {
		deviceCalled = true
		if gotStateDir != stateDir {
			t.Fatalf("stateDir = %q, want %q", gotStateDir, stateDir)
		}
		showPrompt(authopenai.DeviceCode{
			VerificationURL: "https://auth.openai.com/codex/device",
			UserCode:        "AUTO-9999",
		})
		return authopenai.AuthStatus{
			SignedIn: true,
			Source:   authopenai.AuthSourceOAuth,
			Email:    "auto@example.com",
		}, nil
	}
	openAILoginAction = func(ctx context.Context, _ string, _ string, _ func(string) error, _ func(context.Context) (string, error)) (authopenai.AuthStatus, error) {
		t.Fatal("openAILoginAction (browser) called, want device path")
		return authopenai.AuthStatus{}, nil
	}

	var stdout, stderr bytes.Buffer
	if err := runOpenAI([]string{"login", "--state-dir", stateDir}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("runOpenAI() error = %v", err)
	}
	if !deviceCalled {
		t.Fatal("device login action not called, want auto-detected device flow")
	}
	out := stdout.String()
	if !strings.Contains(out, "auth_mode=device") {
		t.Fatalf("stdout = %q, want auth_mode=device line", out)
	}
	if !strings.Contains(out, "auth_mode_reason=auto_no_display") {
		t.Fatalf("stdout = %q, want auth_mode_reason=auto_no_display in auth_mode line", out)
	}
	if strings.Contains(out, "auth_mode=device ") && strings.Contains(out, "(") {
		t.Fatalf("stdout = %q, want script-friendly auth_mode fields without parenthetical text", out)
	}
	if !strings.Contains(out, "device_code=AUTO-9999") {
		t.Fatalf("stdout = %q, want device_code line from stubbed action", out)
	}
}

func TestOpenAIStatusIsCompactAndScriptFriendly(t *testing.T) {
	stateDir := t.TempDir()

	origStatus := openAIStatusAction
	t.Cleanup(func() { openAIStatusAction = origStatus })

	expiry := time.Date(2026, 5, 8, 1, 2, 3, 0, time.UTC)
	openAIStatusAction = func(gotStateDir string, _ string) (authopenai.AuthStatus, error) {
		if gotStateDir != stateDir {
			t.Fatalf("stateDir = %q, want %q", gotStateDir, stateDir)
		}
		return authopenai.AuthStatus{
			SignedIn:     true,
			Source:       authopenai.AuthSourceOAuth,
			Email:        "user@example.com",
			AccountID:    "acct_123",
			WorkspaceID:  "ws_123",
			Expiry:       expiry,
			NeedsRefresh: true,
			NeedsLogin:   false,
		}, nil
	}

	var stdout, stderr bytes.Buffer
	if err := runOpenAI([]string{"status", "--state-dir", stateDir}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("runOpenAI() error = %v", err)
	}

	if got := strings.TrimSpace(stdout.String()); got != "state=signed-in source=oauth email=user@example.com account_id=acct_123 workspace_id=ws_123 expiry=2026-05-08T01:02:03Z needs_refresh=true needs_login=false" {
		t.Fatalf("stdout = %q", got)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestOpenAILogoutDeletesOnlySerfOwnedAuthState(t *testing.T) {
	// Isolate from any OPENAI_API_KEY / stored OAuth in the dev environment
	// so the post-logout status reports signed-out, not env-fallback.
	oaitest.IsolateOpenAIAuth(t)
	stateDir := t.TempDir()

	if err := authopenai.SaveAuth(stateDir, "openai", authopenai.AuthRecord{
		Version:      1,
		Provider:     "openai",
		Source:       authopenai.AuthSourceOAuth,
		ObtainedAt:   time.Date(2026, 5, 7, 23, 15, 0, 0, time.UTC),
		TokenType:    "Bearer",
		Scope:        "openid profile email offline_access",
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		Expiry:       time.Date(2026, 5, 8, 0, 15, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}

	keepPath := filepath.Join(stateDir, "keep.txt")
	if err := os.WriteFile(keepPath, []byte("keep"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	var stdout, stderr bytes.Buffer
	if err := runOpenAI([]string{"logout", "--state-dir", stateDir}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("runOpenAI() error = %v", err)
	}

	if _, err := os.Stat(authopenai.AuthFilePath(stateDir, "openai")); !os.IsNotExist(err) {
		t.Fatalf("auth file stat error = %v, want not exist", err)
	}
	if _, err := os.Stat(keepPath); err != nil {
		t.Fatalf("keep file stat error = %v, want nil", err)
	}
	if got := strings.TrimSpace(stdout.String()); got != "state=signed-out source=signed-out deleted=true" {
		t.Fatalf("stdout = %q, want signed-out logout result", got)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}
