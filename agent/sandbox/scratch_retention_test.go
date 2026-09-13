package sandbox

import (
	"bytes"
	"maps"
	"os"
	"path/filepath"
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
