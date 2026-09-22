package sandbox

import (
	"bytes"
	"errors"
	"maps"
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
	retained, retainErr := scratchDirectoryRetained(dir)
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
	fresh, err := ResetScratchRetentionIfReleased(owner)
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
	if _, err := ResetScratchRetentionIfReleased(owner); err != nil {
		t.Fatalf("reset over the settled leftover pin: %v", err)
	}
	if _, err := os.Stat(filepath.Join(scratch.Dir, scratchPinName)); !os.IsNotExist(err) {
		t.Fatalf("the reset left a collectible directory's pin in place: %v", err)
	}
	retained, err := scratchDirectoryRetained(scratch.Dir)
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
	if err := ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("second terminal release: %v", err)
	}
	fresh, err := ResetScratchRetentionIfReleased(owner)
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
	retained, err = scratchDirectoryRetained(held.Dir)
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

	fresh, err := ResetScratchRetentionIfReleased(owner)
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
	if _, err := ResetScratchRetentionIfReleased(owner); err == nil {
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
