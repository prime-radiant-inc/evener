package sandbox

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// hostTempForTest points worldTempBases at a base this test owns, made
// world-usable (0777 + sticky) so the selection check accepts it, and returns
// that base.
func hostTempForTest(t *testing.T) string {
	t.Helper()
	base := filepath.Join(t.TempDir(), "host-temp")
	if err := os.Mkdir(base, sessionTmpLeafMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, sessionTmpLeafMode); err != nil {
		t.Fatal(err)
	}
	old := worldTempBases
	worldTempBases = []string{base}
	t.Cleanup(func() { worldTempBases = old })
	return base
}

// isolateScratchBases removes every scratch allocation base from the sweep's
// candidate set so a test's own host-temp base is the only one walked.
func isolateScratchBases(t *testing.T) {
	t.Helper()
	missing := filepath.Join(t.TempDir(), "missing-base")
	oldTemp, oldCache := sessionScratchTempDir, sessionScratchUserCacheDir
	sessionScratchTempDir = func() string { return missing }
	sessionScratchUserCacheDir = func() (string, error) { return "", errors.New("no cache dir") }
	t.Cleanup(func() {
		sessionScratchTempDir, sessionScratchUserCacheDir = oldTemp, oldCache
	})
}

// TestSessionTmpContainerShape pins the container's two-level contract: a
// prefix-named, owner-controlled container at 0711 (traversable, so an
// arbitrary uid can reach the leaf; not writable, so no other uid can create or
// hold the .evener-session.lock the reclaim depends on) and a 1777 sticky leaf
// inside it.
func TestSessionTmpContainerShape(t *testing.T) {
	base := hostTempForTest(t)
	tmp, err := NewSessionTmp()
	if err != nil {
		t.Fatalf("NewSessionTmp: %v", err)
	}
	t.Cleanup(func() { _ = tmp.Remove() })

	container := filepath.Dir(tmp.Dir)
	if filepath.Base(tmp.Dir) != sessionTmpLeafName {
		t.Errorf("leaf = %q, want the %q subdirectory of the container", tmp.Dir, sessionTmpLeafName)
	}
	if !strings.HasPrefix(filepath.Base(container), sessionScratchPrefix) {
		t.Errorf("container %q must carry the %q prefix, or the crashed-scratch sweep reaps it by nothing", container, sessionScratchPrefix)
	}
	canonicalBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(container) != canonicalBase {
		t.Errorf("container %q is not directly inside the chosen base %q", container, canonicalBase)
	}
	if got := fileMode(t, container); got != 0o711 {
		t.Errorf("container mode = %04o, want 0711 (reachable by others, not writable by them)", got)
	}
	leafInfo, err := os.Stat(tmp.Dir)
	if err != nil {
		t.Fatalf("stat leaf: %v", err)
	}
	if got := leafInfo.Mode().Perm(); got != 0o777 {
		t.Errorf("leaf mode = %04o, want 0777", got)
	}
	if leafInfo.Mode()&os.ModeSticky == 0 {
		t.Errorf("leaf mode = %v, want the sticky bit set so no uid may remove another's temp entry", leafInfo.Mode())
	}

	// The container holds a live lease, and releasing it makes the container
	// acquirable again — the same liveness discipline a session scratch has, which
	// is what makes the 24h reclaim safe.
	if !tmp.HasLease() {
		t.Fatal("a fresh container must hold its liveness lease")
	}
	if _, contended, _ := acquireScratchLease(filepath.Join(container, sessionScratchLeaseName)); !contended {
		t.Error("a fresh container's lease must be contended")
	}
	if err := tmp.Retain(); err != nil {
		t.Fatalf("Retain: %v", err)
	}
	if tmp.HasLease() {
		t.Error("Retain must release the lease")
	}
	lease, contended, err := acquireScratchLease(filepath.Join(container, sessionScratchLeaseName))
	if err != nil || contended {
		t.Fatalf("after Retain the lease must be acquirable: contended=%v err=%v", contended, err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("release probe lease: %v", err)
	}
	if _, err := os.Stat(container); err != nil {
		t.Fatalf("Retain must keep the directory: %v", err)
	}
}

// TestSessionTmpRemoveReclaimsContainer proves Remove takes the whole container
// (lease included) and refuses anything outside the session scratch namespace.
func TestSessionTmpRemoveReclaimsContainer(t *testing.T) {
	base := hostTempForTest(t)
	tmp, err := NewSessionTmp()
	if err != nil {
		t.Fatalf("NewSessionTmp: %v", err)
	}
	container := filepath.Dir(tmp.Dir)
	if err := tmp.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(container); !os.IsNotExist(err) {
		t.Fatalf("container remains after Remove: %v", err)
	}

	unrelated := filepath.Join(base, "ordinary-temp")
	if err := os.Mkdir(unrelated, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := &SessionTmp{Dir: filepath.Join(unrelated, sessionTmpLeafName), container: unrelated, base: base}
	if err := outside.Remove(); err == nil {
		t.Fatal("Remove accepted a container outside the session scratch namespace")
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("Remove touched an unrelated directory: %v", err)
	}
}

// TestSessionTmpRequiresWorldUsableBase: the container must never be created in
// a base an arbitrary uid cannot use — that is the whole point — so a private
// base (0700) and a missing base are both refused, and no directory is left
// behind by the refusal.
func TestSessionTmpRequiresWorldUsableBase(t *testing.T) {
	private := t.TempDir()
	old := worldTempBases
	t.Cleanup(func() { worldTempBases = old })

	worldTempBases = []string{private}
	if _, err := NewSessionTmp(); err == nil {
		t.Fatal("NewSessionTmp accepted a base that is neither world-writable nor sticky")
	}
	worldTempBases = []string{"", filepath.Join(private, "missing"), private}
	if _, err := NewSessionTmp(); err == nil {
		t.Fatal("NewSessionTmp accepted a base set with no usable base")
	}
	if entries, err := os.ReadDir(private); err != nil || len(entries) != 0 {
		t.Fatalf("a refused container leaked into the rejected base: %v %v", entries, err)
	}
}

// TestSessionTmpUsableByArbitraryUID is the #495 regression. The permission bits
// are asserted unconditionally — they are the entire mechanism the kernel uses
// for a foreign user — and the live `mktemp -d` runs as another uid whenever this
// host lets the test drop privileges.
//
// Cleanup of what the other uid creates stays best-effort, and this test does not
// assume otherwise: it attempts Remove and force-removes through the same
// privilege-drop tool when that fails. The unremovable case — a NON-EMPTY nested
// 0700 subtree, which the owner cannot descend into — is covered deterministically
// and without sudo by TestSessionTmpSweepReportsUnremovableContainer. (An EMPTY
// nested directory is removable: the sticky bit on the leaf lets the leaf's owner
// unlink any entry, empty or not, that it is not the owner of.)
func TestSessionTmpUsableByArbitraryUID(t *testing.T) {
	if _, err := worldUsableTempBase(); err != nil {
		t.Skipf("no world-usable host temp on this host: %v", err)
	}
	tmp, err := NewSessionTmp()
	if err != nil {
		t.Fatalf("NewSessionTmp: %v", err)
	}
	container := filepath.Dir(tmp.Dir)
	t.Cleanup(func() {
		if err := tmp.Remove(); err != nil {
			// Best-effort by construction: a foreign nested subtree is
			// unremovable. Leave no residue behind by force when we can.
			_ = exec.Command("sudo", "-n", "rm", "-rf", container).Run()
		}
	})

	if got := fileMode(t, container); got != 0o711 {
		t.Fatalf("container mode = %04o, want 0711 so another uid can traverse to the leaf", got)
	}
	leafInfo, err := os.Stat(tmp.Dir)
	if err != nil {
		t.Fatalf("stat leaf: %v", err)
	}
	if leafInfo.Mode().Perm() != 0o777 || leafInfo.Mode()&os.ModeSticky == 0 {
		t.Fatalf("leaf mode = %v, want 0777 + sticky", leafInfo.Mode())
	}

	uid := privilegeDropTarget(t)
	if uid == "" {
		t.Skip("no working privilege drop to another uid on this host; permission bits asserted above")
	}
	created := runAsUID(t, uid, tmp.Dir, "mktemp", "-d")
	if !strings.HasPrefix(created, tmp.Dir+string(filepath.Separator)) {
		t.Fatalf("arbitrary uid created %q, want a directory inside the container leaf %q", created, tmp.Dir)
	}
	info, err := os.Stat(created)
	if err != nil || !info.IsDir() {
		t.Fatalf("arbitrary uid's temp dir %q must exist: %v", created, err)
	}

	// A flat file the other uid made IS removable by the leaf's owner (that is
	// what the sticky bit guarantees).
	flat := runAsUID(t, uid, tmp.Dir, "mktemp")
	if err := os.Remove(flat); err != nil {
		t.Errorf("the container owner must be able to unlink a flat entry another uid created: %v", err)
	}
}

// TestSessionTmpContainerReapedByCrashedSweep: the container's base is walked by
// the crashed-scratch sweep, so the existing 24h reclaim covers it under the same
// lease discipline — a stale released container goes, a stale one with a live
// lease stays.
func TestSessionTmpContainerReapedByCrashedSweep(t *testing.T) {
	hostTempForTest(t)
	isolateScratchBases(t)
	workspace := t.TempDir()

	stale, err := NewSessionTmp()
	if err != nil {
		t.Fatalf("NewSessionTmp: %v", err)
	}
	staleContainer := filepath.Dir(stale.Dir)
	if err := stale.Retain(); err != nil {
		t.Fatalf("Retain: %v", err)
	}
	live, err := NewSessionTmp()
	if err != nil {
		t.Fatalf("NewSessionTmp: %v", err)
	}
	t.Cleanup(func() { _ = live.Remove() })
	liveContainer := filepath.Dir(live.Dir)

	aged := time.Now().Add(-2 * crashedSessionScratchMaxAge)
	for _, dir := range []string{staleContainer, liveContainer} {
		if err := os.Chtimes(dir, aged, aged); err != nil {
			t.Fatal(err)
		}
	}

	if err := SweepCrashedSessionScratch(workspace); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if _, err := os.Stat(staleContainer); !os.IsNotExist(err) {
		t.Errorf("stale released container was not reaped: %v", err)
	}
	if _, err := os.Stat(liveContainer); err != nil {
		t.Errorf("sweep removed a container holding a live lease: %v", err)
	}
}

// TestSessionTmpSweepReportsUnremovableContainer: a container the owner cannot
// unlink — the same failure a foreign uid's nested 0700 subtree produces — is
// reported and left in place, and the sweep still reclaims the rest rather than
// aborting the base.
func TestSessionTmpSweepReportsUnremovableContainer(t *testing.T) {
	hostTempForTest(t)
	isolateScratchBases(t)
	workspace := t.TempDir()

	stuck, err := NewSessionTmp()
	if err != nil {
		t.Fatalf("NewSessionTmp: %v", err)
	}
	stuckContainer := filepath.Dir(stuck.Dir)
	if err := stuck.Retain(); err != nil {
		t.Fatalf("Retain: %v", err)
	}
	// With the container itself non-writable, its leaf cannot be unlinked from
	// it, so removal must fail.
	if err := os.Chmod(stuckContainer, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(stuckContainer, 0o711)
		_ = os.RemoveAll(stuckContainer)
	})
	reapable, err := NewSessionTmp()
	if err != nil {
		t.Fatalf("NewSessionTmp: %v", err)
	}
	reapableContainer := filepath.Dir(reapable.Dir)
	if err := reapable.Retain(); err != nil {
		t.Fatalf("Retain: %v", err)
	}

	aged := time.Now().Add(-2 * crashedSessionScratchMaxAge)
	for _, dir := range []string{stuckContainer, reapableContainer} {
		if err := os.Chtimes(dir, aged, aged); err != nil {
			t.Fatal(err)
		}
	}

	sweepErr := SweepCrashedSessionScratch(workspace)
	if sweepErr == nil {
		t.Error("sweep reported success though it could not remove a container")
	}
	if _, err := os.Stat(stuckContainer); err != nil {
		t.Errorf("sweep must leave an unremovable container in place for the operator: %v", err)
	}
	if _, err := os.Stat(reapableContainer); !os.IsNotExist(err) {
		t.Errorf("one unremovable container must not abort the reclaim of the rest: %v", err)
	}
}

// privilegeDropTarget returns a username this host can become non-interactively,
// or "" when the test must not attempt a live drop.
func privilegeDropTarget(t *testing.T) string {
	t.Helper()
	if os.Getuid() == 0 {
		return ""
	}
	if _, err := exec.LookPath("sudo"); err != nil {
		return ""
	}
	if err := exec.Command("sudo", "-n", "-u", "nobody", "true").Run(); err != nil {
		return ""
	}
	return "nobody"
}

// runAsUID runs argv as username uid with TMPDIR set to dir, returning the
// trimmed stdout. It fails the test when the command cannot run at all.
func runAsUID(t *testing.T, uid, dir string, argv ...string) string {
	t.Helper()
	args := append([]string{"-n", "-u", uid, "env", "TMPDIR=" + dir}, argv...)
	out, err := exec.Command("sudo", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("sudo %v: %v\n%s", args, err, out)
	}
	got := strings.TrimSpace(string(out))
	if got == "" {
		t.Fatalf("sudo %v produced no output", args)
	}
	return got
}
