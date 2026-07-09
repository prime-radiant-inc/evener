package hooks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/sandbox"
)

// TestHookCommandEnvScrubsSecrets covers reconciliation #5: hook commands built
// their env straight from os.Environ(), so they saw serf's provider API key that
// every other spawned command already scrubs. The scrub now applies to hook env
// regardless of sandboxing.
func TestHookCommandEnvScrubsSecrets(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-must-not-leak")
	t.Setenv("MY_SECRET", "nope")
	t.Setenv("GITHUB_TOKEN", "ghp-nope")
	t.Setenv("HARMLESS", "keep-me")

	hook := plugin.RegisteredHook{Type: "command", Command: "env", Timeout: 5}
	res, err := executeCommandHook(context.Background(), hook, Input{CWD: "/tmp", HookEventName: "X"})
	if err != nil {
		t.Fatalf("executeCommandHook: %v", err)
	}
	for _, leaked := range []string{"sk-must-not-leak", "OPENAI_API_KEY", "MY_SECRET", "GITHUB_TOKEN"} {
		if strings.Contains(res.Stdout, leaked) {
			t.Errorf("hook env leaked %q:\n%s", leaked, res.Stdout)
		}
	}
	if !strings.Contains(res.Stdout, "HARMLESS=keep-me") {
		t.Errorf("hook env must keep non-secret vars:\n%s", res.Stdout)
	}
}

func realBwrapWrapper(t *testing.T, home, cwd, sessionTmp string) *sandbox.Wrapper {
	t.Helper()
	if testing.Short() {
		t.Skip("real-bwrap integration test skipped under -short")
	}
	facts := sandbox.RealProber{}.Probe()
	if facts.OS != "linux" || !facts.BwrapCapable || facts.BwrapPath == "" {
		t.Skip("bwrap not capable on this host")
	}
	facts.Home = home
	net := true
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeWorkspaceWrite, Network: &net}, facts, cwd)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	w, err := sandbox.NewWrapper(rp, facts.BwrapPath, sessionTmp)
	if err != nil {
		t.Fatalf("NewWrapper: %v", err)
	}
	return w
}

func TestHookCommandConfined(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	const secret = "HOOK-LEAKED-SECRET"
	if err := os.WriteFile(filepath.Join(home, ".ssh", "id"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	cwd := sandbox.MaterializeWorkspace(t, sandbox.MainCheckout)
	w := realBwrapWrapper(t, home, cwd, t.TempDir())

	hook := plugin.RegisteredHook{
		Type:    "command",
		Command: "cat " + filepath.Join(home, ".ssh", "id") + " 2>&1 || true; echo comm=$(cat /proc/1/comm)",
		Timeout: 15,
	}
	res, err := executeCommandHook(context.Background(), hook, Input{CWD: cwd, HookEventName: "PreToolUse"}, w)
	if err != nil {
		t.Fatalf("executeCommandHook: %v", err)
	}
	if strings.Contains(res.Stdout+res.Stderr, secret) {
		t.Errorf("a sandboxed hook leaked a masked credential:\n%s\n%s", res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "comm=bwrap") {
		t.Errorf("a sandboxed hook must run under the kernel sandbox (PID 1 = bwrap):\n%s", res.Stdout)
	}
}
