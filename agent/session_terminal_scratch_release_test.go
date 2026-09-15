package agent

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/sandbox"
)

// TestTerminalCloseReleasesRetainedScratchPoolBeforeRetentionRelease proves the
// consequence of the round-13 F2 finding: a normal terminal close of a session
// holding an unadopted retained-scratch handle must release that handle before
// sandbox.ReleaseScratchRetention runs. The observable consequence is the
// per-directory identity pin actually being removed.
//
// A successful restore reacquires a lease for every manifest reference and
// pools any allocation no live environment adopts (a cold/unrestored delegate,
// a parked or orphan binding). While the pool still holds that directory's
// lease, ReleaseScratchRetention sees contention and leaves the pin, permanently
// pinning the directory against age-based collection. Releasing the pool first
// frees the lease so the pin can be removed. This test observes the pin's
// absence, not a call count.
func TestTerminalCloseReleasesRetainedScratchPoolBeforeRetentionRelease(t *testing.T) {
	// The pin file name is sandbox's unexported scratchPinName; it is the
	// on-disk retention contract (see agent/session_scratch_retention_test.go,
	// which hardcodes the same name for the same reason).
	const pinName = ".evener-retained-session.json"

	dir := t.TempDir()
	root := newQueuePersistTestSession(t, dir)
	owner, ok := root.scratchRetentionOwner()
	if !ok {
		t.Fatal("root had no scratch retention owner")
	}
	base := t.TempDir()
	scratch, err := sandbox.NewSessionScratch(base, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(scratch.Dir) })
	ref := sandbox.ScratchReference{Dir: scratch.Dir, Kind: sandbox.ScratchKindUnsandboxed}
	if err := scratch.Pin(owner, ref); err != nil {
		t.Fatalf("pin retained scratch: %v", err)
	}
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	binding := sandbox.ScratchBinding{
		BindingID:      "E0",
		OwnerSessionID: root.id,
		WorkingDir:     dir,
		Slots:          map[string]sandbox.ScratchSlot{sandbox.ScratchKindUnsandboxed: {Dir: scratch.Dir, OwnsLease: true}},
	}
	if err := sandbox.UpdateScratchBindings(owner, manifest.Revision,
		[]sandbox.ScratchBinding{binding},
		[]sandbox.ScratchConsumerBinding{{SessionID: root.id, CurrentBindingID: "E0"}}); err != nil {
		t.Fatal(err)
	}
	// Release the live lease so prepareRetainedScratch reacquires the handle.
	// The root's own live environment does not hold this allocation, so the
	// reacquired handle stays pooled and unadopted — exactly the leak: nothing
	// else releases it at a terminal close.
	if err := scratch.Retain(); err != nil {
		t.Fatal(err)
	}
	if err := root.prepareRetainedScratch(); err != nil {
		t.Fatalf("prepareRetainedScratch: %v", err)
	}
	pool := root.retainedScratch.Load()
	if pool == nil || len(pool.handles) == 0 {
		t.Fatal("fixture prepared no pooled retained handle")
	}

	root.Close()

	if _, err := os.Stat(filepath.Join(scratch.Dir, pinName)); !os.IsNotExist(err) {
		t.Fatalf("terminal close left the retained scratch pin in place (directory stays pinned): %v", err)
	}
	if root.retainedScratch.Load() != nil {
		t.Fatal("terminal close left the retained scratch pool loaded")
	}
}
