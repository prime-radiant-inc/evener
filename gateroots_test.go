package evener_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gateRootsLib is the durable-root library the Go test streams run under. The
// tests here drive it through a real /bin/sh rather than reading its text: the
// claim is a lock file, the reclaim turns on `kill -0`, and the root's mode is
// what the skill-cache trust check reads, none of which a text assertion can
// exercise.
const gateRootsLib = "scripts/lib/gate-roots.sh"

// The invariant shell prefixes the cases below build on. gateRootsClaim also
// writes a marker inside the root, which doubles as the proof that a claimed
// root is writable.
const (
	gateRootsClaim = ". " + gateRootsLib + "\nevener_claim_gate_root \"$1\""
	gateRootsReset = ". " + gateRootsLib + "\nevener_reset_gate_root \"$1\""
)

// runShSource runs script in one POSIX shell with args as $1.., after asserting
// the library exists. The child gets a minimal environment so an ambient
// TMPDIR or HOME cannot decide the result.
func runShSource(t *testing.T, script string, env []string, args ...string) (string, int) {
	t.Helper()
	if _, err := os.Stat(gateRootsLib); err != nil {
		t.Fatalf("stat %s: %v", gateRootsLib, err)
	}
	base := []string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C"}
	cmd := exec.Command("sh", append([]string{"-c", script, "sh"}, args...)...)
	cmd.Env = envOverride(base, env...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return strings.TrimSpace(string(out)), 0
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("sh -c: %v\noutput:\n%s", err, out)
	}
	return strings.TrimSpace(string(out)), exit.ExitCode()
}

// gateRootUnder returns an absolute gate root shaped exactly like the one the
// runner derives (the guard only permits this shape), placed under a test temp
// directory rather than the caller's TMPDIR.
func gateRootUnder(under string, parts ...string) string {
	return filepath.Join(append([]string{under, "evener-gate-roots-0123456789abcdef"}, parts...)...)
}

// mustRunShSource is runShSource for the cases that must succeed.
func mustRunShSource(t *testing.T, script string, args ...string) string {
	t.Helper()
	out, code := runShSource(t, script, nil, args...)
	if code != 0 {
		t.Fatalf("sh source exited %d:\n%s", code, out)
	}
	return out
}

// claimGateRoot claims root for this test and writes a marker inside it.
func claimGateRoot(t *testing.T, root string) {
	t.Helper()
	mustRunShSource(t, gateRootsClaim+"\nprintf x >\"$1/scratch\"\n", root)
}

// releaseGateRoot releases root, retaining its scratch under keepDir when that
// is non-empty (a red run) and removing it when it is empty (a green run).
func releaseGateRoot(t *testing.T, root, keepDir string) {
	t.Helper()
	mustRunShSource(t, ". "+gateRootsLib+"\nevener_release_gate_root \"$1\" \"$2\"", root, keepDir)
}

// TestDurableGateRootIsStablePerWorktree pins the property the whole change
// rests on: the same worktree must derive the same root path on every run,
// because Go's test cache keys on those paths, while sibling checkouts must not
// share one.
func TestDurableGateRootIsStablePerWorktree(t *testing.T) {
	t.Parallel()
	tmpHome := t.TempDir()
	env := []string{"TMPDIR=" + tmpHome}
	script := ". " + gateRootsLib + "\n" +
		`evener_durable_gate_root "$1"` + "\n" +
		`evener_durable_gate_root "$1"` + "\n" +
		`evener_durable_gate_root "$2"` + "\n"

	out, code := runShSource(t, script, env, "/tmp/worktree-a", "/tmp/worktree-b")
	if code != 0 {
		t.Fatalf("evener_durable_gate_root exited %d:\n%s", code, out)
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 lines, got %d:\n%s", len(lines), out)
	}
	if lines[0] != lines[1] {
		t.Errorf("the same worktree derived two different roots across runs:\n  %s\n  %s", lines[0], lines[1])
	}
	if lines[0] == lines[2] {
		t.Errorf("two different worktrees derived the same root %q; sibling checkouts would share roots", lines[0])
	}
	wantPrefix := filepath.Join(tmpHome, "evener-gate-roots-")
	if !strings.HasPrefix(lines[0], wantPrefix) {
		t.Errorf("root %q is not under %q", lines[0], wantPrefix)
	}
}

// TestClaimGateRootReportsLiveContention drops one claim onto a root a second,
// live shell already holds. That is the case the runner must fall back for: the
// held run must be left untouched, and the second claim must refuse rather than
// empty a root another run is writing into.
func TestClaimGateRootReportsLiveContention(t *testing.T) {
	t.Parallel()
	tmpHome := t.TempDir()
	root := gateRootUnder(tmpHome, "agent")

	holder := exec.Command("sh", "-c",
		". "+gateRootsLib+"\n"+
			`evener_claim_gate_root "$1" || exit 9`+"\n"+
			`touch "$2"`+"\n"+
			"exec sleep 600",
		"sh", root, filepath.Join(tmpHome, "held"))
	holder.Env = envOverride([]string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C"})
	if err := holder.Start(); err != nil {
		t.Fatalf("start holder: %v", err)
	}
	t.Cleanup(func() {
		_ = holder.Process.Kill()
		_ = holder.Wait()
	})
	held := filepath.Join(tmpHome, "held")
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(held); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("holder never claimed %s", root)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The claimed root is TMPDIR for its stream, and the skill-cache trust
	// check refuses a temp root any other user could write to, so 0700 is part
	// of the contract rather than cosmetic.
	info, err := os.Stat(root)
	if err != nil {
		t.Fatalf("stat claimed root: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("claimed root mode = %o, want 700", got)
	}

	// Scratch a live run would own; contention must not touch it.
	scratch := filepath.Join(root, "in-flight")
	if err := os.WriteFile(scratch, []byte("live"), 0o600); err != nil {
		t.Fatalf("write %s: %v", scratch, err)
	}

	out, code := runShSource(t, ". "+gateRootsLib+"\nevener_claim_gate_root \"$1\"", nil, root)
	if code == 0 {
		t.Fatalf("claiming a root a live run holds succeeded; it must report contention instead:\n%s", out)
	}
	if _, err := os.Stat(scratch); err != nil {
		t.Fatalf("the contended claim emptied a live run's root (%s is gone): %v", scratch, err)
	}

	// Reap the holder so its pid answers kill -0 no longer, then the next claim
	// must reclaim, not refuse forever.
	if err := holder.Process.Kill(); err != nil {
		t.Fatalf("kill holder: %v", err)
	}
	_ = holder.Wait()
	out, code = runShSource(t, ". "+gateRootsLib+"\nevener_claim_gate_root \"$1\"", nil, root)
	if code != 0 {
		t.Fatalf("a root whose holder is gone must be reclaimed, got exit %d:\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(root, "in-flight")); err == nil {
		t.Errorf("the reclaimed root was not emptied; a run must start pristine")
	}
	if _, err := os.Stat(root); err != nil {
		t.Errorf("the reclaimed root does not exist: %v", err)
	}
}

// TestReleaseGateRootRemovesOnGreenAndRetainsOnRed pins the two cleanup shapes:
// a green run leaves nothing under the caller's TMPDIR, while a red run moves
// the scratch into the retained log directory beside its logs.
func TestReleaseGateRootRemovesOnGreenAndRetainsOnRed(t *testing.T) {
	t.Parallel()
	tmpHome := t.TempDir()
	root := gateRootUnder(tmpHome, "root")

	claimGateRoot(t, root)
	releaseGateRoot(t, root, "")
	if _, err := os.Stat(root); err == nil {
		t.Errorf("a green release left the root behind")
	}
	if _, err := os.Stat(root + ".lock"); err == nil {
		t.Errorf("a green release left the lock behind; the next run would read it as a live holder")
	}
	if _, err := os.Stat(filepath.Dir(root)); err == nil {
		t.Errorf("a green release left the now-empty base directory behind; nothing should be " +
			"left under the caller's TMPDIR to find")
	}

	keep := filepath.Join(tmpHome, "retained-logs")
	claimGateRoot(t, root)
	releaseGateRoot(t, root, keep)
	if _, err := os.Stat(filepath.Join(keep, "root", "scratch")); err != nil {
		t.Errorf("a red release did not retain the run's scratch under the keep directory: %v", err)
	}
	if _, err := os.Stat(root + ".lock"); err == nil {
		t.Errorf("a red release left the lock behind")
	}
}

// TestResetGateRootRefusesUnsafePaths pins the guard that makes this file's one
// recursive delete reviewable: it may only ever remove a path it is allowed to
// own.
func TestResetGateRootRefusesUnsafePaths(t *testing.T) {
	t.Parallel()
	tmpHome := t.TempDir()
	// A real directory that must survive every refusal. It is deliberately not
	// a refusal target built from t.TempDir(): when this suite runs under the
	// gate, TMPDIR is itself a gate-roots directory, so every t.TempDir() path
	// carries the marker the guard admits, and such a path would be a legal
	// root rather than a refused one.
	survivor := filepath.Join(tmpHome, "survivor")
	if err := os.MkdirAll(survivor, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", survivor, err)
	}

	// The parent-naming case is built by concatenation, not filepath.Join: Join
	// cleans ".." away before the library ever sees it, so the case would not
	// test the guard at all.
	parentNaming := gateRootUnder(tmpHome, "worktree") + "/../escape"
	for _, bad := range []string{
		"",
		"/",
		"relative/path",
		"/definitely-not-a-gate-root/child",
		parentNaming,
	} {
		if out, code := runShSource(t, gateRootsReset, nil, bad); code == 0 {
			t.Errorf("evener_reset_gate_root %q succeeded; it must refuse:\n%s", bad, out)
		}
	}
	if _, err := os.Stat(survivor); err != nil {
		t.Errorf("a refused reset deleted %s anyway: %v", survivor, err)
	}
}

// TestRunModuleTestsRetainsDurableRootsOnFailure drives the real runner and
// pins the wiring the unit tests above cannot see: that a stream actually runs
// under the durable root, and that a red run moves that root into the log
// directory it prints, so the scratch a failure left behind survives beside its
// logs. The module is deliberately absent, so the stream fails at `cd` and no
// test binary is ever built.
func TestRunModuleTestsRetainsDurableRootsOnFailure(t *testing.T) {
	if _, err := os.Stat("scripts/gate/run-module-tests.sh"); err != nil {
		t.Fatalf("stat runner: %v", err)
	}
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "tmp"), 0o755); err != nil {
		t.Fatalf("mkdir tmp: %v", err)
	}

	cmd := exec.Command("scripts/gate/run-module-tests.sh", "-short")
	cmd.Env = envOverride([]string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C"},
		"HOME="+filepath.Join(tmp, "home"),
		"TMPDIR="+filepath.Join(tmp, "tmp"),
		// The Go caches stay ambient so this does not recompile the world.
		"GOCACHE="+goEnv(t, "GOCACHE"),
		"GOPATH="+goEnv(t, "GOPATH"),
		"MODULES=this-module-does-not-exist",
		"WEB=0")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("the runner reported success for a module that does not exist:\n%s", out)
	}

	logdir := ""
	for _, line := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "full logs: "); ok {
			logdir = strings.TrimSpace(rest)
		}
	}
	if logdir == "" {
		t.Fatalf("the runner did not print the retained log directory:\n%s", out)
	}
	retained := filepath.Join(logdir, "roots", "this-module-does-not-exist")
	if _, err := os.Stat(retained); err != nil {
		t.Errorf("a red run did not retain the stream's durable root under the log directory "+
			"(%s): %v\nrunner output:\n%s", retained, err, out)
	}
}
