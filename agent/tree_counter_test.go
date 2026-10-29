package agent

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// TestTreeCounterReserveRelease verifies the atomic check-and-reserve logic:
// 16 reservations succeed at cap 16, the 17th fails, releasing one allows
// another to succeed.
func TestTreeCounterReserveRelease(t *testing.T) {
	t.Parallel()
	c := newTreeCounter(16)

	// Reserve up to cap (16) — all must succeed.
	for i := range 16 {
		if !c.reserve(slotKindJob) {
			t.Fatalf("reserve %d: expected true (under cap), got false", i+1)
		}
	}

	// 17th reservation must fail — at cap.
	if c.reserve(slotKindJob) {
		t.Fatal("reserve 17: expected false (at cap), got true")
	}

	// Release one slot.
	c.releaseKind(slotKindJob)

	// Now one reservation should succeed again.
	if !c.reserve(slotKindJob) {
		t.Fatal("reserve after release: expected true, got false")
	}
}

// TestTreeCounterSharedAcrossTree verifies that a child session's treeCounter
// is the SAME pointer as the one threaded through the root's spawnConfig.
// Reservations made on the root counter are visible via the child session's
// counter because they share a pointer.
//
// Approach: construct a root session (parentSessionID == "") and a child
// session (parentSessionID set), both built via NewSession. The root mints a
// fresh treeCounter; the child inherits the pointer via cfg.spawn.treeCounter.
// Assert both sessions hold the same pointer.
//
// Red today: treeCounter field does not exist on Session or spawnConfig.
func TestTreeCounterSharedAcrossTree(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	workDir := t.TempDir()
	c := llm.NewClient()
	env := execenv.NewLocalExecutionEnvironment(workDir)

	// Build a root session — parentSessionID == "" triggers minting.
	rootCfg := SessionConfig{
		StateDir: stateDir,
	}
	root, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), env, rootCfg)
	if err != nil {
		t.Fatalf("NewSession (root): %v", err)
	}
	defer root.Close()

	rootCounter := root.treeCounter
	if rootCounter == nil {
		t.Fatal("root session treeCounter is nil; expected a minted counter")
	}

	// Build a child session carrying the root's counter pointer.
	childStateDir := t.TempDir()
	childCfg := SessionConfig{
		StateDir: childStateDir,
	}
	childCfg.spawn.parentSessionID = "root-session-id"
	childCfg.spawn.depth = 1
	childCfg.spawn.treeCounter = rootCounter

	child, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), env, childCfg)
	if err != nil {
		t.Fatalf("NewSession (child): %v", err)
	}
	defer child.Close()

	// The child must hold the SAME pointer.
	if child.treeCounter != rootCounter {
		t.Fatalf("child treeCounter %p != root treeCounter %p; pointer not shared", child.treeCounter, rootCounter)
	}

	// Demonstrate shared state: reserve via root counter, verify child counter
	// (same pointer) reflects the reservation.
	if !rootCounter.reserve(slotKindJob) {
		t.Fatal("root counter reserve: expected true")
	}
	// child.treeCounter IS rootCounter, so the count is already reflected.
	// Release to keep the counter balanced.
	child.treeCounter.releaseKind(slotKindJob)
}

// TestDriveAtCapacityDoesNotLaunchOrSettle checks admission at the controller's
// leased drive capacity, with canonical custody and actual provider execution.
func TestDriveAtCapacityDoesNotLaunchOrSettle(t *testing.T) {
	t.Parallel()
	a := finiteUpdateAdapter(t, "CAPACITY-85")
	gate := make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	defer release()
	a.finiteScript("CAPACITY-85", updateReply(finalResponse("coordinator-idle-85")), updateGate(gate, func(_ context.Context, req llm.Request) (llm.Response, error) {
		if !requestContainsText(req, "capacity-body-85") {
			t.Error("released execution omitted accepted payload")
		}
		return finalResponse("useful-capacity-result-85"), nil
	}))
	root := newMessagingRoot(t, a, nil)
	coord := root.createDelegate(context.Background(), delegateArgs{Task: "CAPACITY-85"})
	if coord.Err != nil {
		t.Fatal(coord.Err)
	}
	waitForRuntimeSubagent(t, root, coord.ChildSessionID)
	root.sendersWG.Wait()
	sub := root.getSub(coord.ChildSessionID)
	receiver := sub.sess
	c := root.delegateController
	before := delegateAggregateSnapshot(t, c, receiver.owningDelegateID)
	c.mu.Lock()
	c.driveLimit, c.drivesInUse = 1, 1
	c.mu.Unlock()
	const id = "capacity-custody-85"
	observationEnqueue(t, receiver, id, "capacity-body-85")
	// The result means handled, not launched. All admission assertions below
	// inspect the real generation, executor, provider and receipt owners.
	root.driveStableDelegateAttention(sub)
	assertNotificationPending(t, receiver, []string{id})
	after := delegateAggregateSnapshot(t, c, receiver.owningDelegateID)
	if after.Generation != before.Generation || after.CurrentRunOpen {
		t.Fatalf("capacity refusal started generation: before=%+v after=%+v", before, after)
	}
	sub.mu.Lock()
	running, driving := sub.running, sub.driving
	sub.mu.Unlock()
	if running || driving || len(a.requestsFor("CAPACITY-85")) != 1 {
		t.Fatal("capacity refusal executed provider or retained an execution start")
	}
	receiver.mu.Lock()
	executing := receiver.execution.running
	receiver.mu.Unlock()
	if executing {
		t.Fatal("capacity refusal started receiver execution")
	}
	fold := rootAttentionFold(t, receiver)
	if accepted, exists := fold.content[id]; !exists || accepted.Text() != "capacity-body-85" || len(fold.observed) != 0 || len(fold.resolutions) != 0 {
		t.Fatalf("capacity refusal changed canonical custody: %+v", fold)
	}
	c.mu.Lock()
	held := c.drivesInUse
	c.mu.Unlock()
	if held != 1 {
		t.Fatalf("capacity refusal changed held slots: %d", held)
	}

	c.mu.Lock()
	c.drivesInUse--
	c.mu.Unlock()
	c.recheckNotificationReceivers()
	a.waitForRequests(t, "CAPACITY-85", 2)
	assertLeasedDriveCount(t, c, 1)
	assertNotificationPending(t, receiver, []string{id})
	fold = rootAttentionFold(t, receiver)
	if len(fold.observed) != 0 {
		t.Fatal("request admission invented successful observation")
	}
	release()
	root.sendersWG.Wait() // Joins provider, generation settlement and capacity release.
	assertNotificationPending(t, receiver, []string{})
	incidentNotificationFacts(t, root.stateDir, receiver.ID(), id)
	data, err := readTranscriptFull(transcriptPath(root.stateDir, receiver.ID()), "")
	if err != nil {
		t.Fatal(err)
	}
	receipts := 0
	for _, entry := range data.Entries {
		if slices.Contains(entry.Turn.ObservedNotificationIDs, id) {
			receipts++
			if !slices.Equal(entry.Turn.ObservedNotificationIDs, []string{id}) {
				t.Fatalf("response covered unexpected IDs: %v", entry.Turn.ObservedNotificationIDs)
			}
		}
	}
	if receipts != 1 {
		t.Fatalf("physical response receipts = %d want1", receipts)
	}
	after = delegateAggregateSnapshot(t, c, receiver.owningDelegateID)
	if after.Generation != before.Generation+1 || after.CurrentRunOpen {
		t.Fatalf("released generation not settled: %+v", after)
	}
	if got := len(a.requestsFor("CAPACITY-85")); got != 2 {
		t.Fatalf("provider requests=%d want2", got)
	}
	c.mu.Lock()
	held = c.drivesInUse
	c.mu.Unlock()
	if held != 0 {
		t.Fatalf("settled drive retained capacity: %d", held)
	}
}

// TestRestoredRootMintsTreeCounter proves that restoring a ROOT session through
// the real restore path mints the tree-wide counter (spec §4), so the 16-delegate
// tree cap is operative after a process restart. Without a minted counter,
// reserveTreeSlot treats nil as unbounded and the cap is bypassed.
//
// Red today: the RestoreSessionFromMetaWithConfig struct literal omits the
// treeCounter field and nothing mints it, so a restored root has treeCounter==nil.
// (TestCounterIdleFreesAndRestartRebuild only MODELS restart via a manual
// assignment; it never exercises the real restore path, which is this gap.)
func TestRestoredRootMintsTreeCounter(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	meta := schema.SessionMeta{
		ID:        "01TESTRESTORECOUNTER",
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config:    (SessionConfig{}).toSnapshot(),
	}
	restored, err := RestoreSessionFromMetaWithConfig(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, RestoreSessionConfig{StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	defer restored.Close()

	if restored.treeCounter == nil {
		t.Fatal("restored root treeCounter is nil; expected a minted counter (tree cap inoperative after restart)")
	}
}

// TestTreeCounterConfigurableCapAndErrorText pins the configured cap and the
// formatted capacity error: the text names the live cap, preserves the
// tree_at_capacity prefix token and trailing-period guidance, and matches the
// errTreeAtCapacity sentinel via errors.Is.
func TestTreeCounterConfigurableCapAndErrorText(t *testing.T) {
	t.Parallel()
	c := newTreeCounter(3)
	for i := range 3 {
		if !c.reserve(slotKindJob) {
			t.Fatalf("reserve %d failed below cap 3", i+1)
		}
	}
	if c.reserve(slotKindJob) {
		t.Fatal("reserve succeeded at cap 3")
	}
	err := &treeCapacityError{cap: 3}
	want := "tree_at_capacity: 3 delegate turn slots in use across this session tree (0 delegate jobs, 0 drive turns). " +
		"Wait for completions to free slots, job_stop work you no longer need, or narrow your fan-out and retry."
	if err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
	if !errors.Is(err, errTreeAtCapacity) {
		t.Fatal("treeCapacityError does not match errTreeAtCapacity via errors.Is")
	}
}

// TestTreeCounterOccupancyByKind pins the per-kind occupancy accounting: job
// and drive reservations are counted separately, release decrements only its
// own kind, and release stays idempotent.
func TestTreeCounterOccupancyByKind(t *testing.T) {
	t.Parallel()
	c := newTreeCounter(4)
	if !c.reserve(slotKindJob) {
		t.Fatal("job reserve failed")
	}
	if !c.reserve(slotKindDrive) {
		t.Fatal("drive reserve failed")
	}
	total, jobs, drives, limit := c.occupancy()
	if total != 2 || jobs != 1 || drives != 1 || limit != 4 {
		t.Fatalf("occupancy = (%d, %d, %d, %d), want (2, 1, 1, 4)", total, jobs, drives, limit)
	}
	c.releaseKind(slotKindJob)
	c.releaseKind(slotKindJob) // a stray double release must not drive jobs negative
	_, jobs, drives, _ = c.occupancy()
	if jobs != 0 {
		t.Fatalf("jobs = %d after release, want 0", jobs)
	}
	if drives != 1 {
		t.Fatalf("drives = %d after job release, want 1", drives)
	}
}

// TestTreeCapacityErrorOccupancyClause pins the occupancy breakdown in the
// formatted capacity error: the text names the cap and the job/drive split.
func TestTreeCapacityErrorOccupancyClause(t *testing.T) {
	t.Parallel()
	err := &treeCapacityError{cap: 50, jobs: 12, drives: 4}
	want := "tree_at_capacity: 50 delegate turn slots in use across this session tree (12 delegate jobs, 4 drive turns). " +
		"Wait for completions to free slots, job_stop work you no longer need, or narrow your fan-out and retry."
	if err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
	if !errors.Is(err, errTreeAtCapacity) {
		t.Fatal("treeCapacityError lost errTreeAtCapacity matching")
	}
}

// TestTreeCapacityErrorForReadsDriveBudget pins the occupancy-honesty fix:
// drive turns reserve on the separate driveCounter, so the spawn counter's
// per-kind drive tally is always 0. The capacity error must source its drive
// figure from the drive budget, or a drive-saturated tree reports a dead 0.
func TestTreeCapacityErrorForReadsDriveBudget(t *testing.T) {
	t.Parallel()
	s := &Session{
		treeCounter:  newTreeCounter(50),
		driveCounter: newTreeCounter(defaultMaxConcurrentDriveTurns),
	}
	s.treeCounter.reserve(slotKindJob)
	for range 3 {
		s.driveCounter.reserve(slotKindDrive)
	}
	err := s.treeCapacityErrorFor()
	want := "tree_at_capacity: 50 delegate turn slots in use across this session tree (1 delegate jobs, 3 drive turns). " +
		"Wait for completions to free slots, job_stop work you no longer need, or narrow your fan-out and retry."
	if err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
	if !errors.Is(err, errTreeAtCapacity) {
		t.Fatal("treeCapacityErrorFor lost errTreeAtCapacity matching")
	}
}

// TestTurnSlotOccupancyShowsDriveOnlyTree pins the job_list/status surface:
// with zero spawn slots held but drive turns in flight, occupancy is still
// reported (not elided), and the drive figure comes from the drive budget.
func TestTurnSlotOccupancyShowsDriveOnlyTree(t *testing.T) {
	t.Parallel()
	s := &Session{
		treeCounter:  newTreeCounter(50),
		driveCounter: newTreeCounter(defaultMaxConcurrentDriveTurns),
	}
	if occ := turnSlotOccupancyOf(s); occ != nil {
		t.Fatalf("idle tree should report no occupancy, got %+v", occ)
	}
	s.driveCounter.reserve(slotKindDrive)
	occ := turnSlotOccupancyOf(s)
	if occ == nil {
		t.Fatal("drive-held tree must report occupancy, got nil")
	}
	if occ.InUse != 0 || occ.Jobs != 0 || occ.Drives != 1 || occ.Cap != 50 {
		t.Fatalf("occupancy = %+v, want InUse=0 Jobs=0 Drives=1 Cap=50", occ)
	}
}

// TestDriveBudgetIndependentOfSpawnBudget proves the drive-down budget split:
// a saturated spawn (tree) budget does not block a drive turn, and a saturated
// drive budget does not block a spawn reservation.
func TestDriveBudgetIndependentOfSpawnBudget(t *testing.T) {
	t.Parallel()

	// Part 1: saturated drive budget never blocks a spawn reservation.
	sess := &Session{treeCounter: newTreeCounter(50), driveCounter: newTreeCounter(1)}
	if !sess.driveCounter.reserve(slotKindDrive) {
		t.Fatal("setup: drive reserve failed")
	}
	if _, ok := sess.reserveTreeSlot(slotKindJob); !ok {
		t.Fatal("spawn slot blocked by saturated drive budget")
	}
}

// TestDriveTurnReservesFromDriveCounter proves a drive turn holds a
// driveCounter slot, not a spawn-budget slot: with the spawn budget
// saturated, the drive still launches, holds a drive slot for its duration,
// and releases it at turn end.
func TestDriveTurnReservesFromDriveCounter(t *testing.T) {
	t.Parallel()
	entered, gate := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	defer release()
	returns := newActivationReturns()
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{func(llm.Request) llm.Response {
		close(entered)
		<-gate
		return finalResponse("drive-result-71af")
	}}
	root := restoreSupervisionRoot(t, fixture, nil)
	returns.configure(&root.cfg)
	t.Cleanup(func() { returns.close(t, root) })
	root.delegateController.mu.Lock()
	root.delegateController.turnLimit = 1
	if !root.delegateController.reserveCapacityLocked(delegateTurnCapacity) {
		t.Fatal("setup: turn reserve failed")
	}
	root.delegateController.mu.Unlock()
	if err := root.delegateController.enqueueNotification(fixture.childID, notification{ID: "drive-budget-71af", Message: llm.User("drive-budget-body-71af")}); err != nil {
		t.Fatal(err)
	}
	waitForTestSignal(t, entered, "drive provider entry with return held")
	activation := returns.take(t, fixture.childID)
	assertLeasedDriveCount(t, root.delegateController, 1)
	root.delegateController.mu.Lock()
	got := root.delegateController.turnsInUse
	root.delegateController.mu.Unlock()
	if got != 1 {
		t.Fatalf("spawn budget moved to %d during notification, want pinned 1", got)
	}
	release()
	waitForTestSignal(t, activation.generationDone, "drive terminal generation")
	waitForTestSignal(t, activation.returned, "drive executor tail")
	assertLeasedDriveCount(t, root.delegateController, 0)
}

// firstCompleteThenBlockAdapter completes the first request (the coordinator's
// own turn) and blocks every later request (drive turns) until the request
// context is done, modeling a hung provider call that only a context cancel
// can interrupt.
type firstCompleteThenBlockAdapter struct {
	mu                  sync.Mutex
	i                   int
	entered, returnGate chan struct{}
	result              struct {
		err  error
		done chan struct{}
	}
}

func (a *firstCompleteThenBlockAdapter) Name() string { return "openai" }
func (a *firstCompleteThenBlockAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	a.mu.Lock()
	a.i++
	n := a.i
	a.mu.Unlock()
	if n == 1 {
		resp := finalResponse("coordinator idle")
		resp.Provider = "openai"
		resp.Model = req.Model
		return resp, nil
	}
	close(a.entered)
	// Keep the reservation observable even if the real deadline has expired.
	<-a.returnGate
	<-ctx.Done()
	a.result.err = ctx.Err()
	close(a.result.done)
	return llm.Response{}, a.result.err
}
func (a *firstCompleteThenBlockAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

// TestDriveTurnTimeoutFreesSlot proves a drive turn whose child hangs is
// cancelled at driveTurnTimeout and its drive-budget slot is freed — a hung
// child cannot pin the budget until parent close.
func TestDriveTurnTimeoutFreesSlot(t *testing.T) {
	old := driveTurnTimeout
	driveTurnTimeout = 100 * time.Millisecond
	t.Cleanup(func() { driveTurnTimeout = old })
	fixture := newColdStableDelegateFixture(t, "")
	adapter := &firstCompleteThenBlockAdapter{entered: make(chan struct{}), returnGate: make(chan struct{})}
	adapter.result.done = make(chan struct{})
	release := sync.OnceFunc(func() { close(adapter.returnGate) })
	defer release()
	fixture.client.Register(adapter)
	root := restoreSupervisionRoot(t, fixture, nil)
	returns := newActivationReturns()
	returns.configure(&root.cfg)
	t.Cleanup(func() { returns.close(t, root) })
	warmStableSupervisionDelegate(t, root, fixture)
	warm := returns.take(t, fixture.childID)
	waitForTestSignal(t, warm.generationDone, "warm generation")
	waitForTestSignal(t, warm.returned, "warm executor tail")
	if err := root.delegateController.enqueueNotification(fixture.childID, notification{ID: "drive-deadline-71af", Message: llm.User("drive-deadline-body-71af")}); err != nil {
		t.Fatal(err)
	}
	waitForTestSignal(t, adapter.entered, "deadline drive provider entry")
	activation := returns.take(t, fixture.childID)
	if activation.lease != (delegateLease{delegateID: fixture.delegateID, generation: warm.lease.generation + 1}) || activation.generationDone == warm.generationDone {
		t.Fatalf("deadline activation=%+v warm=%+v", activation, warm)
	}
	assertLeasedDriveCount(t, root.delegateController, 1)
	release()
	waitForTestSignal(t, adapter.result.done, "actual drive context expiry")
	if !errors.Is(adapter.result.err, context.DeadlineExceeded) {
		t.Fatalf("drive provider error=%v, want real deadline expiry", adapter.result.err)
	}
	waitForTestSignal(t, activation.generationDone, "deadline drive terminal generation")
	waitForTestSignal(t, activation.returned, "deadline drive executor tail")
	assertLeasedDriveCount(t, root.delegateController, 0)
}

// The removed unleased redrive's pacing guarantee belongs to common readiness.
func TestNotificationRetryPacesCommonDispatch(t *testing.T) {
	s := observationSession(t, &skillContinuationAdapter{}, false)
	clock := agenttest.NewFakeClock()
	s.clock = clock
	wakes := make(chan struct{}, 2)
	s.SetNotifyFunc(func() { wakes <- struct{}{} })
	if _, err := s.appendDelegateNotificationDurably("paced-529a", "payload-529a"); err != nil {
		t.Fatal(err)
	}
	s.scheduleNotificationRetry()
	clock.Advance(jobNotificationRetryInitialDelay - time.Nanosecond)
	clock.Drain()
	select {
	case <-wakes:
		t.Fatal("retry fired before pacing interval")
	default:
	}
	clock.Advance(time.Nanosecond)
	clock.Drain()
	select {
	case <-wakes:
	default:
		t.Fatal("paced retry did not publish receiver readiness")
	}
}

// assertLeasedDriveCount follows held provider entry or exact executor return.
func assertLeasedDriveCount(t *testing.T, c *delegateTreeController, want int) {
	t.Helper()
	c.mu.Lock()
	got := c.drivesInUse
	c.mu.Unlock()
	if got != want {
		t.Fatalf("leased notification capacity=%d, want %d", got, want)
	}
}
