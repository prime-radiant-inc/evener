package main

import (
	"bytes"
	"context"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent"
	authopenai "primeradiant.com/serf/internal/auth/openai"
	"primeradiant.com/serf/llm"
	_ "primeradiant.com/serf/llm/providers/openai"
)

// TestNewSessionFromEnv verifies that we can create a working session
// from environment variables. This is the core wiring test.
func TestNewSessionFromEnv(t *testing.T) {
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY not set")
	}

	client, err := llm.NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}

	profile := agent.NewOpenAIProfile("gpt-5-mini-2025-08-07")
	env := agent.NewLocalExecutionEnvironment(t.TempDir())

	sess, err := agent.NewSession(client, profile, env, agent.SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Session should emit SESSION_START on creation.
	select {
	case ev := <-sess.Events():
		if ev.Kind != agent.EventSessionStart {
			t.Fatalf("expected SESSION_START, got %s", ev.Kind)
		}
	default:
		t.Fatal("expected SESSION_START event")
	}
}

// TestProcessInputSimplePrompt sends a simple prompt to the model and verifies
// that the session returns a non-empty text response.
func TestProcessInputSimplePrompt(t *testing.T) {
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY not set")
	}

	client, err := llm.NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}

	profile := agent.NewOpenAIProfile("gpt-5-mini-2025-08-07")
	env := agent.NewLocalExecutionEnvironment(t.TempDir())

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
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY not set")
	}

	client, err := llm.NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}

	tmpDir := t.TempDir()
	profile := agent.NewOpenAIProfile("gpt-5-mini-2025-08-07")
	env := agent.NewLocalExecutionEnvironment(tmpDir)

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

	openAIStatusAction = func(string) (authopenai.AuthStatus, error) {
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
	openAILoginAction = func(ctx context.Context, gotStateDir string, openBrowser func(string) error, readRedirectURL func(context.Context) (string, error)) (authopenai.AuthStatus, error) {
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

func TestOpenAIStatusIsCompactAndScriptFriendly(t *testing.T) {
	stateDir := t.TempDir()

	origStatus := openAIStatusAction
	t.Cleanup(func() { openAIStatusAction = origStatus })

	expiry := time.Date(2026, 5, 8, 1, 2, 3, 0, time.UTC)
	openAIStatusAction = func(gotStateDir string) (authopenai.AuthStatus, error) {
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
	stateDir := t.TempDir()

	if err := authopenai.SaveAuth(stateDir, authopenai.AuthRecord{
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

	if _, err := os.Stat(authopenai.AuthFilePath(stateDir)); !os.IsNotExist(err) {
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
