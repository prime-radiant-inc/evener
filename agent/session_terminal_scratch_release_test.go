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
	// The retained allocation is published as the session's OWN installed
	// binding, not as an extra one. Production re-points a session's consumer
	// only inside stageScratchSwapBinding, which moves the owning slots off the
	// source binding in the same transaction, so a binding that keeps a
	// lease-owning slot always keeps a consumer role. Minting a second binding
	// and re-pointing root.id at it would orphan the session's real binding,
	// which no production path does.
	if len(manifest.Bindings) != 1 || len(manifest.Consumers) != 1 {
		t.Fatalf("fixture expected the session's single installed binding: %+v", manifest)
	}
	binding := sandbox.ScratchBinding{
		BindingID:      manifest.Bindings[0].BindingID,
		OwnerSessionID: root.id,
		WorkingDir:     dir,
		Slots:          map[string]sandbox.ScratchSlot{sandbox.ScratchKindUnsandboxed: {Dir: scratch.Dir, OwnsLease: true}},
	}
	if err := sandbox.UpdateScratchBindings(owner, manifest.Revision,
		[]sandbox.ScratchBinding{binding},
		[]sandbox.ScratchConsumerBinding{{SessionID: root.id, CurrentBindingID: binding.BindingID}}); err != nil {
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

// TestTerminalReleaseSealDeclinesInFlightRefreshSeed pins the round-10
// terminal-detach race: a refresh pass that reacquired a lease and is
// mid-install while the terminal close runs must not publish a seed pool after
// the terminal detach already swept the pointer. Nothing would ever release
// that pool's handles — every consumer is already torn down — and
// ReleaseScratchRetention, running after the detach, would find their leases
// contended and leave their pins for a collector that cannot finish while the
// daemon holds the leases. The seal makes the losing pass undo its own publish
// and hand the leases back, so the tombstone release removes the pin.
func TestTerminalReleaseSealDeclinesInFlightRefreshSeed(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("root had no scratch retention owner")
	}
	const consumerID = "01SEALREFRESH1"
	const bindingID = "b-seal-refresh"
	slots, _ := mintRefreshScratchBinding(t, s, bindingID, sandbox.ScratchKindSandbox)
	mapRefreshScratchConsumer(t, s, consumerID, bindingID)
	retainedDir := slots[sandbox.ScratchKindSandbox].Dir
	// Leases free: the refresh must be able to reacquire — the in-flight
	// state the terminal close then races.
	releaseRefreshFixtureLeases(t, slots)
	if s.retainedScratch.Load() != nil {
		t.Fatal("fixture expected no published pool: the paused pass must take the seed path")
	}

	refreshPaused := make(chan struct{})
	resumeRefresh := make(chan struct{})
	refreshDone := make(chan error, 1)
	s.cfg.testOnly.scratchRefreshBeforeInstall = func(string) {
		close(refreshPaused)
		<-resumeRefresh
	}
	detached := make(chan struct{})
	resumeTerminal := make(chan struct{})
	s.cfg.testOnly.scratchTerminalReleaseAfterDetach = func() {
		close(detached)
		<-resumeTerminal
	}

	go func() { refreshDone <- s.refreshRetainedScratchConsumer(consumerID) }()
	<-refreshPaused
	terminalDone := make(chan struct{})
	go func() {
		defer close(terminalDone)
		s.releaseTerminalScratchRetention()
	}()
	<-detached
	// The terminal detach has swept the pointer and the tombstone has NOT yet
	// been written — the exact window whose revision recheck still passes. Let
	// the refresh's install land inside it.
	close(resumeRefresh)
	if err := <-refreshDone; err != nil {
		t.Fatalf("the sealed decline must not fail the in-flight restore: %v", err)
	}
	close(resumeTerminal)
	<-terminalDone

	if s.retainedScratch.Load() != nil {
		t.Fatal("the refresh published a seed pool after the terminal detach; its handles are unreachable and nothing will ever release them")
	}
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.Released {
		t.Fatal("the terminal release did not commit the Released tombstone")
	}
	const pinName = ".evener-retained-session.json"
	if _, err := os.Stat(filepath.Join(retainedDir, pinName)); !os.IsNotExist(err) {
		t.Fatalf("the terminal release left the in-flight refresh's pin in place (the orphaned pool held the lease): %v", err)
	}
}
