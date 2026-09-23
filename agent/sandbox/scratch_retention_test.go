package sandbox

import (
	"bytes"
	"errors"
	"maps"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/identifier"
)

// scratchRetentionBase overrides the allocator's base discovery to a single
// test-owned base and returns a workspace root outside it.
func scratchRetentionBase(t *testing.T) (base, workspace string) {
	t.Helper()
	base, workspace = t.TempDir(), t.TempDir()
	oldTemp, oldCache := sessionScratchTempDir, sessionScratchUserCacheDir
	sessionScratchTempDir = func() string { return base }
	sessionScratchUserCacheDir = func() (string, error) { return base, nil }
	t.Cleanup(func() { sessionScratchTempDir, sessionScratchUserCacheDir = oldTemp, oldCache })
	// SweepCrashedSessionScratch also walks every world-usable host temp base a
	// session temp container may live in. These tests assert that an aged fixture
	// is (or is not) collected, so letting the sweep reach the machine's real /tmp
	// would make them depend on ambient state — and a stale container this process
	// cannot remove would turn "err == nil" assertions into flakes. Confine it.
	t.Cleanup(SetWorldTempBasesForTesting(nil))
	return base, workspace
}

// TestScratchRetentionStartupSweepKeepsAgedRequiredArtifact is the plan's
// collector test: a pinned, aged, unreleased required artifact survives the
// startup sweep and restores at its original path; once released, the same
// directory is collected.
func TestScratchRetentionStartupSweepKeepsAgedRequiredArtifact(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := ScratchOwner{StateDir: t.TempDir(), RootSessionID: identifier.MustNewSessionID()}
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	ref := ScratchReference{Dir: scratch.Dir, Kind: "unsandboxed"}
	artifact := filepath.Join(scratch.Dir, "required.bin")
	want := []byte("opaque-required-artifact")
	if err := os.WriteFile(artifact, want, 0600); err != nil {
		t.Fatal(err)
	}
	if err := scratch.Pin(owner, ref); err != nil {
		t.Fatal(err)
	}
	if err := scratch.Retain(); err != nil {
		t.Fatal(err)
	}
	aged := time.Now().Add(-2 * crashedSessionScratchMaxAge)
	if err := os.Chtimes(scratch.Dir, aged, aged); err != nil {
		t.Fatal(err)
	}
	if err := SweepCrashedSessionScratch(workspace); err != nil {
		t.Fatalf("sweep with a retained pin: %v", err)
	}
	restored, err := OpenRetainedSessionScratch(owner, ref)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(artifact)
	if err != nil || !bytes.Equal(got, want) || restored.Dir != scratch.Dir {
		t.Fatalf("original required artifact lost: bytes=%q dir=%s err=%v", got, restored.Dir, err)
	}
	if err := restored.Retain(); err != nil {
		t.Fatal(err)
	}
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(scratch.Dir, aged, aged); err != nil {
		t.Fatal(err)
	}
	if err := SweepCrashedSessionScratch(workspace); err != nil {
		t.Fatalf("sweep after release: %v", err)
	}
	if _, err := os.Stat(artifact); !os.IsNotExist(err) {
		t.Fatalf("unreferenced scratch not collected: %v", err)
	}
}

// TestScratchRetentionUnreferencedReleasedStillCollected proves a Released
// tombstone authorizes ordinary age-based collection while an unreleased
// reference (covered above) does not.
func TestScratchRetentionUnreferencedReleasedStillCollected(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := ScratchOwner{StateDir: t.TempDir(), RootSessionID: identifier.MustNewSessionID()}
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	ref := ScratchReference{Dir: scratch.Dir, Kind: "sandbox"}
	if err := scratch.Pin(owner, ref); err != nil {
		t.Fatal(err)
	}
	if err := scratch.Retain(); err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadScratchRetention(owner)
	if err != nil {
		t.Fatalf("LoadScratchRetention: %v", err)
	}
	if manifest.Released || len(manifest.References) != 1 || manifest.Revision == 0 {
		t.Fatalf("pinned manifest = %+v", manifest)
	}
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatal(err)
	}
	aged := time.Now().Add(-2 * crashedSessionScratchMaxAge)
	if err := os.Chtimes(scratch.Dir, aged, aged); err != nil {
		t.Fatal(err)
	}
	if err := SweepCrashedSessionScratch(workspace); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if _, err := os.Stat(scratch.Dir); !os.IsNotExist(err) {
		t.Fatalf("released scratch not collected: %v", err)
	}
}

// TestSweepRemovalSerializesWithTheManifestReset pins the round-31 finding:
// the sweep's retention check reads the Released tombstone without any lock
// the reset's resurrection takes, so a ResetScratchRetentionIfReleased that
// carries the directory's rows into an unreleased manifest can commit between
// the check and the removal. The sweep would then RemoveAll the scratch the
// resurrected manifest names — the retained allocation is lost with the
// manifest permanently pointing at deleted scratch. The serialization makes
// both orders safe: a reset that runs first leaves !Released for the sweep's
// check to read, and one that comes second finds the directory gone and the
// pair dies with the tombstone.
func TestSweepRemovalSerializesWithTheManifestReset(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratch := pinnedScratch(t, base, workspace, owner, ScratchKindSandbox)
	artifact := filepath.Join(scratch.Dir, "retained.bin")
	if err := os.WriteFile(artifact, []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	// The carry needs the full graph: the lease-owning binding plus a
	// consumer naming it (round 16).
	consumer := ScratchConsumerBinding{SessionID: "R", CurrentBindingID: "E0"}
	if err := UpsertScratchBinding(owner, retentionBinding("E0", "R", workspace, map[string]ScratchSlot{
		ScratchKindSandbox: {Dir: scratch.Dir, OwnsLease: true},
	}), consumer); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	// Tombstone while the lease is still held, so the pin survives the
	// release (round 25): Released:true is exactly what makes the sweep read
	// the aged directory collectible.
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := scratch.Retain(); err != nil {
		t.Fatal(err)
	}
	aged := time.Now().Add(-2 * crashedSessionScratchMaxAge)
	if err := os.Chtimes(scratch.Dir, aged, aged); err != nil {
		t.Fatal(err)
	}

	atWindow := make(chan struct{})
	proceed := make(chan struct{})
	resetDone := make(chan error, 1)
	sweepDone := make(chan error, 1)
	scratchSweepBeforeRemove = func() {
		atWindow <- struct{}{}
		<-proceed
	}
	t.Cleanup(func() { scratchSweepBeforeRemove = nil })

	go func() { sweepDone <- SweepCrashedSessionScratch(workspace) }()
	<-atWindow

	go func() {
		_, _, err := ResetScratchRetentionIfReleased(owner)
		resetDone <- err
	}()
	// Give the reset time to finish inside the sweep's hold. Pre-fix it
	// commits the carry in this window; post-fix it blocks on the
	// reclamation serialization until the sweep has removed the directory.
	time.Sleep(250 * time.Millisecond)
	close(proceed)

	select {
	case err := <-resetDone:
		if err != nil {
			t.Fatalf("reset: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("reset did not finish")
	}
	select {
	case err := <-sweepDone:
		if err != nil {
			t.Fatalf("sweep: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("sweep did not finish")
	}

	manifest, err := LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	namesDir := false
	for _, ref := range manifest.References {
		if filepath.Clean(ref.Dir) == filepath.Clean(scratch.Dir) {
			namesDir = true
		}
	}
	_, statErr := os.Stat(scratch.Dir)
	if !manifest.Released && namesDir && os.IsNotExist(statErr) {
		t.Fatalf("the sweep removed a directory the concurrent reset had carried: the resurrected manifest names deleted scratch %s", scratch.Dir)
	}
}

// TestScratchRetentionReportsLeaseAcquisitionFailure is the regression test for
// the swallowed lease error: ReleaseScratchRetention must continue for confirmed
// contention (a genuinely held lease is left for the collector) but must REPORT
// a real acquisition failure (open/stat/chmod), so a release cannot look
// successful when a lease could not be inspected or released.
func TestScratchRetentionReportsLeaseAcquisitionFailure(t *testing.T) {
	t.Run("contention_stays_quiet", func(t *testing.T) {
		base, workspace := scratchRetentionBase(t)
		owner := ScratchOwner{StateDir: t.TempDir(), RootSessionID: identifier.MustNewSessionID()}
		scratch, err := NewSessionScratch(base, workspace)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = scratch.Cleanup() }()
		ref := ScratchReference{Dir: scratch.Dir, Kind: "unsandboxed"}
		if err := scratch.Pin(owner, ref); err != nil {
			t.Fatal(err)
		}
		// The live lease is still held (Retain was not called), so the release
		// must skip the directory without reporting a failure.
		if err := ReleaseScratchRetention(owner); err != nil {
			t.Fatalf("a held lease must not be reported as a release failure: %v", err)
		}
	})

	t.Run("non_contention_error_reported", func(t *testing.T) {
		base, workspace := scratchRetentionBase(t)
		owner := ScratchOwner{StateDir: t.TempDir(), RootSessionID: identifier.MustNewSessionID()}
		scratch, err := NewSessionScratch(base, workspace)
		if err != nil {
			t.Fatal(err)
		}
		ref := ScratchReference{Dir: scratch.Dir, Kind: "unsandboxed"}
		if err := scratch.Pin(owner, ref); err != nil {
			t.Fatal(err)
		}
		if err := scratch.Retain(); err != nil {
			t.Fatal(err)
		}
		// Replace the lease file with a directory: acquireScratchLease's
		// open(O_RDWR) of a directory fails for a reason that is not contention
		// (EWOULDBLOCK/EAGAIN), so the release must surface it.
		leasePath := filepath.Join(scratch.Dir, sessionScratchLeaseName)
		if err := os.Remove(leasePath); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(leasePath, 0o700); err != nil {
			t.Fatal(err)
		}
		err = ReleaseScratchRetention(owner)
		if err == nil {
			t.Fatal("non-contention lease failure was silently swallowed as contention")
		}
		if !strings.Contains(err.Error(), scratch.Dir) {
			t.Fatalf("reported error does not identify the affected directory: %v", err)
		}
	})
}

// TestScratchRetentionPinFailurePreventsLeaseRelease proves a failed pin leaves
// the live ownership untouched: the lease stays held and no reference is
// committed, so the allocation cannot be silently collected around failure.
func TestScratchRetentionPinFailurePreventsLeaseRelease(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := ScratchOwner{StateDir: t.TempDir(), RootSessionID: identifier.MustNewSessionID()}
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	// A reference naming a different directory must be refused, not written.
	if err := scratch.Pin(owner, ScratchReference{Dir: filepath.Join(base, "elsewhere"), Kind: "sandbox"}); err == nil {
		t.Fatal("Pin accepted a reference for a foreign directory")
	}
	if scratch.lease == nil {
		t.Fatal("failed Pin released the live scratch lease")
	}
	manifest, err := LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.References) != 0 {
		t.Fatalf("failed Pin committed references: %+v", manifest.References)
	}
	if _, err := os.Stat(filepath.Join(scratch.Dir, scratchPinName)); !os.IsNotExist(err) {
		t.Fatalf("failed Pin wrote a directory pin: %v", err)
	}
}

// TestScratchRetentionRestoreSweepBothOrders proves the retention check and
// removal are ordered safely: sweep-then-open and open-then-sweep both preserve
// the artifact, and open refuses once the owner's manifest is released.
func TestScratchRetentionRestoreSweepBothOrders(t *testing.T) {
	for _, order := range []string{"sweep-open", "open-sweep"} {
		t.Run(order, func(t *testing.T) {
			base, workspace := scratchRetentionBase(t)
			owner := ScratchOwner{StateDir: t.TempDir(), RootSessionID: identifier.MustNewSessionID()}
			scratch, err := NewSessionScratch(base, workspace)
			if err != nil {
				t.Fatal(err)
			}
			ref := ScratchReference{Dir: scratch.Dir, Kind: "unsandboxed"}
			artifact := filepath.Join(scratch.Dir, "keep.bin")
			if err := os.WriteFile(artifact, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := scratch.Pin(owner, ref); err != nil {
				t.Fatal(err)
			}
			if err := scratch.Retain(); err != nil {
				t.Fatal(err)
			}
			aged := time.Now().Add(-2 * crashedSessionScratchMaxAge)
			if err := os.Chtimes(scratch.Dir, aged, aged); err != nil {
				t.Fatal(err)
			}
			if order == "sweep-open" {
				if err := SweepCrashedSessionScratch(workspace); err != nil {
					t.Fatal(err)
				}
			}
			restored, err := OpenRetainedSessionScratch(owner, ref)
			if err != nil {
				t.Fatalf("open retained scratch: %v", err)
			}
			if order == "open-sweep" {
				if err := SweepCrashedSessionScratch(workspace); err != nil {
					t.Fatal(err)
				}
			}
			if got, err := os.ReadFile(artifact); err != nil || string(got) != "keep" {
				t.Fatalf("artifact after %s: %q err=%v", order, got, err)
			}
			if err := restored.Retain(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestScratchRetentionConflictingOrUnreadablePin proves a malformed or
// conflicting pin conservatively retains its directory and surfaces a bounded
// diagnostic instead of being treated as collectible.
func TestScratchRetentionConflictingOrUnreadablePin(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	dir := filepath.Join(base, sessionScratchPrefix+"conflict")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, scratchPinName), []byte("{not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	aged := time.Now().Add(-2 * crashedSessionScratchMaxAge)
	if err := os.Chtimes(dir, aged, aged); err != nil {
		t.Fatal(err)
	}
	err := SweepCrashedSessionScratch(workspace)
	if err == nil {
		t.Fatal("sweep accepted a malformed pin with no diagnostic")
	}
	if !strings.Contains(err.Error(), "retention pin") {
		t.Fatalf("diagnostic did not name the retention pin: %v", err)
	}
	if _, statErr := os.Stat(dir); statErr != nil {
		t.Fatalf("malformed pin did not conservatively retain its directory: %v", statErr)
	}
}

// TestScratchRetentionPinBeforeReferenceOrdering proves the directory pin is
// durable before the manifest reference: after Pin, both exist, and each names
// the same immutable identity.
func TestScratchRetentionPinBeforeReferenceOrdering(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := ScratchOwner{StateDir: t.TempDir(), RootSessionID: identifier.MustNewSessionID()}
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	ref := ScratchReference{Dir: scratch.Dir, Kind: "sandbox"}
	if err := scratch.Pin(owner, ref); err != nil {
		t.Fatal(err)
	}
	pinned, err := readScratchDirectoryPin(scratch.Dir)
	if err != nil {
		t.Fatalf("directory pin: %v", err)
	}
	if pinned.Owner != owner || pinned.Dir != filepath.Clean(scratch.Dir) || pinned.Kind != ref.Kind {
		t.Fatalf("pin identity = %+v", pinned)
	}
	manifest, err := LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.References) != 1 || filepath.Clean(manifest.References[0].Dir) != filepath.Clean(ref.Dir) {
		t.Fatalf("manifest references = %+v", manifest.References)
	}
}

// TestScratchRetentionFailedManifestPublicationRemovesThePin is the regression
// test for the unreclaimable-pin leak. Pin writes the directory pin FIRST (so a
// concurrent collector cannot collect the directory in the window before the
// manifest reference exists) and publishes the reference second. When that
// publication fails after the pin is durable, the pin used to survive with no
// manifest reference, and ReleaseScratchRetention only removes pins listed in
// manifest.References, so the directory stayed pinned against collection
// forever. A failed publication must leave the allocation exactly as the failed
// call found it: no pin, no reference, and therefore collectible again.
//
// The failure is a real filesystem condition, not a hook: a successful first Pin
// creates the retention directory, lock and manifest, and chmod'ing that
// directory to 0o500 leaves acquireScratchRetentionLock working (the lock file
// already exists and the search bit is intact) and writeScratchDirectoryPin
// working (a pin lives in the scratch directory) while
// atomicWritePrivateFile's os.CreateTemp of the manifest temp file fails EACCES.
func TestScratchRetentionFailedManifestPublicationRemovesThePin(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a read-only retention directory cannot be created as root")
	}
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	// A first successful pin creates the retention directory, its lock and the
	// manifest the second call will fail to extend.
	existing := pinnedScratch(t, base, workspace, owner, ScratchKindSandbox)
	failing, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = failing.Cleanup() })
	retentionDir := scratchRetentionDir(owner)
	if err := os.Chmod(retentionDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(retentionDir, 0o700) })

	err = failing.Pin(owner, ScratchReference{Dir: failing.Dir, Kind: ScratchKindUnsandboxed})
	if err == nil {
		t.Fatal("Pin succeeded although the manifest could not be published")
	}
	pinPath := filepath.Join(failing.Dir, scratchPinName)
	if _, statErr := os.Stat(pinPath); !os.IsNotExist(statErr) {
		t.Fatalf("a failed manifest publication left the pin %q behind: the directory is pinned against collection and no manifest reference lets ReleaseScratchRetention reclaim it (stat err = %v)", pinPath, statErr)
	}
	manifest, err := LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.References) != 1 || filepath.Clean(manifest.References[0].Dir) != filepath.Clean(existing.Dir) {
		t.Fatalf("failed publication changed the manifest references: %+v", manifest.References)
	}
	// The preexisting pin the failed call was never allowed to touch is intact.
	if pin, pinErr := readScratchDirectoryPin(existing.Dir); pinErr != nil || pin.Owner != owner {
		t.Fatalf("failed publication damaged the preexisting pin: %+v err=%v", pin, pinErr)
	}
	// The consequence: ordinary age-based collection can now reclaim the
	// directory, which it could not while the orphan pin outlived the call.
	if err := failing.Retain(); err != nil {
		t.Fatal(err)
	}
	aged := time.Now().Add(-2 * crashedSessionScratchMaxAge)
	if err := os.Chtimes(failing.Dir, aged, aged); err != nil {
		t.Fatal(err)
	}
	if err := SweepCrashedSessionScratch(workspace); err != nil {
		t.Fatalf("sweep after the failed publication: %v", err)
	}
	if _, statErr := os.Stat(failing.Dir); !os.IsNotExist(statErr) {
		t.Fatalf("the directory orphaned by the failed publication was not reclaimed: %v", statErr)
	}
}

// TestScratchRetentionRollbackKeepsAPublishedPin proves the rollback's
// published-reference guard: atomicWritePrivateFile commits the manifest rename
// before it fsyncs the containing directory, so a writeScratchRetention error
// can arrive after the reference is already durable. Removing the pin then would
// leave a directory collectible while the manifest still claims it, so the
// rollback must re-read the manifest and leave a published pin exactly as it is.
func TestScratchRetentionRollbackKeepsAPublishedPin(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratch := pinnedScratch(t, base, workspace, owner, ScratchKindSandbox)
	dir, err := canonicalScratchPath(scratch.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := rollbackUnpublishedScratchPin(owner, dir, ScratchKindSandbox); err != nil {
		t.Fatalf("rollback reported an error for a published pin: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, scratchPinName)); statErr != nil {
		t.Fatalf("rollback removed a pin the manifest still references: %v", statErr)
	}
	retained, retainErr := ScratchDirectoryRetained(dir)
	if retainErr != nil || !retained {
		t.Fatalf("published directory = retained %v err %v, want retained with no diagnostic", retained, retainErr)
	}
}

// TestScratchRetentionPinScratchBindingRollsBackAFailedCallsPins proves the
// atomic writer leaves no durable trace when a later allocation's pin fails: the
// earlier allocation's directory pin, written before the failure, is removed
// again, no reference is appended and the binding is not published, so the
// allocation is exactly as collectible as the failed call found it.
func TestScratchRetentionPinScratchBindingRollsBackAFailedCallsPins(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a read-only allocation directory cannot be created as root")
	}
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	first, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Cleanup() })
	second, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Cleanup() })
	// A read-only allocation directory denies the pin file its write. The kinds
	// are pinned in sorted order, so first is pinned before second fails.
	if err := os.Chmod(second.Dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(second.Dir, 0o700) })

	err = PinScratchBinding(owner, retentionBinding("E0", owner.RootSessionID, workspace, nil), map[string]*SessionScratch{
		ScratchKindSandbox:     first,
		ScratchKindUnsandboxed: second,
	}, nil)
	if err == nil || !strings.Contains(err.Error(), second.Dir) {
		t.Fatalf("PinScratchBinding error = %v, want the failed pin of %q (the allocation pinned second)", err, second.Dir)
	}
	manifest, err := LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.References) != 0 || len(manifest.Bindings) != 0 {
		t.Fatalf("failed call left durable state: references=%+v bindings=%+v", manifest.References, manifest.Bindings)
	}
	if _, statErr := os.Stat(filepath.Join(first.Dir, scratchPinName)); !os.IsNotExist(statErr) {
		t.Fatalf("the pin of the allocation pinned before the failure survived it: %v", statErr)
	}
}

func retentionOwner(t *testing.T) ScratchOwner {
	t.Helper()
	return ScratchOwner{StateDir: t.TempDir(), RootSessionID: identifier.MustNewSessionID()}
}

func pinnedScratch(t *testing.T, base, workspace string, owner ScratchOwner, kind string) *SessionScratch {
	t.Helper()
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	if err := scratch.Pin(owner, ScratchReference{Dir: scratch.Dir, Kind: kind}); err != nil {
		t.Fatal(err)
	}
	return scratch
}

func retentionBinding(bindingID, ownerSessionID, workingDir string, slots map[string]ScratchSlot) ScratchBinding {
	cloned := make(map[string]ScratchSlot, len(slots))
	maps.Copy(cloned, slots)
	return ScratchBinding{BindingID: bindingID, OwnerSessionID: ownerSessionID, WorkingDir: workingDir, Slots: cloned}
}

func retentionBindingByID(t *testing.T, owner ScratchOwner, bindingID string) ScratchBinding {
	t.Helper()
	manifest, err := LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	for _, binding := range manifest.Bindings {
		if binding.BindingID == bindingID {
			return binding
		}
	}
	t.Fatalf("binding %q missing from manifest: %+v", bindingID, manifest.Bindings)
	return ScratchBinding{}
}

// TestScratchRetentionStaleRetryKeepsConcurrentMint proves plan 648's rebase
// rule: a stale retry that still names only the caller's observed slot must not
// erase a slot a concurrent writer minted into the same binding.
func TestScratchRetentionStaleRetryKeepsConcurrentMint(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	a := pinnedScratch(t, base, workspace, owner, ScratchKindSandbox)
	b := pinnedScratch(t, base, workspace, owner, ScratchKindUnsandboxed)
	consumer := ScratchConsumerBinding{SessionID: "R", CurrentBindingID: "E0"}
	observed := retentionBinding("E0", "R", workspace, map[string]ScratchSlot{
		ScratchKindSandbox: {Dir: a.Dir, OwnsLease: true},
	})
	if err := UpsertScratchBinding(owner, observed, consumer); err != nil {
		t.Fatalf("seed E0/A: %v", err)
	}
	// A concurrent writer mints B into E0.
	if err := UpsertScratchBinding(owner, retentionBinding("E0", "R", workspace, map[string]ScratchSlot{
		ScratchKindSandbox:     {Dir: a.Dir, OwnsLease: true},
		ScratchKindUnsandboxed: {Dir: b.Dir, OwnsLease: true},
	}), consumer); err != nil {
		t.Fatalf("concurrent mint B: %v", err)
	}
	// The stale caller retries its observed record, which still names only A.
	if err := UpsertScratchBinding(owner, observed, consumer); err != nil {
		t.Fatalf("stale retry: %v", err)
	}
	e0 := retentionBindingByID(t, owner, "E0")
	slot, ok := e0.Slots[ScratchKindUnsandboxed]
	if !ok || filepath.Clean(slot.Dir) != filepath.Clean(b.Dir) || !slot.OwnsLease {
		t.Fatalf("stale retry erased the concurrently minted slot: %+v", e0.Slots)
	}
	if slotA := e0.Slots[ScratchKindSandbox]; filepath.Clean(slotA.Dir) != filepath.Clean(a.Dir) {
		t.Fatalf("stale retry changed A's slot: %+v", e0.Slots)
	}
}

// TestScratchRetentionMergedMoveKeepsBothCurrentSlots proves plan 646/648's
// single-transaction move is accepted and preserves both allocations: E0 keeps
// B while E1 takes A's owning slot.
func TestScratchRetentionMergedMoveKeepsBothCurrentSlots(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	a := pinnedScratch(t, base, workspace, owner, ScratchKindSandbox)
	b := pinnedScratch(t, base, workspace, owner, ScratchKindUnsandboxed)
	consumer := ScratchConsumerBinding{SessionID: "R", CurrentBindingID: "E0"}
	if err := UpsertScratchBinding(owner, retentionBinding("E0", "R", workspace, map[string]ScratchSlot{
		ScratchKindSandbox: {Dir: a.Dir, OwnsLease: true},
	}), consumer); err != nil {
		t.Fatalf("seed E0/A: %v", err)
	}
	if err := UpsertScratchBinding(owner, retentionBinding("E0", "R", workspace, map[string]ScratchSlot{
		ScratchKindSandbox:     {Dir: a.Dir, OwnsLease: true},
		ScratchKindUnsandboxed: {Dir: b.Dir, OwnsLease: true},
	}), consumer); err != nil {
		t.Fatalf("concurrent mint B: %v", err)
	}
	manifest, err := LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	err = UpdateScratchBindings(owner, manifest.Revision, []ScratchBinding{
		retentionBinding("E0", "R", workspace, map[string]ScratchSlot{
			ScratchKindUnsandboxed: {Dir: b.Dir, OwnsLease: true},
		}),
		retentionBinding("E1", "R", workspace, map[string]ScratchSlot{
			ScratchKindSandbox: {Dir: a.Dir, OwnsLease: true},
		}),
	}, []ScratchConsumerBinding{{SessionID: "R", CurrentBindingID: "E1"}})
	if err != nil {
		t.Fatalf("single-transaction move rejected: %v", err)
	}
	e0 := retentionBindingByID(t, owner, "E0")
	e1 := retentionBindingByID(t, owner, "E1")
	if slot := e0.Slots[ScratchKindUnsandboxed]; !slot.OwnsLease || filepath.Clean(slot.Dir) != filepath.Clean(b.Dir) {
		t.Fatalf("E0 did not keep B: %+v", e0.Slots)
	}
	if _, stillOwns := e0.Slots[ScratchKindSandbox]; stillOwns {
		t.Fatalf("E0 still owns A after the move: %+v", e0.Slots)
	}
	if slot := e1.Slots[ScratchKindSandbox]; !slot.OwnsLease || filepath.Clean(slot.Dir) != filepath.Clean(a.Dir) {
		t.Fatalf("E1 did not take A's owning slot: %+v", e1.Slots)
	}
}

// TestScratchRetentionOpenRevalidatesAfterConcurrentRelease proves Open
// revalidates the tombstone and pin after it acquires the directory lease: a
// release that commits between the initial validation and the lease acquisition
// must not yield a usable handle for an allocation the release already
// tombstoned.
func TestScratchRetentionOpenRevalidatesAfterConcurrentRelease(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := ScratchOwner{StateDir: t.TempDir(), RootSessionID: identifier.MustNewSessionID()}
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	ref := ScratchReference{Dir: scratch.Dir, Kind: "unsandboxed"}
	if err := scratch.Pin(owner, ref); err != nil {
		t.Fatal(err)
	}
	if err := scratch.Retain(); err != nil {
		t.Fatal(err)
	}
	// Simulate a terminal release that interleaved after the initial validation
	// but before the lease acquisition. It bypasses the manifest lock that the
	// real ReleaseScratchRetention holds, which is exactly the stale-validation
	// window the post-lease revalidation must close.
	scratchRetentionOpenBeforeLease = func() {
		manifest, err := LoadScratchRetention(owner)
		if err != nil {
			t.Errorf("load manifest in hook: %v", err)
			return
		}
		manifest.Released = true
		manifest.Revision++
		if err := writeScratchRetention(owner, manifest); err != nil {
			t.Errorf("write tombstone in hook: %v", err)
			return
		}
		if err := os.Remove(filepath.Join(scratch.Dir, scratchPinName)); err != nil {
			t.Errorf("remove pin in hook: %v", err)
		}
	}
	t.Cleanup(func() { scratchRetentionOpenBeforeLease = nil })
	if _, err := OpenRetainedSessionScratch(owner, ref); err == nil {
		t.Fatal("Open returned a handle after a concurrent release tombstoned the manifest and removed the pin")
	}
}

// TestScratchRetentionOpenRefusesWhileManifestLocked proves opening is
// serialized with a terminal release under the manifest lock: while another
// writer holds that lock (as ReleaseScratchRetention does across its tombstone
// and pin removal), Open must not acquire the directory lease and return a
// handle from a stale validation.
func TestScratchRetentionOpenRefusesWhileManifestLocked(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := ScratchOwner{StateDir: t.TempDir(), RootSessionID: identifier.MustNewSessionID()}
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	ref := ScratchReference{Dir: scratch.Dir, Kind: "unsandboxed"}
	if err := scratch.Pin(owner, ref); err != nil {
		t.Fatal(err)
	}
	if err := scratch.Retain(); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireScratchRetentionLock(owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRetainedSessionScratch(owner, ref); err == nil {
		_ = lock.Release()
		t.Fatal("Open returned a handle while another writer held the manifest lock")
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	restored, err := OpenRetainedSessionScratch(owner, ref)
	if err != nil {
		t.Fatalf("open after the lock was released: %v", err)
	}
	if err := restored.Retain(); err != nil {
		t.Fatal(err)
	}
}

// TestScratchMutationsRefuseReleasedManifest proves the round-11 terminal-write
// hole: once ReleaseScratchRetention commits the tombstone, every binding
// mutation must refuse the manifest. A losing writer retrying through a
// lock-contention window must not be able to resurrect bindings or add
// references to an authority that already authorized collection — and the
// bounded retry must treat the refusal as terminal, never as contention.
func TestScratchMutationsRefuseReleasedManifest(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	original, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = original.Cleanup() })
	binding := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: original.Dir, OwnsLease: true}})
	if err := PinScratchBinding(owner, binding, map[string]*SessionScratch{ScratchKindSandbox: original}, nil); err != nil {
		t.Fatalf("pin the pre-release binding: %v", err)
	}
	if err := original.Retain(); err != nil {
		t.Fatal(err)
	}
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("terminal release: %v", err)
	}
	manifest, err := LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.Released {
		t.Fatal("fixture expected the Released tombstone")
	}

	fresh, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fresh.Cleanup() })
	consumer := ScratchConsumerBinding{SessionID: owner.RootSessionID, CurrentBindingID: binding.BindingID}

	if err := PinScratchBinding(owner, binding, map[string]*SessionScratch{ScratchKindSandbox: fresh}, nil); !errors.Is(err, ErrScratchRetentionReleased) {
		t.Fatalf("PinScratchBinding on a released manifest returned %v; the tombstone must close the manifest to writers", err)
	}
	if err := UpsertScratchBinding(owner, binding, consumer); !errors.Is(err, ErrScratchRetentionReleased) {
		t.Fatalf("UpsertScratchBinding on a released manifest returned %v; the tombstone must close the manifest to writers", err)
	}
	if err := UpsertScratchBindingOnly(owner, binding); !errors.Is(err, ErrScratchRetentionReleased) {
		t.Fatalf("UpsertScratchBindingOnly on a released manifest returned %v; the tombstone must close the manifest to writers", err)
	}
	if err := UpdateScratchBindings(owner, manifest.Revision, []ScratchBinding{binding}, nil); !errors.Is(err, ErrScratchRetentionReleased) {
		t.Fatalf("UpdateScratchBindings on a released manifest returned %v; the tombstone must close the manifest to writers", err)
	}

	var retryCalls int
	retryErr := RetryScratchLockContention(func() error {
		retryCalls++
		return ErrScratchRetentionReleased
	})
	if !errors.Is(retryErr, ErrScratchRetentionReleased) || retryCalls != 1 {
		t.Fatalf("the bounded retry treated a released manifest as contention (%d calls, err %v); the refusal is terminal", retryCalls, retryErr)
	}
}

// TestResetReleasedRetriesLockContention pins the round-12 retry gap: the
// released-manifest reset takes the manifest's fail-fast update lock, and an
// in-process writer holding it must refuse the reset only transiently — a
// single attempt would fail the restore the reset is part of.
func TestResetReleasedRetriesLockContention(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	original, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = original.Cleanup() })
	binding := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: original.Dir, OwnsLease: true}})
	if err := PinScratchBinding(owner, binding, map[string]*SessionScratch{ScratchKindSandbox: original}, nil); err != nil {
		t.Fatalf("pin the pre-release binding: %v", err)
	}
	if err := original.Retain(); err != nil {
		t.Fatal(err)
	}
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("terminal release: %v", err)
	}

	takeLock := make(chan struct{})
	lockTaken := make(chan struct{})
	lockReleased := make(chan struct{})
	go func() {
		<-takeLock
		_ = WithScratchRetentionLock(owner, func() error {
			close(lockTaken)
			// A bounded hold: longer than the reset's first attempt, shorter
			// than the bounded retry's whole span.
			time.Sleep(2 * time.Millisecond)
			return nil
		})
		close(lockReleased)
	}()
	close(takeLock)
	<-lockTaken
	fresh, _, err := ResetScratchRetentionIfReleased(owner)
	if err != nil {
		t.Fatalf("the reset gave up on a transient lock refusal: %v", err)
	}
	<-lockReleased
	if fresh.Released || len(fresh.References) != 0 {
		t.Fatalf("the reset returned %+v, want a fresh empty manifest", fresh)
	}
}

// TestResetReleasedReconcilesLeftoverPins pins the round-12 collector hazard:
// the terminal release deliberately leaves pins whose leases are contended,
// relying on the tombstone to read them as collectible. A reset that simply
// dropped them would strand each surviving pin against a manifest with no
// matching reference — retained forever with a diagnostic on every sweep. The
// reset must finish the release for pins whose lease is now free, and carry a
// reference for the pins still held.
func TestResetReleasedReconcilesLeftoverPins(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	binding := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratch.Dir, OwnsLease: true}})
	// The lease stays held through the terminal release, so the pin is left
	// behind on a tombstoned manifest.
	if err := PinScratchBinding(owner, binding, map[string]*SessionScratch{ScratchKindSandbox: scratch}, nil); err != nil {
		t.Fatalf("pin the pre-release binding: %v", err)
	}
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("terminal release: %v", err)
	}
	if _, err := os.Stat(filepath.Join(scratch.Dir, scratchPinName)); err != nil {
		t.Fatalf("fixture expected the contended pin to survive the release: %v", err)
	}

	// Settled before the reset: the reset finishes the release and the pin
	// dies with the manifest.
	if err := scratch.Retain(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResetScratchRetentionIfReleased(owner); err != nil {
		t.Fatalf("reset over the settled leftover pin: %v", err)
	}
	if _, err := os.Stat(filepath.Join(scratch.Dir, scratchPinName)); !os.IsNotExist(err) {
		t.Fatalf("the reset left a collectible directory's pin in place: %v", err)
	}
	retained, err := ScratchDirectoryRetained(scratch.Dir)
	if err != nil || retained {
		t.Fatalf("the settled dir must read as ordinary collectible after the reset: retained=%v err=%v", retained, err)
	}

	// Still held at the reset: the pin/reference pair must stay coherent —
	// retained silently, never the no-matching-reference diagnostic.
	held, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Cleanup() })
	second := retentionBinding("E1", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: held.Dir, OwnsLease: true}})
	if err := PinScratchBinding(owner, second, map[string]*SessionScratch{ScratchKindSandbox: held}, nil); err != nil {
		t.Fatalf("pin the second pre-release binding: %v", err)
	}
	// The round-16 reset only carries a reference whose graph rows can
	// travel with it: the still-held binding's consumer is what makes the
	// carried pair one the root's own reader accepts.
	if err := UpsertScratchBinding(owner, second, ScratchConsumerBinding{SessionID: "consumer-reconcile", CurrentBindingID: second.BindingID}); err != nil {
		t.Fatalf("publish the second binding's consumer: %v", err)
	}
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("second terminal release: %v", err)
	}
	fresh, _, err := ResetScratchRetentionIfReleased(owner)
	if err != nil {
		t.Fatalf("reset over the still-held pin: %v", err)
	}
	carried := false
	for _, ref := range fresh.References {
		if filepath.Clean(ref.Dir) == filepath.Clean(held.Dir) && ref.Kind == ScratchKindSandbox {
			carried = true
		}
	}
	if !carried {
		t.Fatalf("the reset dropped the reference for the still-held pin %q; the collector would retain it forever", held.Dir)
	}
	retained, err = ScratchDirectoryRetained(held.Dir)
	if err != nil {
		t.Fatalf("the still-held dir's pin/reference pair reads as incoherent: %v", err)
	}
	if !retained {
		t.Fatal("a pin still held by a live owner must read as retained")
	}
}

// TestScratchPinRejectsReleasedManifest pins the round-13 writer gap: Pin
// still published references and pins onto a terminally released manifest —
// the one writer left outside the ErrScratchRetentionReleased discipline.
func TestScratchPinRejectsReleasedManifest(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	binding := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratch.Dir, OwnsLease: true}})
	if err := PinScratchBinding(owner, binding, map[string]*SessionScratch{ScratchKindSandbox: scratch}, nil); err != nil {
		t.Fatalf("pin the pre-release binding: %v", err)
	}
	// The scratch's own lease stays held, so the terminal release leaves the
	// pin behind on the tombstone.
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("terminal release: %v", err)
	}
	err = scratch.Pin(owner, ScratchReference{Dir: scratch.Dir, Kind: ScratchKindSandbox})
	if !errors.Is(err, ErrScratchRetentionReleased) {
		t.Fatalf("pin onto a terminally released manifest: %v, want %v", err, ErrScratchRetentionReleased)
	}
}

// TestResetReleasedTreatsCollectedReferenceAsStale pins the round-13 reset
// gap: a reference whose directory the collector already removed from under
// the tombstone must die with the manifest, not fail the reset on a lease
// that can never be opened again.
func TestResetReleasedTreatsCollectedReferenceAsStale(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	binding := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratch.Dir, OwnsLease: true}})
	if err := PinScratchBinding(owner, binding, map[string]*SessionScratch{ScratchKindSandbox: scratch}, nil); err != nil {
		t.Fatalf("pin the pre-release binding: %v", err)
	}
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("terminal release: %v", err)
	}
	if _, err := os.Stat(filepath.Join(scratch.Dir, scratchPinName)); err != nil {
		t.Fatalf("fixture expected the contended pin to survive the release: %v", err)
	}
	// Settle the lease, then let the collector take the directory: the pin
	// dies with it and only the tombstoned reference remains.
	if err := scratch.Retain(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(scratch.Dir); err != nil {
		t.Fatal(err)
	}

	fresh, _, err := ResetScratchRetentionIfReleased(owner)
	if err != nil {
		t.Fatalf("the reset failed on an already-collected reference: %v", err)
	}
	if fresh.Released {
		t.Fatal("the reset left the manifest tombstoned")
	}
	if len(fresh.References) != 0 {
		t.Fatalf("the reset carried a stale reference for a collected directory: %+v", fresh.References)
	}
}

// TestResetReleasedReleasesLeaseWhenPinRemoveFails pins the round-13 lease
// leak: when the reset cannot remove a settled pin, it must still release the
// per-directory retention lease it took, or the leaked flock holds the
// directory against every later writer.
func TestResetReleasedReleasesLeaseWhenPinRemoveFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the read-only directory fixture cannot make os.Remove fail on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("the read-only directory fixture cannot make os.Remove fail for root")
	}
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	t.Cleanup(func() { _ = os.Chmod(scratch.Dir, 0o700) })
	binding := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratch.Dir, OwnsLease: true}})
	if err := PinScratchBinding(owner, binding, map[string]*SessionScratch{ScratchKindSandbox: scratch}, nil); err != nil {
		t.Fatalf("pin the pre-release binding: %v", err)
	}
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("terminal release: %v", err)
	}
	// Settled: the lease is free for the reset to take and the pin is left
	// for it to remove. A read-only directory makes that removal fail while
	// the pin itself still reads back valid.
	if err := scratch.Retain(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(scratch.Dir, 0o500); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResetScratchRetentionIfReleased(owner); err == nil {
		t.Fatal("expected the pin-remove failure to surface from the reset")
	}
	probe, _, err := acquireScratchLease(filepath.Join(scratch.Dir, sessionScratchLeaseName))
	if err != nil {
		t.Fatalf("the reset leaked the retention lease: %v", err)
	}
	if err := probe.Release(); err != nil {
		t.Fatal(err)
	}
}

// TestScratchLockContentionDelayDoublesToACap pins the documented spacing
// contract: double from 1ms up to the 8ms ceiling and never past it, so a deep
// retry's waits stay bounded.
func TestScratchLockContentionDelayDoublesToACap(t *testing.T) {
	for _, tc := range []struct {
		attempt int
		want    time.Duration
	}{
		{0, time.Millisecond},
		{1, 2 * time.Millisecond},
		{2, 4 * time.Millisecond},
		{3, 8 * time.Millisecond},
		{4, 8 * time.Millisecond},
		{10, 8 * time.Millisecond},
	} {
		if got := ScratchLockContentionDelay(tc.attempt); got != tc.want {
			t.Fatalf("ScratchLockContentionDelay(%d) = %v, want %v", tc.attempt, got, tc.want)
		}
	}
}

// TestScratchOpenReportsReleasedManifestTyped pins the round-14 typing gap:
// OpenRetainedSessionScratch used to report a terminally released manifest
// with an untyped error, so the refresh could not recognize the seal and
// failed the restore racing a terminal close.
func TestScratchOpenReportsReleasedManifestTyped(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	binding := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratch.Dir, OwnsLease: true}})
	if err := PinScratchBinding(owner, binding, map[string]*SessionScratch{ScratchKindSandbox: scratch}, nil); err != nil {
		t.Fatalf("pin the pre-release binding: %v", err)
	}
	// The scratch's own lease stays held, so the terminal release leaves the
	// reference and pin behind on the tombstone.
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("terminal release: %v", err)
	}
	_, err = OpenRetainedSessionScratch(owner, ScratchReference{Dir: scratch.Dir, Kind: ScratchKindSandbox})
	if !errors.Is(err, ErrScratchRetentionReleased) {
		t.Fatalf("open on a terminally released manifest: %v, want the typed %v", err, ErrScratchRetentionReleased)
	}
}

// TestScratchUpsertAtRevisionRefusesMovedManifest pins the round-14 revision
// check's contract: an upsert built from a superseded snapshot must refuse
// with the stale-revision sentinel instead of merging, and one built from the
// manifest as it stands must succeed.
func TestScratchUpsertAtRevisionRefusesMovedManifest(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	binding := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratch.Dir, OwnsLease: true}})
	if err := PinScratchBinding(owner, binding, map[string]*SessionScratch{ScratchKindSandbox: scratch}, nil); err != nil {
		t.Fatalf("pin the binding: %v", err)
	}
	snapshot, err := LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	// A concurrent writer moves the manifest after the snapshot.
	rival, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rival.Cleanup() })
	rivalBinding := retentionBinding("E1", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: rival.Dir, OwnsLease: true}})
	if err := PinScratchBinding(owner, rivalBinding, map[string]*SessionScratch{ScratchKindSandbox: rival}, nil); err != nil {
		t.Fatalf("pin the rival binding: %v", err)
	}
	consumer := ScratchConsumerBinding{SessionID: "consumer-test", CurrentBindingID: binding.BindingID}
	err = UpsertScratchBindingAtRevision(owner, binding, consumer, snapshot.Revision)
	if !errors.Is(err, ErrScratchRetentionStaleRevision) {
		t.Fatalf("upsert from a superseded snapshot: %v, want %v", err, ErrScratchRetentionStaleRevision)
	}
	// The same upsert derived from the manifest as it now stands succeeds.
	current, err := LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := UpsertScratchBindingAtRevision(owner, binding, consumer, current.Revision); err != nil {
		t.Fatalf("upsert at the current revision: %v", err)
	}
}

// TestScratchUpsertAtRevisionChecksRevisionsAboveMaxInt64 pins round 24's Low:
// the at-revision upsert carried its expected revision through an int64 and
// read a negative narrowing conversion as the "unchecked" sentinel, so
// revisions past MaxInt64 silently bypassed the staleness check the parameter
// exists to enforce. The check must be guarded by an explicit expected/absent
// flag, never by the sign of a conversion.
func TestScratchUpsertAtRevisionChecksRevisionsAboveMaxInt64(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	binding := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratch.Dir, OwnsLease: true}})
	consumer := ScratchConsumerBinding{SessionID: "consumer-rev-24", CurrentBindingID: binding.BindingID}
	if err := PinScratchBinding(owner, binding, map[string]*SessionScratch{ScratchKindSandbox: scratch}, nil); err != nil {
		t.Fatalf("pin the binding: %v", err)
	}
	manifest, err := LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Revision = math.MaxInt64 + 10
	if err := writeScratchRetention(owner, manifest); err != nil {
		t.Fatalf("force the revision past int64: %v", err)
	}
	err = UpsertScratchBindingAtRevision(owner, binding, consumer, math.MaxInt64+11)
	if !errors.Is(err, ErrScratchRetentionStaleRevision) {
		t.Fatalf("upsert from a superseded revision above MaxInt64: %v, want %v", err, ErrScratchRetentionStaleRevision)
	}
	// The same upsert derived from the manifest as it stands still publishes.
	if err := UpsertScratchBindingAtRevision(owner, binding, consumer, math.MaxInt64+10); err != nil {
		t.Fatalf("upsert at the standing revision above MaxInt64: %v", err)
	}
}

// TestScratchRevalidationReportsReleasedTyped pins the round-15 typing gap: the
// post-lease revalidation reported the tombstone with an untyped error, so the
// refresh's errors.Is decline check missed the exact race it was written for —
// a release committing between the initial validation and the lease
// acquisition — and failed the restore instead of declining to fresh scratch.
func TestScratchRevalidationReportsReleasedTyped(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	binding := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratch.Dir, OwnsLease: true}})
	if err := PinScratchBinding(owner, binding, map[string]*SessionScratch{ScratchKindSandbox: scratch}, nil); err != nil {
		t.Fatalf("pin the pre-release binding: %v", err)
	}
	// The scratch's own lease stays held, so the terminal release leaves the
	// reference and pin behind on the tombstone.
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("terminal release: %v", err)
	}
	err = revalidateRetainedScratchAfterLease(owner, scratch.Dir, ScratchKindSandbox)
	if !errors.Is(err, ErrScratchRetentionReleased) {
		t.Fatalf("revalidation over a released manifest: %v, want the typed %v", err, ErrScratchRetentionReleased)
	}
}

// TestResetReleasedCarriesAValidGraph pins the round-16 reset gap: the reset
// carried references for still-held pins into a fresh manifest that never
// gained a single binding row, committing a graph its own restore validator
// rejects — "references but no binding" — that no later reset would repair
// (Released is false again). A carried reference must travel with the binding
// that owns its directory and the consumer role that names it.
func TestResetReleasedCarriesAValidGraph(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	binding := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratch.Dir, OwnsLease: true}})
	consumer := ScratchConsumerBinding{SessionID: "consumer-graph", CurrentBindingID: binding.BindingID}
	if err := PinScratchBinding(owner, binding, map[string]*SessionScratch{ScratchKindSandbox: scratch}, nil); err != nil {
		t.Fatalf("pin the pre-release binding: %v", err)
	}
	if err := UpsertScratchBinding(owner, binding, consumer); err != nil {
		t.Fatalf("publish the pre-release consumer: %v", err)
	}
	// The scratch's own lease stays held, so the terminal release leaves the
	// pin behind and the reset must carry the reference.
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("terminal release: %v", err)
	}
	fresh, _, err := ResetScratchRetentionIfReleased(owner)
	if err != nil {
		t.Fatalf("reset over the held pin: %v", err)
	}
	if len(fresh.References) == 0 {
		t.Fatal("fixture expected the held pin's reference to carry")
	}
	for _, ref := range fresh.References {
		dir := filepath.Clean(ref.Dir)
		ownerID, ok := leaseOwningBinding(fresh, dir)
		if !ok {
			t.Fatalf("carried reference %q is owned by no binding in the reset manifest %+v", ref.Dir, fresh)
		}
		if _, ok := scratchBindingByID(fresh.Bindings, ownerID); !ok {
			t.Fatalf("the carried reference's owning binding %q is absent from the reset manifest", ownerID)
		}
		named := false
		for _, consumer := range fresh.Consumers {
			if consumer.CurrentBindingID == ownerID {
				named = true
			}
		}
		if !named {
			t.Fatalf("the carried binding %q is named by no consumer in the reset manifest %+v", ownerID, fresh.Consumers)
		}
	}
}

// TestResetReleasedCarriesWrapperBindingsAndTheirConsumers pins round 24's
// second Medium: the reset's carry narrowed to the lease-owning binding, so a
// wrapper-only binding — a distinct consumer's identity for the same retained
// directory — died with the tombstone together with its consumer role. That
// consumer's next restore then found no current binding and minted fresh
// scratch instead of borrowing the directory it still held. The reference must
// travel with every binding whose slot names its directory, not only the lease
// owner.
func TestResetReleasedCarriesWrapperBindingsAndTheirConsumers(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	ownerBinding := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratch.Dir, OwnsLease: true}})
	wrapperBinding := retentionBinding("E1", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratch.Dir, OwnsLease: false}})
	ownerConsumer := ScratchConsumerBinding{SessionID: "consumer-owner-24", CurrentBindingID: ownerBinding.BindingID}
	wrapperConsumer := ScratchConsumerBinding{SessionID: "consumer-wrapper-24", CurrentBindingID: wrapperBinding.BindingID}
	if err := PinScratchBinding(owner, ownerBinding, map[string]*SessionScratch{ScratchKindSandbox: scratch}, nil); err != nil {
		t.Fatalf("pin the lease-owning binding: %v", err)
	}
	if err := UpsertScratchBinding(owner, ownerBinding, ownerConsumer); err != nil {
		t.Fatalf("publish the owning consumer: %v", err)
	}
	if err := UpsertScratchBindingOnly(owner, wrapperBinding); err != nil {
		t.Fatalf("publish the wrapper binding: %v", err)
	}
	manifest, err := LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := UpdateScratchBindings(owner, manifest.Revision, nil, []ScratchConsumerBinding{wrapperConsumer}); err != nil {
		t.Fatalf("publish the wrapper consumer: %v", err)
	}
	// The scratch's own lease stays held, so the terminal release leaves the
	// pin behind and the reset must carry the reference with both identities.
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("terminal release: %v", err)
	}
	fresh, _, err := ResetScratchRetentionIfReleased(owner)
	if err != nil {
		t.Fatalf("reset over the held pin: %v", err)
	}
	var wrapperRow ScratchBinding
	for _, binding := range fresh.Bindings {
		if binding.BindingID == wrapperBinding.BindingID {
			wrapperRow = binding
		}
	}
	slot, hasSlot := wrapperRow.Slots[ScratchKindSandbox]
	if !hasSlot {
		t.Fatalf("the reset dropped the wrapper binding %q whose slot borrows the retained %q", wrapperBinding.BindingID, scratch.Dir)
	}
	if filepath.Clean(slot.Dir) != filepath.Clean(scratch.Dir) || slot.OwnsLease {
		t.Fatalf("the carried wrapper slot is %+v; want the borrow of %q", slot, scratch.Dir)
	}
	var wrapperRole ScratchConsumerBinding
	for _, consumer := range fresh.Consumers {
		if consumer.SessionID == wrapperConsumer.SessionID {
			wrapperRole = consumer
		}
	}
	if wrapperRole.CurrentBindingID != wrapperBinding.BindingID {
		t.Fatalf("the reset dropped the wrapper consumer's role: %+v; want it naming %q", wrapperRole, wrapperBinding.BindingID)
	}
	// The lease owner and its consumer carry exactly as before.
	var ownerRow ScratchBinding
	for _, binding := range fresh.Bindings {
		if binding.BindingID == ownerBinding.BindingID {
			ownerRow = binding
		}
	}
	if _, ok := ownerRow.Slots[ScratchKindSandbox]; !ok {
		t.Fatalf("the reset dropped the owning binding %q", ownerBinding.BindingID)
	}
	named := false
	for _, consumer := range fresh.Consumers {
		if consumer.SessionID == ownerConsumer.SessionID && consumer.CurrentBindingID == ownerBinding.BindingID {
			named = true
		}
	}
	if !named {
		t.Fatalf("the reset dropped the owning consumer %q", ownerConsumer.SessionID)
	}
}

// TestResetReleasedDropsContendedReferenceWithoutPin pins the round-16
// pin-validation gap: the contended-carry path preserved a manifest reference
// without checking its immutable pin still exists, so a crash between pin
// removal and publication left the reset persisting a reference that wedges
// every later restore at its pin verification.
func TestResetReleasedDropsContendedReferenceWithoutPin(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	binding := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratch.Dir, OwnsLease: true}})
	if err := PinScratchBinding(owner, binding, map[string]*SessionScratch{ScratchKindSandbox: scratch}, nil); err != nil {
		t.Fatalf("pin the pre-release binding: %v", err)
	}
	// The lease stays held, so the terminal release leaves the pin — and the
	// crash simulation removes the pin file it would have published.
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("terminal release: %v", err)
	}
	if err := os.Remove(filepath.Join(scratch.Dir, scratchPinName)); err != nil {
		t.Fatal(err)
	}
	fresh, _, err := ResetScratchRetentionIfReleased(owner)
	if err != nil {
		t.Fatalf("reset over the held, unpinned reference: %v", err)
	}
	for _, ref := range fresh.References {
		if filepath.Clean(ref.Dir) == filepath.Clean(scratch.Dir) {
			t.Fatalf("the reset carried a reference whose pin is gone: %+v", fresh.References)
		}
	}
}

// TestResetReleasedCarriesMultiRoleConsumerGraph pins the round-17 consumer
// overwrite: a consumer routinely names several bindings, and narrowing it once
// per carried reference let the second carry overwrite the first's roles.
// The surviving carried binding was then named by no consumer — the exact
// graph the restore reader fails closed on, with Released false again and no
// later reset to repair it.
func TestResetReleasedCarriesMultiRoleConsumerGraph(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratchA, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratchA.Cleanup() })
	scratchB, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratchB.Cleanup() })
	bindingE0 := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratchA.Dir, OwnsLease: true}})
	bindingE1 := retentionBinding("E1", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratchB.Dir, OwnsLease: true}})
	if err := PinScratchBinding(owner, bindingE0, map[string]*SessionScratch{ScratchKindSandbox: scratchA}, nil); err != nil {
		t.Fatalf("pin the E0 binding: %v", err)
	}
	if err := PinScratchBinding(owner, bindingE1, map[string]*SessionScratch{ScratchKindSandbox: scratchB}, nil); err != nil {
		t.Fatalf("pin the E1 binding: %v", err)
	}
	consumer := ScratchConsumerBinding{
		SessionID:             "consumer-multi",
		CurrentBindingID:      bindingE0.BindingID,
		ParentSharedBindingID: bindingE1.BindingID,
	}
	if err := UpsertScratchBinding(owner, bindingE0, consumer); err != nil {
		t.Fatalf("publish the multi-role consumer: %v", err)
	}
	// Both leases stay held, so the terminal release leaves both pins behind
	// and the reset must carry both references with their rows.
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("terminal release: %v", err)
	}
	fresh, _, err := ResetScratchRetentionIfReleased(owner)
	if err != nil {
		t.Fatalf("reset over two held pins: %v", err)
	}
	if len(fresh.References) != 2 {
		t.Fatalf("fixture expected both held references to carry: %+v", fresh.References)
	}
	carried, found := ScratchConsumerBinding{}, false
	for _, row := range fresh.Consumers {
		if row.SessionID == consumer.SessionID {
			carried, found = row, true
		}
	}
	if !found {
		t.Fatalf("the multi-role consumer is absent from the reset manifest: %+v", fresh.Consumers)
	}
	if carried.CurrentBindingID != bindingE0.BindingID || carried.ParentSharedBindingID != bindingE1.BindingID {
		t.Fatalf("the carried consumer lost a role across two carried bindings: %+v", carried)
	}
	for _, binding := range fresh.Bindings {
		if len(consumersNamingScratchBinding(fresh.Consumers, binding.BindingID)) == 0 {
			t.Fatalf("carried binding %q is named by no consumer in the reset manifest %+v", binding.BindingID, fresh.Consumers)
		}
	}
}

// TestResetReleasedDropsContendedReferenceWithForeignPin pins the round-17
// identity gap on the contended carry: the reset validated only the pin's
// existence, so a readable pin belonging to another owner was carried into
// the fresh manifest — wedging every later restore at its pin verification,
// with Released false again and no later reset to repair it.
func TestResetReleasedDropsContendedReferenceWithForeignPin(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	foreign := retentionOwner(t)
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	binding := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratch.Dir, OwnsLease: true}})
	consumer := ScratchConsumerBinding{SessionID: "consumer-foreign", CurrentBindingID: binding.BindingID}
	if err := PinScratchBinding(owner, binding, map[string]*SessionScratch{ScratchKindSandbox: scratch}, nil); err != nil {
		t.Fatalf("pin the pre-release binding: %v", err)
	}
	if err := UpsertScratchBinding(owner, binding, consumer); err != nil {
		t.Fatalf("publish the pre-release consumer: %v", err)
	}
	// The lease stays held, so the terminal release leaves the pin — and the
	// fixture replaces it with a foreign owner's pin for the same directory.
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("terminal release: %v", err)
	}
	if err := os.Remove(filepath.Join(scratch.Dir, scratchPinName)); err != nil {
		t.Fatal(err)
	}
	if err := writeScratchDirectoryPin(scratch.Dir, foreign, ScratchReference{Dir: scratch.Dir, Kind: ScratchKindSandbox}); err != nil {
		t.Fatalf("write the foreign pin: %v", err)
	}
	fresh, _, err := ResetScratchRetentionIfReleased(owner)
	if err != nil {
		t.Fatalf("reset over a foreign held pin: %v", err)
	}
	for _, ref := range fresh.References {
		if filepath.Clean(ref.Dir) == filepath.Clean(scratch.Dir) {
			t.Fatalf("the reset carried a reference whose pin belongs to another owner: %+v", fresh.References)
		}
	}
	// A foreign pin is not this reset's to remove: the file stays for its own
	// manifest.
	if _, err := os.Stat(filepath.Join(scratch.Dir, scratchPinName)); err != nil {
		t.Fatalf("the reset removed a foreign pin it does not own: %v", err)
	}
}

// TestResetReleasedDropsFreeLeaseReferenceWithMismatchedPin covers the
// free-lease branch of the same round-17 identity gap: a readable pin with
// our own owner but a kind this reference cannot verify fell into the old
// carry-everything default, wedging later restores the same way.
func TestResetReleasedDropsFreeLeaseReferenceWithMismatchedPin(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	binding := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratch.Dir, OwnsLease: true}})
	consumer := ScratchConsumerBinding{SessionID: "consumer-mismatch", CurrentBindingID: binding.BindingID}
	if err := PinScratchBinding(owner, binding, map[string]*SessionScratch{ScratchKindSandbox: scratch}, nil); err != nil {
		t.Fatalf("pin the pre-release binding: %v", err)
	}
	if err := UpsertScratchBinding(owner, binding, consumer); err != nil {
		t.Fatalf("publish the pre-release consumer: %v", err)
	}
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("terminal release: %v", err)
	}
	// Free the lease so the reset's free-lease branch takes this reference,
	// then replace the pin with our own pin naming the WRONG kind.
	_ = scratch.Retain()
	if err := os.Remove(filepath.Join(scratch.Dir, scratchPinName)); err != nil {
		t.Fatal(err)
	}
	if err := writeScratchDirectoryPin(scratch.Dir, owner, ScratchReference{Dir: scratch.Dir, Kind: ScratchKindUnsandboxed}); err != nil {
		t.Fatalf("write the kind-mismatched pin: %v", err)
	}
	fresh, _, err := ResetScratchRetentionIfReleased(owner)
	if err != nil {
		t.Fatalf("reset over a mismatched free-lease pin: %v", err)
	}
	for _, ref := range fresh.References {
		if filepath.Clean(ref.Dir) == filepath.Clean(scratch.Dir) {
			t.Fatalf("the reset carried a reference whose pin fails identity verification: %+v", fresh.References)
		}
	}
	// Round 19 flipped this pin's fate: the lease is ours right now, so the
	// reset finishes the release for the malformed pin it owns instead of
	// stranding an orphan the collector conservatively retains, with a
	// diagnostic, forever. The directory becomes ordinary.
	if _, err := os.Stat(filepath.Join(scratch.Dir, scratchPinName)); !os.IsNotExist(err) {
		t.Fatalf("the reset left a malformed pin it owns and could remove: stat got %v, want not exist", err)
	}
}

// TestResetReleasedAbortsOnUnreadablePin pins round 19's Medium: a pin the
// reset cannot read might be anyone's protection, so committing past it could
// strand an orphan the collector conservatively retains forever. The reset
// must abort with the tombstone intact, so a later retry heals a transient
// read failure.
func TestResetReleasedAbortsOnUnreadablePin(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	binding := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratch.Dir, OwnsLease: true}})
	consumer := ScratchConsumerBinding{SessionID: "consumer-unreadable", CurrentBindingID: binding.BindingID}
	if err := PinScratchBinding(owner, binding, map[string]*SessionScratch{ScratchKindSandbox: scratch}, nil); err != nil {
		t.Fatalf("pin the pre-release binding: %v", err)
	}
	if err := UpsertScratchBinding(owner, binding, consumer); err != nil {
		t.Fatalf("publish the pre-release consumer: %v", err)
	}
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("terminal release: %v", err)
	}
	// Free the lease so the reset's free-lease branch takes this reference,
	// then corrupt the pin past reading.
	_ = scratch.Retain()
	if err := os.WriteFile(filepath.Join(scratch.Dir, scratchPinName), []byte("not a pin"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResetScratchRetentionIfReleased(owner); err == nil {
		t.Fatal("expected the unreadable pin to abort the reset")
	}
	manifest, err := LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.Released {
		t.Fatal("the aborted reset committed an unreleased manifest past a pin it could not read")
	}
}

// TestResetReleasedAbortsOnContendedMismatchedOwnPin covers the contended arm
// of the same round-19 finding: our own malformed pin under a lease we do not
// hold must abort the reset too — the contention is transient (a live holder
// releases), so the tombstone's retry reaches this reference through the
// free-lease branch, which removes the malformed pin and finishes the
// release.
func TestResetReleasedAbortsOnContendedMismatchedOwnPin(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	binding := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratch.Dir, OwnsLease: true}})
	consumer := ScratchConsumerBinding{SessionID: "consumer-mismatch-c", CurrentBindingID: binding.BindingID}
	if err := PinScratchBinding(owner, binding, map[string]*SessionScratch{ScratchKindSandbox: scratch}, nil); err != nil {
		t.Fatalf("pin the pre-release binding: %v", err)
	}
	if err := UpsertScratchBinding(owner, binding, consumer); err != nil {
		t.Fatalf("publish the pre-release consumer: %v", err)
	}
	// The lease stays held (the contended branch) and the pin is replaced with
	// our own naming the wrong kind.
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("terminal release: %v", err)
	}
	if err := os.Remove(filepath.Join(scratch.Dir, scratchPinName)); err != nil {
		t.Fatal(err)
	}
	if err := writeScratchDirectoryPin(scratch.Dir, owner, ScratchReference{Dir: scratch.Dir, Kind: ScratchKindUnsandboxed}); err != nil {
		t.Fatalf("write the kind-mismatched pin: %v", err)
	}
	if _, _, err := ResetScratchRetentionIfReleased(owner); err == nil {
		t.Fatal("expected the contended mismatched own pin to abort the reset")
	}
	manifest, err := LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.Released {
		t.Fatal("the aborted reset committed an unreleased manifest past its own malformed pin")
	}
	if _, err := os.Stat(filepath.Join(scratch.Dir, scratchPinName)); err != nil {
		t.Fatalf("the aborted reset disturbed the mismatched pin: %v", err)
	}
}

// TestResetReleasedLeavesTheContendedDyingPinInPlace pins round 25's Medium:
// the pair-death path ran lease-less — its only caller is the reset's
// contended branch — so the reset removed a dying pair's own pin while a live
// holder still held the directory's lease, stripping the protection that
// keeps the collector from sweeping the directory out from under the holder.
// The death now leaves a readable contended pin untouched and drops only the
// pair: the holder keeps its protection, and the pin's protection ends with
// the next terminal release's tombstone, which is what authorizes the
// collector to remove the pin and its directory. (Round 18's abort contract
// for this branch — refuse to commit past a removal that fails — retired with
// the removal itself: the death never attempts one under contention, and the
// reinstall that owns this path re-pins the same identity immediately.)
func TestResetReleasedLeavesTheContendedDyingPinInPlace(t *testing.T) {
	base, workspace := scratchRetentionBase(t)
	owner := retentionOwner(t)
	scratch, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	binding := retentionBinding("E0", owner.RootSessionID, workspace,
		map[string]ScratchSlot{ScratchKindSandbox: {Dir: scratch.Dir, OwnsLease: true}})
	// No consumer row is published, so the reset's carry path lets the pair
	// die — and the lease the scratch handle keeps makes that death
	// contended, the only branch the death can run in.
	if err := PinScratchBinding(owner, binding, map[string]*SessionScratch{ScratchKindSandbox: scratch}, nil); err != nil {
		t.Fatalf("pin the pre-release binding: %v", err)
	}
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("terminal release: %v", err)
	}
	// The dying pair's pin is this owner's and its lease is held: the death
	// must leave the pin in place rather than strip it from under the live
	// holder — the reset still completes, so the reinstall that owns this
	// path can republish immediately.
	if _, _, err := ResetScratchRetentionIfReleased(owner); err != nil {
		t.Fatalf("the reset must complete past a contended dying pair: %v", err)
	}
	manifest, err := LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Released {
		t.Fatal("the reset left the manifest tombstoned")
	}
	for _, candidate := range manifest.References {
		if filepath.Clean(candidate.Dir) == filepath.Clean(scratch.Dir) {
			t.Fatal("the dead pair's reference survived the reset")
		}
	}
	if _, err := os.Stat(filepath.Join(scratch.Dir, scratchPinName)); err != nil {
		t.Fatalf("the reset stripped the live holder's protection pin lease-less: %v", err)
	}
	// The left pin is protection, not an orphan the collector may take: the
	// holder still uses the directory, so the sweep conservatively retains it
	// with its diagnostic.
	retained, retErr := ScratchDirectoryRetained(scratch.Dir)
	if !retained || retErr == nil {
		t.Fatalf("the stripped-pin directory read collectible: retained=%v err=%v", retained, retErr)
	}
	// The holder releases its lease without removing the directory, the way a
	// live session's teardown does; the next terminal release tombstones the
	// manifest, which is what authorizes the collector to finally remove the
	// released pin and its directory — the protection the leave extends is
	// bounded by the next terminal cycle.
	if err := scratch.Retain(); err != nil {
		t.Fatal(err)
	}
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("next terminal release: %v", err)
	}
	if retained, retErr := ScratchDirectoryRetained(scratch.Dir); retained || retErr != nil {
		t.Fatalf("the freed-pin directory still reads retained after the terminal release: retained=%v err=%v", retained, retErr)
	}
}

// TestVerifyDyingReferencePin pins the death branch's pin contract: a
// reference whose owning pair died with the tombstoned manifest is dropped
// freely when the pin is absent, a pin that cannot be read aborts the death —
// the same contract the contended and free-lease branches enforce on their
// own reads (round 19) — and a readable pin is LEFT UNTOUCHED: the death's
// only caller is the reset's contended branch, so the pin protects a
// directory whose lease a live holder still holds, and stripping it
// lease-less would leave the holder's directory collectible while the holder
// uses it. This owner's own pin is re-pinned by the reinstall's republish
// (same owner, directory, and kind — the pin write is idempotent) or removed
// by the next terminal release once the lease frees; a foreign pin belongs to
// its own manifest (round 25). The death branch's read is the second one the
// reset takes on the directory, so a cross-process writer is what can flip it
// between the two; the contract is pinned here directly.
func TestVerifyDyingReferencePin(t *testing.T) {
	owner := retentionOwner(t)
	other := retentionOwner(t)

	// A pin that is already absent has nothing to protect: the reference
	// dies with the manifest.
	bare := t.TempDir()
	if err := verifyDyingReferencePin(bare); err != nil {
		t.Fatalf("absent pin: %v", err)
	}

	// This owner's pin is left in place: the death never removes it
	// lease-less.
	ours := t.TempDir()
	if err := writeScratchDirectoryPin(ours, owner, ScratchReference{Dir: ours, Kind: ScratchKindSandbox}); err != nil {
		t.Fatal(err)
	}
	if err := verifyDyingReferencePin(ours); err != nil {
		t.Fatalf("our own pin: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ours, scratchPinName)); err != nil {
		t.Fatalf("the death disturbed this owner's pin: %v", err)
	}

	// A foreign pin belongs to its own manifest and is left alone.
	foreign := t.TempDir()
	if err := writeScratchDirectoryPin(foreign, other, ScratchReference{Dir: foreign, Kind: ScratchKindSandbox}); err != nil {
		t.Fatal(err)
	}
	if err := verifyDyingReferencePin(foreign); err != nil {
		t.Fatalf("foreign pin: %v", err)
	}
	if _, err := os.Stat(filepath.Join(foreign, scratchPinName)); err != nil {
		t.Fatalf("the foreign pin was disturbed: %v", err)
	}

	// A pin that cannot be read aborts the death: committing the reference
	// away past an unreadable pin strands an orphan the collector retains
	// forever under an unreleased manifest, with no later reset left to
	// retry.
	if os.Geteuid() == 0 {
		t.Skip("the unreadable-pin fixture cannot make the read fail for root")
	}
	unreadable := t.TempDir()
	if err := writeScratchDirectoryPin(unreadable, owner, ScratchReference{Dir: unreadable, Kind: ScratchKindSandbox}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(unreadable, scratchPinName), 0o600) })
	if err := os.Chmod(filepath.Join(unreadable, scratchPinName), 0o000); err != nil {
		t.Fatal(err)
	}
	if err := verifyDyingReferencePin(unreadable); err == nil {
		t.Fatal("an unreadable pin must abort the death, not let the reference drop silently")
	}
}
