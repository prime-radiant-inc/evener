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

// sandboxSessionScratchPrefixForTest mirrors sandbox.sessionScratchPrefix. It is
// duplicated deliberately: the session temp container execenv creates must carry
// exactly the prefix the sandbox package's crashed-scratch sweep gates on, and
// that cross-package coupling is what the assertion below pins. There is no
// exported accessor, and adding one just for this test would widen the sandbox
// API for no production caller.
const sandboxSessionScratchPrefixForTest = "evener-sandbox-"

// worldTempForTest points container provisioning at a base this test owns, made
// world-usable (0777 + sticky) so the selection accepts it. It exists because the
// container's shape assertions must not depend on the machine's /tmp —
// AGENTS.md: "Do not make make test or go test ./... depend on ... ambient
// developer machine state" — and because a host with no world-usable base would
// otherwise fail the test while production behaves correctly (it leaves TMPDIR
// inherited).
func worldTempForTest(t *testing.T) string {
	t.Helper()
	base := filepath.Join(t.TempDir(), "host-temp")
	if err := os.Mkdir(base, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0o777|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sandbox.SetWorldTempBasesForTesting([]string{base}))
	return base
}

// requireContainerPlatform skips a test whose assertions are about the POSIX temp
// container. Windows keeps TMPDIR on the session scratch (sandbox.SessionTmpSupported
// is false there), so there is no container to assert about.
func requireContainerPlatform(t *testing.T) {
	t.Helper()
	if !sandbox.SessionTmpSupported {
		t.Skip("the world-usable temp container is POSIX-only; this platform keeps TMPDIR on the scratch")
	}
}

// redirectUserCacheDirForTest points os.UserCacheDir at a directory this test owns
// (HOME and XDG_CACHE_HOME), mirroring cmd/evener's reclaim tests. It exists
// because the crashed-scratch sweep also walks the user cache base: without it a
// test that runs the sweep would read — and delete from — the machine's real cache
// and temp, which AGENTS.md's determinism rule forbids.
func redirectUserCacheDirForTest(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatalf("UserCacheDir: %v", err)
	}
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatalf("create cache base: %v", err)
	}
	return cache
}

// TestCommandEnvironment_UnsandboxedSessionExportsScratchVars: docs/developing-evener/environment.md
// documents EVENER_SCRATCH_DIR with no sandbox-only caveat, so an unsandboxed
// session's spawned commands must see EVENER_SCRATCH_DIR and TMPDIR too, not
// only a sandboxed one.
//
// The two are no longer the same directory. EVENER_SCRATCH_DIR stays the private
// 0700 session scratch — the file tools' writable root — while TMPDIR names a
// world-usable session temp container in a host temp, because an unsandboxed
// command can spawn a child that becomes another uid and no such child can write
// a 0700 directory (#495). The amended decision record
// (docs/superpowers/specs/2026-07-15-session-scratch-and-orchestration-posture-design.md,
// "Environment") states the rule; this test pins its observable shape.
func TestCommandEnvironment_UnsandboxedSessionExportsScratchVars(t *testing.T) {
	requireContainerPlatform(t)
	base := worldTempForTest(t)
	home := t.TempDir()
	worktree := filepath.Join(home, "project")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	env := NewLocalExecutionEnvironment(worktree)
	t.Cleanup(env.Cleanup)
	got := envToMap(env.commandEnvironment(nil))
	scratch, ok := got["EVENER_SCRATCH_DIR"]
	if !ok || strings.TrimSpace(scratch) == "" {
		t.Fatalf("EVENER_SCRATCH_DIR missing from an unsandboxed command env: %v", got)
	}
	info, err := os.Stat(scratch)
	if err != nil || !info.IsDir() {
		t.Fatalf("EVENER_SCRATCH_DIR %q must exist and be a directory: %v", scratch, err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("EVENER_SCRATCH_DIR %q mode = %04o, want the private 0700 scratch", scratch, perm)
	}

	// TMPDIR must NOT name the private scratch: the descendant this export exists
	// for — one that becomes another uid — could not write it.
	tmpDir := got["TMPDIR"]
	if strings.TrimSpace(tmpDir) == "" {
		t.Fatalf("TMPDIR missing from an unsandboxed command env: %v", got)
	}
	if tmpDir == scratch {
		t.Fatalf("TMPDIR = %q names the private scratch, which a privilege-dropping child cannot write (#495)", tmpDir)
	}
	tmpInfo, err := os.Stat(tmpDir)
	if err != nil || !tmpInfo.IsDir() {
		t.Fatalf("TMPDIR %q must exist and be a directory: %v", tmpDir, err)
	}
	if perm := tmpInfo.Mode().Perm(); perm != 0o777 {
		t.Fatalf("TMPDIR %q mode = %04o, want 0777 so an arbitrary uid can create temp files there", tmpDir, perm)
	}
	if tmpInfo.Mode()&os.ModeSticky == 0 {
		t.Fatalf("TMPDIR %q mode = %v, want the sticky bit set so no uid may remove another's entry", tmpDir, tmpInfo.Mode())
	}
	container := filepath.Dir(tmpDir)
	if !strings.HasPrefix(filepath.Base(container), sandboxSessionScratchPrefixForTest) {
		t.Fatalf("temp container %q must carry the sessionScratchPrefix (%q) or the crashed-scratch sweep reaps it by nothing",
			container, sandboxSessionScratchPrefixForTest)
	}
	containerInfo, err := os.Stat(container)
	if err != nil {
		t.Fatalf("temp container %q must exist: %v", container, err)
	}
	if perm := containerInfo.Mode().Perm(); perm != 0o711 {
		t.Fatalf("temp container %q mode = %04o, want 0711 (reachable by others, not writable by them)", container, perm)
	}
	canonicalBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	if got := filepath.Dir(container); got != canonicalBase {
		t.Fatalf("temp container base = %q, want the provisioned world-usable base %q", got, canonicalBase)
	}

	// Provisioned once per env: a second spawn reuses the same directories rather
	// than allocating fresh ones per command.
	got2 := envToMap(env.commandEnvironment(nil))
	if got2["EVENER_SCRATCH_DIR"] != scratch {
		t.Fatalf("second commandEnvironment call allocated a different scratch dir: %q vs %q", got2["EVENER_SCRATCH_DIR"], scratch)
	}
	if got2["TMPDIR"] != tmpDir {
		t.Fatalf("second commandEnvironment call allocated a different temp container: %q vs %q", got2["TMPDIR"], tmpDir)
	}
}

// TestCommandEnvironment_UnsandboxedTmpContainerRetainedAtClose: a close RETAINS
// the temp container — releasing its lease, keeping the directory — exactly as it
// retains the private scratch, and for a reason that rules out removing it: a
// detached command leaves the session on purpose and keeps the TMPDIR it was
// spawned with, so removing the directory at close would strand it on a path that
// no longer exists. Retained means reclaimable, and the test proves the whole
// path: once the container is old enough, the crashed-scratch sweep collects it.
func TestCommandEnvironment_UnsandboxedTmpContainerRetainedAtClose(t *testing.T) {
	requireContainerPlatform(t)
	// Confine BOTH base sets the sweep walks — the scratch allocation bases come
	// from TMPDIR and the user cache dir — so this test neither reads nor deletes
	// the machine's real temp/cache. The world base is confined by worldTempForTest.
	t.Setenv("TMPDIR", t.TempDir())
	redirectUserCacheDirForTest(t)
	worldTempForTest(t)
	worktree := t.TempDir()
	env := NewLocalExecutionEnvironment(worktree)
	tmpDir := envToMap(env.commandEnvironment(nil))["TMPDIR"]
	if tmpDir == "" {
		t.Fatal("an unconfined unsandboxed env must export a TMPDIR container")
	}
	container := filepath.Dir(tmpDir)
	if _, err := os.Stat(container); err != nil {
		t.Fatalf("temp container %q must exist before close: %v", container, err)
	}

	env.Cleanup()

	if _, err := os.Stat(container); err != nil {
		t.Fatalf("Cleanup must RETAIN the session temp container %q (a detached command may still be using it): %v", container, err)
	}
	if scratch := env.SessionScratchDir(); scratch == "" {
		t.Fatal("Cleanup must RETAIN the private scratch for the handoff, not remove it with the temp container")
	}

	aged := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(container, aged, aged); err != nil {
		t.Fatal(err)
	}
	if err := sandbox.SweepCrashedSessionScratch(worktree); err != nil {
		t.Fatalf("sweep over a released container: %v", err)
	}
	if _, err := os.Stat(container); !os.IsNotExist(err) {
		t.Fatalf("a retained container whose lease the close released must be reclaimed by the sweep: %v", err)
	}
}

// TestCommandEnvironment_TmpContainerFailureLeavesTmpDirInherited: a container
// that cannot be provisioned must leave TMPDIR alone — inherited from the process
// env, which is the fail-closed direction — and never fall back to the private
// scratch, which is exactly the value #495 removes. The failure is sticky so a
// broken host temp is not re-probed on every spawn. Provisioning is driven here by
// an empty base set, so the test needs no ambient host temp and no production seam.
func TestCommandEnvironment_TmpContainerFailureLeavesTmpDirInherited(t *testing.T) {
	requireContainerPlatform(t)
	t.Cleanup(sandbox.SetWorldTempBasesForTesting(nil))

	worktree := t.TempDir()
	env := NewLocalExecutionEnvironment(worktree)
	t.Cleanup(env.Cleanup)
	got := envToMap(env.commandEnvironment(nil))
	scratch := got["EVENER_SCRATCH_DIR"]
	if scratch == "" {
		t.Fatal("EVENER_SCRATCH_DIR must still be exported when the temp container cannot be provisioned")
	}
	if got["TMPDIR"] == scratch {
		t.Fatalf("TMPDIR = %q must not fall back to the private scratch when the container fails", got["TMPDIR"])
	}

	// Sticky failure: even after a world-usable base becomes available, the env must
	// not re-attempt provisioning — the failure is recorded once, not re-probed per
	// spawn.
	base := filepath.Join(t.TempDir(), "host-temp")
	if err := os.Mkdir(base, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0o777|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sandbox.SetWorldTempBasesForTesting([]string{base}))
	canonicalBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	again := envToMap(env.commandEnvironment(nil))
	if strings.HasPrefix(again["TMPDIR"], canonicalBase+string(filepath.Separator)) {
		t.Fatalf("a failed container provisioning must be sticky, but a later spawn minted %q under the now-available base", again["TMPDIR"])
	}
}

// TestCommandEnvironment_TmpContainerExportedWhenScratchUnavailable: the two
// variables answer different needs, so an env whose private scratch cannot be
// provisioned must still hand its spawn a world-usable TMPDIR. Inheriting the
// ambient TMPDIR instead would leave a privilege-dropping child on a temp this
// process knows nothing about — possibly a private one — which is the failure #495
// is about.
func TestCommandEnvironment_TmpContainerExportedWhenScratchUnavailable(t *testing.T) {
	requireContainerPlatform(t)
	base := worldTempForTest(t)
	env := NewLocalExecutionEnvironment(t.TempDir())
	t.Cleanup(env.Cleanup)
	// As if the first spawn's scratch provisioning had failed: the sticky failure
	// flag is what unsandboxedScratchDir consults.
	env.scratchMu.Lock()
	env.unsandboxedScratchFailed = true
	env.scratchMu.Unlock()

	if scratch := env.SessionScratchDir(); scratch != "" {
		t.Fatalf("SessionScratchDir = %q, want no scratch for this env", scratch)
	}
	tmpDir := envToMap(env.commandEnvironment(nil))["TMPDIR"]
	if tmpDir == "" {
		t.Fatal("TMPDIR must still be exported when only the scratch is unavailable")
	}
	canonicalBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	if got := filepath.Dir(filepath.Dir(tmpDir)); got != canonicalBase {
		t.Fatalf("TMPDIR = %q, want the container leaf under the world-usable base %q", tmpDir, canonicalBase)
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
