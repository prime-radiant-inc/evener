package hooks

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/sandbox"
)

// TestHookCommandEnvScrubsSecrets covers reconciliation #5: hook commands built
// their env straight from os.Environ(), so they saw evener's provider API key that
// every other spawned command already scrubs. The scrub now applies to hook env
// regardless of sandboxing.
func TestHookCommandEnvScrubsSecrets(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-must-not-leak")
	t.Setenv("MY_SECRET", "nope")
	t.Setenv("GITHUB_TOKEN", "ghp-nope")
	t.Setenv("HARMLESS", "keep-me")

	hook := plugin.RegisteredHook{Type: "command", Command: "env", Timeout: 5}
	res, err := executeCommandHook(context.Background(), hook, Input{CWD: "/tmp", HookEventName: "X"}, newSystemCommandHookRuntime())
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
	res, err := executeCommandHook(context.Background(), hook, Input{CWD: cwd, HookEventName: "PreToolUse"}, newSystemCommandHookRuntime(), w)
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

// A session started in a nested package runs its hooks at the Git worktree
// root, so the env floor must judge npm's project config there. A root .npmrc
// naming an unserved external cache has to fall back to the session scratch
// (#4542 review).
func TestHookCommandFloorUsesTheConfinedWorktreeDir(t *testing.T) {
	worktree := sandbox.MaterializeWorkspace(t, sandbox.MainCheckout)
	packageDir := filepath.Join(worktree, "packages", "foo")
	packageCache := filepath.Join(t.TempDir(), "package-npm")
	rootCache := filepath.Join(t.TempDir(), "root-npm")
	for _, dir := range []string{packageDir, packageCache, rootCache} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(filepath.Join(packageDir, "package.json"), "{}\n")
	writeFile(filepath.Join(packageDir, ".npmrc"), "cache="+packageCache+"\n")
	writeFile(filepath.Join(worktree, ".npmrc"), "cache="+rootCache+"\n")

	home := t.TempDir()
	sessionTmp := t.TempDir()
	net := true
	rp, err := sandbox.Resolve(
		sandbox.SandboxPolicy{Mode: sandbox.ModeWorkspaceWrite, Network: &net},
		sandbox.HostFacts{
			OS: "linux", Home: home, BwrapPath: "/usr/bin/bwrap", BwrapCapable: true,
			OverlaySupported: true,
		},
		packageDir)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if rp.CacheStrategy != sandbox.CacheOverlay {
		t.Fatalf("test premise: cache strategy = %v, want overlay", rp.CacheStrategy)
	}
	if rp.SessionDir != packageDir || rp.Git.WorktreeRoot != worktree {
		t.Fatalf("test premise: session dir = %q, worktree root = %q", rp.SessionDir, rp.Git.WorktreeRoot)
	}
	if !slices.Contains(rp.CacheRoots, packageCache) || slices.Contains(rp.CacheRoots, rootCache) {
		t.Fatalf("test premise: cache roots = %v, want the package cache only", rp.CacheRoots)
	}
	w, err := sandbox.NewWrapper(rp, "/usr/bin/bwrap", sessionTmp)
	if err != nil {
		t.Fatalf("NewWrapper: %v", err)
	}

	got, workdir := "", ""
	runtime := systemCommandHookRuntime{run: func(_ context.Context, _ commandHookInvocation, cmd *exec.Cmd) error {
		for i := 0; i+1 < len(cmd.Args); i++ {
			if cmd.Args[i] == "--chdir" {
				workdir = cmd.Args[i+1]
				break
			}
		}
		for _, kv := range cmd.Env {
			if name, val, found := strings.Cut(kv, "="); found && name == "npm_config_cache" {
				got = val
			}
		}
		return nil
	}}
	if _, err := runtime.Run(context.Background(), commandHookInvocation{
		Program: "scripted-hook", Timeout: time.Second,
		Env: []string{"HOME=" + home}, SandboxWrapper: w,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if workdir != worktree {
		t.Fatalf("hook workdir = %q, want the worktree root %q", workdir, worktree)
	}
	if want := filepath.Join(sessionTmp, "npm"); got != want {
		t.Fatalf("npm_config_cache = %q, want the session scratch %q", got, want)
	}
}
