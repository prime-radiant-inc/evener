package agent

import (
	"context"
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
	if err := sandbox.PinScratchBinding(owner, binding, owned, nil); err != nil {
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

// TestRestoreAdoptionSurvivesPoolDetachAfterDisposal pins the round-17 detach
// race: the pool can be swept after retainedConsumerScratchSlot approved the
// replacement but before the disposal branch's adoption claimed the handle.
// The disposal had already discarded the fresh mint, so the adoption's silent
// no-op left the restored delegate with a wrapper pointing at a retained
// directory it owns no lease on. The restore must re-provision the
// environment's own scratch instead, and report no transfer so the failure
// path still treats that scratch as a plain mint.
func TestRestoreAdoptionSurvivesPoolDetachAfterDisposal(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	const consumerID = "01RESTOREDETACH1"
	const bindingID = "b-restore-detach"
	slots, bindingRow := mintRefreshScratchBinding(t, s, bindingID, sandbox.ScratchKindSandbox)
	mapRefreshScratchConsumer(t, s, consumerID, bindingID)
	retainedDir := slots[sandbox.ScratchKindSandbox].Dir
	t.Cleanup(func() { _ = slots[sandbox.ScratchKindSandbox].Retain() })
	s.retainedScratch.Store(&retainedScratchPool{
		owner:     owner,
		handles:   map[string]*sandbox.SessionScratch{canonicalScratchDir(retainedDir): slots[sandbox.ScratchKindSandbox]},
		bindings:  map[string]sandbox.ScratchBinding{bindingID: bindingRow},
		consumers: map[string]sandbox.ScratchConsumerBinding{consumerID: {SessionID: consumerID, CurrentBindingID: bindingID}},
		contended: map[string]struct{}{},
		adopted:   map[string]string{},
	})

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

	// The detach lands inside the claim window — after the slot read approved
	// the replacement and the disposal discarded the mint.
	s.cfg.testOnly.scratchAdoptionBeforeClaim = func() {
		s.cfg.testOnly.scratchAdoptionBeforeClaim = nil
		s.retainedScratchSealed.Store(true)
		s.detachRetainedScratch()
	}
	adopted, err := s.adoptRestoredConsumerScratch(env, consumerID, true)
	if err != nil {
		t.Fatalf("restore adoption across the detach: %v", err)
	}
	if adopted {
		t.Fatal("a detached-pool fallback reported a transferred allocation")
	}
	// The restored session must own the scratch it runs in: the disposed
	// mint's replacement, not the retained directory beside its live holder.
	refs, err := env.ScratchRetentionReferences()
	if err != nil {
		t.Fatal(err)
	}
	owned := ""
	for _, ref := range refs {
		if ref.Kind == sandbox.ScratchKindSandbox {
			owned = ref.Dir
		}
	}
	if owned == "" {
		t.Fatalf("the detach left the restored session with no owned sandbox scratch (SessionScratchDir %q)", env.SessionScratchDir())
	}
	if filepath.Clean(owned) == filepath.Clean(retainedDir) {
		t.Fatalf("the restored session runs on the retained %q with no lease", retainedDir)
	}
	if got := env.SessionScratchDir(); filepath.Clean(got) != filepath.Clean(owned) {
		t.Fatalf("the wrapper %q does not name the owned scratch %q", got, owned)
	}
	if _, err := os.Stat(owned); err != nil {
		t.Fatalf("the re-provisioned scratch is not on disk: %v", err)
	}
}

// TestRestoreAdoptionReprovisionsWhenTheClaimTurnsContended pins the round-21
// claim-flip hole: the replacement branch's guard reads the slot under one
// pool.mu hold and the claim resolves under another, and a refresh fold racing
// the two can flip the slot to contended in between. The adoption then marks
// the kind pending and reports installed with NO handle transferred — and with
// the fresh mint already disposed, the round-17 heal keyed on that installed
// report never fired, leaving the restored delegate running on the retained
// directory with no lease. The heal must key on the transfer the environment
// actually owns.
func TestRestoreAdoptionReprovisionsWhenTheClaimTurnsContended(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	const consumerID = "01CLAIMFLIP1"
	const bindingID = "b-claim-flip"
	slots, bindingRow := mintRefreshScratchBinding(t, s, bindingID, sandbox.ScratchKindSandbox)
	mapRefreshScratchConsumer(t, s, consumerID, bindingID)
	retainedDir := slots[sandbox.ScratchKindSandbox].Dir
	t.Cleanup(func() { _ = slots[sandbox.ScratchKindSandbox].Retain() })
	key := canonicalScratchDir(retainedDir)
	s.retainedScratch.Store(&retainedScratchPool{
		owner:     owner,
		handles:   map[string]*sandbox.SessionScratch{key: slots[sandbox.ScratchKindSandbox]},
		bindings:  map[string]sandbox.ScratchBinding{bindingID: bindingRow},
		consumers: map[string]sandbox.ScratchConsumerBinding{consumerID: {SessionID: consumerID, CurrentBindingID: bindingID}},
		contended: map[string]struct{}{},
		adopted:   map[string]string{},
	})

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

	// The fold lands between the guard's read and the claim's own hold: the
	// handle leaves the pool and the contention mark goes up — exactly what a
	// racing refresh converging onto a held-elsewhere lease does.
	s.cfg.testOnly.scratchAdoptionBeforeClaim = func() {
		s.cfg.testOnly.scratchAdoptionBeforeClaim = nil
		pool := s.retainedScratch.Load()
		pool.mu.Lock()
		delete(pool.handles, key)
		pool.contended[key] = struct{}{}
		pool.mu.Unlock()
	}
	adopted, err := s.adoptRestoredConsumerScratch(env, consumerID, true)
	if err != nil {
		t.Fatalf("restore adoption across the contended flip: %v", err)
	}
	if adopted {
		t.Fatal("a contended-claim fallback reported a transferred allocation")
	}
	refs, err := env.ScratchRetentionReferences()
	if err != nil {
		t.Fatal(err)
	}
	owned := ""
	for _, ref := range refs {
		if ref.Kind == sandbox.ScratchKindSandbox {
			owned = ref.Dir
		}
	}
	if owned == "" {
		t.Fatalf("the contended flip left the restored session with no owned sandbox scratch (SessionScratchDir %q)", env.SessionScratchDir())
	}
	if filepath.Clean(owned) == filepath.Clean(retainedDir) {
		t.Fatalf("the restored session runs on the retained %q with no lease", retainedDir)
	}
	if got := env.SessionScratchDir(); filepath.Clean(got) != filepath.Clean(owned) {
		t.Fatalf("the wrapper %q does not name the owned scratch %q", got, owned)
	}
	if _, err := os.Stat(owned); err != nil {
		t.Fatalf("the re-provisioned scratch is not on disk: %v", err)
	}
}

// TestScratchRestoreAdoptionReportsNoTransferForABorrow pins the round-22
// adoption-report lie: a replacement whose claim finds the slot already
// adopted by a distinct consumer installs a lease-less borrow — no retained
// allocation was transferred — but the heal's ownership check read the
// borrowed directory as a transferred one and reported adoption. The caller
// keys its failure path on that report ("actually transferred"), routing a
// later restore failure to the retain handoff instead of the settlement.
func TestScratchRestoreAdoptionReportsNoTransferForABorrow(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	const consumerID = "01BORROWREPORT1"
	const sharerID = "01BORROWSHARER1"
	const bindingID = "b-borrow-report"
	slots, bindingRow := mintRefreshScratchBinding(t, s, bindingID, sandbox.ScratchKindSandbox)
	mapRefreshScratchConsumer(t, s, consumerID, bindingID)
	retainedDir := slots[sandbox.ScratchKindSandbox].Dir
	t.Cleanup(func() { _ = slots[sandbox.ScratchKindSandbox].Retain() })
	key := canonicalScratchDir(retainedDir)
	// The slot is already adopted by a distinct consumer, so the restore's
	// claim takes the lease-less borrow — exactly the way a second
	// environment sharing one allocation does.
	s.retainedScratch.Store(&retainedScratchPool{
		owner:     owner,
		handles:   map[string]*sandbox.SessionScratch{key: slots[sandbox.ScratchKindSandbox]},
		bindings:  map[string]sandbox.ScratchBinding{bindingID: bindingRow},
		consumers: map[string]sandbox.ScratchConsumerBinding{consumerID: {SessionID: consumerID, CurrentBindingID: bindingID}},
		adopted:   map[string]string{key: sharerID},
		contended: map[string]struct{}{},
	})

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

	adopted, err := s.adoptRestoredConsumerScratch(env, consumerID, true)
	if err != nil {
		t.Fatalf("restore adoption onto the borrowed slot: %v", err)
	}
	if adopted {
		t.Fatal("a lease-less borrow reported a transferred retained allocation")
	}
	// The borrow itself stands: the environment renders through the shared
	// retained directory without doubling its lease.
	if owned := envScratchRefDir(env, sandbox.ScratchKindSandbox); filepath.Clean(owned) != filepath.Clean(retainedDir) {
		t.Fatalf("the restored session did not borrow the retained %q (owned %q)", retainedDir, owned)
	}
}

// TestScratchRestoreAdoptionRefusesAReleasedManifestsStaleRows pins round 24's
// first Medium: the refresh declined on a released manifest without clearing
// the pool, so the adoption seam reading the pool right below it served the
// stale consumer and binding rows — it rebuilt the wrapper around the retained
// directory, disposed the fresh mint, and transferred a handle the durable
// manifest no longer authorizes. The decline must retire the stale rows so the
// restore keeps its fresh scratch.
func TestScratchRestoreAdoptionRefusesAReleasedManifestsStaleRows(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	const consumerID = "01RELEASESTALE1"
	const bindingID = "b-release-stale"
	slots, bindingRow := mintRefreshScratchBinding(t, s, bindingID, sandbox.ScratchKindSandbox)
	mapRefreshScratchConsumer(t, s, consumerID, bindingID)
	retainedDir := slots[sandbox.ScratchKindSandbox].Dir
	t.Cleanup(func() { _ = slots[sandbox.ScratchKindSandbox].Retain() })
	key := canonicalScratchDir(retainedDir)
	s.retainedScratch.Store(&retainedScratchPool{
		owner:     owner,
		handles:   map[string]*sandbox.SessionScratch{key: slots[sandbox.ScratchKindSandbox]},
		bindings:  map[string]sandbox.ScratchBinding{bindingID: bindingRow},
		consumers: map[string]sandbox.ScratchConsumerBinding{consumerID: {SessionID: consumerID, CurrentBindingID: bindingID}},
		adopted:   map[string]string{},
		contended: map[string]struct{}{},
	})
	// The terminal release tombstones the manifest the pool's rows were derived
	// from; the pool survives with the stale rows in place.
	if err := sandbox.ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("terminal release: %v", err)
	}

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

	adopted, err := s.adoptRestoredConsumerScratch(env, consumerID, true)
	if err != nil {
		t.Fatalf("restore adoption over the released manifest: %v", err)
	}
	if adopted {
		t.Fatal("the restore adopted a retained allocation a released manifest no longer authorizes")
	}
	if got := env.SessionScratchDir(); filepath.Clean(got) != filepath.Clean(fresh) {
		t.Fatalf("the restore left the fresh mint %q and now runs on %q", fresh, got)
	}
	pool := s.retainedScratch.Load()
	if pool == nil {
		t.Fatal("the declined refresh detached the pool")
	}
	pool.mu.Lock()
	_, staleRow := pool.consumers[consumerID]
	pool.mu.Unlock()
	if staleRow {
		t.Fatal("the declined refresh left the stale consumer row servable")
	}
}

// TestScratchRefreshDeclineDropsRowsTheManifestNoLongerHolds pins the
// live-manifest branch of round 24's first Medium: a consumer row the manifest
// no longer contains — here one the manifest never held, the same shape a
// reset's row narrowing leaves after it drops a consumer whose binding did not
// carry — must not survive the refresh's decline in the pool, where the
// adoption seam would keep serving it as though the manifest authorized it.
func TestScratchRefreshDeclineDropsRowsTheManifestNoLongerHolds(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	const consumerID = "01UNMAPPEDROWS1"
	const bindingID = "b-unmapped-rows"
	slots, bindingRow := mintRefreshScratchBinding(t, s, bindingID, sandbox.ScratchKindSandbox)
	t.Cleanup(func() { _ = slots[sandbox.ScratchKindSandbox].Retain() })
	key := canonicalScratchDir(slots[sandbox.ScratchKindSandbox].Dir)
	s.retainedScratch.Store(&retainedScratchPool{
		owner:     owner,
		handles:   map[string]*sandbox.SessionScratch{key: slots[sandbox.ScratchKindSandbox]},
		bindings:  map[string]sandbox.ScratchBinding{bindingID: bindingRow},
		consumers: map[string]sandbox.ScratchConsumerBinding{consumerID: {SessionID: consumerID, CurrentBindingID: bindingID}},
		adopted:   map[string]string{},
		contended: map[string]struct{}{},
	})
	if err := s.refreshRetainedScratchConsumer(consumerID); err != nil {
		t.Fatalf("the decline must not fail the restore: %v", err)
	}
	pool := s.retainedScratch.Load()
	if pool == nil {
		t.Fatal("the declined refresh detached the pool")
	}
	pool.mu.Lock()
	_, staleRow := pool.consumers[consumerID]
	pool.mu.Unlock()
	if staleRow {
		t.Fatal("the declined refresh left the stale consumer row servable")
	}
}

// TestRestoreAdoptionSurvivesPoolDetachBetweenLoads pins round 18's High: the
// pool can detach between adoptConsumerScratch's row read and
// adoptRetainedScratchFor's own pool load. The inner transfer silently no-ops
// on a detached pool while the outer adoption reported a successful transfer,
// so the round-17 heal never fired and the restored delegate kept running on
// a wrapper naming a retained directory it owns no lease on.
func TestRestoreAdoptionSurvivesPoolDetachBetweenLoads(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	const consumerID = "01RESTOREDETACH2"
	const bindingID = "b-restore-detach-2"
	slots, bindingRow := mintRefreshScratchBinding(t, s, bindingID, sandbox.ScratchKindSandbox)
	mapRefreshScratchConsumer(t, s, consumerID, bindingID)
	retainedDir := slots[sandbox.ScratchKindSandbox].Dir
	t.Cleanup(func() { _ = slots[sandbox.ScratchKindSandbox].Retain() })
	s.retainedScratch.Store(&retainedScratchPool{
		owner:     owner,
		handles:   map[string]*sandbox.SessionScratch{canonicalScratchDir(retainedDir): slots[sandbox.ScratchKindSandbox]},
		bindings:  map[string]sandbox.ScratchBinding{bindingID: bindingRow},
		consumers: map[string]sandbox.ScratchConsumerBinding{consumerID: {SessionID: consumerID, CurrentBindingID: bindingID}},
		contended: map[string]struct{}{},
		adopted:   map[string]string{},
	})

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

	// The detach lands in the second window: after the outer adoption read
	// the consumer row, before the transfer reloads the pool.
	s.cfg.testOnly.scratchAdoptionBeforeTransfer = func() {
		s.cfg.testOnly.scratchAdoptionBeforeTransfer = nil
		s.retainedScratchSealed.Store(true)
		s.detachRetainedScratch()
	}
	adopted, err := s.adoptRestoredConsumerScratch(env, consumerID, true)
	if err != nil {
		t.Fatalf("restore adoption across the between-loads detach: %v", err)
	}
	if adopted {
		t.Fatal("a detached-pool no-op reported a transferred allocation")
	}
	refs, err := env.ScratchRetentionReferences()
	if err != nil {
		t.Fatal(err)
	}
	owned := ""
	for _, ref := range refs {
		if ref.Kind == sandbox.ScratchKindSandbox {
			owned = ref.Dir
		}
	}
	if owned == "" {
		t.Fatalf("the between-loads detach left the restored session with no owned sandbox scratch (SessionScratchDir %q)", env.SessionScratchDir())
	}
	if filepath.Clean(owned) == filepath.Clean(retainedDir) {
		t.Fatalf("the restored session runs on the retained %q with no lease", retainedDir)
	}
	if got := env.SessionScratchDir(); filepath.Clean(got) != filepath.Clean(owned) {
		t.Fatalf("the wrapper %q does not name the owned scratch %q", got, owned)
	}
	if _, err := os.Stat(owned); err != nil {
		t.Fatalf("the re-provisioned scratch is not on disk: %v", err)
	}
}

// TestRestoreAdoptsPoolOwnedHandleDespiteStaleContentionMark pins the round-5
// healing at the replacement guard: a pool left holding both a reacquired
// handle and a contention record for the same directory — the wedged state
// the pre-round-5 refresh could write — must still adopt. A pooled handle is
// transferable, so the marker is stale by definition, and the next restore
// takes the retained directory instead of running beside the stranded
// allocation.
func TestRestoreAdoptsPoolOwnedHandleDespiteStaleContentionMark(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	const consumerID = "01RESTOREPOOLHEAL1"
	const bindingID = "b-pool-heal"
	slots, bindingRow := mintRefreshScratchBinding(t, s, bindingID, sandbox.ScratchKindSandbox)
	mapRefreshScratchConsumer(t, s, consumerID, bindingID)
	releaseRefreshFixtureLeases(t, slots)
	retainedDir := slots[sandbox.ScratchKindSandbox].Dir
	handle, err := sandbox.OpenRetainedSessionScratch(owner, sandbox.ScratchReference{Dir: retainedDir, Kind: sandbox.ScratchKindSandbox})
	if err != nil {
		t.Fatalf("pool-owned fixture handle: %v", err)
	}
	// The wedged pool: its own reacquired handle AND a contention record for
	// the same directory, with rows current so the restore adopts from the
	// pool.
	key := canonicalScratchDir(retainedDir)
	s.retainedScratch.Store(&retainedScratchPool{
		owner:     owner,
		handles:   map[string]*sandbox.SessionScratch{key: handle},
		bindings:  map[string]sandbox.ScratchBinding{bindingID: bindingRow},
		consumers: map[string]sandbox.ScratchConsumerBinding{consumerID: {SessionID: consumerID, CurrentBindingID: bindingID}},
		contended: map[string]struct{}{key: {}},
		adopted:   map[string]string{},
	})
	t.Cleanup(func() { _ = slots[sandbox.ScratchKindSandbox].Retain() })

	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	t.Cleanup(func() { env.Cleanup(); env.DisposeSandboxScratch() })
	policy := sbxResolve(t, sbxBwrapFacts(t.TempDir()), env.WorkingDirectory(), sandbox.ModeWorkspaceWrite)
	if err := env.EnableSandbox(policy); err != nil {
		t.Fatalf("provision fresh sandbox scratch: %v", err)
	}

	if _, err := s.adoptRestoredConsumerScratch(env, consumerID, true); err != nil {
		t.Fatalf("restore adoption over a stale contention mark: %v", err)
	}
	if got := env.SessionScratchDir(); filepath.Clean(got) != filepath.Clean(retainedDir) {
		t.Fatalf("the pooled handle behind the stale mark was not adopted; the restored session runs on %q, want the retained %q", got, retainedDir)
	}
}

// TestRestoreKeepsFreshScratchWhenStaleAdoptedClaimStillHeld pins the round-7
// teardown window: a slot this session previously adopted, whose lease the
// idle-release teardown still holds while the runtime pointers are already
// cleared, must read as contended to the replacement guard. The refresh's
// stale-claim probe finds the lease held and must stamp the contention it
// proved — an adopted claim carries no contended record to fall back on, so an
// unstamped proof leaves the guard blind: the replacement disposes the fresh
// scratch and the adoption then fails "already transferred" against the
// session's own stale claim, a user-visible restore failure for every send
// inside the teardown window. Stamped, the restore keeps its fresh scratch and
// the next refresh re-probes the settled lease, re-pools the handle, and
// clears both records.
func TestRestoreKeepsFreshScratchWhenStaleAdoptedClaimStillHeld(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	const consumerID = "01RESTOREADOPTED1"
	const bindingID = "b-adopted-held"
	slots, bindingRow := mintRefreshScratchBinding(t, s, bindingID, sandbox.ScratchKindSandbox)
	mapRefreshScratchConsumer(t, s, consumerID, bindingID)
	releaseRefreshFixtureLeases(t, slots)
	retainedDir := slots[sandbox.ScratchKindSandbox].Dir
	key := canonicalScratchDir(retainedDir)

	// The idle-release window: the session's previous runtime adopted the slot
	// (the claim recorded, the handle gone from the pool) and its teardown
	// still holds the lease while the runtime pointers are already cleared — a
	// concurrent send cold-restores now, with rows current so the refresh
	// takes the stale-claim probe path.
	teardownLease, err := sandbox.OpenRetainedSessionScratch(owner, sandbox.ScratchReference{Dir: retainedDir, Kind: sandbox.ScratchKindSandbox})
	if err != nil {
		t.Fatalf("teardown-held fixture lease: %v", err)
	}
	s.retainedScratch.Store(&retainedScratchPool{
		owner:     owner,
		handles:   map[string]*sandbox.SessionScratch{},
		bindings:  map[string]sandbox.ScratchBinding{bindingID: bindingRow},
		consumers: map[string]sandbox.ScratchConsumerBinding{consumerID: {SessionID: consumerID, CurrentBindingID: bindingID}},
		contended: map[string]struct{}{},
		adopted:   map[string]string{key: consumerID},
	})
	t.Cleanup(func() { _ = slots[sandbox.ScratchKindSandbox].Retain() })
	t.Cleanup(func() { _ = teardownLease.Retain() })

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
		t.Fatalf("restore over a teardown-held adopted claim: %v", err)
	}
	pool := s.retainedScratch.Load()
	pool.mu.Lock()
	_, marked := pool.contended[key]
	pool.mu.Unlock()
	if !marked {
		t.Fatal("the stale-claim probe left the teardown-held adopted slot unstamped; the replacement guard is blind to the contention it proved")
	}
	if got := env.SessionScratchDir(); filepath.Clean(got) != filepath.Clean(fresh) {
		t.Fatalf("the teardown-held adopted slot's replacement disposed the fresh scratch %q; the restored session now runs on %q", fresh, got)
	}
}

// TestScratchRefreshNeverContendsPoolOwnedHandle pins the round-5 ownership
// invariant at the refresh's reacquire: a slot whose lease the POOL already
// holds — a handle a prior refresh reacquired and never adopted — is not
// contention. The re-open fails with the lease-held sentinel against the
// pool's own handle, a transferable allocation, so marking it contended
// would wedge the consumer permanently: the marker blocks every later
// adoption while the pooled handle forever fails the re-probe against
// itself. The refresh must leave the slot unmarked and the handle pooled.
func TestScratchRefreshNeverContendsPoolOwnedHandle(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	const consumerID = "01REFRESHPOOLOWN1"
	const bindingID = "b-pool-owned"
	slots, _ := mintRefreshScratchBinding(t, s, bindingID, sandbox.ScratchKindSandbox)
	mapRefreshScratchConsumer(t, s, consumerID, bindingID)
	releaseRefreshFixtureLeases(t, slots)
	key := canonicalScratchDir(slots[sandbox.ScratchKindSandbox].Dir)

	// A prior refresh reacquired the slot, and its adoption never took the
	// handle: the pool owns the lease while the consumer's rows are missing,
	// so this refresh re-installs its rows and re-opens its own slot.
	handle, err := sandbox.OpenRetainedSessionScratch(owner, sandbox.ScratchReference{Dir: slots[sandbox.ScratchKindSandbox].Dir, Kind: sandbox.ScratchKindSandbox})
	if err != nil {
		t.Fatalf("pool-owned fixture handle: %v", err)
	}
	s.retainedScratch.Store(&retainedScratchPool{
		owner:     owner,
		handles:   map[string]*sandbox.SessionScratch{key: handle},
		bindings:  map[string]sandbox.ScratchBinding{},
		consumers: map[string]sandbox.ScratchConsumerBinding{},
		contended: map[string]struct{}{},
		adopted:   map[string]string{},
	})

	if err := s.refreshRetainedScratchConsumer(consumerID); err != nil {
		t.Fatalf("refresh of %q: %v", consumerID, err)
	}
	pool := s.retainedScratch.Load()
	if pool == nil {
		t.Fatal("no pool was published")
	}
	pool.mu.Lock()
	_, marked := pool.contended[key]
	pooled := pool.handles[key]
	pool.mu.Unlock()
	if marked {
		t.Fatal("the refresh marked the pool's own handle contended; the marker wedges every later adoption against a transferable pooled lease")
	}
	if pooled == nil {
		t.Fatal("the refresh dropped the pool's own reacquired handle")
	}
}

// TestScratchRefreshClearsStaleContendedMarkBesidePooledHandle pins the
// round-9 cleanup half of the disjointness invariant: a contention mark a
// pre-round-9 refresh could write beside a pooled handle is inert (the
// guard requires no handle), but it breaks the maps' disjoint shape, so the
// install path drops it when it finds the slot pool-owned instead of leaving
// the wedged-looking state behind forever.
func TestScratchRefreshClearsStaleContendedMarkBesidePooledHandle(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	const consumerID = "01REFRESHMARKCLR1"
	const bindingID = "b-mark-clear"
	slots, _ := mintRefreshScratchBinding(t, s, bindingID, sandbox.ScratchKindSandbox)
	mapRefreshScratchConsumer(t, s, consumerID, bindingID)
	releaseRefreshFixtureLeases(t, slots)
	key := canonicalScratchDir(slots[sandbox.ScratchKindSandbox].Dir)
	handle, err := sandbox.OpenRetainedSessionScratch(owner, sandbox.ScratchReference{
		Dir:  slots[sandbox.ScratchKindSandbox].Dir,
		Kind: sandbox.ScratchKindSandbox,
	})
	if err != nil {
		t.Fatalf("pool-owned fixture handle: %v", err)
	}
	// The wedged-looking state: the pool's own reacquired handle AND a stale
	// contention mark for the same directory, with the consumer's rows
	// missing so the refresh re-installs them (installRows).
	s.retainedScratch.Store(&retainedScratchPool{
		owner:     owner,
		handles:   map[string]*sandbox.SessionScratch{key: handle},
		bindings:  map[string]sandbox.ScratchBinding{},
		consumers: map[string]sandbox.ScratchConsumerBinding{},
		contended: map[string]struct{}{key: {}},
		adopted:   map[string]string{},
	})

	if err := s.refreshRetainedScratchConsumer(consumerID); err != nil {
		t.Fatalf("refresh of %q: %v", consumerID, err)
	}
	pool := s.retainedScratch.Load()
	if pool == nil {
		t.Fatal("no pool was published")
	}
	pool.mu.Lock()
	_, marked := pool.contended[key]
	pooled := pool.handles[key]
	pool.mu.Unlock()
	if marked {
		t.Fatal("the refresh left a contention mark beside the pooled handle; the stale mark breaks the maps' disjoint shape")
	}
	if pooled == nil {
		t.Fatal("the refresh dropped the pool's own reacquired handle")
	}
}

// TestScratchUpsertRetriesLockContention pins the round-9 third-writer retry:
// the manifest upserts that publish a consumer's roles fail fast on the
// manifest lock like every writer, and that refusal is transient — never a
// durability verdict — so the upsert retries it with backoff instead of
// failing the publication (or letting the swap path record it sticky). The
// contention is injected deterministically: a helper holds the lock
// verifiably across the first upsert attempt and verifiably releases before
// the retry.
func TestScratchUpsertRetriesLockContention(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())

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
	attempts := 0
	s.cfg.testOnly.scratchUpsertAttempt = func() {
		attempts++
		switch attempts {
		case 1:
			close(takeLock)
			<-lockTaken
		case 2:
			close(releaseLock)
			<-lockReleased
		}
	}
	if err := s.installScratchRetention(env); err != nil {
		t.Fatalf("mint publication lost to transient lock contention: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("the upsert took %d attempts, want exactly 2: one refused by the holder, one retried after its release", attempts)
	}
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, consumer := range manifest.Consumers {
		if consumer.SessionID == s.id && consumer.CurrentBindingID != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the retried upsert never published the consumer role: %+v", manifest.Consumers)
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
	if !errors.Is(windowErr, sandbox.ErrScratchRetentionLockHeld) {
		t.Fatalf("in-window update refused with %v; want the install hold's lock sentinel", windowErr)
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
		// The production detach path: serialized on the pool mutex, so the
		// fold the pass is about to run declines under the same mutex and
		// the pass retries against the now-current pointer.
		s.detachRetainedScratch()
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
	var loserPausedOnce sync.Once
	loserDone := make(chan struct{})
	var loserErr error
	// The loser pauses between its opens and its install — and only the
	// loser: the hook carries the refresh's session id — until the holder
	// owns the manifest lock, so the loser's first contention is its install
	// hold itself.
	s.cfg.testOnly.scratchRefreshBeforeInstall = func(sessionID string) {
		if sessionID == loserConsumer {
			loserPausedOnce.Do(func() { close(loserPaused) })
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
	// EVERGREEN, because it reads like one: this holder-side hook waits on
	// loserDone while the holder's refresh still owns the manifest's update
	// lock, which looks like a deadlock — the loser "blocked" on the lock the
	// holder is waiting on. It cannot be one: the manifest's update lock is
	// fail-fast BY DESIGN (a contended writer is refused with
	// ErrScratchRetentionLockHeld, never parked), so the loser is never
	// blocked on the lock while the holder waits here. The loser burns its
	// bounded refused passes — every open and install attempt is refused
	// outright, each costing microseconds — fails loudly after the bound,
	// and closes loserDone, which the loserErr and loserAttempts assertions
	// below prove happened. A deadlock here would hang this test on every
	// run; it instead finishes in well under a second everywhere it runs.
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

// TestScratchRefreshRetriesInstallHoldContentionClears pins the round-3
// retry at install depth END-TO-END: a refresh whose install loses the
// manifest lock to another in-process writer must SUCCEED on its next pass
// once the holder releases, not merely fail loudly after the bound. The
// re-reached before-install hook also pins the hook contract: lock-contention
// retries re-run test hooks, so a hook's one-shot signaling must be
// close-once — an unguarded close panics on the retry pass instead of
// exercising the retry (round 5).
func TestScratchRefreshRetriesInstallHoldContentionClears(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	const consumerID = "01REFRESHHOLDCLR1"
	const bindingID = "b-hold-clears"
	slots, _ := mintRefreshScratchBinding(t, s, bindingID, sandbox.ScratchKindSandbox)
	mapRefreshScratchConsumer(t, s, consumerID, bindingID)
	releaseRefreshFixtureLeases(t, slots)

	// A second in-process writer holds the manifest lock exactly for the
	// refresh's first install window: the hook hands the lock over before
	// the install attempt, and the retry pass's first reacquire open is
	// synchronized after the verifiable release — so pass 1 deterministically
	// loses the install hold and pass 2 deterministically recovers.
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
	var opens atomic.Int32
	var hookPasses atomic.Int32
	s.cfg.testOnly.scratchRefreshOpenOverride = func(ref sandbox.ScratchReference, _ int) error {
		if opens.Add(1) == 2 {
			// The retry pass's first open: release the install-hold holder
			// and wait for its lock to verifiably drop before the real open.
			close(releaseLock)
			<-lockReleased
		}
		return nil
	}
	// The hook's one-shot handover is close-once: lock-contention retries
	// re-run the hook, and an unguarded close would panic on the retry pass
	// instead of exercising the bounded retry.
	s.cfg.testOnly.scratchRefreshBeforeInstall = func(sessionID string) {
		if hookPasses.Add(1) == 1 {
			close(takeLock)
			<-lockTaken
		}
	}
	if err := s.refreshRetainedScratchConsumer(consumerID); err != nil {
		t.Fatalf("install-depth contention did not recover once the holder released: %v", err)
	}
	if got := hookPasses.Load(); got < 2 {
		t.Fatalf("the retry never re-ran the before-install hook (%d passes); hooks must be idempotent across lock-contention retries", got)
	}
	pool := s.retainedScratch.Load()
	if pool == nil {
		t.Fatal("no pool was published")
	}
	pool.mu.Lock()
	row := pool.consumers[consumerID]
	_, sandboxHeld := pool.handles[canonicalScratchDir(slots[sandbox.ScratchKindSandbox].Dir)]
	pool.mu.Unlock()
	if row.CurrentBindingID != bindingID || !sandboxHeld {
		t.Fatalf("recovery left the consumer on %q with its handle pooled=%v", row.CurrentBindingID, sandboxHeld)
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

// TestContendedFallbackScratchIsPinnedAtAdoption pins round 19's Medium: the
// pending marker protected the binding row but nothing pinned the fallback
// mint at adoption — the mint hook ran before the binding install, and a
// restored session that crashes or idles out before its next publication
// leaves the fallback it works in collectible. The fallback must be pinned as
// a bare protected reference the moment its kind is marked pending.
func TestContendedFallbackScratchIsPinnedAtAdoption(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	const consumerID = "01CONTENDEDPIN1"
	const bindingID = "b-contended-pin"
	slots, bindingRow := mintRefreshScratchBinding(t, s, bindingID, sandbox.ScratchKindSandbox)
	mapRefreshScratchConsumer(t, s, consumerID, bindingID)
	retainedDir := slots[sandbox.ScratchKindSandbox].Dir
	t.Cleanup(func() { _ = slots[sandbox.ScratchKindSandbox].Retain() })
	// The contended pool: the fixture handle keeps its lease (the in-process
	// contention the adoption must skip), no reacquired handle.
	s.retainedScratch.Store(&retainedScratchPool{
		owner:     owner,
		handles:   map[string]*sandbox.SessionScratch{},
		bindings:  map[string]sandbox.ScratchBinding{bindingID: bindingRow},
		consumers: map[string]sandbox.ScratchConsumerBinding{consumerID: {SessionID: consumerID, CurrentBindingID: bindingID}},
		contended: map[string]struct{}{canonicalScratchDir(retainedDir): {}},
		adopted:   map[string]string{},
	})

	// The production resume shape: the sandbox is provisioned and its fresh
	// scratch minted BEFORE adoption — the mint whose pin hook no-ops, because
	// the binding is only installed by the adoption that follows.
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	t.Cleanup(func() { env.Cleanup(); env.DisposeSandboxScratch(); env.DisposeUnsandboxedScratch() })
	policy := sbxResolve(t, sbxBwrapFacts(t.TempDir()), env.WorkingDirectory(), sandbox.ModeWorkspaceWrite)
	if err := env.EnableSandbox(policy); err != nil {
		t.Fatalf("provision the resume-time fresh sandbox scratch: %v", err)
	}
	freshSandbox := env.SessionScratchDir()
	if freshSandbox == "" || filepath.Clean(freshSandbox) == filepath.Clean(retainedDir) {
		t.Fatalf("fixture fresh scratch %q must exist apart from the retained %q", freshSandbox, retainedDir)
	}

	if _, err := s.adoptRestoredConsumerScratch(env, consumerID, true); err != nil {
		t.Fatalf("restore adoption over a contended sandbox slot: %v", err)
	}
	// No first-command publication runs between the adoption and this read:
	// the fallback's protection must not depend on one arriving before a
	// crash or an idle release.
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	row, ok := findScratchBinding(manifest, bindingID)
	if !ok {
		t.Fatalf("binding %q vanished from the manifest", bindingID)
	}
	if slot, ok := row.Slots[sandbox.ScratchKindSandbox]; !ok || filepath.Clean(slot.Dir) != filepath.Clean(retainedDir) {
		t.Fatalf("the fallback displaced the binding row's slot: got %+v, want the retained %q", row.Slots, retainedDir)
	}
	pinned := false
	for _, ref := range manifest.References {
		if filepath.Clean(ref.Dir) == filepath.Clean(freshSandbox) {
			pinned = true
		}
	}
	if !pinned {
		t.Fatalf("the fallback sandbox mint %q was left unpinned at adoption: its work is collectible before the next publication", freshSandbox)
	}
}

// TestContendedRetainedSlotKeepsBindingRowAcrossMint pins the round-10
// continuity contract for the production cold-restore shape: resume provisions
// the sandbox and EnableSandbox mints a fresh scratch BEFORE adoption, so a
// contended retained slot is shadowed by a live allocation and the adoption
// leaves both in place — the delegate runs this cycle in the fallback. The
// binding row on the manifest is the only place a later refresh learns which
// directory to re-probe — its stale-claim set is derived from the live rows —
// so a fallback mint that overwrote the row's slot would end the retry: every
// subsequent restore would resume in the empty fallback, and the original
// directory's files would never come back. The mint must stay pinned for
// protection, but as a bare reference: a graph shape
// validateRetainedScratchGraph deliberately sanctions (a "historical" pinned
// reference no slot owns).
func TestContendedRetainedSlotKeepsBindingRowAcrossMint(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	const consumerID = "01CONTENDEDMINT1"
	const bindingID = "b-contended-mint"
	// The fixture handle KEEPS its lease: that held lease is the in-process
	// contention the adoption must skip (the way the idle-release teardown
	// holds it across its own runtime-pointer window).
	slots, bindingRow := mintRefreshScratchBinding(t, s, bindingID, sandbox.ScratchKindSandbox)
	mapRefreshScratchConsumer(t, s, consumerID, bindingID)
	retainedDir := slots[sandbox.ScratchKindSandbox].Dir
	t.Cleanup(func() { _ = slots[sandbox.ScratchKindSandbox].Retain() })
	s.retainedScratch.Store(&retainedScratchPool{
		owner:     owner,
		handles:   map[string]*sandbox.SessionScratch{},
		bindings:  map[string]sandbox.ScratchBinding{bindingID: bindingRow},
		consumers: map[string]sandbox.ScratchConsumerBinding{consumerID: {SessionID: consumerID, CurrentBindingID: bindingID}},
		contended: map[string]struct{}{canonicalScratchDir(retainedDir): {}},
		adopted:   map[string]string{},
	})

	// The production resume shape: the sandbox is provisioned and its fresh
	// scratch minted BEFORE adoption, exactly the flow whose live allocation
	// shadows the contended slot.
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	t.Cleanup(func() { env.Cleanup(); env.DisposeSandboxScratch(); env.DisposeUnsandboxedScratch() })
	policy := sbxResolve(t, sbxBwrapFacts(t.TempDir()), env.WorkingDirectory(), sandbox.ModeWorkspaceWrite)
	if err := env.EnableSandbox(policy); err != nil {
		t.Fatalf("provision the resume-time fresh sandbox scratch: %v", err)
	}
	freshSandbox := env.SessionScratchDir()
	if freshSandbox == "" || filepath.Clean(freshSandbox) == filepath.Clean(retainedDir) {
		t.Fatalf("fixture fresh scratch %q must exist apart from the retained %q", freshSandbox, retainedDir)
	}

	if _, err := s.adoptRestoredConsumerScratch(env, consumerID, true); err != nil {
		t.Fatalf("restore adoption over a contended sandbox slot: %v", err)
	}
	if got := env.SessionScratchDir(); filepath.Clean(got) != filepath.Clean(freshSandbox) {
		t.Fatalf("the contended slot's replacement disposed the fresh scratch %q; the restored session now runs on %q", freshSandbox, got)
	}

	// The delegate's first unsandboxed command mints a second allocation and
	// publishes every owned kind in one pin transaction. The install routes
	// through RestoreSessionScratch only because the test package cannot
	// reach the env-internal mint; the sandbox kind's pending marker is what
	// the assertion exercises, and an unsandboxed install does not touch it.
	freshUnsandboxed, err := sandbox.NewSessionScratch(t.TempDir(), env.WorkingDirectory())
	if err != nil {
		t.Fatalf("mint the first-command unsandboxed scratch: %v", err)
	}
	t.Cleanup(func() { _ = freshUnsandboxed.Retain() })
	if err := env.RestoreSessionScratch(bindingID, sandbox.ScratchReference{Dir: freshUnsandboxed.Dir, Kind: sandbox.ScratchKindUnsandboxed}, freshUnsandboxed); err != nil {
		t.Fatalf("install the first-command unsandboxed scratch: %v", err)
	}
	if err := env.PinOwnedScratch(); err != nil {
		t.Fatalf("publish the owned allocations: %v", err)
	}

	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	row, ok := findScratchBinding(manifest, bindingID)
	if !ok {
		t.Fatalf("binding %q vanished from the manifest", bindingID)
	}
	slot, ok := row.Slots[sandbox.ScratchKindSandbox]
	if !ok || filepath.Clean(slot.Dir) != filepath.Clean(retainedDir) {
		t.Fatalf("the fallback mint displaced the binding row's slot: got %+v, want the retained %q — no later refresh will re-probe the original", slot, retainedDir)
	}
	pinned := false
	for _, ref := range manifest.References {
		if filepath.Clean(ref.Dir) == filepath.Clean(freshSandbox) {
			pinned = true
		}
	}
	if !pinned {
		t.Fatalf("the fallback sandbox mint %q was left unpinned: a protected allocation must publish a reference", freshSandbox)
	}

	// Continuity tail: the contention settles (the teardown releases the
	// lease) and the next restore must re-probe the original directory and
	// resume in it, not in the fallback.
	if err := slots[sandbox.ScratchKindSandbox].Retain(); err != nil {
		t.Fatalf("settle the fixture contention: %v", err)
	}
	env2 := execenv.NewLocalExecutionEnvironment(t.TempDir())
	t.Cleanup(func() { env2.Cleanup(); env2.DisposeSandboxScratch(); env2.DisposeUnsandboxedScratch() })
	policy2 := sbxResolve(t, sbxBwrapFacts(t.TempDir()), env2.WorkingDirectory(), sandbox.ModeWorkspaceWrite)
	if err := env2.EnableSandbox(policy2); err != nil {
		t.Fatalf("provision the follow-up restore's fresh sandbox scratch: %v", err)
	}
	if _, err := s.adoptRestoredConsumerScratch(env2, consumerID, true); err != nil {
		t.Fatalf("follow-up restore after the contention settled: %v", err)
	}
	if got := env2.SessionScratchDir(); filepath.Clean(got) != filepath.Clean(retainedDir) {
		t.Fatalf("the follow-up restore did not resume in the retained directory %q; continuity was lost: %q", retainedDir, got)
	}
}

// TestContendedSlotWithoutLiveAllocationKeepsBindingRow is the claim-path
// variant of the same round-10 contract: an adoption whose environment owns no
// allocation for the contended kind installs the binding row and skips the
// transfer at the claim. The kind must still be marked pending, or the
// environment's first real mint — here EnableSandbox's own allocation, pinned
// by the env-internal post-mint hook — would claim the binding's slot and end
// the retry the same way.
func TestContendedSlotWithoutLiveAllocationKeepsBindingRow(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	const consumerID = "01CONTENDEDCLAIM1"
	const bindingID = "b-contended-claim"
	slots, bindingRow := mintRefreshScratchBinding(t, s, bindingID, sandbox.ScratchKindSandbox)
	mapRefreshScratchConsumer(t, s, consumerID, bindingID)
	retainedDir := slots[sandbox.ScratchKindSandbox].Dir
	t.Cleanup(func() { _ = slots[sandbox.ScratchKindSandbox].Retain() })
	s.retainedScratch.Store(&retainedScratchPool{
		owner:     owner,
		handles:   map[string]*sandbox.SessionScratch{},
		bindings:  map[string]sandbox.ScratchBinding{bindingID: bindingRow},
		consumers: map[string]sandbox.ScratchConsumerBinding{consumerID: {SessionID: consumerID, CurrentBindingID: bindingID}},
		contended: map[string]struct{}{canonicalScratchDir(retainedDir): {}},
		adopted:   map[string]string{},
	})

	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	t.Cleanup(func() { env.Cleanup(); env.DisposeSandboxScratch() })
	// No live allocation for the contended kind: the claim path skips the
	// slot and the binding row is installed on the environment.
	if _, err := s.adoptRestoredConsumerScratch(env, consumerID, false); err != nil {
		t.Fatalf("restore adoption over a contended slot with no live allocation: %v", err)
	}
	if got := env.SessionScratchDir(); got != "" {
		t.Fatalf("fixture expected an environment with no scratch, got %q", got)
	}
	// The first real mint publishes through the env-internal post-mint pin.
	policy := sbxResolve(t, sbxBwrapFacts(t.TempDir()), env.WorkingDirectory(), sandbox.ModeWorkspaceWrite)
	if err := env.EnableSandbox(policy); err != nil {
		t.Fatalf("mint the first real sandbox allocation: %v", err)
	}
	minted := env.SessionScratchDir()
	if minted == "" || filepath.Clean(minted) == filepath.Clean(retainedDir) {
		t.Fatalf("fixture mint %q must exist apart from the retained %q", minted, retainedDir)
	}

	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	row, ok := findScratchBinding(manifest, bindingID)
	if !ok {
		t.Fatalf("binding %q vanished from the manifest", bindingID)
	}
	slot, ok := row.Slots[sandbox.ScratchKindSandbox]
	if !ok || filepath.Clean(slot.Dir) != filepath.Clean(retainedDir) {
		t.Fatalf("the claim-skipped slot was displaced by the first mint: got %+v, want the retained %q", slot, retainedDir)
	}
	pinned := false
	for _, ref := range manifest.References {
		if filepath.Clean(ref.Dir) == filepath.Clean(minted) {
			pinned = true
		}
	}
	if !pinned {
		t.Fatalf("the first mint %q was left unpinned: a protected allocation must publish a reference", minted)
	}
}

// TestScratchRefreshBacksOffLockContention pins the round-11 backoff gap: the
// refresh's re-derive loop retried a fail-fast manifest-lock refusal
// immediately, so five passes — microseconds each — could all lose to one
// fsync-scale hold and fail the delegate's send. The refusal must route
// through the same growing spacing sandbox.RetryScratchLockContention
// applies, so a hold that ends between passes leaves the retry bound intact.
func TestScratchRefreshBacksOffLockContention(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	const consumerID = "01REFRESHBACKOFF1"
	const bindingID = "b-refresh-backoff"
	slots, _ := mintRefreshScratchBinding(t, s, bindingID, sandbox.ScratchKindSandbox)
	mapRefreshScratchConsumer(t, s, consumerID, bindingID)
	releaseRefreshFixtureLeases(t, slots)
	if s.retainedScratch.Load() != nil {
		t.Fatal("fixture expected no published pool")
	}

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
	var backoffs atomic.Int32
	s.cfg.testOnly.scratchLockBackoff = func(int) {
		if backoffs.Add(1) == 1 {
			// The first refused pass hands the lock over mid-backoff —
			// exactly the position the schedule exists to wait a hold out
			// from.
			close(releaseLock)
			<-lockReleased
		}
	}
	// The holder verifiably owns the lock before the refresh's first open,
	// so pass 1 deterministically loses it.
	close(takeLock)
	<-lockTaken
	if err := s.refreshRetainedScratchConsumer(consumerID); err != nil {
		t.Fatalf("the refresh failed a transient manifest-lock hold instead of backing off: %v", err)
	}
	if backoffs.Load() < 1 {
		t.Fatalf("the refresh retried lock contention %d times without any backoff spacing", backoffs.Load())
	}
	pool := s.retainedScratch.Load()
	if pool == nil {
		t.Fatal("the backed-off refresh never published its pool")
	}
}

// TestScratchUpsertRetryKeepsConcurrentRoleUpdate pins the round-11 stale-row
// replay: the upsert retry closures used to capture the consumer record from
// the manifest as it stood before the first attempt, so a concurrent role
// update that committed while a refused attempt waited on the lock was
// overwritten by the retry's replay of the stale snapshot. The retry must
// recompute the row from the manifest as it stands on each attempt.
func TestScratchUpsertRetryKeepsConcurrentRoleUpdate(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	if err := s.installScratchRetention(env); err != nil {
		t.Fatalf("fixture binding installation: %v", err)
	}
	installed, err := env.ScratchRetentionBinding()
	if err != nil {
		t.Fatalf("read the installed binding: %v", err)
	}
	// The competing role value a concurrent writer commits mid-retry, on its
	// own pinned binding so the row it names validates.
	_, roleRow := mintRefreshScratchBinding(t, s, "b-role-competitor", sandbox.ScratchKindSandbox)

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
	attempts := 0
	s.cfg.testOnly.scratchUpsertAttempt = func() {
		attempts++
		switch attempts {
		case 1:
			close(takeLock)
			<-lockTaken
		case 2:
			// The holder is gone; a concurrent writer commits a role update
			// for this consumer before the retry's upsert replays.
			close(releaseLock)
			<-lockReleased
			fresh, err := sandbox.LoadScratchRetention(owner)
			if err != nil {
				t.Fatalf("concurrent writer's load: %v", err)
			}
			b1, ok := findScratchBinding(fresh, installed.BindingID)
			if !ok {
				t.Fatalf("binding %q vanished mid-retry", installed.BindingID)
			}
			concurrent := sandbox.ScratchConsumerBinding{
				SessionID:                s.id,
				CurrentBindingID:         b1.BindingID,
				WorktreeRestoreBindingID: roleRow.BindingID,
			}
			if err := sandbox.UpsertScratchBinding(owner, b1, concurrent); err != nil {
				t.Fatalf("concurrent role update: %v", err)
			}
		}
	}
	// The second, idempotent installation takes the existing-binding branch —
	// the retry path under test.
	if err := s.installScratchRetention(env); err != nil {
		t.Fatalf("idempotent installation lost to the retry: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("the upsert took %d attempts, want exactly 2", attempts)
	}
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	row := scratchConsumerFor(t, manifest, s.id)
	if row.WorktreeRestoreBindingID != roleRow.BindingID {
		t.Fatalf("the retry's upsert overwrote a concurrent role update: worktree-restore role = %q, want the concurrently committed %q", row.WorktreeRestoreBindingID, roleRow.BindingID)
	}
}

// TestContendedSlotPendingSurvivesBindingTransfer pins the round-12 High: a
// contended retained slot's pending marker is part of the logical environment a
// re-rooted clone inherits with its binding. Losing it would let the clone's
// first fresh mint claim the manifest's retained slot and end the continuity
// retry the source was still owed — the same displacement the marker exists to
// prevent, one environment object later.
func TestContendedSlotPendingSurvivesBindingTransfer(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	const consumerID = "01PENDINGXFER1"
	const bindingID = "b-pending-xfer"
	slots, bindingRow := mintRefreshScratchBinding(t, s, bindingID, sandbox.ScratchKindSandbox)
	mapRefreshScratchConsumer(t, s, consumerID, bindingID)
	retainedDir := slots[sandbox.ScratchKindSandbox].Dir
	t.Cleanup(func() { _ = slots[sandbox.ScratchKindSandbox].Retain() })
	s.retainedScratch.Store(&retainedScratchPool{
		owner:     owner,
		handles:   map[string]*sandbox.SessionScratch{},
		bindings:  map[string]sandbox.ScratchBinding{bindingID: bindingRow},
		consumers: map[string]sandbox.ScratchConsumerBinding{consumerID: {SessionID: consumerID, CurrentBindingID: bindingID}},
		contended: map[string]struct{}{canonicalScratchDir(retainedDir): {}},
		adopted:   map[string]string{},
	})

	// The source environment: the production resume shape, whose live fresh
	// sandbox mint shadows the contended retained slot, so the adoption marks
	// the kind pending on it.
	source := execenv.NewLocalExecutionEnvironment(t.TempDir())
	t.Cleanup(func() { source.Cleanup(); source.DisposeSandboxScratch() })
	policy := sbxResolve(t, sbxBwrapFacts(t.TempDir()), source.WorkingDirectory(), sandbox.ModeWorkspaceWrite)
	if err := source.EnableSandbox(policy); err != nil {
		t.Fatalf("provision the source environment's fresh sandbox scratch: %v", err)
	}
	if _, err := s.adoptRestoredConsumerScratch(source, consumerID, true); err != nil {
		t.Fatalf("source adoption over the contended slot: %v", err)
	}

	// The re-rooted clone inherits the binding — and must inherit the pending
	// marker with it.
	target := execenv.NewLocalExecutionEnvironment(t.TempDir())
	t.Cleanup(func() { target.Cleanup(); target.DisposeSandboxScratch() })
	if err := s.inheritScratchRetentionBinding(target, source); err != nil {
		t.Fatalf("inherit the binding onto the re-rooted clone: %v", err)
	}

	// The clone's first real mint publishes through its own post-mint pin.
	clonePolicy := sbxResolve(t, sbxBwrapFacts(t.TempDir()), target.WorkingDirectory(), sandbox.ModeWorkspaceWrite)
	if err := target.EnableSandbox(clonePolicy); err != nil {
		t.Fatalf("mint the clone's first real allocation: %v", err)
	}
	minted := target.SessionScratchDir()
	if minted == "" || filepath.Clean(minted) == filepath.Clean(retainedDir) {
		t.Fatalf("fixture clone mint %q must exist apart from the retained %q", minted, retainedDir)
	}

	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	row, ok := findScratchBinding(manifest, bindingID)
	if !ok {
		t.Fatalf("binding %q vanished from the manifest", bindingID)
	}
	slot, ok := row.Slots[sandbox.ScratchKindSandbox]
	if !ok || filepath.Clean(slot.Dir) != filepath.Clean(retainedDir) {
		t.Fatalf("the clone's first mint displaced the retained slot: got %+v, want the retained %q", slot, retainedDir)
	}
	pinned := false
	for _, ref := range manifest.References {
		if filepath.Clean(ref.Dir) == filepath.Clean(minted) {
			pinned = true
		}
	}
	if !pinned {
		t.Fatalf("the clone's mint %q was left unpinned: a protected allocation must publish a reference", minted)
	}
}

// TestContendedSlotPendingSurvivesEnvironmentSwap is the swap-path twin of
// TestContendedSlotPendingSurvivesBindingTransfer: a moving environment swap
// (stageScratchSwapBinding + AdoptSessionScratch) hands the source's fallback
// allocation and its binding slots to the target, and the pending marker must
// travel with them. The target's post-move pin runs with the marker in hand,
// or the moved fallback mint would claim the binding's slot and no later
// refresh would ever re-probe the retained directory (round 13).
func TestContendedSlotPendingSurvivesEnvironmentSwap(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	const consumerID = "01PENDINGXFER2"
	const bindingID = "b-pending-swap"
	slots, bindingRow := mintRefreshScratchBinding(t, s, bindingID, sandbox.ScratchKindSandbox)
	mapRefreshScratchConsumer(t, s, consumerID, bindingID)
	retainedDir := slots[sandbox.ScratchKindSandbox].Dir
	t.Cleanup(func() { _ = slots[sandbox.ScratchKindSandbox].Retain() })
	s.retainedScratch.Store(&retainedScratchPool{
		owner:     owner,
		handles:   map[string]*sandbox.SessionScratch{},
		bindings:  map[string]sandbox.ScratchBinding{bindingID: bindingRow},
		consumers: map[string]sandbox.ScratchConsumerBinding{consumerID: {SessionID: consumerID, CurrentBindingID: bindingID}},
		contended: map[string]struct{}{canonicalScratchDir(retainedDir): {}},
		adopted:   map[string]string{},
	})

	// The source environment: the production resume shape, whose live fresh
	// sandbox mint shadows the contended retained slot, so the adoption marks
	// the kind pending on it.
	source := execenv.NewLocalExecutionEnvironment(t.TempDir())
	t.Cleanup(func() { source.Cleanup(); source.DisposeSandboxScratch() })
	policy := sbxResolve(t, sbxBwrapFacts(t.TempDir()), source.WorkingDirectory(), sandbox.ModeWorkspaceWrite)
	if err := source.EnableSandbox(policy); err != nil {
		t.Fatalf("provision the source environment's fresh sandbox scratch: %v", err)
	}
	if _, err := s.adoptRestoredConsumerScratch(source, consumerID, true); err != nil {
		t.Fatalf("source adoption over the contended slot: %v", err)
	}

	// The moving swap: the manifest transition is staged first, then the
	// handles move and the target re-pins what it adopted.
	target := execenv.NewLocalExecutionEnvironment(t.TempDir())
	t.Cleanup(func() { target.Cleanup(); target.DisposeSandboxScratch() })
	if err := s.stageScratchSwapBinding(target, source, consumerID); err != nil {
		t.Fatalf("stage the swap binding: %v", err)
	}
	target.AdoptSessionScratch(source)
	minted := target.SessionScratchDir()
	if minted == "" || filepath.Clean(minted) == filepath.Clean(retainedDir) {
		t.Fatalf("fixture moved mint %q must exist apart from the retained %q", minted, retainedDir)
	}

	targetBinding, err := target.ScratchRetentionBinding()
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	row, ok := findScratchBinding(manifest, targetBinding.BindingID)
	if !ok {
		t.Fatalf("binding %q vanished from the manifest", targetBinding.BindingID)
	}
	slot, ok := row.Slots[sandbox.ScratchKindSandbox]
	if !ok || filepath.Clean(slot.Dir) != filepath.Clean(retainedDir) {
		t.Fatalf("the target's re-pin displaced the retained slot: got %+v, want the retained %q", slot, retainedDir)
	}
	pinned := false
	for _, ref := range manifest.References {
		if filepath.Clean(ref.Dir) == filepath.Clean(minted) {
			pinned = true
		}
	}
	if !pinned {
		t.Fatalf("the moved mint %q was left unpinned: a protected allocation must publish a reference", minted)
	}
}

// TestScratchUpsertWindowKeepsConcurrentRoleUpdate pins the round-14
// load→upsert window: the install closures recompute their rows from a
// manifest load taken WITHOUT the update lock, so a concurrent role update
// that commits between that load and the upsert's own lock was still replaced
// wholesale by the closure's now-stale row. The upsert must refuse a manifest
// that moved since its row was derived, so the closure re-derives instead of
// overwriting.
func TestScratchUpsertWindowKeepsConcurrentRoleUpdate(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	if err := s.installScratchRetention(env); err != nil {
		t.Fatalf("fixture binding installation: %v", err)
	}
	installed, err := env.ScratchRetentionBinding()
	if err != nil {
		t.Fatalf("read the installed binding: %v", err)
	}
	// The competing role value a concurrent writer commits inside the
	// window, on its own pinned binding so the row it names validates.
	_, roleRow := mintRefreshScratchBinding(t, s, "b-role-window", sandbox.ScratchKindSandbox)

	s.cfg.testOnly.scratchUpsertAfterLoad = func() {
		// The concurrent role update lands between the closure's row
		// derivation and its upsert taking the manifest lock.
		fresh, err := sandbox.LoadScratchRetention(owner)
		if err != nil {
			t.Fatalf("concurrent writer's load: %v", err)
		}
		published, ok := findScratchBinding(fresh, installed.BindingID)
		if !ok {
			t.Fatalf("binding %q vanished inside the window", installed.BindingID)
		}
		concurrent := sandbox.ScratchConsumerBinding{
			SessionID:                s.id,
			CurrentBindingID:         published.BindingID,
			WorktreeRestoreBindingID: roleRow.BindingID,
		}
		if err := sandbox.UpsertScratchBinding(owner, published, concurrent); err != nil {
			t.Fatalf("concurrent role update: %v", err)
		}
		// One shot: the retry the refusal provokes must re-derive cleanly.
		s.cfg.testOnly.scratchUpsertAfterLoad = nil
	}
	// The second, idempotent installation takes the existing-binding branch —
	// the closure under test.
	if err := s.installScratchRetention(env); err != nil {
		t.Fatalf("idempotent installation lost to the load→upsert window: %v", err)
	}
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	row := scratchConsumerFor(t, manifest, s.id)
	if row.WorktreeRestoreBindingID != roleRow.BindingID {
		t.Fatalf("the upsert overwrote a concurrent role update committed after its snapshot: worktree-restore role = %q, want the concurrently committed %q", row.WorktreeRestoreBindingID, roleRow.BindingID)
	}
}

// TestScratchAdoptionAbsorbsContendedOwnClaim pins the round-14 stale-claim
// hazard: the refresh's stale-claim probe stamps contention but deliberately
// leaves the adopted record in place, and claimRetainedScratchSlot reports any
// adopted key as uncontended — so an adoption that owns no live allocation of
// the kind hit "already transferred" against its own stale claim instead of
// running on fresh scratch and leaving the row for the next refresh to
// re-probe.
func TestScratchAdoptionAbsorbsContendedOwnClaim(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	const consumerID = "01CONTENDEDOWN1"
	const bindingID = "b-contended-own"
	slots, bindingRow := mintRefreshScratchBinding(t, s, bindingID, sandbox.ScratchKindSandbox)
	mapRefreshScratchConsumer(t, s, consumerID, bindingID)
	retainedDir := slots[sandbox.ScratchKindSandbox].Dir
	t.Cleanup(func() { _ = slots[sandbox.ScratchKindSandbox].Retain() })
	// The post-refresh state the reviewer described: the probe could not
	// reacquire (the lease is held elsewhere in this process) and stamped
	// contention, while the adopted record stayed — it is also what lets a
	// distinct consumer borrow the directory.
	s.retainedScratch.Store(&retainedScratchPool{
		owner:     owner,
		handles:   map[string]*sandbox.SessionScratch{},
		bindings:  map[string]sandbox.ScratchBinding{bindingID: bindingRow},
		consumers: map[string]sandbox.ScratchConsumerBinding{consumerID: {SessionID: consumerID, CurrentBindingID: bindingID}},
		contended: map[string]struct{}{canonicalScratchDir(retainedDir): {}},
		adopted:   map[string]string{canonicalScratchDir(retainedDir): consumerID},
	})

	// An environment with no live allocation of the kind — the shape whose
	// existingKinds guard cannot absorb the stale claim.
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	t.Cleanup(func() { env.Cleanup(); env.DisposeSandboxScratch() })
	if _, err := s.adoptRestoredConsumerScratch(env, consumerID, false); err != nil {
		t.Fatalf("adoption failed against its own contended stale claim: %v", err)
	}
	pending := env.RetentionPendingKinds()
	if len(pending) != 1 || pending[0] != sandbox.ScratchKindSandbox {
		t.Fatalf("the contended own-claim must mark the kind pending for the next refresh to re-probe, got %v", pending)
	}
}

// TestScratchRefreshOpenDeclinesOnReleasedManifest pins the round-14
// terminal-close race: when the terminal release seals the manifest between a
// refresh pass's snapshot and its reacquire, the open reports the released
// manifest and the refresh must hand back every lease it already reacquired
// and decline — the same clean exit the install's seal path takes — instead of
// failing the restore.
func TestScratchRefreshOpenDeclinesOnReleasedManifest(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	const consumerID = "01REFRESHSEAL1"
	const bindingID = "b-refresh-seal"
	slots, _ := mintRefreshScratchBinding(t, s, bindingID, sandbox.ScratchKindSandbox, sandbox.ScratchKindUnsandboxed)
	mapRefreshScratchConsumer(t, s, consumerID, bindingID)
	// Both leases settle so the pass's first reacquire succeeds for real;
	// the second open returns the release race's sentinel.
	for _, handle := range slots {
		if err := handle.Retain(); err != nil {
			t.Fatal(err)
		}
	}
	s.cfg.testOnly.scratchRefreshOpenOverride = func(ref sandbox.ScratchReference, call int) error {
		if call >= 2 {
			return sandbox.ErrScratchRetentionReleased
		}
		return nil
	}
	if err := s.refreshRetainedScratchConsumer(consumerID); err != nil {
		t.Fatalf("the refresh treated a sealed manifest as fatal: %v", err)
	}
	// The pass must have handed back the lease its first open reacquired, or
	// the declined refresh pins the allocation against every later writer.
	for kind, handle := range slots {
		reopened, err := sandbox.OpenRetainedSessionScratch(owner, sandbox.ScratchReference{Dir: handle.Dir, Kind: kind})
		if err != nil {
			t.Fatalf("re-open %s after the declined pass: %v", kind, err)
		}
		if err := reopened.Retain(); err != nil {
			t.Fatal(err)
		}
	}
}

// TestScratchReinstallRegistersAfterManifestReset pins the round-15 rebind gap:
// after a terminal release and the reset it provokes, an environment carrying
// its old binding identity found that binding missing from the fresh manifest
// and the install silently returned, leaving later scratch publications without
// a binding row or consumer role.
func TestScratchReinstallRegistersAfterManifestReset(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	t.Cleanup(func() { env.Cleanup(); env.DisposeSandboxScratch() })
	if err := s.installScratchRetention(env); err != nil {
		t.Fatalf("fixture binding installation: %v", err)
	}
	installed, err := env.ScratchRetentionBinding()
	if err != nil {
		t.Fatalf("read the installed binding: %v", err)
	}

	// The terminal release tombstones the root's manifest; the next install
	// resets it, and the environment's own identity must re-register.
	if err := sandbox.ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("terminal release: %v", err)
	}
	if err := s.installScratchRetention(env); err != nil {
		t.Fatalf("reinstall over the reset manifest: %v", err)
	}

	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := findScratchBinding(manifest, installed.BindingID); !ok {
		t.Fatalf("the reset dropped the environment's binding and the reinstall did not re-register binding %q", installed.BindingID)
	}
	row := scratchConsumerFor(t, manifest, s.id)
	if row.CurrentBindingID != installed.BindingID {
		t.Fatalf("the reinstall left the consumer unregistered: current binding = %q, want the environment's %q", row.CurrentBindingID, installed.BindingID)
	}
}

// TestScratchReinstallRepinsOwnedScratchAfterManifestReset pins round 18's
// Medium: a reset-orphaned identity whose environment still owned live
// handles was re-registered slotless, leaving the owned scratch with no
// manifest reference, slot, or pin until some later mint happened to
// publish it.
func TestScratchReinstallRepinsOwnedScratchAfterManifestReset(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	t.Cleanup(func() { env.Cleanup(); env.DisposeSandboxScratch() })
	policy := sbxResolve(t, sbxBwrapFacts(t.TempDir()), env.WorkingDirectory(), sandbox.ModeWorkspaceWrite)
	if err := env.EnableSandbox(policy); err != nil {
		t.Fatalf("provision fresh sandbox scratch: %v", err)
	}
	if err := s.installScratchRetention(env); err != nil {
		t.Fatalf("fixture binding installation: %v", err)
	}
	installed, err := env.ScratchRetentionBinding()
	if err != nil {
		t.Fatalf("read the installed binding: %v", err)
	}
	live := env.SessionScratchDir()
	if live == "" {
		t.Fatal("fixture environment owns no scratch")
	}
	// Move the consumer role onto a second binding before the release, so the
	// reset's carry path lets the orphaned pair die instead of carrying it
	// (the carried shape is pinned by TestScratchReinstallKeepsLiveLeasedScratch).
	slots, _ := mintRefreshScratchBinding(t, s, "b-role-move", sandbox.ScratchKindSandbox)
	t.Cleanup(func() { _ = slots[sandbox.ScratchKindSandbox].Retain() })
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := sandbox.UpdateScratchBindings(owner, manifest.Revision, nil,
		[]sandbox.ScratchConsumerBinding{{SessionID: s.id, CurrentBindingID: "b-role-move"}}); err != nil {
		t.Fatalf("move the consumer role off the environment's binding: %v", err)
	}

	if err := sandbox.ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("terminal release: %v", err)
	}
	if err := s.installScratchRetention(env); err != nil {
		t.Fatalf("reinstall over the reset manifest: %v", err)
	}

	manifest, err = sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	row, ok := findScratchBinding(manifest, installed.BindingID)
	if !ok {
		t.Fatalf("the reinstall dropped the environment's binding %q", installed.BindingID)
	}
	slot, has := row.Slots[sandbox.ScratchKindSandbox]
	if !has || filepath.Clean(slot.Dir) != filepath.Clean(live) {
		t.Fatalf("the republished binding %q does not name the environment's live scratch %q: %+v", installed.BindingID, live, row.Slots)
	}
	// The repin must also restore the pin: an open of the live directory has
	// to fail on the environment's own held lease, not on a missing pin.
	if _, err := sandbox.OpenRetainedSessionScratch(owner, sandbox.ScratchReference{Dir: live, Kind: sandbox.ScratchKindSandbox}); !errors.Is(err, sandbox.ErrScratchRetentionLeaseHeld) {
		t.Fatalf("the environment's live scratch was left unpinned: open got %v, want %v", err, sandbox.ErrScratchRetentionLeaseHeld)
	}
	role := scratchConsumerFor(t, manifest, s.id)
	if role.CurrentBindingID != installed.BindingID {
		t.Fatalf("the reinstall left the consumer unregistered: current binding = %q, want the environment's %q", role.CurrentBindingID, installed.BindingID)
	}
}

// TestScratchAdoptionKeepsClaimedHandleFromDetach pins the round-15 claim race:
// a pooled handle stays in the releasable map between the adoption's claim and
// its install, so a terminal detach racing the adoption Retained the claimed
// lease out from under it and the restored environment ran on scratch nobody
// owned. From the claim until the transfer settles, the handle must be the
// adopter's alone.
func TestScratchAdoptionKeepsClaimedHandleFromDetach(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	const consumerID = "01CLAIMDETACH1"
	const bindingID = "b-claim-detach"
	slots, bindingRow := mintRefreshScratchBinding(t, s, bindingID, sandbox.ScratchKindSandbox)
	mapRefreshScratchConsumer(t, s, consumerID, bindingID)
	retainedDir := slots[sandbox.ScratchKindSandbox].Dir
	t.Cleanup(func() { _ = slots[sandbox.ScratchKindSandbox].Retain() })
	s.retainedScratch.Store(&retainedScratchPool{
		owner:     owner,
		handles:   map[string]*sandbox.SessionScratch{canonicalScratchDir(retainedDir): slots[sandbox.ScratchKindSandbox]},
		bindings:  map[string]sandbox.ScratchBinding{bindingID: bindingRow},
		consumers: map[string]sandbox.ScratchConsumerBinding{consumerID: {SessionID: consumerID, CurrentBindingID: bindingID}},
		contended: map[string]struct{}{},
		adopted:   map[string]string{},
	})

	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	t.Cleanup(func() { env.Cleanup(); env.DisposeSandboxScratch() })
	claimed := make(chan struct{})
	resume := make(chan struct{})
	s.cfg.testOnly.scratchAdoptionAfterClaim = func() {
		close(claimed)
		<-resume
	}
	adoptErr := make(chan error, 1)
	go func() {
		_, _, err := s.adoptConsumerScratch(env, consumerID)
		adoptErr <- err
	}()
	<-claimed
	// The terminal detach sweeps the pool while the adoption holds a claimed
	// transfer: the claimed handle must not be released out from under it.
	s.retainedScratchSealed.Store(true)
	s.detachRetainedScratch()
	close(resume)
	if err := <-adoptErr; err != nil {
		t.Fatalf("adoption across the detach: %v", err)
	}
	if got := env.SessionScratchDir(); filepath.Clean(got) != filepath.Clean(retainedDir) {
		t.Fatalf("the adopted scratch %q is not the retained %q", got, retainedDir)
	}
	// The adopter must still hold the lease: the detach had no claim on a
	// handle already spoken for.
	if _, err := sandbox.OpenRetainedSessionScratch(owner, sandbox.ScratchReference{Dir: retainedDir, Kind: sandbox.ScratchKindSandbox}); !errors.Is(err, sandbox.ErrScratchRetentionLeaseHeld) {
		t.Fatalf("the detach released the claimed handle's lease: open got %v, want %v", err, sandbox.ErrScratchRetentionLeaseHeld)
	}
}

// TestChildTeardownReleasesTheSeededPool pins the round-15 child-pool leak: a
// child session never runs prepareRetainedScratch, so the pool its
// restore-adoption refresh seeds is the only release path those handles have —
// and no child close path ever reached it, leaving the leases held for the
// daemon's life and pinning contended slots against every later cold restore.
func TestChildTeardownReleasesTheSeededPool(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	const consumerID = "01CHILDTDOWN1"
	const bindingID = "b-child-teardown"
	slots, bindingRow := mintRefreshScratchBinding(t, s, bindingID, sandbox.ScratchKindSandbox)
	mapRefreshScratchConsumer(t, s, consumerID, bindingID)
	retainedDir := slots[sandbox.ScratchKindSandbox].Dir
	t.Cleanup(func() { _ = slots[sandbox.ScratchKindSandbox].Retain() })
	// The child shape: the owner resolves to the root's manifest, this session
	// is not the root, and the seeded pool is process-local to it alone.
	s.delegateRootSessionID = "01ROOTTESTROOT1"
	s.retainedScratch.Store(&retainedScratchPool{
		owner:     owner,
		handles:   map[string]*sandbox.SessionScratch{canonicalScratchDir(retainedDir): slots[sandbox.ScratchKindSandbox]},
		bindings:  map[string]sandbox.ScratchBinding{bindingID: bindingRow},
		consumers: map[string]sandbox.ScratchConsumerBinding{consumerID: {SessionID: consumerID, CurrentBindingID: bindingID}},
		contended: map[string]struct{}{},
		adopted:   map[string]string{},
	})

	teardownChildSession(context.Background(), s, retainChildScratch)

	// The teardown must have handed the seeded pool's leases back: a leaked
	// flock pins the slot against every later cold restore of this delegate.
	probe, err := sandbox.OpenRetainedSessionScratch(owner, sandbox.ScratchReference{Dir: retainedDir, Kind: sandbox.ScratchKindSandbox})
	if err != nil {
		t.Fatalf("the child teardown leaked the seeded pool: %v", err)
	}
	if err := probe.Retain(); err != nil {
		t.Fatal(err)
	}
}

// TestScratchReinstallKeepsLiveLeasedScratch pins the round-16 republish gap:
// when a manifest reset orphaned an environment's binding, the re-registration
// republished the identity with its slots cleared and never re-pinned the
// environment's live leased scratch — leaving the manifest with no reference
// or slot naming a directory the session is still working in, breaking
// continuity on every later restore.
func TestScratchReinstallKeepsLiveLeasedScratch(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	t.Cleanup(func() { env.Cleanup(); env.DisposeSandboxScratch() })
	policy := sbxResolve(t, sbxBwrapFacts(t.TempDir()), env.WorkingDirectory(), sandbox.ModeWorkspaceWrite)
	if err := env.EnableSandbox(policy); err != nil {
		t.Fatalf("mint the environment's live scratch: %v", err)
	}
	liveDir := env.SessionScratchDir()
	if liveDir == "" {
		t.Fatal("fixture expected a minted scratch directory")
	}
	if err := s.installScratchRetention(env); err != nil {
		t.Fatalf("fixture binding installation: %v", err)
	}
	installed, err := env.ScratchRetentionBinding()
	if err != nil {
		t.Fatalf("read the installed binding: %v", err)
	}

	// The terminal release tombstones the manifest (the env holds its lease,
	// so its pin survives); the reinstall resets it, and the live allocation
	// must still be owned afterwards.
	if err := sandbox.ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("terminal release: %v", err)
	}
	if err := s.installScratchRetention(env); err != nil {
		t.Fatalf("reinstall over the reset manifest: %v", err)
	}

	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	row, ok := findScratchBinding(manifest, installed.BindingID)
	if !ok {
		t.Fatalf("binding %q is absent after the reinstall", installed.BindingID)
	}
	slot, ok := row.Slots[sandbox.ScratchKindSandbox]
	if !ok || filepath.Clean(slot.Dir) != filepath.Clean(liveDir) {
		t.Fatalf("the reinstalled binding lost the live leased scratch: slot = %+v, want the live %q", slot, liveDir)
	}
	referenced := false
	for _, ref := range manifest.References {
		if filepath.Clean(ref.Dir) == filepath.Clean(liveDir) {
			referenced = true
		}
	}
	if !referenced {
		t.Fatalf("the live leased scratch %q is referenced by no allocation in the reinstalled manifest", liveDir)
	}
}

// TestScratchReinstallAdoptsTheCarriedConsumerBinding pins the round-21
// released-manifest orphan: the reset carries a contended binding and its
// consumer for exactly the session being cold-restored, but the reinstall's
// fresh path minted a NEW binding id and REPLACED the carried consumer row —
// leaving the carried binding's lease-owning slot named by no consumer role,
// the shape the graph reader fails closed on, with Released false again and
// no later reset to repair it. The reinstall must adopt the carried binding:
// the consumer row keeps naming it, its slot keeps naming the retained
// directory, and the fresh mint pins as the pending fallback it is.
func TestScratchReinstallAdoptsTheCarriedConsumerBinding(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	const childID = "01CARRIEDCHILD1"
	const bindingID = "b-carried-child"
	slots, _ := mintRefreshScratchBinding(t, s, bindingID, sandbox.ScratchKindSandbox)
	mapRefreshScratchConsumer(t, s, childID, bindingID)
	retainedDir := slots[sandbox.ScratchKindSandbox].Dir
	t.Cleanup(func() { _ = slots[sandbox.ScratchKindSandbox].Retain() })

	// The lease stays held, so the terminal release leaves the pin behind and
	// the reset carries the contended pair.
	if err := sandbox.ReleaseScratchRetention(owner); err != nil {
		t.Fatalf("terminal release: %v", err)
	}

	// The cold restore reconstructs the child's environment fresh: a new
	// object with no installed binding and a freshly minted scratch.
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

	if err := s.installChildScratchRetention(env, childID); err != nil {
		t.Fatalf("reinstall the carried child binding: %v", err)
	}

	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	// The graph must stay reader-valid: the carried binding owns a
	// lease-owning slot, so some consumer role must name it.
	if err := validateRetainedScratchGraph(manifest); err != nil {
		t.Fatalf("the reinstall committed an invalid retention graph: %v", err)
	}
	row, ok := findScratchBinding(manifest, bindingID)
	if !ok {
		t.Fatalf("the carried binding %q is absent after the reinstall", bindingID)
	}
	slot, hasSlot := row.Slots[sandbox.ScratchKindSandbox]
	if !hasSlot || filepath.Clean(slot.Dir) != filepath.Clean(retainedDir) {
		t.Fatalf("the carried binding's slot must keep naming the retained %q: %+v", retainedDir, row.Slots)
	}
	current := ""
	for _, consumer := range manifest.Consumers {
		if consumer.SessionID == childID {
			current = consumer.CurrentBindingID
		}
	}
	if current != bindingID {
		t.Fatalf("the child's consumer row names %q, want the carried %q", current, bindingID)
	}
	// The fresh mint is a fallback for the carried slot, not a replacement:
	// its kind stays pending so it pins as a bare protected reference.
	pending := false
	for _, kind := range env.RetentionPendingKinds() {
		if kind == sandbox.ScratchKindSandbox {
			pending = true
		}
	}
	if !pending {
		t.Fatalf("the fresh mint's kind is not pending: %v", env.RetentionPendingKinds())
	}
}

// TestScratchReinstallAdoptsWhenAReleaseRacesTheInstall pins the round-22
// released-flag race: the install read the manifest's Released state before
// the reset's own locked read, so a terminal release landing in between let
// the install's reset carry the contended pair while the stale flag said
// not-released — the fresh mint then replaced the carried consumer row and
// orphaned the carried binding's lease-owning slot, the shape the graph
// reader rejects with no later reset left to repair it. The reset itself
// now reports whether it performed the carry, computed under its own lock.
func TestScratchReinstallAdoptsWhenAReleaseRacesTheInstall(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	const childID = "01RACERELEASE1"
	const bindingID = "b-race-release"
	slots, _ := mintRefreshScratchBinding(t, s, bindingID, sandbox.ScratchKindSandbox)
	mapRefreshScratchConsumer(t, s, childID, bindingID)
	retainedDir := slots[sandbox.ScratchKindSandbox].Dir
	t.Cleanup(func() { _ = slots[sandbox.ScratchKindSandbox].Retain() })

	// The terminal release lands inside the install's window, after its
	// Released view and before its reset's own locked read: the lease stays
	// held, so the reset the install then runs carries the contended pair.
	s.cfg.testOnly.scratchInstallBeforeReset = func() {
		s.cfg.testOnly.scratchInstallBeforeReset = nil
		if err := sandbox.ReleaseScratchRetention(owner); err != nil {
			t.Errorf("terminal release inside the install window: %v", err)
		}
	}

	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	t.Cleanup(func() { env.Cleanup(); env.DisposeSandboxScratch() })
	policy := sbxResolve(t, sbxBwrapFacts(t.TempDir()), env.WorkingDirectory(), sandbox.ModeWorkspaceWrite)
	if err := env.EnableSandbox(policy); err != nil {
		t.Fatalf("provision fresh sandbox scratch: %v", err)
	}

	if err := s.installChildScratchRetention(env, childID); err != nil {
		t.Fatalf("reinstall across the racing release: %v", err)
	}

	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateRetainedScratchGraph(manifest); err != nil {
		t.Fatalf("the reinstall committed an invalid retention graph: %v", err)
	}
	row, ok := findScratchBinding(manifest, bindingID)
	if !ok {
		t.Fatalf("the carried binding %q is absent after the reinstall", bindingID)
	}
	slot, hasSlot := row.Slots[sandbox.ScratchKindSandbox]
	if !hasSlot || filepath.Clean(slot.Dir) != filepath.Clean(retainedDir) {
		t.Fatalf("the carried binding's slot must keep naming the retained %q: %+v", retainedDir, row.Slots)
	}
	current := ""
	for _, consumer := range manifest.Consumers {
		if consumer.SessionID == childID {
			current = consumer.CurrentBindingID
		}
	}
	if current != bindingID {
		t.Fatalf("the child's consumer row names %q, want the carried %q", current, bindingID)
	}
}

// TestReprovisionKeepsTheWrapperForAnOwnedSandboxScratch pins the ordering
// the round-23 Low flags: the ownership check that preserves an environment
// already holding a sandbox scratch must run before anything clears the
// kernel wrapper, or a preserved environment is left with a live scratch and
// no wrapper to run commands through. Both production call sites pre-check
// the fact this test exercises directly.
func TestReprovisionKeepsTheWrapperForAnOwnedSandboxScratch(t *testing.T) {
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	t.Cleanup(func() { env.Cleanup(); env.DisposeSandboxScratch() })
	policy := sbxResolve(t, sbxBwrapFacts(t.TempDir()), env.WorkingDirectory(), sandbox.ModeWorkspaceWrite)
	if err := env.EnableSandbox(policy); err != nil {
		t.Fatalf("provision fresh sandbox scratch: %v", err)
	}
	if env.Wrapper == nil {
		t.Fatal("fixture environment has no kernel wrapper to preserve")
	}
	if err := reprovisionUnclaimedSandboxScratch(env); err != nil {
		t.Fatalf("reprovision beside an owned scratch: %v", err)
	}
	if env.Wrapper == nil {
		t.Fatal("the ownership-preserving early return cleared the kernel wrapper beside a live owned scratch")
	}
	if owned := envScratchRefDir(env, sandbox.ScratchKindSandbox); owned == "" {
		t.Fatal("the owned sandbox scratch did not survive the reprovision call")
	}
}

// TestScratchOwnClaimReadsContentionAtomically pins the round-16 snapshot gap:
// the own-claim branch read contention with a second pool lookup after the
// claim's own hold, so a concurrent refresh fold flipping the mark between
// the two misclassified a stale own-claim as a hard "already transferred"
// error. The claim's snapshot must be the authority.
func TestScratchOwnClaimReadsContentionAtomically(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	const consumerID = "01ATOMICCLAIM1"
	const bindingID = "b-atomic-claim"
	slots, bindingRow := mintRefreshScratchBinding(t, s, bindingID, sandbox.ScratchKindSandbox)
	mapRefreshScratchConsumer(t, s, consumerID, bindingID)
	retainedDir := slots[sandbox.ScratchKindSandbox].Dir
	t.Cleanup(func() { _ = slots[sandbox.ScratchKindSandbox].Retain() })
	key := canonicalScratchDir(retainedDir)
	s.retainedScratch.Store(&retainedScratchPool{
		owner:     owner,
		handles:   map[string]*sandbox.SessionScratch{},
		bindings:  map[string]sandbox.ScratchBinding{bindingID: bindingRow},
		consumers: map[string]sandbox.ScratchConsumerBinding{consumerID: {SessionID: consumerID, CurrentBindingID: bindingID}},
		contended: map[string]struct{}{key: {}},
		adopted:   map[string]string{key: consumerID},
	})

	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	t.Cleanup(func() { env.Cleanup(); env.DisposeSandboxScratch() })
	s.cfg.testOnly.scratchClaimResolved = func() {
		// A concurrent refresh fold clears the contention mark between the
		// claim's snapshot and a second lookup.
		pool := s.retainedScratch.Load()
		pool.mu.Lock()
		delete(pool.contended, key)
		pool.mu.Unlock()
	}
	if _, _, err := s.adoptConsumerScratch(env, consumerID); err != nil {
		t.Fatalf("the adoption misclassified its own contended claim after the fold: %v", err)
	}
	pending := env.RetentionPendingKinds()
	if len(pending) != 1 || pending[0] != sandbox.ScratchKindSandbox {
		t.Fatalf("the contended own-claim must mark the kind pending off the claim's own snapshot, got %v", pending)
	}
}

// TestScratchNewBindingRetryKeepsConcurrentRoleUpdate is the new-binding twin of
// TestScratchUpsertRetryKeepsConcurrentRoleUpdate: the first-ever publication's
// retry closure must recompute its rows too, or a role update committed while a
// refused attempt waited on the lock is overwritten by the stale replay.
func TestScratchNewBindingRetryKeepsConcurrentRoleUpdate(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		t.Fatal("session has no scratch retention owner")
	}
	_, roleRow := mintRefreshScratchBinding(t, s, "b-role-competitor", sandbox.ScratchKindSandbox)

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
	attempts := 0
	s.cfg.testOnly.scratchUpsertAttempt = func() {
		attempts++
		switch attempts {
		case 1:
			close(takeLock)
			<-lockTaken
		case 2:
			close(releaseLock)
			<-lockReleased
			fresh, err := sandbox.LoadScratchRetention(owner)
			if err != nil {
				t.Fatalf("concurrent writer's load: %v", err)
			}
			installed, err := s.env.(*execenv.LocalExecutionEnvironment).ScratchRetentionBinding()
			if err != nil {
				t.Fatalf("read the installed binding: %v", err)
			}
			published, ok := findScratchBinding(fresh, installed.BindingID)
			if !ok {
				t.Fatalf("binding %q vanished mid-retry", installed.BindingID)
			}
			concurrent := sandbox.ScratchConsumerBinding{
				SessionID:                s.id,
				CurrentBindingID:         published.BindingID,
				WorktreeRestoreBindingID: roleRow.BindingID,
			}
			if err := sandbox.UpsertScratchBinding(owner, published, concurrent); err != nil {
				t.Fatalf("concurrent role update: %v", err)
			}
		}
	}
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	if err := s.installScratchRetention(env); err != nil {
		t.Fatalf("first-ever installation lost to the retry: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("the upsert took %d attempts, want exactly 2", attempts)
	}
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	row := scratchConsumerFor(t, manifest, s.id)
	if row.WorktreeRestoreBindingID != roleRow.BindingID {
		t.Fatalf("the retry's upsert overwrote a concurrent role update: worktree-restore role = %q, want the concurrently committed %q", row.WorktreeRestoreBindingID, roleRow.BindingID)
	}
}
