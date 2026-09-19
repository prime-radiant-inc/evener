package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestSessionScratchBasePrefersATraversableBase pins the allocation half of issue
// #495: the scratch's own modes cannot make $TMPDIR reachable if an ANCESTOR of the
// base is private, because a path lookup needs other-user execute on every component
// down to the scratch. A defaulted allocation must therefore prefer a base whose
// whole chain is traversable, instead of taking the first base that merely exists.
func TestSessionScratchBasePrefersATraversableBase(t *testing.T) {
	root := t.TempDir()
	privateParent := filepath.Join(root, "private-ancestor")
	if err := os.Mkdir(privateParent, 0o700); err != nil {
		t.Fatal(err)
	}
	privateBase := filepath.Join(privateParent, "base")
	if err := os.Mkdir(privateBase, 0o700); err != nil {
		t.Fatal(err)
	}
	// The traversable candidate must live under a genuinely other-traversable chain
	// (t.TempDir() itself is 0700, so its children can never qualify).
	publicBase, err := os.MkdirTemp("/tmp", "evener-scratch-public-")
	if err != nil {
		t.Skipf("no writable system temp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(publicBase) })
	if err := os.Chmod(publicBase, 0o755); err != nil {
		t.Fatal(err)
	}
	if !scratchBaseReachable(publicBase) {
		t.Skipf("host has no other-traversable temp base: %q", publicBase)
	}
	// The allocator canonicalizes a base (EvalSymlinks), and on hosts where /tmp is a
	// symlink — macOS /tmp -> private/tmp — the scratch path is the resolved spelling.
	// Compare against the canonical form so the assertion holds there too.
	canonicalPublic, err := filepath.EvalSymlinks(publicBase)
	if err != nil {
		t.Fatalf("canonicalize the fixture base: %v", err)
	}
	if scratchBaseReachable(privateBase) {
		t.Fatalf("fixture base %q must not be traversable through its private ancestor", privateBase)
	}

	oldTemp, oldCache := sessionScratchTempDir, sessionScratchUserCacheDir
	sessionScratchTempDir = func() string { return privateBase }
	sessionScratchUserCacheDir = func() (string, error) { return publicBase, nil }
	t.Cleanup(func() { sessionScratchTempDir, sessionScratchUserCacheDir = oldTemp, oldCache })

	scratch, err := NewSessionScratch("", t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionScratch: %v", err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })

	if !strings.HasPrefix(scratch.Dir, canonicalPublic) {
		t.Fatalf("scratch %q must be allocated under the traversable base %q, not the private ancestor chain", scratch.Dir, canonicalPublic)
	}
	// The kernel check a dropped UID performs: other-user execute on every component.
	for dir := scratch.Dir; ; {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o001 == 0 {
			t.Fatalf("ancestor %q of the scratch grants no other-user traverse, so $TMPDIR is unreachable to a dropped UID", dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
}

// TestSessionScratchBaseRejectsWorldWritableNonStickyBase: a world-writable base
// without the sticky bit lets any local user delete or replace this session's
// container — and with it the sandbox's own grant target — so it must not be chosen
// while a protected candidate exists.
func TestSessionScratchBaseRejectsWorldWritableNonStickyBase(t *testing.T) {
	unprotected, err := os.MkdirTemp("/tmp", "evener-scratch-unprotected-")
	if err != nil {
		t.Skipf("no writable system temp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(unprotected) })
	if err := os.Chmod(unprotected, 0o777); err != nil {
		t.Fatal(err)
	}
	protected, err := os.MkdirTemp("/tmp", "evener-scratch-protected-")
	if err != nil {
		t.Skipf("no writable system temp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(protected) })
	if err := os.Chmod(protected, os.ModeSticky|0o777); err != nil {
		t.Fatal(err)
	}
	if scratchBaseUnteplaceable(unprotected) {
		t.Fatalf("a world-writable, non-sticky base %q must not count as shared", unprotected)
	}
	if !scratchBaseUnteplaceable(protected) {
		t.Skipf("host has no sticky world-writable base fixture: %q", protected)
	}
	canonicalProtected, err := filepath.EvalSymlinks(protected)
	if err != nil {
		t.Fatalf("canonicalize the fixture base: %v", err)
	}

	oldTemp, oldCache := sessionScratchTempDir, sessionScratchUserCacheDir
	sessionScratchTempDir = func() string { return unprotected }
	sessionScratchUserCacheDir = func() (string, error) { return protected, nil }
	t.Cleanup(func() { sessionScratchTempDir, sessionScratchUserCacheDir = oldTemp, oldCache })

	scratch, err := NewSessionScratch("", t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionScratch: %v", err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	if !strings.HasPrefix(scratch.Dir, canonicalProtected) {
		t.Fatalf("scratch %q must avoid the world-writable non-sticky base and use %q", scratch.Dir, canonicalProtected)
	}
}

// TestSessionScratchBaseRejectsUntrustedAncestor: a base that is itself
// traversable and not writable still sits under a REPLACEABLE ancestor if any
// component of its path is group- or other-writable without the sticky bit — another
// user could then replace the base's name, and with it this session's container.
// Integrity is checked along the whole chain, not only on the base.
func TestSessionScratchBaseRejectsUntrustedAncestor(t *testing.T) {
	untrustedParent, err := os.MkdirTemp("/tmp", "evener-untrusted-ancestor-")
	if err != nil {
		t.Skipf("no writable system temp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(untrustedParent) })
	if err := os.Chmod(untrustedParent, 0o777); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(untrustedParent, "base")
	if err := os.Mkdir(base, 0o711); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0o711); err != nil {
		t.Fatal(err)
	}
	if !scratchBaseReachable(base) {
		t.Skipf("fixture base %q is not reachable on this host", base)
	}
	if scratchBaseUnteplaceable(base) {
		t.Fatalf("a base under a world-writable non-sticky ancestor %q must not count as safe", untrustedParent)
	}

	protected, err := os.MkdirTemp("/tmp", "evener-protected-")
	if err != nil {
		t.Skipf("no writable system temp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(protected) })
	if err := os.Chmod(protected, os.ModeSticky|0o777); err != nil {
		t.Fatal(err)
	}
	canonicalProtected, err := filepath.EvalSymlinks(protected)
	if err != nil {
		t.Fatal(err)
	}

	oldTemp, oldCache := sessionScratchTempDir, sessionScratchUserCacheDir
	sessionScratchTempDir = func() string { return base }
	sessionScratchUserCacheDir = func() (string, error) { return protected, nil }
	t.Cleanup(func() { sessionScratchTempDir, sessionScratchUserCacheDir = oldTemp, oldCache })

	scratch, err := NewSessionScratch("", t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionScratch: %v", err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	if !strings.HasPrefix(scratch.Dir, canonicalProtected) {
		t.Fatalf("scratch %q must avoid the replaceable ancestor chain and use %q", scratch.Dir, canonicalProtected)
	}
}

// TestSessionScratchBaseRefusesWhenEveryCandidateIsReplaceable: integrity is the one
// property the allocator never concedes. When the only bases on offer are
// world-writable without the sticky bit, any local user could replace this session's
// container (and with it the sandbox's own grant target), so allocation fails loudly
// instead of returning one of them.
func TestSessionScratchBaseRefusesWhenEveryCandidateIsReplaceable(t *testing.T) {
	first, err := os.MkdirTemp("/tmp", "evener-replaceable-a-")
	if err != nil {
		t.Skipf("no writable system temp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(first) })
	second, err := os.MkdirTemp("/tmp", "evener-replaceable-b-")
	if err != nil {
		t.Skipf("no writable system temp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(second) })
	for _, dir := range []string{first, second} {
		if err := os.Chmod(dir, 0o777); err != nil {
			t.Fatal(err)
		}
	}

	oldTemp, oldCache := sessionScratchTempDir, sessionScratchUserCacheDir
	sessionScratchTempDir = func() string { return first }
	sessionScratchUserCacheDir = func() (string, error) { return second, nil }
	t.Cleanup(func() { sessionScratchTempDir, sessionScratchUserCacheDir = oldTemp, oldCache })

	if _, err := NewSessionScratch("", t.TempDir()); err == nil {
		t.Fatalf("allocation must refuse when every candidate base is replaceable (%q, %q)", first, second)
	}
}

// TestSweepCrashedSessionScratchSkipsReplaceableBases: a base another user can
// replace (group/other-writable without the sticky bit) lets them redirect a name
// between the sweep's lease check and its removal, so the sweep must not touch it at
// all — it only reclaims scratch from bases this allocator would use.
func TestSweepCrashedSessionScratchSkipsReplaceableBases(t *testing.T) {
	base, err := os.MkdirTemp("/tmp", "evener-sweep-replaceable-")
	if err != nil {
		t.Skipf("no writable system temp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	if err := os.Chmod(base, 0o777); err != nil {
		t.Fatal(err)
	}
	aged := filepath.Join(base, sessionScratchPrefix+"aged")
	if err := os.Mkdir(aged, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * crashedSessionScratchMaxAge)
	if err := os.Chtimes(aged, old, old); err != nil {
		t.Fatal(err)
	}
	if scratchBaseUnteplaceable(base) {
		t.Fatalf("fixture base %q must count as replaceable", base)
	}

	oldTemp, oldCache := sessionScratchTempDir, sessionScratchUserCacheDir
	sessionScratchTempDir = func() string { return base }
	sessionScratchUserCacheDir = func() (string, error) { return "", errors.New("no cache base") }
	t.Cleanup(func() { sessionScratchTempDir, sessionScratchUserCacheDir = oldTemp, oldCache })

	if err := SweepCrashedSessionScratch(t.TempDir()); err != nil {
		t.Fatalf("sweep over a replaceable base: %v", err)
	}
	if _, err := os.Stat(aged); err != nil {
		t.Fatalf("the sweep must leave an allocation in a replaceable base alone: %v", err)
	}
}

// TestCleanupTightensWhenForeignResidueBlocksRemoval: a privilege-dropping child can
// own a tree inside the shared temp subtree, which this session cannot remove (it is
// not the owner and cannot become root). Cleanup must report the residue and tighten
// the container so no further foreign entry can be created in what is left, rather
// than leaving a 0711 container that keeps growing.
func TestCleanupTightensWhenForeignResidueBlocksRemoval(t *testing.T) {
	scratch, err := NewSessionScratch(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	oldRemove := sessionScratchRemoveAll
	sessionScratchRemoveAll = func(string) error { return errors.New("foreign-owned tree") }
	t.Cleanup(func() { sessionScratchRemoveAll = oldRemove })

	if err := scratch.Cleanup(); err == nil {
		t.Fatal("Cleanup must report a removal it could not complete")
	}
	sessionScratchRemoveAll = oldRemove
	if got := fileMode(t, scratch.Dir); got != 0o700 {
		t.Fatalf("container mode after a failed removal = %04o, want 0700 (no further foreign entries)", got)
	}
}

// TestScratchModeChecksArePlatformGated: a platform that does not record POSIX
// permission or sticky bits (Windows: Go synthesizes writable bits and never sets
// os.ModeSticky) cannot be judged by them — every location would look replaceable and
// allocation would refuse outright, the crash sweep would skip every base, and every
// borrow would be refused. The mode-based judgements must stand down there instead.
func TestScratchModeChecksArePlatformGated(t *testing.T) {
	old := scratchModesRecorded
	scratchModesRecorded = false
	t.Cleanup(func() { scratchModesRecorded = old })

	unprotected, err := os.MkdirTemp("/tmp", "evener-unrecorded-modes-")
	if err != nil {
		t.Skipf("no writable system temp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(unprotected) })
	if err := os.Chmod(unprotected, 0o777); err != nil {
		t.Fatal(err)
	}
	if !scratchBaseUnteplaceable(unprotected) {
		t.Fatalf("a platform that records no modes must not judge %q replaceable from modes", unprotected)
	}

	// The borrow validator's mode checks must stand down too: a container a recording
	// platform would reject is accepted on one that records no modes.
	dir := filepath.Join(t.TempDir(), sessionScratchPrefix+"unrecorded")
	if err := os.MkdirAll(SessionScratchPrivateDir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(SessionScratchTmpDir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, sessionScratchSetupMode); err != nil {
		t.Fatal(err)
	}
	if err := validateSessionScratchLayout(dir); err != nil {
		t.Fatalf("a platform that records no modes must not fail the layout on modes: %v", err)
	}
}

// allowTestCleanup opens a settled container back up so the test harness can remove
// it: a live scratch withholds write even from its owner, and Evener's own teardown
// (Cleanup, or the retained mode Retain applies) is what chmods it first. Tests that
// settle a container directly must do the same before the harness sweeps up.
func allowTestCleanup(t *testing.T, dir string) {
	t.Helper()
	t.Cleanup(func() { _ = os.Chmod(dir, sessionScratchSetupMode) })
}

func TestSessionScratchLifecycle(t *testing.T) {
	base := t.TempDir()
	scratch, err := NewSessionScratch(base, t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionScratch: %v", err)
	}
	if !strings.HasPrefix(filepath.Base(scratch.Dir), sessionScratchPrefix) {
		t.Errorf("session scratch %q must carry the %q prefix", scratch.Dir, sessionScratchPrefix)
	}
	if fi, err := os.Stat(scratch.Dir); err != nil || !fi.IsDir() {
		t.Fatalf("scratch must exist as a directory: %v", err)
	} else if got := fi.Mode().Perm(); got != 0o511 {
		t.Fatalf("scratch mode = %04o, want 0511 (traversable so the shared temp child is reachable, and writable by nobody)", got)
	}
	// The live container is writable by NOBODY, which is what keeps a 0644 artifact
	// from being dropped beside the exported subtrees (issue #495); the session's own
	// scratch is the private subtree.
	if err := os.WriteFile(filepath.Join(scratch.Dir, "scratch"), []byte("x"), 0o600); err == nil {
		t.Fatal("a settled scratch container must not be writable, even by its owner")
	}
	if err := os.WriteFile(filepath.Join(SessionScratchPrivateDir(scratch.Dir), "scratch"), []byte("x"), 0o600); err != nil {
		t.Fatalf("the private subtree must be writable: %v", err)
	}
	// The liveness lease is Evener's own metadata write INSIDE the settled container,
	// and it lands because acquisition opens a brief owner-only setup window that
	// restores the exported mode the container carried. A baked-in 0511 must not be
	// mistaken for "no window needed", which would make every mint fail here.
	if _, err := os.Stat(filepath.Join(scratch.Dir, sessionScratchLeaseName)); err != nil {
		t.Fatalf("the liveness lease must exist inside the settled container after minting: %v", err)
	}
	if err := scratch.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if _, err := os.Stat(scratch.Dir); !os.IsNotExist(err) {
		t.Fatalf("scratch remains after cleanup: %v", err)
	}
}

// TestWritableScratchWindowRefusesAConcurrentSetupMode pins the window's restore
// target. The window used to restore the mode it SAMPLED, so a second window that
// took the container to its setup mode between the first window's layout check and
// its stat made the first window restore 0700 over the settled 0511 — whichever
// restore landed last decided the mode. A 0700 live scratch is exactly the #495
// failure: a privilege-dropping child's $TMPDIR is unreachable, and the container
// becomes writable by its owner. The window now restores only an exported mode and
// refuses a setup mode instead.
func TestWritableScratchWindowRefusesAConcurrentSetupMode(t *testing.T) {
	if !scratchModesRecorded {
		t.Skip("the exported container modes are not recorded on this platform")
	}
	base := t.TempDir()
	scratch, err := NewSessionScratch(base, t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionScratch: %v", err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })

	original := scratchWindowBeforeSample
	t.Cleanup(func() { scratchWindowBeforeSample = original })
	scratchWindowBeforeSample = func(path string) {
		scratchWindowBeforeSample = original
		// Stand in for a concurrent window that has already taken the container to its
		// setup mode; that window restores an exported mode when it closes.
		if err := os.Chmod(path, sessionScratchSetupMode); err != nil {
			t.Errorf("simulate a concurrent window: %v", err)
		}
	}

	ran := false
	err = withWritableScratchContainer(scratch.Dir, func() error {
		ran = true
		return nil
	})
	if ran {
		t.Error("fn must not run while the container is at another window's setup mode")
	}
	if err == nil {
		t.Error("a setup mode observed inside the window must be refused, not restored")
	}
	fi, statErr := os.Stat(scratch.Dir)
	if statErr != nil {
		t.Fatalf("stat scratch: %v", statErr)
	}
	if got := fi.Mode().Perm(); got != sessionScratchSetupMode {
		t.Errorf("the refused window must leave the container mode alone: got %04o, want %04o", got, sessionScratchSetupMode)
	}
}

// TestWritableScratchWindowRestoresTheExportedModeItCarried pins the other half of the
// contract: the window returns each exported container to the exported mode it
// carried, so a retained scratch stays owner-writable for manual cleanup and a live
// one stays unwritable by everybody.
func TestWritableScratchWindowRestoresTheExportedModeItCarried(t *testing.T) {
	if !scratchModesRecorded {
		t.Skip("the exported container modes are not recorded on this platform")
	}
	base := t.TempDir()
	scratch, err := NewSessionScratch(base, t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionScratch: %v", err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })

	for _, tc := range []struct {
		name  string
		setup func() error
		want  os.FileMode
	}{
		{"live", func() error { return nil }, sessionScratchDirMode},
		{"retained", scratch.Retain, sessionScratchRetainedDirMode},
	} {
		if err := tc.setup(); err != nil {
			t.Fatalf("%s: prepare container: %v", tc.name, err)
		}
		if err := withWritableScratchContainer(scratch.Dir, func() error { return nil }); err != nil {
			t.Fatalf("%s: window: %v", tc.name, err)
		}
		fi, err := os.Stat(scratch.Dir)
		if err != nil {
			t.Fatalf("%s: stat scratch: %v", tc.name, err)
		}
		if got := fi.Mode().Perm(); got != tc.want {
			t.Errorf("%s: container mode after the window = %04o, want %04o", tc.name, got, tc.want)
		}
	}
}

// TestWritableScratchWindowHoldsTheContainerLock pins the serialization the window needs
// on top of an exported-mode-only restore. Two windows that both read the exported mode
// before either chmod'ed used to interleave as A.chmod(0700); B.chmod(0700); A.fn ok;
// A.chmod(0511); B.fn — so B's callback ran against the live, write-withheld mode, and
// its write failed with EACCES. In practice that reported lease contention as a hard
// failure from OpenRetainedSessionScratch and stopped Pin/rollbackUnpublishedScratchPin
// from publishing or clearing a retention pin.
func TestWritableScratchWindowHoldsTheContainerLock(t *testing.T) {
	base := t.TempDir()
	scratch, err := NewSessionScratch(base, t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionScratch: %v", err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })

	freeInsideWindow := make(chan bool, 1)
	if err := withWritableScratchContainer(scratch.Dir, func() error {
		probe, err := openScratchDirNoFollow(scratch.Dir)
		if err != nil {
			return err
		}
		defer probe.Close() //nolint:errcheck // read-only directory handle
		free, err := tryLockScratchDirFile(probe)
		if err != nil {
			return err
		}
		freeInsideWindow <- free
		return nil
	}); err != nil {
		t.Fatalf("window: %v", err)
	}
	if <-freeInsideWindow {
		t.Error("another writer took the container while a window was running: the window must hold the container lock for its whole span")
	}
}

// TestWritableScratchWindowsRunTheirCallbacksWritably runs several windows at once: every
// callback must be able to write inside the container, and the container must be left
// live. That is the harm the serialization removes — a callback that runs against the
// write-withheld live mode cannot write at all.
func TestWritableScratchWindowsRunTheirCallbacksWritably(t *testing.T) {
	base := t.TempDir()
	scratch, err := NewSessionScratch(base, t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionScratch: %v", err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })

	const workers = 4
	var wg sync.WaitGroup
	results := make(chan error, workers)
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results <- withWritableScratchContainer(scratch.Dir, func() error {
				return os.WriteFile(filepath.Join(scratch.Dir, fmt.Sprintf("artifact-%d", i)), []byte("x"), 0o600)
			})
		}(i)
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Errorf("a window's callback could not write inside the container: %v", err)
		}
	}
	fi, err := os.Stat(scratch.Dir)
	if err != nil {
		t.Fatalf("stat scratch: %v", err)
	}
	if got := fi.Mode().Perm(); got != sessionScratchDirMode {
		t.Errorf("container mode after concurrent windows = %04o, want %04o", got, sessionScratchDirMode)
	}
}

// TestPrepareSessionScratchLeavesASettledLiveContainerAlone pins that a container which
// is already settled at the live mode is not re-widened. Every wrapper rebuild and
// re-root re-runs prepareSessionScratch, and taking the setup window for it transiently
// changed an already-valid 0511 container to 0700, which a privilege-dropping child's
// $TMPDIR lookup can lose.
func TestPrepareSessionScratchLeavesASettledLiveContainerAlone(t *testing.T) {
	base := t.TempDir()
	scratch, err := NewSessionScratch(base, t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionScratch: %v", err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })

	original := scratchContainerChmod
	t.Cleanup(func() { scratchContainerChmod = original })
	chmods := 0
	scratchContainerChmod = func(file *os.File, mode os.FileMode) error {
		chmods++
		return original(file, mode)
	}
	if err := prepareSessionScratch(scratch.Dir); err != nil {
		t.Fatalf("prepare settled scratch: %v", err)
	}
	if chmods != 0 {
		t.Errorf("prepareSessionScratch re-took the setup window on a settled live container (%d container chmods): the mode must change only when the layout needs work", chmods)
	}
	fi, err := os.Stat(scratch.Dir)
	if err != nil {
		t.Fatalf("stat scratch: %v", err)
	}
	if got := fi.Mode().Perm(); got != sessionScratchDirMode {
		t.Errorf("container mode after preparing a settled scratch = %04o, want %04o", got, sessionScratchDirMode)
	}
}

// TestPrepareSessionScratchRestoresAModeWhenTheLayoutFails pins the mode a failed layout
// leaves behind. A LEGACY container is 0700 with the session's files still at its root, so
// a migration that stopped partway must not publish 0511: everything the relocation has not
// reached yet would become readable to any local user who knows its name. A container that
// already carries an exported mode is restored to exactly that mode.
func TestPrepareSessionScratchRestoresAModeWhenTheLayoutFails(t *testing.T) {
	if !scratchModesRecorded {
		t.Skip("the exported container modes are not recorded on this platform")
	}
	base := t.TempDir()
	legacy := filepath.Join(base, sessionScratchPrefix+"legacy")
	if err := os.Mkdir(legacy, sessionScratchSetupMode); err != nil {
		t.Fatal(err)
	}
	// A retained container: already in the exported layout, so the failure comes from a
	// layout step that reads the container, not from the migration.
	retained, err := NewSessionScratch(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionScratch: %v", err)
	}
	t.Cleanup(func() { _ = retained.Cleanup() })
	if err := retained.Retain(); err != nil {
		t.Fatalf("Retain: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(legacy, sessionScratchSetupMode) })

	for _, tc := range []struct {
		name string
		dir  string
		want os.FileMode
	}{
		{"legacy", legacy, sessionScratchSetupMode},
		{"retained", retained.Dir, sessionScratchRetainedDirMode},
	} {
		original := sessionScratchReadDir
		sessionScratchReadDir = func(string) ([]os.DirEntry, error) {
			return nil, errors.New("forced read failure")
		}
		err := prepareSessionScratch(tc.dir)
		sessionScratchReadDir = original
		if err == nil {
			t.Fatalf("%s: a layout that cannot be read must report a failure", tc.name)
		}
		fi, err := os.Stat(tc.dir)
		if err != nil {
			t.Fatalf("%s: stat scratch: %v", tc.name, err)
		}
		if got := fi.Mode().Perm(); got != tc.want {
			t.Errorf("%s: container mode after a failed layout = %04o, want %04o", tc.name, got, tc.want)
		}
	}
}

// TestBorrowedScratchWithholdsContainerWriteUntilTheLastConsumerFinishes pins that a
// borrowing consumer gets the same protection a session gets. A borrower is live, so the
// container must withhold write from it — a wrapperless consumer could otherwise drop a
// 0644 artifact beside the exported subtrees, where any local user could read it by name
// (issue #495) — and the retained, owner-writable mode the manual cleanup needs must come
// back only once the last consumer has finished.
func TestBorrowedScratchWithholdsContainerWriteUntilTheLastConsumerFinishes(t *testing.T) {
	if !scratchModesRecorded {
		t.Skip("the exported container modes are not recorded on this platform")
	}
	base := t.TempDir()
	scratch, err := NewSessionScratch(base, t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionScratch: %v", err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	if err := scratch.Retain(); err != nil {
		t.Fatalf("Retain: %v", err)
	}
	containerMode := func() os.FileMode {
		t.Helper()
		fi, err := os.Stat(scratch.Dir)
		if err != nil {
			t.Fatalf("stat scratch: %v", err)
		}
		return fi.Mode().Perm()
	}
	if got := containerMode(); got != sessionScratchRetainedDirMode {
		t.Fatalf("retained container mode = %04o, want %04o", got, sessionScratchRetainedDirMode)
	}
	first, err := BorrowRetainedSessionScratch(scratch.Dir)
	if err != nil {
		t.Fatalf("first borrow: %v", err)
	}
	second, err := BorrowRetainedSessionScratch(scratch.Dir)
	if err != nil {
		t.Fatalf("second borrow: %v", err)
	}
	if got := containerMode(); got != sessionScratchDirMode {
		t.Errorf("borrowed container mode = %04o, want %04o (a borrowing consumer must not be able to write beside the subtrees)", got, sessionScratchDirMode)
	}
	if err := first.Retain(); err != nil {
		t.Fatalf("release first borrow: %v", err)
	}
	if got := containerMode(); got != sessionScratchDirMode {
		t.Errorf("container mode after the first of two consumers finished = %04o, want %04o (the retained mode must wait for the last one)", got, sessionScratchDirMode)
	}
	if err := second.Retain(); err != nil {
		t.Fatalf("release second borrow: %v", err)
	}
	if got := containerMode(); got != sessionScratchRetainedDirMode {
		t.Errorf("container mode after every consumer finished = %04o, want %04o", got, sessionScratchRetainedDirMode)
	}
}

// TestBorrowReleaseDoesNotDowngradeALiveSession pins that the retained mode waits for a
// live LEASE as well as for the borrowers. A lease-owning session holds no claim on the
// private subtree — its lock is on the lease file — so a settle that counted borrowers
// alone let a borrowing consumer that finished first downgrade the live adopter's
// container to 0711: owner-writable and other-traversable, which is exactly the state a
// 0644 artifact beside the exported subtrees must never be readable through (issue #495).
func TestBorrowReleaseDoesNotDowngradeALiveSession(t *testing.T) {
	if !scratchModesRecorded {
		t.Skip("the exported container modes are not recorded on this platform")
	}
	base := t.TempDir()
	// A live, lease-owning session: NewSessionScratch holds the lease for its lifetime.
	live, err := NewSessionScratch(base, t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionScratch: %v", err)
	}
	t.Cleanup(func() { _ = live.Cleanup() })
	borrow, err := BorrowRetainedSessionScratch(live.Dir)
	if err != nil {
		t.Fatalf("borrow a live container: %v", err)
	}
	if err := borrow.Retain(); err != nil {
		t.Fatalf("release the borrow: %v", err)
	}
	fi, err := os.Stat(live.Dir)
	if err != nil {
		t.Fatalf("stat scratch: %v", err)
	}
	if got := fi.Mode().Perm(); got != sessionScratchDirMode {
		t.Errorf("container mode after a borrower finished while the lease was live = %04o, want %04o (a live session must keep the container write-withheld)", got, sessionScratchDirMode)
	}
}

// TestFailedMintLeavesNoScratchBehind pins that a mint whose layout fails removes its
// container. The failed layout is restored to an exported mode, and the live mode
// withholds owner write, so a plain os.RemoveAll could not unlink what was inside it and
// the allocation would sit there — plus its partial subtrees — until the 24h sweep.
func TestFailedMintLeavesNoScratchBehind(t *testing.T) {
	base := t.TempDir()
	original := sessionScratchReadDir
	t.Cleanup(func() { sessionScratchReadDir = original })
	sessionScratchReadDir = func(string) ([]os.DirEntry, error) {
		return nil, errors.New("forced read failure")
	}
	if _, err := NewSessionScratch(base, t.TempDir()); err == nil {
		t.Fatal("a scratch whose layout cannot be read must not mint")
	}
	sessionScratchReadDir = original
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), sessionScratchPrefix) {
			_ = os.Chmod(filepath.Join(base, entry.Name()), sessionScratchSetupMode)
			t.Errorf("a failed mint left %q behind", entry.Name())
		}
	}
}

// TestWrapperMigratesALegacySquatterAtASubtreeName pins that the wrapper path does not
// refuse what the migration it precedes exists to repair. Evener's own allocation can hold a
// legacy root file at a reserved subtree name, and the restore of a retained scratch rebuilds
// the wrapper BEFORE the restore re-settles the layout — so a pre-check that refused the
// squatter would abort the restore of a directory layOutSessionScratch would have repaired by
// moving the file aside.
func TestWrapperMigratesALegacySquatterAtASubtreeName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), sessionScratchPrefix+"squatter")
	if err := os.Mkdir(dir, sessionScratchSetupMode); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sessionScratchTmpName), []byte("legacy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := prepareSessionScratchForWrapper(dir); err != nil {
		t.Fatalf("a wrapper must migrate a legacy squatter rather than refuse it: %v", err)
	}
	if fi, err := os.Stat(SessionScratchTmpDir(dir)); err != nil || !fi.IsDir() {
		t.Fatalf("the shared temp subtree must be a directory after the migration: %v", err)
	}
	if err := os.Chmod(dir, sessionScratchSetupMode); err != nil {
		t.Fatal(err)
	}
}

// TestScratchBaseRejectsAnotherUsersStickyComponent pins the base-integrity judgement. A
// sticky, other-writable component was treated as unteplaceable on its mode alone — but the
// sticky bit only stops another user from removing OUR entry: the DIRECTORY's owner may
// delete or rename anything inside it, sticky or not. A 1777 temp base owned by another
// local user is therefore as replaceable as a plain world-writable one, and replacing it
// moves the sandbox bind and grant targets with it.
func TestScratchBaseRejectsAnotherUsersStickyComponent(t *testing.T) {
	if !scratchModesRecorded {
		t.Skip("the exported modes are not recorded on this platform")
	}
	base := filepath.Join(t.TempDir(), "sticky-base")
	if err := os.Mkdir(base, sessionScratchTmpMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, sessionScratchTmpMode); err != nil {
		t.Fatal(err)
	}
	if _, unteplaceable := scratchBaseFacts(base); !unteplaceable {
		t.Fatalf("a sticky base this process owns must stay usable (%q)", base)
	}
	original := fileOwnerID
	t.Cleanup(func() { fileOwnerID = original })
	fileOwnerID = func(os.FileInfo) (int, bool) { return currentUserID() + 1, true }
	if _, unteplaceable := scratchBaseFacts(base); unteplaceable {
		t.Errorf("a sticky base owned by another local user must not be treated as unteplaceable (%q)", base)
	}
}

// TestSettleRetainedModeProbesTheLeaseUnderTheContainerLock pins the ordering the lease
// check needs. Probing the lease outside the container lock leaves a window where a
// restorer acquires the lease between the probe and the mode change, so the settle relaxes
// the container to owner-writable while a live session owns it (issue #495).
func TestSettleRetainedModeProbesTheLeaseUnderTheContainerLock(t *testing.T) {
	base := t.TempDir()
	scratch, err := NewSessionScratch(base, t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionScratch: %v", err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	if err := scratch.Retain(); err != nil {
		t.Fatalf("Retain: %v", err)
	}

	original := scratchLeaseHeld
	t.Cleanup(func() { scratchLeaseHeld = original })
	probeUnderLock := false
	scratchLeaseHeld = func(dir string) bool {
		probe, err := openScratchDirNoFollow(dir)
		if err != nil {
			return true
		}
		defer probe.Close() //nolint:errcheck // read-only directory handle
		free, err := tryLockScratchDirFile(probe)
		if err != nil {
			return true
		}
		probeUnderLock = !free
		// Pretend a lease is held, so the settle is expected to change nothing.
		return true
	}
	if err := settleRetainedContainerMode(scratch.Dir); err != nil {
		t.Fatalf("settle retained container: %v", err)
	}
	if !probeUnderLock {
		t.Error("the lease probe ran outside the container lock: a restorer could acquire the lease between the probe and the mode change")
	}
}

// TestWindowRestoreFailureDoesNotLeakTheLease pins that a caller of the setup window cannot
// lose a lease the callback already opened. The window fails if its restore chmod fails,
// after the callback has acquired the lease; a caller that only inspects the window error
// would drop the handle, leaving the lease held (an open descriptor with an exclusive
// flock) until the garbage collector finalizes it — which makes a retained scratch
// un-restorable, un-reclaimable and un-settleable for the life of the process.
func TestWindowRestoreFailureDoesNotLeakTheLease(t *testing.T) {
	base := t.TempDir()
	scratch, err := NewSessionScratch(base, t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionScratch: %v", err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	if err := scratch.Retain(); err != nil {
		t.Fatalf("Retain: %v", err)
	}

	original := scratchContainerChmod
	t.Cleanup(func() { scratchContainerChmod = original })
	scratchContainerChmod = func(file *os.File, mode os.FileMode) error {
		if mode != sessionScratchSetupMode {
			return errors.New("forced restore failure")
		}
		return original(file, mode)
	}
	lease, _, err := acquireScratchLeaseInWindow(scratch.Dir)
	if err == nil {
		t.Fatal("a window whose restore chmod fails must report the failure")
	}
	if lease != nil {
		t.Error("a failed window must not hand a lease back to the caller")
	}
	scratchContainerChmod = original
	// The lease has to be free again: the failure must not leave it held.
	free, contended, err := acquireScratchLease(filepath.Join(scratch.Dir, sessionScratchLeaseName))
	if err != nil || contended {
		t.Fatalf("the lease is still held after the failed window: lease=%v contended=%v err=%v", free, contended, err)
	}
	if err := free.Release(); err != nil {
		t.Fatalf("release probe lease: %v", err)
	}
}

// TestSessionScratchSharedTmpIsUsableByPrivilegeDroppingChildren pins the fix for
// issue #495: a session scratch was mode 0700 and its path was exported as TMPDIR
// to every descendant, so a child that deliberately ran as another user could not
// create a temp file there and failed with a bare "Permission denied" naming a
// path the user never chose. The scratch now keeps its private files under a
// 0711 parent and exports TMPDIR as a dedicated sticky, world-writable
// subdirectory, so a foreign user gets /tmp-like semantics: create anything, and
// delete only what it owns.
//
// The kernel's check for a foreign user is entirely permission bits — traverse
// (o+x) on every ancestor, write (o+w) on the target — so this asserts exactly
// those bits, which is also the part the old layout failed.
func TestSessionScratchSharedTmpIsUsableByPrivilegeDroppingChildren(t *testing.T) {
	base := t.TempDir()
	scratch, err := NewSessionScratch(base, t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionScratch: %v", err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })

	tmp := SessionScratchTmpDir(scratch.Dir)
	if tmp == "" || tmp == scratch.Dir {
		t.Fatalf("a session scratch must expose a shared temp subdirectory distinct from the private scratch, got %q", tmp)
	}
	tmpInfo, err := os.Stat(tmp)
	if err != nil || !tmpInfo.IsDir() {
		t.Fatalf("shared temp directory %q must exist: %v", tmp, err)
	}
	if got := tmpInfo.Mode().Perm(); got != 0o777 {
		t.Fatalf("shared temp mode = %04o, want 0777 (any user can create temp files)", got)
	}
	if tmpInfo.Mode()&os.ModeSticky == 0 {
		t.Fatalf("shared temp directory %q must carry the sticky bit (no user removes another's files)", tmp)
	}

	// The agent's own files live in a 0700 subtree, so a session artifact written
	// at write_file's default 0644 is still unreadable by any other user.
	private := SessionScratchPrivateDir(scratch.Dir)
	privateInfo, err := os.Stat(private)
	if err != nil || !privateInfo.IsDir() {
		t.Fatalf("private scratch subtree %q must exist: %v", private, err)
	}
	if got := privateInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("private scratch subtree mode = %04o, want 0700 (owner only)", got)
	}

	// The container itself must grant a foreign user exactly traversal and nothing
	// more: reachable (o+x), never group/other readable or writable.
	scratchInfo, err := os.Stat(scratch.Dir)
	if err != nil {
		t.Fatalf("stat private scratch: %v", err)
	}
	perm := scratchInfo.Mode().Perm()
	if perm&0o001 == 0 {
		t.Fatalf("private scratch mode = %04o, must stay traversable (o+x) or the shared temp subdirectory is unreachable", perm)
	}
	if perm&0o066 != 0 {
		t.Fatalf("private scratch mode = %04o, must not become group/other readable or writable", perm)
	}
}

// TestPrepareSessionScratchRefusesSymlinkedSubtrees: a session owns its scratch
// and can leave any entry behind there, including a symlink named "tmp" or
// "private". Repairing the layout at restore must remove such an entry, never
// follow it — a followed symlink would hand an arbitrary directory outside the
// scratch the exported world-writable mode.
func TestPrepareSessionScratchRefusesSymlinkedSubtrees(t *testing.T) {
	base := t.TempDir()
	scratch, err := NewSessionScratch(base, t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionScratch: %v", err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })

	victim := t.TempDir()
	if err := os.Chmod(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	tmp := SessionScratchTmpDir(scratch.Dir)
	// The settled container withholds owner write; a legacy scratch is writable, so
	// open the same window Evener's own setup uses.
	if err := os.Chmod(scratch.Dir, sessionScratchSetupMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(tmp); err != nil {
		t.Fatalf("remove the real temp subtree: %v", err)
	}
	if err := os.Symlink(victim, tmp); err != nil {
		t.Fatalf("plant the symlink: %v", err)
	}

	if err := prepareSessionScratch(scratch.Dir); err != nil {
		t.Fatalf("prepareSessionScratch must repair a planted symlink, got %v", err)
	}
	if got := fileMode(t, victim); got != 0o755 {
		t.Fatalf("prepareSessionScratch followed the planted symlink and chmod'ed its target: victim mode = %04o, want 0755", got)
	}
	info, err := os.Lstat(tmp)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("planted symlink was not replaced by a real directory: info=%v err=%v", info, err)
	}
	// The entry itself is preserved, moved aside under a reserved-name-safe spelling:
	// removing it would destroy something the session left in its own scratch.
	movedAside := filepath.Join(SessionScratchPrivateDir(scratch.Dir), sessionScratchTmpName+"."+sessionScratchLegacySuffix)
	if info, err := os.Lstat(movedAside); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("a non-directory entry at a subtree name must be moved aside, not unlinked: info=%v err=%v", info, err)
	}
	if info.Mode().Perm() != 0o777 || info.Mode()&os.ModeSticky == 0 {
		t.Fatalf("repaired temp subtree mode = %v, want 1777", info.Mode())
	}
}

// TestPrepareSessionScratchRepairsPreFixLayout: a scratch minted before this
// layout existed is a bare 0700 directory with no subtrees. Restoring it must
// repair the container mode too — a 0700 container makes the new temp subtree
// unreachable to another user, so the original permission failure would persist
// for every retained scratch (issue #495).
func TestPrepareSessionScratchRepairsPreFixLayout(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, sessionScratchPrefix+"legacy")
	allowTestCleanup(t, dir)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := prepareSessionScratch(dir); err != nil {
		t.Fatalf("prepareSessionScratch on a pre-fix layout: %v", err)
	}
	if got := fileMode(t, dir); got != 0o511 {
		t.Fatalf("container mode = %04o, want 0511 (a 0700 container hides the temp subtree)", got)
	}
	if got := fileMode(t, SessionScratchPrivateDir(dir)); got != 0o700 {
		t.Fatalf("private subtree mode = %04o, want 0700", got)
	}
	info, err := os.Stat(SessionScratchTmpDir(dir))
	if err != nil || !info.IsDir() {
		t.Fatalf("temp subtree missing after repair: %v", err)
	}
	if info.Mode().Perm() != 0o777 || info.Mode()&os.ModeSticky == 0 {
		t.Fatalf("temp subtree mode = %v, want 1777", info.Mode())
	}
}

// TestPrepareSessionScratchPreservesRegularFileAtReservedName: a legacy session
// could leave a REGULAR FILE whose name Evener now reserves for an exported
// subtree. That file is session data, not Evener metadata, so the migration must
// relocate it into the private subtree rather than unlink it while creating the
// subtree (issue #495).
func TestPrepareSessionScratchPreservesRegularFileAtReservedName(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, sessionScratchPrefix+"legacy")
	allowTestCleanup(t, dir)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sessionScratchTmpName), []byte("legacy data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sessionScratchPrivateName), []byte("other data"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := prepareSessionScratch(dir); err != nil {
		t.Fatalf("prepareSessionScratch with regular files at reserved names: %v", err)
	}

	// The payloads survive somewhere under the private subtree (the exact names are
	// an implementation detail of moving them aside).
	preserved := map[string]bool{}
	private := SessionScratchPrivateDir(dir)
	if err := filepath.WalkDir(private, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		preserved[string(data)] = true
		return nil
	}); err != nil {
		t.Fatalf("walk the private subtree: %v", err)
	}
	for _, want := range []string{"legacy data", "other data"} {
		if !preserved[want] {
			t.Fatalf("a legacy regular file at a reserved name was destroyed (payload %q gone); found %v", want, preserved)
		}
	}
	// The exported subtrees are real directories again, with their exported modes.
	for _, sub := range SessionScratchWriteRoots(dir) {
		info, err := os.Lstat(sub)
		if err != nil || !info.IsDir() {
			t.Fatalf("exported subtree %q missing after migration: info=%v err=%v", sub, info, err)
		}
	}
	if info, err := os.Lstat(SessionScratchTmpDir(dir)); err != nil || info.Mode().Perm() != 0o777 || info.Mode()&os.ModeSticky == 0 {
		t.Fatalf("temp subtree not re-created with its exported mode: info=%v err=%v", info, err)
	}
}

// TestPrepareSessionScratchBreaksHardLinksBeforeTightening: a legacy artifact can be
// a hard link to a file outside the scratch (a worktree file, say). Hard links share
// one inode, so tightening the relocated file in place would tighten that outside
// file too. The migration must give the scratch its own copy first.
func TestPrepareSessionScratchBreaksHardLinksBeforeTightening(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "worktree-file")
	if err := os.WriteFile(outside, []byte("shared payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(outside, 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, sessionScratchPrefix+"legacy")
	allowTestCleanup(t, dir)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(dir, "artifact.bin")
	if err := os.Link(outside, linked); err != nil {
		t.Skipf("this filesystem does not support hard links: %v", err)
	}

	if err := prepareSessionScratch(dir); err != nil {
		t.Fatalf("prepareSessionScratch: %v", err)
	}

	// The file outside the scratch keeps its own permissions: the migration must not
	// reach it through the shared inode.
	outsideInfo, err := os.Stat(outside)
	if err != nil {
		t.Fatal(err)
	}
	if got := outsideInfo.Mode().Perm(); got != 0o644 {
		t.Fatalf("a file outside the scratch was tightened through a hard link: mode = %04o, want 0644", got)
	}

	// The scratch's own copy is present, tightened, and a distinct inode.
	moved := filepath.Join(SessionScratchPrivateDir(dir), "artifact.bin")
	movedInfo, err := os.Stat(moved)
	if err != nil {
		t.Fatalf("the hard-linked artifact was not relocated: %v", err)
	}
	if got := movedInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("relocated hard-linked artifact mode = %04o, want 0600", got)
	}
	if os.SameFile(outsideInfo, movedInfo) {
		t.Fatal("the relocated artifact still shares the outside file's inode")
	}
	if data, err := os.ReadFile(moved); err != nil || string(data) != "shared payload" {
		t.Fatalf("relocated artifact content = %q err %v", data, err)
	}
}

// TestPrepareSessionScratchRelocatesLegacyRootArtifacts: a legacy scratch keeps a
// session's files directly at its root, several of them world-readable by their own
// mode. Loosening the container to 0711 without moving them would expose those files
// to any local user who can traverse the base and guess a name (issue #495), so the
// migration must relocate them into the 0700 private subtree BEFORE the container
// becomes traversable, and must not clobber a file the private subtree already has.
func TestPrepareSessionScratchRelocatesLegacyRootArtifacts(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, sessionScratchPrefix+"legacy")
	allowTestCleanup(t, dir)
	if err := os.MkdirAll(filepath.Join(dir, "gocache", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report.txt"), []byte("findings"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gocache", "sub", "blob"), []byte("cache"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A legacy entry that already carries the reserved temp name is reused as the
	// exported temp subtree, so its old contents must be hardened rather than
	// published world-readable through the 1777 mode.
	if err := os.MkdirAll(filepath.Join(dir, sessionScratchTmpName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sessionScratchTmpName, "legacy.tmp"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := prepareSessionScratch(dir); err != nil {
		t.Fatalf("prepareSessionScratch on a populated legacy layout: %v", err)
	}

	private := SessionScratchPrivateDir(dir)
	if got, err := os.ReadFile(filepath.Join(private, "report.txt")); err != nil || string(got) != "findings" {
		t.Fatalf("legacy artifact was not relocated intact: %q err %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(private, "gocache", "sub", "blob")); err != nil || string(got) != "cache" {
		t.Fatalf("legacy nested artifact was not relocated intact: %q err %v", got, err)
	}
	if got := fileMode(t, filepath.Join(private, "report.txt")); got != 0o600 {
		t.Fatalf("relocated file mode = %04o, want 0600 (owner only)", got)
	}
	if got := fileMode(t, filepath.Join(private, "gocache")); got != 0o700 {
		t.Fatalf("relocated directory mode = %04o, want 0700 (owner only)", got)
	}
	if got := fileMode(t, filepath.Join(dir, sessionScratchTmpName, "legacy.tmp")); got != 0o600 {
		t.Fatalf("legacy temp file mode = %04o, want 0600 (the reused temp subtree is world-writable)", got)
	}

	// Nothing but the reserved entries may remain directly under the now-0711
	// container: any session file left there is readable by other users by name.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if slices.Contains(scratchContainerReservedNames, entry.Name()) {
			continue
		}
		t.Fatalf("legacy entry %q is still directly under the traversable container", entry.Name())
	}

	// A relocation never clobbers: build a populated private subtree beside a
	// legacy root file of the same name.
	second := filepath.Join(base, sessionScratchPrefix+"collide")
	allowTestCleanup(t, second)
	if err := os.MkdirAll(second, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(second, "report.txt"), []byte("legacy"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(SessionScratchPrivateDir(second), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(SessionScratchPrivateDir(second), "report.txt"), []byte("current"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := prepareSessionScratch(second); err != nil {
		t.Fatalf("prepareSessionScratch with a name collision: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(SessionScratchPrivateDir(second), "report.txt")); err != nil || string(got) != "current" {
		t.Fatalf("collision clobbered the private file: %q err %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(SessionScratchPrivateDir(second), "report.txt.1")); err != nil || string(got) != "legacy" {
		t.Fatalf("relocated collision took no suffixed name: %q err %v", got, err)
	}
}

func TestSessionScratchRetainReleasesLeaseWithoutRemovingDirectory(t *testing.T) {
	base := t.TempDir()
	scratch, err := NewSessionScratch(base, t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionScratch: %v", err)
	}
	if err := scratch.Retain(); err != nil {
		t.Fatalf("Retain: %v", err)
	}
	if _, err := os.Stat(scratch.Dir); err != nil {
		t.Fatalf("Retain removed the scratch directory: %v", err)
	}

	lease, contended, err := acquireScratchLease(filepath.Join(scratch.Dir, sessionScratchLeaseName))
	if err != nil || contended {
		t.Fatalf("Retain did not release the lease: contended=%v err=%v", contended, err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("release retained-directory probe lease: %v", err)
	}
	if err := scratch.Cleanup(); err != nil {
		t.Fatalf("manual Cleanup: %v", err)
	}
}

func TestSessionScratchCleanupRefusesUnownedPath(t *testing.T) {
	base := t.TempDir()
	unrelated := filepath.Join(base, "ordinary-temp")
	if err := os.Mkdir(unrelated, 0o700); err != nil {
		t.Fatal(err)
	}
	scratch := &SessionScratch{Dir: unrelated, base: base}
	if err := scratch.Cleanup(); err == nil {
		t.Fatal("Cleanup accepted a directory outside its allocated namespace")
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("Cleanup touched unrelated directory: %v", err)
	}
}

func TestSessionScratchAgeSweepsOnlyStaleEvenerDirs(t *testing.T) {
	base := t.TempDir()
	stale := filepath.Join(base, sessionScratchPrefix+"crashed")
	fresh := filepath.Join(base, sessionScratchPrefix+"fresh")
	foreign := filepath.Join(base, "not-evener-keepme")
	for _, dir := range []string{stale, fresh, foreign} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-48 * time.Hour)
	for _, dir := range []string{stale, foreign} {
		if err := os.Chtimes(dir, old, old); err != nil {
			t.Fatal(err)
		}
	}

	sweepCrashedSessionScratch(base)
	scratch, err := NewSessionScratch(base, t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionScratch: %v", err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale crashed-session directory remains: %v", err)
	}
	for _, dir := range []string{fresh, foreign} {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("sweep removed %q: %v", dir, err)
		}
	}
}

func TestSessionScratchSweepSkipsOldLiveLease(t *testing.T) {
	base := t.TempDir()
	workspace := t.TempDir()
	live, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatalf("create live scratch: %v", err)
	}
	t.Cleanup(func() { _ = live.Cleanup() })
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(live.Dir, old, old); err != nil {
		t.Fatal(err)
	}

	sweepCrashedSessionScratch(base)
	next, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatalf("create next scratch: %v", err)
	}
	t.Cleanup(func() { _ = next.Cleanup() })
	if _, err := os.Stat(live.Dir); err != nil {
		t.Fatalf("sweep removed old scratch with a live lease: %v", err)
	}
}

func TestSessionScratchSweepRemovesOldReleasedLease(t *testing.T) {
	base := t.TempDir()
	crashed := filepath.Join(base, sessionScratchPrefix+"released")
	if err := os.Mkdir(crashed, 0o700); err != nil {
		t.Fatal(err)
	}
	lease, contended, err := acquireScratchLease(filepath.Join(crashed, sessionScratchLeaseName))
	if err != nil || contended {
		t.Fatalf("acquire fixture lease: contended=%v err=%v", contended, err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("release fixture lease: %v", err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(crashed, old, old); err != nil {
		t.Fatal(err)
	}

	sweepCrashedSessionScratch(base)
	scratch, err := NewSessionScratch(base, t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionScratch: %v", err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	if _, err := os.Stat(crashed); !os.IsNotExist(err) {
		t.Fatalf("old scratch with released lease remains: %v", err)
	}
}

func TestNewSessionScratchDoesNotSweepReleasedDirectories(t *testing.T) {
	base := t.TempDir()
	released := filepath.Join(base, sessionScratchPrefix+"released")
	if err := os.Mkdir(released, 0o700); err != nil {
		t.Fatal(err)
	}
	lease, contended, err := acquireScratchLease(filepath.Join(released, sessionScratchLeaseName))
	if err != nil || contended {
		t.Fatalf("acquire fixture lease: contended=%v err=%v", contended, err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("release fixture lease: %v", err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(released, old, old); err != nil {
		t.Fatal(err)
	}

	scratch, err := NewSessionScratch(base, t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionScratch: %v", err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup(); _ = os.RemoveAll(released) })
	if _, err := os.Stat(released); err != nil {
		t.Fatalf("NewSessionScratch must not auto-clean a released directory: %v", err)
	}
}

func TestSessionScratchFallsBackOutsideWorkspace(t *testing.T) {
	workspace := t.TempDir()
	cacheBase := t.TempDir()
	canonicalCacheBase, err := filepath.EvalSymlinks(cacheBase)
	if err != nil {
		t.Fatal(err)
	}
	oldTemp, oldCache := sessionScratchTempDir, sessionScratchUserCacheDir
	sessionScratchTempDir = func() string { return filepath.Join(workspace, "tmp") }
	sessionScratchUserCacheDir = func() (string, error) { return cacheBase, nil }
	t.Cleanup(func() {
		sessionScratchTempDir = oldTemp
		sessionScratchUserCacheDir = oldCache
	})
	if err := os.MkdirAll(sessionScratchTempDir(), 0o755); err != nil {
		t.Fatal(err)
	}

	scratch, err := NewSessionScratch("", workspace)
	if err != nil {
		t.Fatalf("NewSessionScratch: %v", err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	if !pathWithin(scratch.Dir, canonicalCacheBase) {
		t.Fatalf("scratch %q is not under cache fallback %q", scratch.Dir, cacheBase)
	}
	if pathWithin(scratch.Dir, workspace) {
		t.Fatalf("scratch %q is inside workspace %q", scratch.Dir, workspace)
	}
}

func TestSessionScratchDoesNotChmodCandidateBase(t *testing.T) {
	base := t.TempDir()
	if err := os.Chmod(base, 0o750); err != nil {
		t.Fatal(err)
	}
	scratch, err := NewSessionScratch(base, t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionScratch: %v", err)
	}
	if got := fileMode(t, base); got != 0o750 {
		t.Fatalf("base mode after allocation = %04o, want 0750", got)
	}
	if got := fileMode(t, scratch.Dir); got != 0o511 {
		t.Fatalf("scratch mode = %04o, want 0511", got)
	}
	if err := scratch.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if got := fileMode(t, base); got != 0o750 {
		t.Fatalf("base mode after cleanup = %04o, want 0750", got)
	}
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func TestSweepCrashedSessionScratchReportsUnusableBase(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-base")
	if err := sweepCrashedSessionScratch(missing); err == nil {
		t.Error("sweep of an unreadable base reported success")
	}

	oldTemp, oldCache := sessionScratchTempDir, sessionScratchUserCacheDir
	sessionScratchTempDir = func() string { return missing }
	sessionScratchUserCacheDir = func() (string, error) { return "", errors.New("no cache dir") }
	t.Cleanup(func() {
		sessionScratchTempDir = oldTemp
		sessionScratchUserCacheDir = oldCache
	})
	if err := SweepCrashedSessionScratch(t.TempDir()); err == nil {
		t.Error("sweep with no usable scratch base reported success")
	}
}

func TestSessionScratchAllocationLeavesCacheBaseAloneWhenTempBaseWorks(t *testing.T) {
	consulted := false
	oldCache := sessionScratchUserCacheDir
	sessionScratchUserCacheDir = func() (string, error) {
		consulted = true
		return "", errors.New("cache base is unavailable")
	}
	t.Cleanup(func() { sessionScratchUserCacheDir = oldCache })

	scratch, err := NewSessionScratch(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionScratch: %v", err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	if consulted {
		t.Error("allocation reached for the user cache base while the temp base was usable")
	}
}
