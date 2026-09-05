package execenv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/sandbox"
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
// evener actually runs — a worktree-isolated delegate and a managed-worktree
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

// TestCommandEnvironment_UnsandboxedSessionExportsScratchVars: docs/developing-evener/environment.md
// documents EVENER_SCRATCH_DIR with no sandbox-only caveat, so an unsandboxed
// session's spawned commands must see EVENER_SCRATCH_DIR and TMPDIR too, not
// only a sandboxed one.
func TestCommandEnvironment_UnsandboxedSessionExportsScratchVars(t *testing.T) {
	home := t.TempDir()
	worktree := filepath.Join(home, "project")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	env := NewLocalExecutionEnvironment(worktree)
	got := envToMap(env.commandEnvironment(nil))
	scratch, ok := got["EVENER_SCRATCH_DIR"]
	if !ok || strings.TrimSpace(scratch) == "" {
		t.Fatalf("EVENER_SCRATCH_DIR missing from an unsandboxed command env: %v", got)
	}
	if got["TMPDIR"] != scratch {
		t.Fatalf("TMPDIR = %q, want it to match EVENER_SCRATCH_DIR %q", got["TMPDIR"], scratch)
	}
	info, err := os.Stat(scratch)
	if err != nil || !info.IsDir() {
		t.Fatalf("EVENER_SCRATCH_DIR %q must exist and be a directory: %v", scratch, err)
	}
	// Provisioned once per env: a second spawn reuses the same directory rather
	// than allocating a fresh one per command.
	got2 := envToMap(env.commandEnvironment(nil))
	if got2["EVENER_SCRATCH_DIR"] != scratch {
		t.Fatalf("second commandEnvironment call allocated a different scratch dir: %q vs %q", got2["EVENER_SCRATCH_DIR"], scratch)
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
	env.inheritedEnv = func() []string { return []string{"PATH=/usr/bin:/bin"} }
	w, err := sandbox.NewWrapper(
		sandbox.ResolvedPolicy{Mode: sandbox.ModeWorkspaceWrite, Backend: sandbox.BackendBwrap},
		"/usr/bin/bwrap", t.TempDir())
	if err != nil {
		t.Fatalf("NewWrapper: %v", err)
	}
	env.Wrapper = w

	got := envToMap(env.commandEnvironment(nil))
	if _, ok := got["EVENER_SCRATCH_DIR"]; ok {
		t.Fatalf("commandEnvironment must not itself inject EVENER_SCRATCH_DIR for a sandboxed env; ApplyEnvFloor owns that: %v", got)
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
	for i := range n {
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

// TestUnsandboxedScratchDirGitProbeDoesNotSelfDeadlock pins the one
// working-directory shape that used to drive the lazy scratch mint through a
// `git rev-parse` subprocess, and through that into itself.
//
// structuralWorktreeRoot stops at the FIRST ".git" entry it finds walking up
// from cwd and reports a miss ("", false) when that entry is neither a
// directory nor a parseable "gitdir:" pointer; hasGitEntryAncestor, walking the
// same chain, only checks that the entry EXISTS. So a directory whose nearest
// ".git" is a regular file that is not a gitdir pointer — a checkout left
// half-written by an interrupted `git worktree add`/`git submodule` step, a
// ".git" file restored from an archive, a stray file with that name — misses
// structurally and still reaches the git-subprocess fallback. An INTACT linked
// worktree or submodule does not qualify: its "gitdir:" pointer parses, so the
// structural resolver answers without forking.
//
// Every spawn builds its environment through overlaySessionEnv, which mints the
// scratch dir, so a mint that forks git re-enters unsandboxedScratchDir and
// blocks forever on the non-reentrant mutex it is already holding (and, once
// that lock is reordered away, on gitRootCache.lookup's mutex, which is held
// across the same probe). The mint must therefore resolve its workspace anchor
// without spawning anything at all.
func TestUnsandboxedScratchDirGitProbeDoesNotSelfDeadlock(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	work := filepath.Join(repo, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git"), []byte("not a gitdir pointer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := structuralWorktreeRoot(work); ok {
		t.Fatal("layout no longer misses structuralWorktreeRoot; this test would not exercise the git fallback")
	}
	if present, known := hasGitEntryAncestor(work); !present || !known {
		t.Fatalf("layout has no observable .git ancestor (present=%v known=%v); this test would not exercise the git fallback", present, known)
	}

	env := NewLocalExecutionEnvironment(work)
	env.sandboxTmpBase = t.TempDir()
	env.inheritedEnv = func() []string { return []string{"PATH=/usr/bin:/bin"} }

	// Concurrent callers also pin the once-only invariant on this path: the
	// mint may not race-allocate a second scratch dir for the same env.
	const callers = 8
	results := make(chan string, callers)
	for range callers {
		go func() { results <- envToMap(env.commandEnvironment(nil))["EVENER_SCRATCH_DIR"] }()
	}

	// TRIPWIRE: minting a scratch dir is a handful of stat/mkdir calls, well
	// under a millisecond; this ceiling only fires when the mint wedges.
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	got := make([]string, 0, callers)
	for range callers {
		select {
		case scratch := <-results:
			got = append(got, scratch)
		case <-deadline.C:
			t.Fatalf("scratch mint deadlocked: only %d of %d callers returned", len(got), callers)
		}
	}

	if strings.TrimSpace(got[0]) == "" {
		t.Fatalf("EVENER_SCRATCH_DIR missing from an unsandboxed command env in a repo with an unparseable .git file")
	}
	if info, err := os.Stat(got[0]); err != nil || !info.IsDir() {
		t.Fatalf("EVENER_SCRATCH_DIR %q must exist and be a directory: %v", got[0], err)
	}
	for i, scratch := range got {
		if scratch != got[0] {
			t.Fatalf("caller %d got scratch dir %q, want %q (every caller must share one dir)", i, scratch, got[0])
		}
	}
}
