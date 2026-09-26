package agent

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
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
	t.Parallel()
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
	t.Parallel()
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

// TestRetirementScratchSealDeclinesInFlightRefreshSeed pins round 28's second
// Medium: releaseRetirementScratch detached the pool without setting the seal
// the terminal and child-teardown paths set, so a refresh pass interleaved
// between the detach and its seed CAS republished a pool for a session that
// had already released its runtime — and because the retirement consumed
// closeOnce, the terminal release that would sweep the republished pool can
// never run: its handles hold every retained directory's lease for the
// daemon's life, marking the slots contended for future cold restores and
// pinning the directories against the collector.
func TestRetirementScratchSealDeclinesInFlightRefreshSeed(t *testing.T) {
	t.Parallel()
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("root had no scratch retention owner")
	}
	const consumerID = "01RETIRESEAL1"
	const bindingID = "b-retire-seal"
	slots, _ := mintRefreshScratchBinding(t, s, bindingID, sandbox.ScratchKindSandbox)
	mapRefreshScratchConsumer(t, s, consumerID, bindingID)
	retainedDir := slots[sandbox.ScratchKindSandbox].Dir
	// Leases free: the refresh must be able to reacquire — the in-flight
	// state the retirement then races.
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
	resumeRetirement := make(chan struct{})
	s.cfg.testOnly.scratchRetirementAfterDetach = func() {
		close(detached)
		<-resumeRetirement
	}

	go func() { refreshDone <- s.refreshRetainedScratchConsumer(consumerID) }()
	<-refreshPaused
	retirementDone := make(chan struct{})
	go func() {
		defer close(retirementDone)
		s.releaseRetirementScratch()
	}()
	<-detached
	// The retirement detach has swept the pointer and nothing will ever run
	// the terminal release after it — the retirement consumed closeOnce — so
	// a seed that lands in this window is republished forever: let the
	// refresh's install land inside it.
	close(resumeRefresh)
	if err := <-refreshDone; err != nil {
		t.Fatalf("the sealed decline must not fail the in-flight restore: %v", err)
	}
	close(resumeRetirement)
	<-retirementDone

	if s.retainedScratch.Load() != nil {
		t.Fatal("the refresh published a seed pool after the retirement detach; nothing can ever release its handles")
	}
	if !s.retainedScratchSealed.Load() {
		t.Fatal("the retirement left the session unsealed: a later refresh could republish the same orphaned pool")
	}
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Released {
		t.Fatal("the retirement wrote a Released tombstone it must never write")
	}
	// The retirement keeps every durable record: the pin survives beside the
	// manifest the sealed refresh never touched.
	const pinName = ".evener-retained-session.json"
	if _, err := os.Stat(filepath.Join(retainedDir, pinName)); err != nil {
		t.Fatalf("the interleaved refresh's pin went missing: %v", err)
	}
}

// TestTerminalReleaseRetriesManifestLockContention pins the round-11 terminal
// lock race: the tombstone transaction takes the manifest's fail-fast update
// lock, so an in-process writer holding it must refuse the terminal release
// only transiently. A single attempt would warn and return with every
// consumer already gone — the tombstone unwritten and the pins durable
// forever. The release retries the refusal with the bounded growing backoff,
// so a hold that ends between attempts still commits the tombstone and
// removes the pin.
func TestTerminalReleaseRetriesManifestLockContention(t *testing.T) {
	t.Parallel()
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("root had no scratch retention owner")
	}
	const consumerID = "01TERMINALRETRY1"
	const bindingID = "b-terminal-retry"
	slots, _ := mintRefreshScratchBinding(t, s, bindingID, sandbox.ScratchKindSandbox)
	mapRefreshScratchConsumer(t, s, consumerID, bindingID)
	retainedDir := slots[sandbox.ScratchKindSandbox].Dir
	releaseRefreshFixtureLeases(t, slots)
	if s.retainedScratch.Load() != nil {
		t.Fatal("fixture expected no published pool")
	}

	// The holder keeps the manifest's update lock across the terminal
	// release's first attempt — verifiably taken before the attempt runs —
	// and hands it over only after that attempt has deterministically lost,
	// so the retry's next attempt runs against a verifiably free lock.
	takeLock := make(chan struct{})
	lockTaken := make(chan struct{})
	releaseLock := make(chan struct{})
	lockReleased := make(chan struct{})
	go func() {
		<-takeLock
		_ = sandbox.WithScratchRetentionLock(owner, func() error {
			close(lockTaken)
			<-releaseLock
			return nil
		})
		close(lockReleased)
	}()

	attempt1Lost := make(chan struct{})
	attempt2Seen := make(chan struct{})
	handover1 := make(chan struct{})
	handover2 := make(chan struct{})
	var attempts atomic.Int32
	s.cfg.testOnly.scratchTerminalReleaseAttempt = func(n int) {
		switch attempts.Add(1) {
		case 1:
			close(takeLock)
			<-lockTaken
			close(attempt1Lost)
			<-handover1
		case 2:
			close(attempt2Seen)
			<-handover2
		}
	}

	terminalDone := make(chan struct{})
	go func() {
		defer close(terminalDone)
		s.releaseTerminalScratchRetention()
	}()
	<-attempt1Lost
	close(handover1)
	select {
	case <-attempt2Seen:
	case <-terminalDone:
		t.Fatal("the terminal release gave up after one transient lock refusal; the tombstone must retry it")
	}
	close(releaseLock)
	<-lockReleased
	close(handover2)
	<-terminalDone

	if got := attempts.Load(); got < 2 {
		t.Fatalf("the terminal release stopped after attempt %d; the fail-fast refusal must route through the bounded retry", got)
	}
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.Released {
		t.Fatal("the terminal release left the tombstone unwritten after a transient lock refusal")
	}
	const pinName = ".evener-retained-session.json"
	if _, err := os.Stat(filepath.Join(retainedDir, pinName)); !os.IsNotExist(err) {
		t.Fatalf("the retried release left the pin in place: %v", err)
	}
}

// TestTerminalDetachSweepOwnsSeededHandles pins the round-11 double-release
// race in the seal path: the published seed pool aliases the refresh pass's own
// handles map, so when the terminal detach sweeps the freshly published pool
// between the seed CAS and the seal check, it Retains every one of those
// handles. The losing pass must NOT release them again — Retain is
// unsynchronized, so the second release races the sweep's on the same objects
// (a -race failure and a possible flock against a recycled descriptor). The
// undo CAS decides ownership: only a pass that actually un-published its seed
// still owns its leases.
func TestTerminalDetachSweepOwnsSeededHandles(t *testing.T) {
	t.Parallel()
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("root had no scratch retention owner")
	}
	const consumerID = "01SEALSWEEPWIN1"
	const bindingID = "b-seal-sweep"
	slots, _ := mintRefreshScratchBinding(t, s, bindingID, sandbox.ScratchKindSandbox)
	mapRefreshScratchConsumer(t, s, consumerID, bindingID)
	retainedDir := slots[sandbox.ScratchKindSandbox].Dir
	releaseRefreshFixtureLeases(t, slots)
	if s.retainedScratch.Load() != nil {
		t.Fatal("fixture expected no published pool")
	}

	// The refresh pauses inside its install hold, after its seed won the
	// publish CAS — the manifest lock held while the seeded pool is live.
	seedPublished := make(chan struct{})
	resumeRefresh := make(chan struct{})
	s.cfg.testOnly.scratchRefreshAfterSeedCAS = func() {
		close(seedPublished)
		<-resumeRefresh
	}
	// The terminal sweep is held before its first lease release, so the
	// losing pass's own release provably overlaps it without a
	// synchronization edge — the exact production race.
	sweepHolding := make(chan struct{})
	resumeSweep := make(chan struct{})
	var sweepHoldOnce sync.Once
	s.cfg.testOnly.scratchDetachRetainHook = func() {
		sweepHoldOnce.Do(func() {
			close(sweepHolding)
			<-resumeSweep
		})
	}
	detachSwept := make(chan struct{})
	resumeTerminal := make(chan struct{})
	s.cfg.testOnly.scratchTerminalReleaseAfterDetach = func() {
		close(detachSwept)
		<-resumeTerminal
	}

	refreshDone := make(chan error, 1)
	go func() { refreshDone <- s.refreshRetainedScratchConsumer(consumerID) }()
	<-seedPublished
	terminalDone := make(chan struct{})
	go func() {
		defer close(terminalDone)
		s.releaseTerminalScratchRetention()
	}()
	<-sweepHolding
	// The sweep owns the pointer (its CAS already took it) and is mid-loop;
	// resume the pass and the sweep together so both release paths run
	// unsynchronized against the same handles — the pass must opt out and
	// release nothing.
	close(resumeRefresh)
	close(resumeSweep)
	if err := <-refreshDone; err != nil {
		t.Fatalf("the swept seed's decline must not fail the in-flight restore: %v", err)
	}
	<-detachSwept
	close(resumeTerminal)
	<-terminalDone

	if s.retainedScratch.Load() != nil {
		t.Fatal("a pool survived the terminal release")
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
		t.Fatalf("the terminal release left the pin in place: %v", err)
	}
}
