package execenv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/serf/agent/sandbox"
)

// resetLoginShellPATHCache clears the process-wide login-shell PATH cache
// (see resolveOSVersion's identical osVersionOnce pattern) so a test can
// force a fresh probe instead of observing whatever an earlier test cached.
func resetLoginShellPATHCache(t *testing.T) {
	t.Helper()
	prevOnce := loginShellPATHOnce
	prevValue := loginShellPATHValue
	prevOutput := loginShellPATHOutput
	t.Cleanup(func() {
		loginShellPATHOnce = prevOnce
		loginShellPATHValue = prevValue
		loginShellPATHOutput = prevOutput
	})
	loginShellPATHOnce = sync.Once{}
	loginShellPATHValue = ""
}

// --- kata 31gh: developer PATH -------------------------------------------

// TestCommandEnvironment_LoginShellPATHOverridesInherited is the kata 31gh
// regression: a daemon/session launched from a context whose inherited PATH
// lacks the developer's tool directories (macOS launchd/GUI/systemd skip the
// shell rc chain that adds e.g. /opt/homebrew/bin) must still give spawned
// commands the login shell's PATH.
func TestCommandEnvironment_LoginShellPATHOverridesInherited(t *testing.T) {
	env := &LocalExecutionEnvironment{
		RootDir: t.TempDir(),
		inheritedEnv: func() []string {
			return []string{"PATH=/usr/bin:/bin", "HOME=/home/dev"}
		},
		LoginPATH: "/opt/homebrew/bin:/usr/bin:/bin",
	}
	got := envToMap(env.commandEnvironment(nil))
	if got["PATH"] != "/opt/homebrew/bin:/usr/bin:/bin" {
		t.Fatalf("PATH = %q, want the login-shell PATH", got["PATH"])
	}
	if got["HOME"] != "/home/dev" {
		t.Fatalf("HOME = %q, unrelated vars must flow through untouched", got["HOME"])
	}
	if n := countKey(env.commandEnvironment(nil), "PATH"); n != 1 {
		t.Fatalf("PATH must appear exactly once in the command env, got %d", n)
	}
}

// TestCommandEnvironment_ExtraPATHWinsOverLoginShell: a caller-supplied PATH
// in extra (e.g. injectLocalVenvPath's caller) stays authoritative over the
// login-shell override.
func TestCommandEnvironment_ExtraPATHWinsOverLoginShell(t *testing.T) {
	env := &LocalExecutionEnvironment{
		RootDir:      t.TempDir(),
		inheritedEnv: func() []string { return []string{"PATH=/usr/bin"} },
		LoginPATH:    "/opt/homebrew/bin:/usr/bin",
	}
	got := envToMap(env.commandEnvironment(map[string]string{"PATH": "/explicit/bin"}))
	if got["PATH"] != "/explicit/bin" {
		t.Fatalf("PATH = %q, want the caller-supplied override", got["PATH"])
	}
}

// TestCommandEnvironment_NoLoginPATHLeavesInheritedUnchanged: an empty
// LoginPATH (probe never ran, failed, or timed out) is byte-identical to
// today's behavior.
func TestCommandEnvironment_NoLoginPATHLeavesInheritedUnchanged(t *testing.T) {
	env := &LocalExecutionEnvironment{
		RootDir:      t.TempDir(),
		inheritedEnv: func() []string { return []string{"PATH=/usr/bin:/bin"} },
	}
	got := envToMap(env.commandEnvironment(nil))
	if got["PATH"] != "/usr/bin:/bin" {
		t.Fatalf("PATH = %q, want the inherited PATH unchanged", got["PATH"])
	}
}

func countKey(env []string, key string) int {
	n := 0
	for _, kv := range env {
		if k, _, ok := strings.Cut(kv, "="); ok && k == key {
			n++
		}
	}
	return n
}

// TestLoginShellPATH_ResolvesAndCaches: the probe runs $SHELL -lc 'echo $PATH'
// exactly once per process and caches the result, mirroring OSVersion's
// osVersionOnce pattern.
func TestLoginShellPATH_ResolvesAndCaches(t *testing.T) {
	resetLoginShellPATHCache(t)
	t.Setenv("SHELL", "/bin/zsh")
	calls := 0
	loginShellPATHOutput = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls++
		if name != "/bin/zsh" || len(args) != 2 || args[0] != "-lc" || args[1] != "echo $PATH" {
			t.Fatalf("unexpected probe invocation: name=%q args=%v", name, args)
		}
		return []byte("/opt/homebrew/bin:/usr/bin:/bin\n"), nil
	}
	if got := LoginShellPATH(); got != "/opt/homebrew/bin:/usr/bin:/bin" {
		t.Fatalf("LoginShellPATH() = %q", got)
	}
	if got := LoginShellPATH(); got != "/opt/homebrew/bin:/usr/bin:/bin" {
		t.Fatalf("LoginShellPATH() second call = %q", got)
	}
	if calls != 1 {
		t.Fatalf("probe ran %d times, want exactly 1 (cached)", calls)
	}
}

// TestLoginShellPATH_FallsBackOnProbeFailure: a failing probe (non-zero exit,
// timeout, or no $SHELL) never blocks or errors — it resolves to "", and
// commandEnvironment then leaves PATH untouched (see
// TestCommandEnvironment_NoLoginPATHLeavesInheritedUnchanged).
func TestLoginShellPATH_FallsBackOnProbeFailure(t *testing.T) {
	resetLoginShellPATHCache(t)
	t.Setenv("SHELL", "/bin/zsh")
	loginShellPATHOutput = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, errors.New("boom")
	}
	if got := LoginShellPATH(); got != "" {
		t.Fatalf("LoginShellPATH() = %q, want empty on probe failure", got)
	}
}

// TestLoginShellPATH_NoShellFallsBack: an unset $SHELL never invokes the probe.
func TestLoginShellPATH_NoShellFallsBack(t *testing.T) {
	resetLoginShellPATHCache(t)
	t.Setenv("SHELL", "")
	loginShellPATHOutput = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		t.Fatal("probe must not run when $SHELL is unset")
		return nil, nil
	}
	if got := LoginShellPATH(); got != "" {
		t.Fatalf("LoginShellPATH() = %q, want empty", got)
	}
}

// --- always-on session scratch vars ---------------------------------------

// TestCommandEnvironment_UnsandboxedSessionExportsScratchVars: docs/environment.md
// documents SERF_SCRATCH_DIR with no sandbox-only caveat, so an unsandboxed
// session's spawned commands must see SERF_SCRATCH_DIR and TMPDIR too, not
// only a sandboxed one.
func TestCommandEnvironment_UnsandboxedSessionExportsScratchVars(t *testing.T) {
	home := t.TempDir()
	worktree := filepath.Join(home, "project")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	env := NewLocalExecutionEnvironment(worktree)
	got := envToMap(env.commandEnvironment(nil))
	scratch, ok := got["SERF_SCRATCH_DIR"]
	if !ok || strings.TrimSpace(scratch) == "" {
		t.Fatalf("SERF_SCRATCH_DIR missing from an unsandboxed command env: %v", got)
	}
	if got["TMPDIR"] != scratch {
		t.Fatalf("TMPDIR = %q, want it to match SERF_SCRATCH_DIR %q", got["TMPDIR"], scratch)
	}
	info, err := os.Stat(scratch)
	if err != nil || !info.IsDir() {
		t.Fatalf("SERF_SCRATCH_DIR %q must exist and be a directory: %v", scratch, err)
	}
	// Provisioned once per env: a second spawn reuses the same directory rather
	// than allocating a fresh one per command.
	got2 := envToMap(env.commandEnvironment(nil))
	if got2["SERF_SCRATCH_DIR"] != scratch {
		t.Fatalf("second commandEnvironment call allocated a different scratch dir: %q vs %q", got2["SERF_SCRATCH_DIR"], scratch)
	}
}

// TestCommandEnvironment_SandboxedSessionScratchUnchanged: a sandboxed env's
// scratch/TMPDIR vars come from sandbox.ApplyEnvFloor at the actual spawn
// site (command_runtime.go's wrapCommandForSandbox), not from
// commandEnvironment — this locks in that commandEnvironment itself must
// never inject scratch vars when a kernel Wrapper is attached, so the
// unsandboxed addition cannot regress sandboxed behavior.
func TestCommandEnvironment_SandboxedSessionScratchUnchanged(t *testing.T) {
	worktree := t.TempDir()
	env := NewLocalExecutionEnvironment(worktree)
	w, err := sandbox.NewWrapper(
		sandbox.ResolvedPolicy{Mode: sandbox.ModeWorkspaceWrite, Backend: sandbox.BackendBwrap},
		"/usr/bin/bwrap", t.TempDir())
	if err != nil {
		t.Fatalf("NewWrapper: %v", err)
	}
	env.Wrapper = w

	got := envToMap(env.commandEnvironment(nil))
	if _, ok := got["SERF_SCRATCH_DIR"]; ok {
		t.Fatalf("commandEnvironment must not itself inject SERF_SCRATCH_DIR for a sandboxed env; ApplyEnvFloor owns that: %v", got)
	}
}
