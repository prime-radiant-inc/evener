package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// waitForTreeCount polls the tree counter until it reaches want or the deadline
// elapses. Reservations and releases happen on spawn/resume/drive/finalize
// goroutines, so the count is observed asynchronously. The 5 s deadline is
// generous enough to accommodate -race on slow/few-core boxes without being so
// long that a genuine hang goes undetected.
func waitForTreeCount(t *testing.T, c *treeCounter, want int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := c.n.Load(); got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("tree counter = %d, want %d (timed out)", c.n.Load(), want)
}

// waitForDriveCount mirrors waitForTreeCount for the drive-down budget
// counter.
func waitForDriveCount(t *testing.T, c *treeCounter, want int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := c.n.Load(); got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("drive counter = %d, want %d (timed out)", c.n.Load(), want)
}

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
		StateDir:         stateDir,
		NoProjectPrompts: true,
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
		StateDir:         childStateDir,
		NoProjectPrompts: true,
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

// TestDriveAtCapacityDoesNotLaunchOrSettle proves the drive-budget/§3
// interaction: when the drive budget is at capacity, a drive does NOT launch
// (driveSubagentNotificationTurn returns false), does NOT settle, and the
// child's durable signal persists so the next loop boundary retries (spec §3:
// the ledger is durable; no retry daemon).
func TestDriveAtCapacityDoesNotLaunchOrSettle(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(_ llm.Request) llm.Response { return finalResponse("coordinator idle") },
			// A drive turn would consume step 1. At capacity it must never run.
			func(_ llm.Request) llm.Response { return finalResponse("must not run") },
		},
	})
	sess := newSession(t, withClient(c), withConfig(SessionConfig{MaxSubagentDepth: 2, NoProjectPrompts: true, StateDir: t.TempDir()}), withoutGitSnapshot())

	coord := sess.createDelegate(context.Background(), delegateArgs{
		Task: "coordinate",
	})
	if coord.Err != nil {
		t.Fatalf("createDelegate (coordinator): %v", coord.Err)
	}
	waitForRuntimeSubagent(t, sess, coord.ChildSessionID)
	waitForTreeCount(t, sess.treeCounter, 0)

	coordSub := sess.subagents.get(coord.ChildSessionID)
	if coordSub == nil || coordSub.sess == nil {
		t.Fatalf("coordinator subagent %q not found", coord.ChildSessionID)
	}
	enqueueCompletedJobNotification(t, coordSub.sess, "worker-1")
	if got := coordSub.sess.peekNotifications(); got == 0 {
		t.Fatal("coordinator has no pending notification; test setup failed")
	}

	// Saturate the drive budget directly so the drive cannot claim a slot.
	sess.driveCounter = newTreeCounter(1)
	if !sess.driveCounter.reserve(slotKindDrive) {
		t.Fatal("manual saturating drive reserve failed")
	}

	if sess.driveSubagentNotificationTurn(coordSub) {
		t.Fatal("driveSubagentNotificationTurn returned true at capacity; the drive must not launch")
	}
	// No slot was consumed by the (rejected) drive — still exactly 1 held.
	if got := sess.driveCounter.n.Load(); got != 1 {
		t.Fatalf("drive counter = %d after rejected drive, want 1 (drive reserved nothing)", got)
	}
	// The durable signal persists: the coordinator's queued notification is not
	// drained or settled, so a later boundary (with a free slot) can retry.
	if got := coordSub.sess.peekNotifications(); got == 0 {
		t.Fatal("coordinator's pending notification was drained by a drive that should not have launched")
	}

	// Free one slot; the retry now launches and drains the signal.
	sess.driveCounter.releaseKind(slotKindDrive)
	if !sess.driveSubagentNotificationTurn(coordSub) {
		t.Fatal("driveSubagentNotificationTurn returned false after a slot freed; the retry should launch")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && coordSub.sess.peekNotifications() != 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := coordSub.sess.peekNotifications(); got != 0 {
		t.Fatalf("coordinator still has %d pending after the retry drive drained it", got)
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
		Config:    (SessionConfig{NoProjectPrompts: true}).toSnapshot(),
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
	release := make(chan struct{})
	var releaseOnce sync.Once
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(_ llm.Request) llm.Response { return finalResponse("coordinator idle") },
			func(_ llm.Request) llm.Response {
				<-release
				return finalResponse("drive done")
			},
		},
	})
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	sess := newSession(t, withClient(c), withConfig(SessionConfig{MaxSubagentDepth: 2, NoProjectPrompts: true, StateDir: t.TempDir()}), withoutGitSnapshot())

	coord := sess.createDelegate(context.Background(), delegateArgs{
		Task: "coordinate",
	})
	if coord.Err != nil {
		t.Fatalf("createDelegate (coordinator): %v", coord.Err)
	}
	waitForRuntimeSubagent(t, sess, coord.ChildSessionID)
	waitForTreeCount(t, sess.treeCounter, 0)

	coordSub := sess.subagents.get(coord.ChildSessionID)
	if coordSub == nil || coordSub.sess == nil {
		t.Fatalf("coordinator subagent %q not found", coord.ChildSessionID)
	}
	enqueueCompletedJobNotification(t, coordSub.sess, "worker-1")

	// Saturate the spawn budget: the drive must still launch.
	sess.treeCounter = newTreeCounter(1)
	if !sess.treeCounter.reserve(slotKindJob) {
		t.Fatal("setup: tree reserve failed")
	}
	if !sess.driveSubagentNotificationTurn(coordSub) {
		t.Fatal("drive did not launch with the spawn budget saturated")
	}
	// While the drive turn runs, the drive counter holds the slot and the
	// spawn budget is untouched (still exactly its pinned 1).
	waitForDriveCount(t, sess.driveCounter, 1)
	if got := sess.treeCounter.n.Load(); got != 1 {
		t.Fatalf("spawn budget moved to %d during drive, want pinned 1", got)
	}

	releaseOnce.Do(func() { close(release) })
	waitForDriveCount(t, sess.driveCounter, 0)
}

// firstCompleteThenBlockAdapter completes the first request (the coordinator's
// own turn) and blocks every later request (drive turns) until the request
// context is done, modeling a hung provider call that only a context cancel
// can interrupt.
type firstCompleteThenBlockAdapter struct {
	mu sync.Mutex
	i  int
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
	<-ctx.Done()
	return llm.Response{}, ctx.Err()
}
func (a *firstCompleteThenBlockAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

// TestDriveTurnTimeoutFreesSlot proves a drive turn whose child hangs is
// cancelled at driveTurnTimeout and its drive-budget slot is freed — a hung
// child cannot pin the budget until parent close.
func TestDriveTurnTimeoutFreesSlot(t *testing.T) {
	// NOT parallel: this test shrinks a package-level timing var; parallel
	// siblings must never observe the mutated value.
	old := driveTurnTimeout
	driveTurnTimeout = 100 * time.Millisecond
	t.Cleanup(func() { driveTurnTimeout = old })

	c := llm.NewClient()
	c.Register(&firstCompleteThenBlockAdapter{})
	sess := newSession(t, withClient(c), withConfig(SessionConfig{MaxSubagentDepth: 2, NoProjectPrompts: true, StateDir: t.TempDir()}), withoutGitSnapshot())

	coord := sess.createDelegate(context.Background(), delegateArgs{
		Task: "coordinate",
	})
	if coord.Err != nil {
		t.Fatalf("createDelegate (coordinator): %v", coord.Err)
	}
	waitForRuntimeSubagent(t, sess, coord.ChildSessionID)
	coordSub := sess.subagents.get(coord.ChildSessionID)
	if coordSub == nil || coordSub.sess == nil {
		t.Fatalf("coordinator subagent %q not found", coord.ChildSessionID)
	}
	enqueueCompletedJobNotification(t, coordSub.sess, "worker-1")

	if !sess.driveSubagentNotificationTurn(coordSub) {
		t.Fatal("drive did not launch")
	}
	waitForDriveCount(t, sess.driveCounter, 1)
	// The child hangs; the timeout must cancel the turn and free the slot.
	waitForDriveCount(t, sess.driveCounter, 0)
}

// TestDriveRedriveIsPaced proves the post-turn re-drive waits
// driveRedriveMinInterval instead of launching immediately: with attention
// still queued on the child, no drive is in flight shortly after the re-check
// begins, but one launches after the interval.
func TestDriveRedriveIsPaced(t *testing.T) {
	// NOT parallel: this test shrinks a package-level timing var; parallel
	// siblings must never observe the mutated value.
	old := driveRedriveMinInterval
	driveRedriveMinInterval = 300 * time.Millisecond
	t.Cleanup(func() { driveRedriveMinInterval = old })

	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(_ llm.Request) llm.Response { return finalResponse("coordinator idle") },
			func(_ llm.Request) llm.Response { return finalResponse("drive done") },
		},
	})
	sess := newSession(t, withClient(c), withConfig(SessionConfig{MaxSubagentDepth: 2, NoProjectPrompts: true, StateDir: t.TempDir()}), withoutGitSnapshot())

	coord := sess.createDelegate(context.Background(), delegateArgs{
		Task: "coordinate",
	})
	if coord.Err != nil {
		t.Fatalf("createDelegate (coordinator): %v", coord.Err)
	}
	waitForRuntimeSubagent(t, sess, coord.ChildSessionID)
	coordSub := sess.subagents.get(coord.ChildSessionID)
	if coordSub == nil || coordSub.sess == nil {
		t.Fatalf("coordinator subagent %q not found", coord.ChildSessionID)
	}
	// Queue attention on the child but drive nothing: the re-check owns the
	// (paced) launch.
	enqueueCompletedJobNotification(t, coordSub.sess, "worker-1")

	done := make(chan struct{})
	go func() {
		defer close(done)
		sess.redriveChildIfAttentionRemains(context.Background(), coordSub, coordSub.sess)
	}()
	// Inside the pacing interval, no drive may be in flight.
	time.Sleep(100 * time.Millisecond)
	if got := sess.driveCounter.n.Load(); got != 0 {
		t.Fatalf("re-drive launched inside the pacing interval: drive counter = %d", got)
	}
	// The counter check alone is necessary but not sufficient: a full pacing
	// removal launches the fast fake drive at ~0ms, which reserves AND
	// releases the counter back to 0 well before this 100ms sample — so
	// counter==0 passes whether the re-drive never launched OR already
	// launched and finished. An early drive would also have drained the
	// child's queued notification, so peekNotifications() staying > 0 at the
	// same sample point is the necessary companion: it can only hold if the
	// re-drive genuinely has not run yet.
	if got := coordSub.sess.peekNotifications(); got == 0 {
		t.Fatalf("re-drive already drained notifications inside the pacing interval: peekNotifications = %d", got)
	}
	// After the interval, the paced re-drive launches, runs, and drains the
	// child's queued attention. Awaiting the drain (peek→0) is the reliable
	// completion signal that the paced drive actually ran — a drive turn is the
	// only path that drains the child's notifications. The live drive-slot gauge
	// peaks at 1 for only the turn's brief duration, so sampling it for ==1
	// races the reserve→release and is the source of the historical flake.
	<-done
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && coordSub.sess.peekNotifications() != 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := coordSub.sess.peekNotifications(); got != 0 {
		t.Fatalf("paced re-drive left %d notifications undrained", got)
	}
	// The drive released its budget slot once the turn completed.
	waitForDriveCount(t, sess.driveCounter, 0)
}
