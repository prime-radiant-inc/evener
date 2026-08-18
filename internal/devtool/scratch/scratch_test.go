package scratch

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixtureTMPDIR points TMPDIR at a fresh directory and returns its
// symlink-resolved form (macOS hands out /var/folders paths that resolve to
// /private/var, and the package canonicalizes).
func fixtureTMPDIR(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolving fixture TMPDIR: %v", err)
	}
	return resolved
}

// deadPid returns a pid that is guaranteed not to be running: a child we
// spawned, waited for, and therefore reaped ourselves.
func deadPid(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("spawning short-lived child: %v", err)
	}
	return cmd.Process.Pid
}

// livePid returns the pid of a child that stays alive until test cleanup.
func livePid(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "300")
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawning long-lived child: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return cmd.Process.Pid
}

func seedDir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("seeding %s: %v", path, err)
	}
	if err := os.WriteFile(filepath.Join(path, "marker"), []byte("decoy\n"), 0o644); err != nil {
		t.Fatalf("seeding marker in %s: %v", path, err)
	}
}

func TestAcquireMintsPrefixPidUnderTMPDIR(t *testing.T) {
	tmp := fixtureTMPDIR(t)
	d, err := Acquire("demo", nil)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer d.Release()
	want := filepath.Join(tmp, fmt.Sprintf("demo.%d", os.Getpid()))
	if d.Path() != want {
		t.Fatalf("Path() = %q, want %q", d.Path(), want)
	}
	info, err := os.Stat(d.Path())
	if err != nil || !info.IsDir() {
		t.Fatalf("acquired path is not a directory: %v", err)
	}
}

func TestAcquireCollisionIsLoud(t *testing.T) {
	tmp := fixtureTMPDIR(t)
	stale := filepath.Join(tmp, fmt.Sprintf("demo.%d", os.Getpid()))
	seedDir(t, stale)
	d, err := Acquire("demo", nil)
	if err == nil {
		d.Release()
		t.Fatalf("Acquire over an existing same-pid directory succeeded; want loud failure")
	}
	if !strings.Contains(err.Error(), stale) {
		t.Fatalf("collision error %q does not name the path %q", err, stale)
	}
	if _, statErr := os.Stat(filepath.Join(stale, "marker")); statErr != nil {
		t.Fatalf("collision handling touched the existing directory: %v", statErr)
	}
}

func TestAcquireRefusesBadTMPDIR(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	for name, tmpdir := range map[string]string{
		"root":    "/",
		"home":    home,
		"missing": "/nonexistent-scratch-tmpdir-for-test",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("TMPDIR", tmpdir)
			d, err := Acquire("demo", nil)
			if err == nil {
				d.Release()
				t.Fatalf("Acquire with TMPDIR=%s succeeded; want refusal", tmpdir)
			}
		})
	}
}

func TestAcquireRefusesPathySuffix(t *testing.T) {
	fixtureTMPDIR(t)
	for _, prefix := range []string{"", "a/b", "../escape"} {
		if d, err := Acquire(prefix, nil); err == nil {
			d.Release()
			t.Fatalf("Acquire(%q) succeeded; want refusal", prefix)
		}
	}
}

func TestReleaseRemovesOnlyItsDir(t *testing.T) {
	tmp := fixtureTMPDIR(t)
	decoy := filepath.Join(tmp, "demo.decoy-sibling")
	seedDir(t, decoy)
	d, err := Acquire("demo", nil)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := os.WriteFile(filepath.Join(d.Path(), "log"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("writing into scratch: %v", err)
	}
	d.Release()
	if _, err := os.Stat(d.Path()); !os.IsNotExist(err) {
		t.Fatalf("Release left the scratch directory behind: %v", err)
	}
	if _, err := os.Stat(filepath.Join(decoy, "marker")); err != nil {
		t.Fatalf("Release reached a sibling it did not mint: %v", err)
	}
}

func TestReleaseIsIdempotent(t *testing.T) {
	fixtureTMPDIR(t)
	var warn bytes.Buffer
	d, err := Acquire("demo", &warn)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	d.Release()
	d.Release()
	if warn.Len() != 0 {
		t.Fatalf("double Release wrote warnings: %q", warn.String())
	}
}

func TestKeepOnFailureRetainsAndPoints(t *testing.T) {
	fixtureTMPDIR(t)
	var warn bytes.Buffer
	d, err := Acquire("demo", &warn)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	d.KeepOnFailure()
	d.Release()
	if _, err := os.Stat(d.Path()); err != nil {
		t.Fatalf("KeepOnFailure did not retain the directory: %v", err)
	}
	want := fmt.Sprintf("full logs: %s\n", d.Path())
	if warn.String() != want {
		t.Fatalf("retained-logs pointer = %q, want %q", warn.String(), want)
	}
	// Clean up by hand; retention was the point.
	_ = os.RemoveAll(d.Path())
}

func TestReclaimOwnRemovesDeadPidDirs(t *testing.T) {
	tmp := fixtureTMPDIR(t)
	dead := filepath.Join(tmp, fmt.Sprintf("demo.%d", deadPid(t)))
	seedDir(t, dead)
	var warn bytes.Buffer
	ReclaimOwn("demo", &warn)
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Fatalf("dead-pid leftover survived reclaim: %v", err)
	}
	if warn.Len() != 0 {
		t.Fatalf("clean reclaim wrote warnings: %q", warn.String())
	}
}

func TestReclaimOwnKeepsLiveAndOwnPids(t *testing.T) {
	tmp := fixtureTMPDIR(t)
	live := filepath.Join(tmp, fmt.Sprintf("demo.%d", livePid(t)))
	own := filepath.Join(tmp, fmt.Sprintf("demo.%d", os.Getpid()))
	seedDir(t, live)
	seedDir(t, own)
	ReclaimOwn("demo", nil)
	for _, kept := range []string{live, own} {
		if _, err := os.Stat(filepath.Join(kept, "marker")); err != nil {
			t.Fatalf("reclaim removed a live run's scratch %s: %v", kept, err)
		}
	}
}

func TestReclaimOwnTouchesOnlyExactPrefixDeadPidDirs(t *testing.T) {
	tmp := fixtureTMPDIR(t)
	dead := deadPid(t)

	// Decoys that must all survive: wrong prefix, prefix without the dot
	// boundary, non-numeric suffix, a plain file, a nested (non-direct)
	// child, and a symlink whose target must also stay.
	keepDirs := []string{
		filepath.Join(tmp, fmt.Sprintf("other.%d", dead)),
		filepath.Join(tmp, fmt.Sprintf("demoX.%d", dead)),
		filepath.Join(tmp, "demo.notapid"),
		filepath.Join(tmp, "sub", fmt.Sprintf("demo.%d", dead)),
	}
	for _, dir := range keepDirs {
		seedDir(t, dir)
	}
	plainFile := filepath.Join(tmp, fmt.Sprintf("demo.%d", dead)+"file")
	if err := os.WriteFile(plainFile, []byte("x\n"), 0o644); err != nil {
		t.Fatalf("seeding plain file: %v", err)
	}
	target := filepath.Join(tmp, "sub", "target")
	seedDir(t, target)
	link := filepath.Join(tmp, fmt.Sprintf("demo.%d", deadPid(t)))
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("seeding symlink: %v", err)
	}

	ReclaimOwn("demo", nil)

	for _, dir := range keepDirs {
		if _, err := os.Stat(filepath.Join(dir, "marker")); err != nil {
			t.Fatalf("reclaim removed decoy %s: %v", dir, err)
		}
	}
	if _, err := os.Stat(plainFile); err != nil {
		t.Fatalf("reclaim removed a plain file: %v", err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("reclaim removed a symlink: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "marker")); err != nil {
		t.Fatalf("reclaim followed a symlink out to its target: %v", err)
	}
}

func TestReclaimOwnReportsFailedRemovalAndContinues(t *testing.T) {
	tmp := fixtureTMPDIR(t)
	blockedParent := filepath.Join(tmp, "sub")
	// A dead-pid leftover the reclaimer cannot remove: RemoveAll of a direct
	// child needs write permission on TMPDIR itself, so make TMPDIR
	// read-only after seeding. Both entries qualify; neither removal can
	// succeed; both must be reported and neither may abort the sweep.
	first := filepath.Join(tmp, fmt.Sprintf("demo.%d", deadPid(t)))
	second := filepath.Join(tmp, fmt.Sprintf("demo.%d", deadPid(t)))
	seedDir(t, first)
	seedDir(t, second)
	_ = blockedParent
	if err := os.Chmod(tmp, 0o555); err != nil {
		t.Fatalf("locking fixture TMPDIR: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(tmp, 0o755) })

	var warn bytes.Buffer
	ReclaimOwn("demo", &warn)

	got := warn.String()
	for _, leftover := range []string{first, second} {
		if !strings.Contains(got, "could not reclaim abandoned scratch "+leftover) {
			t.Fatalf("failed removal of %s not reported; warnings: %q", leftover, got)
		}
	}
}

func TestAcquireReclaimsBeforeMinting(t *testing.T) {
	tmp := fixtureTMPDIR(t)
	stale := filepath.Join(tmp, fmt.Sprintf("demo.%d", deadPid(t)))
	seedDir(t, stale)
	d, err := Acquire("demo", nil)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer d.Release()
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("Acquire did not reclaim the dead-pid leftover first: %v", err)
	}
}

// TestScratchSigkillHelper is not a test: it is the child half of
// TestReclaimAfterSIGKILL, re-executed from this test binary. It acquires
// scratch and then blocks until it is killed.
func TestScratchSigkillHelper(t *testing.T) {
	ready := os.Getenv("SCRATCH_SIGKILL_HELPER_READY")
	if ready == "" {
		t.Skip("helper process entry point; runs only under TestReclaimAfterSIGKILL")
	}
	d, err := Acquire("demo", nil)
	if err != nil {
		t.Fatalf("helper Acquire: %v", err)
	}
	if err := os.WriteFile(ready, []byte(d.Path()+"\n"), 0o644); err != nil {
		t.Fatalf("helper writing ready file: %v", err)
	}
	// Never Released on this path: the parent SIGKILLs us mid-hold. The
	// ceiling is a tripwire, not a mechanism (flakes policy) — the parent
	// kills long before it.
	time.Sleep(5 * time.Minute)
}

func TestReclaimAfterSIGKILL(t *testing.T) {
	tmp := fixtureTMPDIR(t)
	ready := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command(os.Args[0], "-test.run=TestScratchSigkillHelper$", "-test.v")
	cmd.Env = append(os.Environ(),
		"TMPDIR="+tmp,
		"SCRATCH_SIGKILL_HELPER_READY="+ready,
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting helper: %v", err)
	}
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()

	var holderPath string
	deadline := time.Now().Add(60 * time.Second)
	for {
		if raw, err := os.ReadFile(ready); err == nil && len(raw) > 0 {
			holderPath = strings.TrimSpace(string(raw))
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("helper never acquired scratch")
		}
		time.Sleep(20 * time.Millisecond)
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("SIGKILLing helper: %v", err)
	}
	if _, err := cmd.Process.Wait(); err != nil {
		t.Fatalf("reaping helper: %v", err)
	}

	if _, err := os.Stat(holderPath); err != nil {
		t.Fatalf("SIGKILL should leave the scratch behind; stat: %v", err)
	}
	wantName := fmt.Sprintf("demo.%d", cmd.Process.Pid)
	if filepath.Base(holderPath) != wantName {
		t.Fatalf("helper scratch named %q, want %q", filepath.Base(holderPath), wantName)
	}

	ReclaimOwn("demo", nil)
	if _, err := os.Stat(holderPath); !os.IsNotExist(err) {
		t.Fatalf("next run's reclaim did not remove the SIGKILL leftover: %v", err)
	}
}

// ReclaimOwn must refuse the prefixes Acquire refuses: an empty prefix would
// otherwise match every hidden dot-directory under TMPDIR and remove any
// with a dead-pid suffix — another tool's ".cache.12345", say.
func TestReclaimOwnRefusesInvalidPrefixesAndTouchesNothing(t *testing.T) {
	for _, prefix := range []string{"", ".", "/", "a/b", "dot.ted"} {
		t.Run(fmt.Sprintf("prefix=%q", prefix), func(t *testing.T) {
			tmp := fixtureTMPDIR(t)
			dead := deadPid(t)
			decoys := []string{
				filepath.Join(tmp, fmt.Sprintf(".cache.%d", dead)),
				filepath.Join(tmp, fmt.Sprintf("..%d", dead)),
				filepath.Join(tmp, fmt.Sprintf("dot.ted.%d", dead)),
			}
			for _, decoy := range decoys {
				seedDir(t, decoy)
			}
			var warn bytes.Buffer
			ReclaimOwn(prefix, &warn)
			if !strings.Contains(warn.String(), fmt.Sprintf("invalid prefix %q", prefix)) {
				t.Fatalf("refusal not reported for prefix %q; warnings: %q", prefix, warn.String())
			}
			for _, decoy := range decoys {
				if _, err := os.Stat(filepath.Join(decoy, "marker")); err != nil {
					t.Fatalf("ReclaimOwn(%q) touched decoy %s: %v", prefix, decoy, err)
				}
			}
		})
	}
}

// Guard against pid parsing quirks: strconv.Atoi accepts "+1" and "-1",
// which "${leftover##*.}"-style suffixes never legitimately carry.
func TestReclaimOwnRejectsSignedPidSuffixes(t *testing.T) {
	tmp := fixtureTMPDIR(t)
	signed := filepath.Join(tmp, "demo.+1")
	seedDir(t, signed)
	ReclaimOwn("demo", nil)
	if _, err := os.Stat(filepath.Join(signed, "marker")); err != nil {
		t.Fatalf("reclaim removed a signed-suffix decoy: %v", err)
	}
}
