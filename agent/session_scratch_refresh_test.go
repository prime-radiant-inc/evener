package agent

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/sandbox"
)

// Tests pinning the retained-scratch refresh's concurrency and staleness
// contracts from the #2130 review round: reacquired leases are released when a
// pass fails mid-way, a concurrent pool seed folds into the first publish
// instead of displacing it, the parked worktree binding read takes the pool
// lock, rows the manifest moved after the pool was built are re-adopted, and
// the manifest revision is rechecked before anything is installed. Round 2
// adds: the install window serializes with manifest updates (no update can
// commit between the recheck and the rows landing), and the fold refuses a
// pool a terminal release detached mid-pass.

// mintRefreshScratchBinding mints one owning scratch per kind and publishes
// bindingID with every slot pinned, the leases left held by the returned
// handles exactly the way a live consumer holds them.
func mintRefreshScratchBinding(t *testing.T, s *Session, bindingID string, kinds ...string) (map[string]*sandbox.SessionScratch, sandbox.ScratchBinding) {
	t.Helper()
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	base := t.TempDir()
	work := t.TempDir()
	slots := make(map[string]*sandbox.SessionScratch, len(kinds))
	owned := make(map[string]*sandbox.SessionScratch, len(kinds))
	binding := sandbox.ScratchBinding{BindingID: bindingID, OwnerSessionID: bindingOwnerForTest, Slots: map[string]sandbox.ScratchSlot{}}
	for _, kind := range kinds {
		handle, err := sandbox.NewSessionScratch(base, work)
		if err != nil {
			t.Fatalf("mint %s scratch: %v", kind, err)
		}
		slots[kind] = handle
		owned[kind] = handle
		binding.Slots[kind] = sandbox.ScratchSlot{Dir: handle.Dir, OwnsLease: true}
	}
	if err := sandbox.PinScratchBinding(owner, binding, owned); err != nil {
		t.Fatalf("pin binding %q: %v", bindingID, err)
	}
	return slots, binding
}

// bindingOwnerForTest is the neutral owner identity the mint helper stamps on
// bindings it publishes; no consumer mapping reads it.
const bindingOwnerForTest = "01BINDINGOWNERTST1"

// mapRefreshScratchConsumer maps consumerID onto bindingID in the manifest.
func mapRefreshScratchConsumer(t *testing.T, s *Session, consumerID, bindingID string) {
	t.Helper()
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	consumer := sandbox.ScratchConsumerBinding{SessionID: consumerID, CurrentBindingID: bindingID}
	if err := sandbox.UpdateScratchBindings(owner, manifest.Revision, nil, []sandbox.ScratchConsumerBinding{consumer}); err != nil {
		t.Fatalf("map consumer %q onto %q: %v", consumerID, bindingID, err)
	}
}

// releaseRefreshFixtureLeases drops every handle's lease, the state a released
// delegate presents: the manifest pins and consumer rows stay durable while
// the in-process leases are free for a refresh to reacquire.
func releaseRefreshFixtureLeases(t *testing.T, slots map[string]*sandbox.SessionScratch) {
	t.Helper()
	for kind, handle := range slots {
		if err := handle.Retain(); err != nil {
			t.Fatalf("release %s slot lease: %v", kind, err)
		}
	}
}

// TestScratchRefreshReleasesReacquiredLeasesOnOpenError pins the error path: a
// refresh pass that fails after reacquiring some of a consumer's slots must
// release the leases it already took, or the stranded handles hold their slots
// for the life of a process that never installs them.
func TestScratchRefreshReleasesReacquiredLeasesOnOpenError(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	const consumerID = "01REFRESHLEASEERR1"
	const bindingID = "b-refresh-lease-err"
	slots, _ := mintRefreshScratchBinding(t, s, bindingID, sandbox.ScratchKindSandbox, sandbox.ScratchKindUnsandboxed)
	mapRefreshScratchConsumer(t, s, consumerID, bindingID)
	releaseRefreshFixtureLeases(t, slots)

	// The slot order of the reacquire loop is a map range, so the failing open
	// must land by CALL INDEX: the first reacquire runs for real, the second
	// fails, and whichever slot came first is the one whose lease must not be
	// stranded.
	sentinel := errors.New("injected open failure")
	s.cfg.testOnly.scratchRefreshOpenOverride = func(ref sandbox.ScratchReference, call int) error {
		_ = ref
		if call == 2 {
			return sentinel
		}
		return nil
	}
	err := s.refreshRetainedScratchConsumer(consumerID)
	if !errors.Is(err, sentinel) {
		t.Fatalf("refresh: want the injected open failure, got %v", err)
	}
	for kind, handle := range slots {
		reopened, openErr := sandbox.OpenRetainedSessionScratch(owner, sandbox.ScratchReference{Dir: handle.Dir, Kind: kind})
		if openErr != nil {
			t.Fatalf("refresh stranded the %s slot's lease: %v", kind, openErr)
		}
		if err := reopened.Retain(); err != nil {
			t.Fatalf("release reopened %s slot lease: %v", kind, err)
		}
	}
}

// TestScratchRefreshConcurrentSeedKeepsFirstPool pins the poolless seed's
// publish: when no pool was ever published, two concurrent cold restores both
// reach the seed path, and the second must FOLD into the first's pool — never
// displace and release it, which would drop the first consumer's rows and
// release the handles it reacquired mid-restore.
func TestScratchRefreshConcurrentSeedKeepsFirstPool(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	const firstConsumer = "01REFRESHSEEDFIRST1"
	const firstBinding = "b-seed-first"
	const secondConsumer = "01REFRESHSEEDSECD1"
	const secondBinding = "b-seed-second"
	firstSlots, _ := mintRefreshScratchBinding(t, s, firstBinding, sandbox.ScratchKindSandbox)
	mapRefreshScratchConsumer(t, s, firstConsumer, firstBinding)
	secondSlots, _ := mintRefreshScratchBinding(t, s, secondBinding, sandbox.ScratchKindSandbox)
	mapRefreshScratchConsumer(t, s, secondConsumer, secondBinding)
	releaseRefreshFixtureLeases(t, firstSlots)
	releaseRefreshFixtureLeases(t, secondSlots)
	if s.retainedScratch.Load() != nil {
		t.Fatal("fixture expected no published pool")
	}

	// Fire the concurrent restore from the before-install seam: the second
	// consumer's refresh publishes the first pool while this one holds its own
	// reacquired handles. The guard keeps the nested refresh from recursing
	// into the seam again.
	var hooked atomic.Bool
	s.cfg.testOnly.scratchRefreshBeforeInstall = func(string) {
		if hooked.Load() {
			return
		}
		hooked.Store(true)
		if err := s.refreshRetainedScratchConsumer(secondConsumer); err != nil {
			t.Errorf("concurrent refresh of %q: %v", secondConsumer, err)
		}
	}
	if err := s.refreshRetainedScratchConsumer(firstConsumer); err != nil {
		t.Fatalf("refresh of %q: %v", firstConsumer, err)
	}
	pool := s.retainedScratch.Load()
	if pool == nil {
		t.Fatal("no pool was published")
	}
	pool.mu.Lock()
	firstRow, hasFirst := pool.consumers[firstConsumer]
	_, hasSecond := pool.consumers[secondConsumer]
	_, secondHeld := pool.handles[canonicalScratchDir(secondSlots[sandbox.ScratchKindSandbox].Dir)]
	pool.mu.Unlock()
	if !hasFirst || !hasSecond {
		t.Fatalf("seed publish lost a consumer: first=%v second=%v", hasFirst, hasSecond)
	}
	if firstRow.CurrentBindingID != firstBinding {
		t.Fatalf("first consumer row maps onto %q, want %q", firstRow.CurrentBindingID, firstBinding)
	}
	if !secondHeld {
		t.Fatal("seed publish released the concurrent pool's reacquired handle")
	}
}

// TestScratchRefreshAdoptsRowsMovedAfterPoolBuild pins the diff contract: the
// refresh must reinstall a consumer's rows whenever the manifest's differ from
// the pool's — a consumer row that merely EXISTS is not proof the binding it
// names is still the live one.
func TestScratchRefreshAdoptsRowsMovedAfterPoolBuild(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	const consumerID = "01REFRESHROWMOVE1"
	const firstBinding = "b-row-move-first"
	const movedBinding = "b-row-move-moved"
	firstSlots, firstBindingRow := mintRefreshScratchBinding(t, s, firstBinding, sandbox.ScratchKindSandbox)
	mapRefreshScratchConsumer(t, s, consumerID, firstBinding)

	// The init-time pool: consumer and binding rows as init loaded them, no
	// handle held for the slot (the consumer holds its lease live — an
	// engineered absence the refresh must not disturb).
	s.retainedScratch.Store(&retainedScratchPool{
		owner:     owner,
		handles:   map[string]*sandbox.SessionScratch{},
		bindings:  map[string]sandbox.ScratchBinding{firstBinding: firstBindingRow},
		consumers: map[string]sandbox.ScratchConsumerBinding{consumerID: {SessionID: consumerID, CurrentBindingID: firstBinding}},
		contended: map[string]struct{}{},
		adopted:   map[string]string{},
	})
	releaseRefreshFixtureLeases(t, firstSlots)

	// The manifest moves the consumer onto a fresh binding after the pool was
	// built — the drift a worktree move or shared-binding swap creates.
	movedSlots, _ := mintRefreshScratchBinding(t, s, movedBinding, sandbox.ScratchKindSandbox)
	releaseRefreshFixtureLeases(t, movedSlots)
	mapRefreshScratchConsumer(t, s, consumerID, movedBinding)

	if err := s.refreshRetainedScratchConsumer(consumerID); err != nil {
		t.Fatalf("refresh of %q: %v", consumerID, err)
	}
	pool := s.retainedScratch.Load()
	pool.mu.Lock()
	row := pool.consumers[consumerID]
	_, movedPooled := pool.bindings[movedBinding]
	_, movedHandle := pool.handles[canonicalScratchDir(movedSlots[sandbox.ScratchKindSandbox].Dir)]
	pool.mu.Unlock()
	if row.CurrentBindingID != movedBinding {
		t.Fatalf("pool kept the consumer on %q after the manifest moved it to %q", row.CurrentBindingID, movedBinding)
	}
	if !movedPooled {
		t.Fatal("refresh did not install the moved binding's rows")
	}
	if !movedHandle {
		t.Fatal("refresh did not reacquire the moved binding's slot")
	}
}

// TestScratchRefreshRechecksRevisionBeforeInstall pins the revision recheck:
// rows are derived from a manifest snapshot and installed after reacquiring
// leases, so a manifest update that lands in between must invalidate the pass —
// the refresh drops its reacquires and re-derives instead of installing rows a
// superseded revision produced.
func TestScratchRefreshRechecksRevisionBeforeInstall(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	const consumerID = "01REFRESHREVISION1"
	const firstBinding = "b-revision-first"
	const movedBinding = "b-revision-moved"
	firstSlots, _ := mintRefreshScratchBinding(t, s, firstBinding, sandbox.ScratchKindSandbox)
	mapRefreshScratchConsumer(t, s, consumerID, firstBinding)
	releaseRefreshFixtureLeases(t, firstSlots)

	// Move the consumer onto a fresh binding from the before-install seam, the
	// exact window between the refresh deriving its rows and installing them.
	var hooked atomic.Bool
	s.cfg.testOnly.scratchRefreshBeforeInstall = func(string) {
		if hooked.Load() {
			return
		}
		hooked.Store(true)
		movedSlots, _ := mintRefreshScratchBinding(t, s, movedBinding, sandbox.ScratchKindSandbox)
		releaseRefreshFixtureLeases(t, movedSlots)
		mapRefreshScratchConsumer(t, s, consumerID, movedBinding)
	}
	if err := s.refreshRetainedScratchConsumer(consumerID); err != nil {
		t.Fatalf("refresh of %q: %v", consumerID, err)
	}
	pool := s.retainedScratch.Load()
	if pool == nil {
		t.Fatal("no pool was published")
	}
	pool.mu.Lock()
	row := pool.consumers[consumerID]
	_, movedPooled := pool.bindings[movedBinding]
	pool.mu.Unlock()
	if row.CurrentBindingID != movedBinding {
		t.Fatalf("refresh installed rows from a superseded revision: consumer on %q, want %q", row.CurrentBindingID, movedBinding)
	}
	if !movedPooled {
		t.Fatal("refresh installed the superseded revision's binding rows")
	}
	// The superseded pass's reacquired lease must have been released with the
	// retry, or the pass stranded it exactly the way the open-error path would.
	if _, err := sandbox.OpenRetainedSessionScratch(owner, sandbox.ScratchReference{Dir: firstSlots[sandbox.ScratchKindSandbox].Dir, Kind: sandbox.ScratchKindSandbox}); err != nil {
		t.Fatalf("refresh stranded the superseded pass's reacquired lease: %v", err)
	}
}

// TestParkedWorktreeBindingIDReadsUnderConcurrentPoolMutation pins the
// worktree-re-entry read's lock: installConsumerRefresh mutates consumer rows
// after the pool is published, so the parked-binding read must copy its row out
// under the pool mutex. A reader that touches the map unlocked raises the
// runtime's unrecoverable concurrent-map throw. Writers and readers run fixed
// coordinated batches behind a start barrier — no wall-clock sleep. Readers
// additionally wait for the first committed write, so the observation assertion
// is deterministic: the remaining ~150 writes are still in flight while the
// readers run, and a read that misses the committed row is a real bug, not a
// scheduling race.
func TestParkedWorktreeBindingIDReadsUnderConcurrentPoolMutation(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	const bindingID = "b-parked-lock"
	s.retainedScratch.Store(&retainedScratchPool{
		owner:     owner,
		handles:   map[string]*sandbox.SessionScratch{},
		bindings:  map[string]sandbox.ScratchBinding{},
		consumers: map[string]sandbox.ScratchConsumerBinding{},
		contended: map[string]struct{}{},
		adopted:   map[string]string{},
	})
	start := make(chan struct{})
	firstWrite := make(chan struct{})
	var firstWriteOnce sync.Once
	sawWritten := make(chan struct{}, 8)
	const writesPerWriter = 50
	const readsPerReader = 50
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			<-start
			for range writesPerWriter {
				s.installConsumerRefresh(s.retainedScratch.Load(),
					sandbox.ScratchConsumerBinding{SessionID: s.id, WorktreeRestoreBindingID: bindingID},
					sandbox.ScratchBinding{BindingID: bindingID},
					nil, nil,
				)
				firstWriteOnce.Do(func() { close(firstWrite) })
			}
		})
	}
	for range 4 {
		wg.Go(func() {
			<-start
			<-firstWrite
			for range readsPerReader {
				got := s.parkedWorktreeBindingID()
				if got != bindingID && got != "" {
					t.Errorf("parked binding read %q, want %q or empty-before-first-write", got, bindingID)
				}
				if got == bindingID {
					select {
					case sawWritten <- struct{}{}:
					default:
					}
				}
			}
		})
	}
	close(start)
	wg.Wait()
	select {
	case <-sawWritten:
	default:
		t.Fatal("no reader observed a written value; the mutation and read streams never interleaved")
	}
}

// TestRestoreKeepsFreshScratchWhenRetainedSlotContended pins the round-4
// ownership guard: a contended retained sandbox slot — its lease held in this
// process by the racing idle-release teardown — must never be a replacement
// target. Disposing the fresh allocation to adopt a directory the adoption
// cannot take the lease of would leave the restored delegate running on the
// retained scratch unowned, beside its in-process holder.
func TestRestoreKeepsFreshScratchWhenRetainedSlotContended(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	const consumerID = "01RESTORECONTENDED1"
	const bindingID = "b-restore-contended"
	slots, bindingRow := mintRefreshScratchBinding(t, s, bindingID, sandbox.ScratchKindSandbox)
	mapRefreshScratchConsumer(t, s, consumerID, bindingID)
	retainedDir := slots[sandbox.ScratchKindSandbox].Dir
	// The pool the previous refresh left: current rows, the slot recorded
	// contended (the teardown held its lease), no reacquired handle.
	s.retainedScratch.Store(&retainedScratchPool{
		owner:     owner,
		handles:   map[string]*sandbox.SessionScratch{},
		bindings:  map[string]sandbox.ScratchBinding{bindingID: bindingRow},
		consumers: map[string]sandbox.ScratchConsumerBinding{consumerID: {SessionID: consumerID, CurrentBindingID: bindingID}},
		contended: map[string]struct{}{canonicalScratchDir(retainedDir): {}},
		adopted:   map[string]string{},
	})
	t.Cleanup(func() { _ = slots[sandbox.ScratchKindSandbox].Retain() })

	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	t.Cleanup(func() { env.Cleanup(); env.DisposeSandboxScratch() })
	policy := sbxResolve(t, sbxBwrapFacts(t.TempDir()), env.WorkingDirectory(), sandbox.ModeWorkspaceWrite)
	if err := env.EnableSandbox(policy); err != nil {
		t.Fatalf("provision fresh sandbox scratch: %v", err)
	}
	fresh := env.SessionScratchDir()
	if fresh == "" || filepath.Clean(fresh) == filepath.Clean(retainedDir) {
		t.Fatalf("fixture fresh scratch %q must exist apart from the retained %q", fresh, retainedDir)
	}

	if _, err := s.adoptRestoredConsumerScratch(env, consumerID, true); err != nil {
		t.Fatalf("restore adoption with a contended retained slot: %v", err)
	}
	if got := env.SessionScratchDir(); filepath.Clean(got) != filepath.Clean(fresh) {
		t.Fatalf("the contended slot's replacement disposed the fresh scratch %q; the restored session now runs on %q without the lease", fresh, got)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("the fresh scratch was disposed against a contended slot: %v", err)
	}
}

// TestScratchRefreshInstallWindowBlocksManifestUpdates pins the round-2
// serialization: the revision recheck and the row install hold the manifest's
// durable update lock, so a manifest update cannot commit between them —
// without the hold, an update landing in that window installs rows a
// revision the manifest already superseded.
func TestScratchRefreshInstallWindowBlocksManifestUpdates(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	const consumerID = "01REFRESHWINLOCK1"
	const firstBinding = "b-window-first"
	const movedBinding = "b-window-moved"
	firstSlots, _ := mintRefreshScratchBinding(t, s, firstBinding, sandbox.ScratchKindSandbox)
	mapRefreshScratchConsumer(t, s, consumerID, firstBinding)
	movedSlots, _ := mintRefreshScratchBinding(t, s, movedBinding, sandbox.ScratchKindSandbox)
	releaseRefreshFixtureLeases(t, firstSlots)
	releaseRefreshFixtureLeases(t, movedSlots)

	// The in-window update attempt runs synchronously inside the hook: the
	// manifest's update lock is fail-fast (a contended writer is refused with
	// "locked by another writer", the API's existing contention model), so
	// the attempt either commits — the unserialized red — or is refused
	// because the refresh's install hold owns the lock.
	var windowErr error
	s.cfg.testOnly.scratchRefreshAfterRecheck = func(string) {
		manifest, err := sandbox.LoadScratchRetention(owner)
		if err != nil {
			t.Errorf("in-window load: %v", err)
			return
		}
		consumer := sandbox.ScratchConsumerBinding{SessionID: consumerID, CurrentBindingID: movedBinding}
		windowErr = sandbox.UpdateScratchBindings(owner, manifest.Revision, nil, []sandbox.ScratchConsumerBinding{consumer})
	}
	if err := s.refreshRetainedScratchConsumer(consumerID); err != nil {
		t.Fatalf("refresh of %q: %v", consumerID, err)
	}
	if windowErr == nil {
		t.Fatal("a manifest update committed inside the refresh's recheck-to-install window; the install is not serialized against manifest writers")
	}
	// The refusal was the install hold, not a broken manifest: once the
	// refresh returns and releases the lock, the same update commits.
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatalf("post-window load: %v", err)
	}
	consumer := sandbox.ScratchConsumerBinding{SessionID: consumerID, CurrentBindingID: movedBinding}
	if err := sandbox.UpdateScratchBindings(owner, manifest.Revision, nil, []sandbox.ScratchConsumerBinding{consumer}); err != nil {
		t.Fatalf("post-window update after the install hold released: %v", err)
	}
}

// TestScratchRefreshFoldSkipsDetachedPool pins the round-2 fold guard: a
// terminal release can swap the published pool out between a pass's load and
// its fold, and a fold that lands in the detached pool strands the pass's
// reacquired leases — the release never clears them again. The guarded fold
// declines, the pass releases its handles and retries, and the rows land in
// the pool the session publishes next.
func TestScratchRefreshFoldSkipsDetachedPool(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	const consumerID = "01REFRESHDETACHED1"
	const bindingID = "b-detached-fold"
	slots, _ := mintRefreshScratchBinding(t, s, bindingID, sandbox.ScratchKindSandbox)
	mapRefreshScratchConsumer(t, s, consumerID, bindingID)
	s.retainedScratch.Store(&retainedScratchPool{
		owner:     owner,
		handles:   map[string]*sandbox.SessionScratch{},
		bindings:  map[string]sandbox.ScratchBinding{},
		consumers: map[string]sandbox.ScratchConsumerBinding{},
		contended: map[string]struct{}{},
		adopted:   map[string]string{},
	})
	releaseRefreshFixtureLeases(t, slots)

	// A terminal release detaches and drops the published pool from inside
	// the pass's window.
	var hooked atomic.Bool
	s.cfg.testOnly.scratchRefreshBeforeInstall = func(string) {
		if hooked.Load() {
			return
		}
		hooked.Store(true)
		releaseRetainedScratchPool(s.retainedScratch.Swap(nil))
	}
	if err := s.refreshRetainedScratchConsumer(consumerID); err != nil {
		t.Fatalf("refresh of %q: %v", consumerID, err)
	}
	pool := s.retainedScratch.Load()
	if pool == nil {
		t.Fatal("the fold landed in the detached pool and nothing published a replacement; the rows and reacquired handles went nowhere")
	}
	pool.mu.Lock()
	row := pool.consumers[consumerID]
	_, slotHeld := pool.handles[canonicalScratchDir(slots[sandbox.ScratchKindSandbox].Dir)]
	pool.mu.Unlock()
	if row.CurrentBindingID != bindingID {
		t.Fatalf("republished consumer row maps onto %q, want %q", row.CurrentBindingID, bindingID)
	}
	if !slotHeld {
		t.Fatal("the retry did not reacquire and pool the slot after the detached fold")
	}
}

// TestScratchRefreshRetriesOpenLockContention pins the round-3 contention
// contract at the reacquire: an open that loses the manifest-lock race is a
// retry, never a restore failure — the pass hands its reacquired leases back
// and the next pass re-derives against the now-free lock.
func TestScratchRefreshRetriesOpenLockContention(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	const consumerID = "01REFRESHOPENLOCK1"
	const bindingID = "b-open-lock"
	slots, _ := mintRefreshScratchBinding(t, s, bindingID, sandbox.ScratchKindSandbox, sandbox.ScratchKindUnsandboxed)
	mapRefreshScratchConsumer(t, s, consumerID, bindingID)
	releaseRefreshFixtureLeases(t, slots)

	var injected atomic.Bool
	s.cfg.testOnly.scratchRefreshOpenOverride = func(_ sandbox.ScratchReference, call int) error {
		if call == 2 && injected.CompareAndSwap(false, true) {
			return sandbox.ErrScratchRetentionLockHeld
		}
		return nil
	}
	if err := s.refreshRetainedScratchConsumer(consumerID); err != nil {
		t.Fatalf("refresh of %q failed on transient lock contention: %v", consumerID, err)
	}
	if !injected.Load() {
		t.Fatal("the fixture never observed the contention it was built to inject")
	}
	pool := s.retainedScratch.Load()
	if pool == nil {
		t.Fatal("no pool was published")
	}
	pool.mu.Lock()
	row := pool.consumers[consumerID]
	_, sandboxHeld := pool.handles[canonicalScratchDir(slots[sandbox.ScratchKindSandbox].Dir)]
	_, unsandboxedHeld := pool.handles[canonicalScratchDir(slots[sandbox.ScratchKindUnsandboxed].Dir)]
	pool.mu.Unlock()
	if row.CurrentBindingID != bindingID || !sandboxHeld || !unsandboxedHeld {
		t.Fatalf("retry left the consumer on %q with handles pooled sandbox=%v unsandboxed=%v", row.CurrentBindingID, sandboxHeld, unsandboxedHeld)
	}
}

// TestScratchRefreshRetriesInstallHoldContention pins the round-3 contention
// contract at the install hold: a concurrent refresh holding the manifest lock
// makes the loser retry its passes rather than fail its restore outright, and
// a contention that never clears still fails loudly after the bound instead of
// silently swallowing the rows. The loser's first pass is synchronized to
// reach its install before the holder acquires, so the pass-1 contention is
// the install hold itself; the holder then never releases inside the window,
// so the loser deterministically exhausts the bound.
func TestScratchRefreshRetriesInstallHoldContention(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	const holderConsumer = "01REFRESHHOLDWIN1"
	const holderBinding = "b-hold-window"
	const loserConsumer = "01REFRESHLOSEWIN1"
	const loserBinding = "b-lose-window"
	holderSlots, _ := mintRefreshScratchBinding(t, s, holderBinding, sandbox.ScratchKindSandbox)
	mapRefreshScratchConsumer(t, s, holderConsumer, holderBinding)
	loserSlots, _ := mintRefreshScratchBinding(t, s, loserBinding, sandbox.ScratchKindSandbox)
	mapRefreshScratchConsumer(t, s, loserConsumer, loserBinding)
	releaseRefreshFixtureLeases(t, holderSlots)
	releaseRefreshFixtureLeases(t, loserSlots)
	loserDir := loserSlots[sandbox.ScratchKindSandbox].Dir

	var loserAttempts atomic.Int32
	s.cfg.testOnly.scratchRefreshOpenOverride = func(ref sandbox.ScratchReference, _ int) error {
		if ref.Dir == loserDir {
			loserAttempts.Add(1)
		}
		return nil
	}
	holderHeld := make(chan struct{})
	loserPaused := make(chan struct{})
	loserDone := make(chan struct{})
	var loserErr error
	// The loser pauses between its opens and its install — and only the
	// loser: the hook carries the refresh's session id — until the holder
	// owns the manifest lock, so the loser's first contention is its install
	// hold itself.
	s.cfg.testOnly.scratchRefreshBeforeInstall = func(sessionID string) {
		if sessionID == loserConsumer {
			close(loserPaused)
			<-holderHeld
		}
	}
	go func() {
		defer close(loserDone)
		loserErr = s.refreshRetainedScratchConsumer(loserConsumer)
	}()
	// Only after the loser is paused does the holder take the install hold
	// and keep it for the loser's whole remaining window.
	<-loserPaused
	s.cfg.testOnly.scratchRefreshAfterRecheck = func(sessionID string) {
		if sessionID == holderConsumer {
			close(holderHeld)
		}
		<-loserDone
	}
	if err := s.refreshRetainedScratchConsumer(holderConsumer); err != nil {
		t.Fatalf("holder refresh of %q: %v", holderConsumer, err)
	}
	<-loserDone
	if loserErr == nil {
		t.Fatal("a refresh that never won the lock returned success; the contention path is not fail-loud after the bound")
	}
	if got := loserAttempts.Load(); got < 2 {
		t.Fatalf("the loser failed its restore on pass %d of lock contention instead of retrying; contention must route through the bounded retry, not fail the send", got)
	}
}

// TestScratchRefreshReprobesContendedSlotAfterRelease pins the round-3
// contended re-probe: a slot the pool recorded contended — its lease was held
// by the racing idle-release teardown at a previous refresh — is re-probed on
// every refresh. A lease that landed back is reacquired and its contention
// record cleared; one still held proves the contention live and stays marked.
func TestScratchRefreshReprobesContendedSlotAfterRelease(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	const consumerID = "01REFRESHCONTENDED1"
	const bindingID = "b-contended-reprobe"
	slots, bindingRow := mintRefreshScratchBinding(t, s, bindingID, sandbox.ScratchKindSandbox, sandbox.ScratchKindUnsandboxed)
	mapRefreshScratchConsumer(t, s, consumerID, bindingID)
	freeKey := canonicalScratchDir(slots[sandbox.ScratchKindSandbox].Dir)
	heldKey := canonicalScratchDir(slots[sandbox.ScratchKindUnsandboxed].Dir)
	// The pool the previous refresh left: current rows, both slots recorded
	// contended, no handle for either — the teardown raced that restore and
	// its leases were still held then.
	s.retainedScratch.Store(&retainedScratchPool{
		owner:     owner,
		handles:   map[string]*sandbox.SessionScratch{},
		bindings:  map[string]sandbox.ScratchBinding{bindingID: bindingRow},
		consumers: map[string]sandbox.ScratchConsumerBinding{consumerID: {SessionID: consumerID, CurrentBindingID: bindingID}},
		contended: map[string]struct{}{freeKey: {}, heldKey: {}},
		adopted:   map[string]string{},
	})
	// The teardown has since settled for the sandbox slot only; the
	// unsandboxed slot's lease is still held in this process.
	if err := slots[sandbox.ScratchKindSandbox].Retain(); err != nil {
		t.Fatalf("release the settled slot's lease: %v", err)
	}
	t.Cleanup(func() { _ = slots[sandbox.ScratchKindUnsandboxed].Retain() })

	if err := s.refreshRetainedScratchConsumer(consumerID); err != nil {
		t.Fatalf("refresh of %q: %v", consumerID, err)
	}
	pool := s.retainedScratch.Load()
	pool.mu.Lock()
	_, freePooled := pool.handles[freeKey]
	_, freeStillContended := pool.contended[freeKey]
	_, heldStillContended := pool.contended[heldKey]
	_, heldPooled := pool.handles[heldKey]
	pool.mu.Unlock()
	if !freePooled {
		t.Fatal("a contended slot whose lease landed back was never re-probed; the restore lost its retained scratch")
	}
	if freeStillContended {
		t.Fatal("the re-probed slot's contention record outlived the reacquire that cleared it")
	}
	if !heldStillContended || heldPooled {
		t.Fatalf("a still-held slot must keep its contention record and gain no handle; contended=%v pooled=%v", heldStillContended, heldPooled)
	}
}
