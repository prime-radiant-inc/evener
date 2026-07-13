package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// requireRealBwrap gates a test on a bwrap-capable host and skips under -short so
// the fast unit gate never shells out to bubblewrap. It returns real host facts
// (with the resolved bwrap path) for building a live wrapper.
func requireRealBwrap(t *testing.T) HostFacts {
	t.Helper()
	if testing.Short() {
		t.Skip("real-bwrap integration test skipped under -short")
	}
	facts := RealProber{}.Probe()
	if facts.OS != "linux" || !facts.BwrapCapable || facts.BwrapPath == "" {
		t.Skip("bwrap not capable on this host")
	}
	return facts
}

// runWrapped resolves mode against real bwrap facts anchored at home, builds a
// live wrapper, wraps a `bash -c script` and runs it, returning combined output.
func runWrapped(t *testing.T, facts HostFacts, mode Mode, netOn bool, cwd, sessionTmp, script string) (string, error) {
	t.Helper()
	net := netOn
	rp, err := Resolve(SandboxPolicy{Mode: mode, Network: &net}, facts, cwd)
	if err != nil {
		t.Fatalf("Resolve(%v): %v", mode, err)
	}
	w, err := NewWrapper(rp, facts.BwrapPath, sessionTmp)
	if err != nil {
		t.Fatalf("NewWrapper: %v", err)
	}
	argv := w.Wrap([]string{"/bin/bash", "-c", script}, cwd)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	// Mirror the real spawn sites: raise the sandbox env floor (TMPDIR + cache
	// redirect + secret drops) and inherit no extra fds.
	cmd.Env = ApplyEnvFloor(os.Environ(), rp, sessionTmp)
	cmd.ExtraFiles = nil
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestBwrapLinkedWorktreeStarts covers verifier finding F2 end-to-end: a linked
// worktree's common .git is outside the worktree and read-only, so the sandbox
// must not try to pin a missing protected surface there (which would EROFS-abort).
// The fixture lives OUTSIDE /tmp because the sandbox mounts a fresh tmpfs over
// /tmp — a /tmp fixture's common dir would be shadowed and hide the bug.
func TestBwrapLinkedWorktreeStarts(t *testing.T) {
	facts := requireRealBwrap(t)

	// Keep the fixture outside /tmp, which the sandbox replaces with tmpfs.
	// /var/tmp also remains traversable when the test runner has elevated host
	// privileges that bubblewrap intentionally drops.
	base, err := os.MkdirTemp("/var/tmp", "sbxlwt-")
	if err != nil {
		t.Skipf("create non-/tmp sandbox fixture: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })

	main := filepath.Join(base, "main")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	gitHarness(t, main, "init", "-q")
	gitHarness(t, main, "commit", "-q", "--allow-empty", "-m", "init")
	wt := filepath.Join(base, "wt")
	gitHarness(t, main, "worktree", "add", "-q", wt)
	cwd := resolveCleanPath(wt)

	facts.Home = t.TempDir()
	// If the sandbox aborts (EROFS on a dead pin), the command fails to start.
	out, err := runWrapped(t, facts, ModeWorkspaceWrite, true, cwd, t.TempDir(),
		`echo LWT-OK; git status --porcelain >/dev/null 2>&1; echo "git=$?"`)
	if err != nil {
		t.Fatalf("linked-worktree sandbox failed to start/run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "LWT-OK") {
		t.Errorf("linked-worktree sandbox did not run the command:\n%s", out)
	}
}

// TestBwrapReadOnlyTmpWorktreeStarts covers MED finding #5 end-to-end: a
// read-only session whose worktree lives under /tmp must still start. Read-only
// mode grants no write root, so without re-binding the cwd read-only after the
// /tmp tmpfs, --chdir into the shadowed worktree aborts the sandbox.
func TestBwrapReadOnlyTmpWorktreeStarts(t *testing.T) {
	facts := requireRealBwrap(t)
	facts.Home = t.TempDir()
	cwd := MaterializeWorkspace(t, MainCheckout) // t.TempDir()-based, under /tmp
	if !pathUnder(cwd, "/tmp") {
		t.Skipf("test needs a /tmp-based cwd; TempDir gave %q", cwd)
	}
	out, err := runWrapped(t, facts, ModeReadOnly, true, cwd, t.TempDir(),
		`echo RO-OK; pwd`)
	if err != nil {
		t.Fatalf("read-only /tmp worktree sandbox failed to start: %v\n%s", err, out)
	}
	if !strings.Contains(out, "RO-OK") {
		t.Errorf("sandbox did not run the command:\n%s", out)
	}
	if !strings.Contains(out, cwd) {
		t.Errorf("pwd inside the sandbox must be the re-bound /tmp worktree %q:\n%s", cwd, out)
	}
}

func TestBwrapConfinesAndMasks(t *testing.T) {
	facts := requireRealBwrap(t)

	// A fake home with a credential file that MUST be invisible inside the sandbox.
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	const secret = "TOPSECRET-KEY-MATERIAL"
	if err := os.WriteFile(filepath.Join(home, ".ssh", "id_ed25519"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	facts.Home = home

	cwd := MaterializeWorkspace(t, MainCheckout)
	sessionTmp := t.TempDir()

	script := strings.Join([]string{
		`echo "comm=$(cat /proc/1/comm)"`,
		`echo "secret=$(cat '` + filepath.Join(home, ".ssh", "id_ed25519") + `' 2>/dev/null)"`,
		`echo "sshdir=$(ls -A '` + filepath.Join(home, ".ssh") + `' 2>&1)"`,
	}, "; ")

	out, err := runWrapped(t, facts, ModeWorkspaceWrite, true, cwd, sessionTmp, script)
	if err != nil {
		t.Fatalf("wrapped command failed: %v\noutput:\n%s", err, out)
	}

	if strings.Contains(out, secret) {
		t.Errorf("credential content leaked into the sandbox:\n%s", out)
	}
	// PID 1 inside is the sandbox init, not a host process — host PID state is gone.
	if !strings.Contains(out, "comm=bwrap") {
		t.Errorf("expected /proc/1/comm to be the sandbox init (bwrap); host process state visible:\n%s", out)
	}
	// The masked ~/.ssh reads as empty (the tmpfs overlay), not the real dir.
	if !strings.Contains(out, "sshdir=") || strings.Contains(out, "id_ed25519") {
		t.Errorf("masked ~/.ssh must be an empty tmpfs, not the real credential dir:\n%s", out)
	}
}

func TestBwrapDeniesGitConfigWrite(t *testing.T) {
	facts := requireRealBwrap(t)
	facts.Home = t.TempDir()
	cwd := MaterializeWorkspace(t, MainCheckout)
	sessionTmp := t.TempDir()

	script := strings.Join([]string{
		`git config --local core.hooksPath /tmp/evil 2>&1 | head -1`,
		`echo "config-exit=${PIPESTATUS[0]}"`,
		`echo "hook-write=$(echo pwned > '` + filepath.Join(cwd, ".git", "hooks", "post-commit") + `' 2>&1; echo exit=$?)"`,
	}, "; ")

	out, err := runWrapped(t, facts, ModeWorkspaceWrite, true, cwd, sessionTmp, script)
	if err != nil {
		t.Fatalf("wrapped command failed unexpectedly: %v\noutput:\n%s", err, out)
	}
	if strings.Contains(out, "config-exit=0") {
		t.Errorf("git config --local write into a sandboxed .git/config must fail:\n%s", out)
	}
	if strings.Contains(out, "hook-write=exit=0") {
		t.Errorf("writing a git hook must be denied inside the sandbox:\n%s", out)
	}
	// The real hook file must not exist on the host afterward.
	if _, err := os.Stat(filepath.Join(cwd, ".git", "hooks", "post-commit")); err == nil {
		t.Errorf("a git hook was persisted to the host despite the sandbox")
	}
}
