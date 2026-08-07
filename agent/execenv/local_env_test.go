package execenv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"

	"primeradiant.com/serf/agent/sandbox"
)

// resetLoginShellPATHCache clears the process-wide login-shell PATH cache
// (see resolveOSVersion's identical osVersionOnce pattern) so a test can
// force a fresh probe instead of observing whatever an earlier test cached.
func resetLoginShellPATHCache(t *testing.T) {
	t.Helper()
	prevValue := loginShellPATHValue
	prevOutput := loginShellPATHOutput
	t.Cleanup(func() {
		// A sync.Once cannot be saved and restored by value (go vet copylocks);
		// cleanup hands back a FRESH one, which costs a later caller at most one
		// re-probe through the restored real output func.
		loginShellPATHOnce = sync.Once{}
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

// TestLoginShellPATH_TakesLastLineOverNoisyRCBanner: an rc file that prints a
// banner to stdout before running (nvm/conda version notices, a MOTD-style
// .zshrc echo) must not get prepended into the resolved PATH as a garbage
// ":"-separated segment — the probe's own "echo $PATH" output is always the
// LAST thing the login shell prints, so only the last non-empty line counts.
func TestLoginShellPATH_TakesLastLineOverNoisyRCBanner(t *testing.T) {
	resetLoginShellPATHCache(t)
	t.Setenv("SHELL", "/bin/zsh")
	loginShellPATHOutput = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("nvm is not compatible with the npm config \"prefix\" option\n\n/opt/homebrew/bin:/usr/bin:/bin\n"), nil
	}
	if got := LoginShellPATH(); got != "/opt/homebrew/bin:/usr/bin:/bin" {
		t.Fatalf("LoginShellPATH() = %q, want the last line only (rc banner excluded)", got)
	}
}

// TestLoginPATH_SurvivesReRoot: the developer-PATH fix must reach the paths
// serf actually runs — a worktree-isolated delegate and a managed-worktree
// switch both re-root the session env through WithWorkingDirectory, so a
// child that dropped LoginPATH would silently revert to the launchd/GUI
// PATH the fix exists to replace.
func TestLoginPATH_SurvivesReRoot(t *testing.T) {
	env := &LocalExecutionEnvironment{
		RootDir:      t.TempDir(),
		inheritedEnv: func() []string { return []string{"PATH=/usr/bin:/bin"} },
		LoginPATH:    "/opt/homebrew/bin:/usr/bin:/bin",
	}
	child := env.WithWorkingDirectory(t.TempDir())
	if child.LoginPATH != env.LoginPATH {
		t.Fatalf("child LoginPATH = %q, want the parent's %q", child.LoginPATH, env.LoginPATH)
	}
	if got := envToMap(child.commandEnvironment(nil))["PATH"]; got != env.LoginPATH {
		t.Fatalf("re-rooted child PATH = %q, want the login-shell PATH", got)
	}
}

// TestLoginPATH_SurvivesSandboxInvocationGrant: the M7 escalation re-dispatch
// clone runs a real tool call, so it must spawn with the same PATH as the env
// it clones.
func TestLoginPATH_SurvivesSandboxInvocationGrant(t *testing.T) {
	worktree := t.TempDir()
	env := &LocalExecutionEnvironment{
		RootDir:      worktree,
		inheritedEnv: func() []string { return []string{"PATH=/usr/bin:/bin"} },
		LoginPATH:    "/opt/homebrew/bin:/usr/bin:/bin",
		Sandbox:      &sandbox.ResolvedPolicy{Mode: sandbox.ModeWorkspaceWrite, Backend: sandbox.BackendBwrap},
	}
	clone, ok := env.WithSandboxInvocationGrant(filepath.Join(worktree, "granted")).(*LocalExecutionEnvironment)
	if !ok {
		t.Fatal("WithSandboxInvocationGrant did not return a clone for an enforced policy")
	}
	if clone.LoginPATH != env.LoginPATH {
		t.Fatalf("grant clone LoginPATH = %q, want the parent's %q", clone.LoginPATH, env.LoginPATH)
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

// TestUnsandboxedScratchDirConcurrentProvisioning: unsandboxedScratchDir is
// called from commandEnvironment, which concurrent ExecCommand/ExecArgv calls
// can reach at the same time — the mutex-guarded lazy provisioning must hand
// every caller the SAME directory, never race-allocate two. Run with -race.
func TestUnsandboxedScratchDirConcurrentProvisioning(t *testing.T) {
	worktree := t.TempDir()
	env := NewLocalExecutionEnvironment(worktree)

	const n = 16
	results := make([]string, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			results[i] = env.unsandboxedScratchDir()
		}(i)
	}
	wg.Wait()

	if results[0] == "" {
		t.Fatal("unsandboxedScratchDir() returned empty")
	}
	for i, got := range results {
		if got != results[0] {
			t.Fatalf("goroutine %d got scratch dir %q, want %q (every caller must share one dir)", i, got, results[0])
		}
	}
}

// TestCleanupReleasesUnsandboxedScratchLease is the leak regression from the
// task-1 review: off/unsandboxed is the DEFAULT sandbox mode, and
// session_lifecycle.go calls Cleanup() on every session close, so if Cleanup
// never released the unsandboxedScratch lease, a long-running `serf serve`
// daemon would hold one open file descriptor (the lease flock) per session
// for the rest of the process's life — sweepCrashedSessionScratch can only
// reclaim a lease that is currently acquirable. Verified at the OS level: a
// fresh flock attempt on the SAME lease file must succeed immediately after
// Cleanup(), proving the held lock was actually released, not merely that no
// error was returned.
func TestCleanupReleasesUnsandboxedScratchLease(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("flock-based lease verification is unix-only")
	}
	worktree := t.TempDir()
	env := NewLocalExecutionEnvironment(worktree)
	scratch := env.unsandboxedScratchDir()
	if scratch == "" {
		t.Fatal("unsandboxedScratchDir provisioning failed")
	}

	env.Cleanup()

	// ".serf-session.lock" mirrors sandbox.SessionScratch's lease filename
	// convention (agent/sandbox/session_scratch.go); there is no exported way
	// to introspect lease state, so this checks the real OS-level lock instead.
	leasePath := filepath.Join(scratch, ".serf-session.lock")
	f, err := os.OpenFile(leasePath, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open lease file: %v", err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("lease still held after Cleanup (leak): %v", err)
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
