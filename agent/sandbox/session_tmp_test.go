//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

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

// hostTempForTest points the world temp bases at a base this test owns, made
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
	t.Cleanup(SetWorldTempBasesForTesting([]string{base}))
	return base
}

// hostHasWorldUsableTempBase reports whether this machine actually offers a base a
// container could live in. The live arbitrary-uid test uses the REAL bases on
// purpose — an injected fixture base sits under t.TempDir(), which an arbitrary uid
// cannot even traverse — so it has to skip rather than fail where none serves.
func hostHasWorldUsableTempBase() bool {
	candidates, err := worldTempBaseCandidates()
	if err != nil {
		return false
	}
	for _, candidate := range candidates {
		if _, ok := validWorldTempBase(candidate); ok {
			return true
		}
	}
	return false
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
// base (0700), a base that is world-writable but NOT world-traversable (1776: an
// arbitrary uid can reach nothing inside it), a missing base and an empty base set
// are all refused, and no directory is left behind by the refusal.
func TestSessionTmpRequiresWorldUsableBase(t *testing.T) {
	private := t.TempDir()
	noTraverse := filepath.Join(t.TempDir(), "no-traverse")
	if err := os.Mkdir(noTraverse, 0o700); err != nil {
		t.Fatal(err)
	}
	// 1776: world-writable and sticky, but no world execute, so an arbitrary uid
	// cannot even chdir into it — a writable, unreachable base cannot serve.
	if err := os.Chmod(noTraverse, 0o776|os.ModeSticky); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name  string
		bases []string
	}{
		{"private base", []string{private}},
		{"non-traversable base", []string{noTraverse}},
		{"missing and private bases", []string{"", filepath.Join(private, "missing"), private}},
		{"no bases", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(SetWorldTempBasesForTesting(tc.bases))
			if _, err := NewSessionTmp(); err == nil {
				t.Fatalf("NewSessionTmp accepted %v", tc.bases)
			}
			// Every candidate base, not just the first: a refusal must leave nothing
			// behind in ANY base it tried.
			for _, candidate := range tc.bases {
				entries, err := os.ReadDir(candidate)
				if err != nil {
					continue // not a directory; nothing can have leaked into it
				}
				if len(entries) != 0 {
					t.Fatalf("a refused container leaked into the rejected base %q: %v", candidate, entries)
				}
			}
		})
	}
}

// TestSessionTmpFallsBackToALaterBase: a candidate that cannot serve must not end
// provisioning while a later candidate can. The first entries here are rejected by
// the mode check (a plain file, a private dir); the last is a real world-usable
// base, and the container must come from it.
//
// The valid-but-refusing sub-case (a base whose modes pass and whose MkdirTemp
// still fails) is not constructible portably — a 1777/sticky base grants exactly
// the creation it advertises — so what the loop's error-join covers there is
// exercised by the all-candidates-fail case above.
func TestSessionTmpFallsBackToALaterBase(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(notADir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	healthy := filepath.Join(t.TempDir(), "healthy")
	if err := os.Mkdir(healthy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(healthy, sessionTmpLeafMode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(SetWorldTempBasesForTesting([]string{notADir, t.TempDir(), healthy}))

	tmp, err := NewSessionTmp()
	if err != nil {
		t.Fatalf("NewSessionTmp must fall back to a later usable base: %v", err)
	}
	t.Cleanup(func() { _ = tmp.Remove() })
	canonical, err := filepath.EvalSymlinks(healthy)
	if err != nil {
		t.Fatal(err)
	}
	if container := filepath.Dir(tmp.Dir); filepath.Dir(container) != canonical {
		t.Fatalf("container %q did not come from the last usable base %q", container, canonical)
	}
}

// TestSessionTmpPrivilegeDropE2E is the #495 field vector, end to end: a child
// that is another uid creates its temp directory inside the container leaf.
//
// It is an explicit opt-in (EVENER_TMPDIR_PRIVDROP_E2E=1), per
// docs/developing-evener/testing.md: it needs a real world-usable host temp, the
// external `mktemp`, a `sudo` that may drop to `nobody`, and the real container
// bases — none of which a default test may depend on. The permission bits that
// make a foreign uid's write possible are asserted deterministically and without
// any of that by TestSessionTmpContainerShape, and the container/leaf modes by
// agent/execenv's TestCommandEnvironment_UnsandboxedSessionExportsScratchVars.
//
// Cleanup of what the other uid creates stays best-effort, and this test does not
// assume otherwise: it attempts Remove and force-removes through the same
// privilege-drop tool when that fails. The unremovable case — a NON-EMPTY nested
// 0700 subtree, which the owner cannot descend into — is covered deterministically
// by TestSessionTmpSweepReportsUnremovableContainer. (An EMPTY nested directory is
// removable: the sticky bit on the leaf lets the leaf's owner unlink any entry,
// empty or not, that it is not the owner of.)
//
// Run it with:
//
//	EVENER_TMPDIR_PRIVDROP_E2E=1 go test ./agent/sandbox -run TestSessionTmpPrivilegeDropE2E -count=1 -v
func TestSessionTmpPrivilegeDropE2E(t *testing.T) {
	if os.Getenv("EVENER_TMPDIR_PRIVDROP_E2E") != "1" {
		t.Skip("set EVENER_TMPDIR_PRIVDROP_E2E=1 to run the privilege-drop end-to-end check")
	}
	// The sweep this test runs walks the scratch allocation bases as well as the
	// world temp bases, so both are confined: the scratch bases to a path that does
	// not exist, the world base to the fixture below. Without this the run would
	// read and delete the machine's real temp/cache.
	isolateScratchBases(t)
	if !hostHasWorldUsableTempBase() {
		t.Skipf("this host offers no world-usable host temp base (%v)", defaultWorldTempBases)
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

	// The reclaim must not touch a directory under our prefix that is not ours: it
	// walks world-writable temp bases, where any local user can plant that name, and
	// acquiring our lease inside someone else's directory and removing it
	// recursively would destroy files we do not own.
	//
	// The sweep's base set is confined to this fixture — the scratch bases are
	// isolated above and the world base is this one — so the run cannot pick up
	// unrelated residue or touch the machine's real temp: this check is about
	// ownership, not about the host. The fixture root lives directly under /tmp
	// because an arbitrary uid has to be able to reach it, and it is made
	// traversable (0711) with a world-writable child (1777) so another uid can plant
	// a directory in it, exactly as it could in /tmp.
	// Directly under the world-usable /tmp rather than t.TempDir(): the test
	// process's own temp path sits under a 0700 directory, which another uid cannot
	// even traverse, so a fixture there would be unreachable for the very reason
	// R12 names rather than for the ownership rule under test.
	fixtureRoot, err := os.MkdirTemp("/tmp", sessionScratchPrefix+"ownertest-")
	if err != nil {
		t.Fatalf("create fixture root: %v", err)
	}
	if err := os.Chmod(fixtureRoot, 0o711); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exec.Command("sudo", "-n", "rm", "-rf", fixtureRoot).Run() })
	base := filepath.Join(fixtureRoot, "host")
	if err := os.Mkdir(base, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, sessionTmpLeafMode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exec.Command("sudo", "-n", "rm", "-rf", base).Run() })

	foreign := filepath.Join(base, sessionScratchPrefix+"foreign")
	runAsUID(t, uid, base, "mkdir", foreign)
	// World-writable + sticky, like a directory an attacker would plant: without the
	// ownership guard the sweep would acquire its lease inside it, so the guard is
	// the only thing between the reclaim and someone else's files.
	runAsUID(t, uid, base, "chmod", "1777", foreign)
	runAsUID(t, uid, base, "sh", "-c", "echo keep > "+foreign+"/payload")
	// Only the owner (or root) may set a directory's times, so age it through the
	// privilege-drop tool: the sweep decides by mtime, and this fixture has to look
	// stale for the ownership guard to be what saves it.
	if out, err := exec.Command("sudo", "-n", "touch", "-d", "2 days ago", foreign).CombinedOutput(); err != nil {
		t.Fatalf("age foreign fixture: %v\n%s", err, out)
	}

	t.Cleanup(SetWorldTempBasesForTesting([]string{base}))
	if err := SweepCrashedSessionScratch(t.TempDir()); err != nil {
		t.Fatalf("sweep over a foreign-owned candidate: %v", err)
	}
	if _, err := os.Stat(filepath.Join(foreign, "payload")); err != nil {
		t.Fatalf("the reclaim removed a directory this process does not own: %v", err)
	}
	// The direct observable: the guard skips the candidate BEFORE acquiring a lease,
	// so the sweep must not have created its lock file inside someone else's
	// directory at all.
	if _, err := os.Stat(filepath.Join(foreign, sessionScratchLeaseName)); !os.IsNotExist(err) {
		t.Fatalf("the reclaim created a lease inside a directory this process does not own: %v", err)
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

// TestSessionTmpContainerReapedWhenWorkspaceContainsItsBase: a container is
// minted in a world temp base regardless of where the workspace is (NewSessionTmp
// takes no workspace), so the reclaim has to visit that base even when the
// scratch allocator's workspace filter would refuse it — a workspace that
// CONTAINS the base, or is the base. Without that, such a session's containers
// are never reclaimed and accumulate as world-traversable directories.
func TestSessionTmpContainerReapedWhenWorkspaceContainsItsBase(t *testing.T) {
	base := hostTempForTest(t)
	isolateScratchBases(t)
	// A workspace ABOVE the base: the base is inside it, so scratch allocation
	// refuses the base and only the container rule can put it back in the sweep.
	workspace := filepath.Dir(base)

	stale, err := NewSessionTmp()
	if err != nil {
		t.Fatalf("NewSessionTmp: %v", err)
	}
	container := filepath.Dir(stale.Dir)
	if err := stale.Retain(); err != nil {
		t.Fatalf("Retain: %v", err)
	}
	aged := time.Now().Add(-2 * crashedSessionScratchMaxAge)
	if err := os.Chtimes(container, aged, aged); err != nil {
		t.Fatal(err)
	}

	if err := SweepCrashedSessionScratch(workspace); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if _, err := os.Stat(container); !os.IsNotExist(err) {
		t.Fatalf("a container under a base the workspace contains must still be reclaimed: %v", err)
	}
}

// TestSessionTmpSweepReportsUnremovableContainer: a container the owner cannot
// unlink — the same failure a foreign uid's nested 0700 subtree produces — is
// reported and left in place, and the sweep still reclaims the rest rather than
// aborting the base.
func TestSessionTmpSweepReportsUnremovableContainer(t *testing.T) {
	if os.Geteuid() == 0 {
		// Root bypasses a 0500 container with CAP_DAC_OVERRIDE, so the removal this
		// test needs to fail would succeed and neither assertion would mean what it
		// says. Same guard as the sibling permission-denial tests in
		// scratch_retention_test.go.
		t.Skip("an unremovable container cannot be created as root")
	}
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
// trimmed stdout (empty for a command that prints nothing, e.g. mkdir). It fails
// the test only when the command cannot run at all; callers that need a value
// assert it themselves.
func runAsUID(t *testing.T, uid, dir string, argv ...string) string {
	t.Helper()
	args := append([]string{"-n", "-u", uid, "env", "TMPDIR=" + dir}, argv...)
	out, err := exec.Command("sudo", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("sudo %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
