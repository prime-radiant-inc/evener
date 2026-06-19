package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// waitForTreeCount polls the tree counter until it reaches want or the deadline
// elapses. Reservations and releases happen on spawn/resume/drive/finalize
// goroutines, so the count is observed asynchronously.
func waitForTreeCount(t *testing.T, c *treeCounter, want int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := c.n.Load(); got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("tree counter = %d, want %d (timed out)", c.n.Load(), want)
}

// TestTreeCounterReserveRelease verifies the atomic check-and-reserve logic:
// 16 reservations succeed, the 17th fails, releasing one allows another to succeed.
//
// Red today: type treeCounter does not exist.
func TestTreeCounterReserveRelease(t *testing.T) {
	c := newTreeCounter()

	// Reserve up to cap (16) — all must succeed.
	for i := range 16 {
		if !c.reserve() {
			t.Fatalf("reserve %d: expected true (under cap), got false", i+1)
		}
	}

	// 17th reservation must fail — at cap.
	if c.reserve() {
		t.Fatal("reserve 17: expected false (at cap), got true")
	}

	// Release one slot.
	c.release()

	// Now one reservation should succeed again.
	if !c.reserve() {
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
	if !rootCounter.reserve() {
		t.Fatal("root counter reserve: expected true")
	}
	// child.treeCounter IS rootCounter, so the count is already reflected.
	// Release to keep the counter balanced.
	child.treeCounter.release()
}

// TestCounterReservesOnSpawnResumeDrive proves that each of the three paths that
// launch a running delegate turn reserves a tree-counter slot while its turn
// runs, and that terminal finalize AND the abandon path release it.
func TestCounterReservesOnSpawnResumeDrive(t *testing.T) {
	t.Run("spawn reserves and terminal finalize releases", func(t *testing.T) {
		release := make(chan struct{})
		var releaseOnce sync.Once
		c := llm.NewClient()
		c.Register(&fakeAdapter{
			name: "openai",
			steps: []func(req llm.Request) llm.Response{
				func(_ llm.Request) llm.Response {
					<-release
					return communicateWithDefaultOutput("spawn done")
				},
			},
		})
		sess := newDelegateTestSession(t, c)
		t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

		spawned := sess.createDelegate(context.Background(), delegateArgs{
			Task:       "hold a slot",
			Background: true,
		})
		if spawned.Err != nil {
			t.Fatalf("createDelegate: %v", spawned.Err)
		}
		// The spawn launched a running delegate turn; its slot must be reserved.
		waitForTreeCount(t, sess.treeCounter, 1)

		// Releasing the blocking step lets the turn end → terminal finalize releases.
		releaseOnce.Do(func() { close(release) })
		waitForShellDone(t, sess.jobManager, spawned.JobID)
		waitForTreeCount(t, sess.treeCounter, 0)
	})

	t.Run("resume reserves and abandon releases", func(t *testing.T) {
		adapter := &resumeBlockingDelegateAdapter{name: "openai", secondStarted: make(chan struct{})}
		c := llm.NewClient()
		c.Register(adapter)
		sess := newDelegateTestSession(t, c)

		first := sess.createDelegate(context.Background(), delegateArgs{
			Task:           "first finishes",
			Background:     false,
			BlockTimeoutMS: 5000,
		})
		if first.Err != nil {
			t.Fatalf("createDelegate: %v", first.Err)
		}
		if first.Status != jobstore.StatusCompleted {
			t.Fatalf("first = %+v, want completed", first)
		}
		// The first turn ended: idle delegate holds no reservation.
		waitForTreeCount(t, sess.treeCounter, 0)

		res := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
			Target:         first.DelegateID,
			Message:        "resume and block",
			OnIdle:         "start",
			Background:     true,
			BackgroundSet:  true,
			BlockTimeoutMS: 1000,
		})
		if res.Err != nil {
			t.Fatalf("sendDelegateMessage: %v", res.Err)
		}
		<-adapter.secondStarted
		// The resume launched a running delegate turn; its slot must be reserved.
		waitForTreeCount(t, sess.treeCounter, 1)

		// Abandon the running resume job → the abandon path releases the slot.
		sess.jobManager.abandonRunningJob(res.JobID)
		waitForTreeCount(t, sess.treeCounter, 0)
	})

	t.Run("drive reserves and turn end releases", func(t *testing.T) {
		release := make(chan struct{})
		var releaseOnce sync.Once
		c := llm.NewClient()
		c.Register(&fakeAdapter{
			name: "openai",
			steps: []func(req llm.Request) llm.Response{
				// Step 0: coordinator's initial turn ends cleanly.
				func(_ llm.Request) llm.Response {
					return finalResponse("coordinator idle")
				},
				// Step 1: the drive turn (EntryNotification). Block so the drive
				// goroutine holds its reservation while we observe the counter.
				func(_ llm.Request) llm.Response {
					<-release
					return finalResponse("ack")
				},
			},
		})
		sess := newDelegateTestSession(t, c)
		t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

		coord := sess.createDelegate(context.Background(), delegateArgs{
			Task:       "coordinate",
			Background: true,
		})
		if coord.Err != nil {
			t.Fatalf("createDelegate (coordinator): %v", coord.Err)
		}
		waitForShellDone(t, sess.jobManager, coord.JobID)
		// Coordinator ended its own turn → idle → no reservation.
		waitForTreeCount(t, sess.treeCounter, 0)

		_, coordID, err := decodeRef(coord.TranscriptRef)
		if err != nil {
			t.Fatalf("decodeRef: %v", err)
		}
		coordSub := sess.subagents.get(coordID)
		if coordSub == nil || coordSub.sess == nil {
			t.Fatalf("coordinator subagent %q not found", coordID)
		}
		// Queue a worker completion on the coordinator so a drive turn has work.
		enqueueCompletedDelegateNotification(t, coordSub.sess, "worker-1")

		// Drive the coordinator's notification turn directly.
		if !sess.driveSubagentNotificationTurn(coordSub) {
			t.Fatal("driveSubagentNotificationTurn returned false; expected a launched drive turn")
		}
		// The drive turn is running and holds a reservation.
		waitForTreeCount(t, sess.treeCounter, 1)

		// Let the drive turn finish → its turn end releases the slot.
		releaseOnce.Do(func() { close(release) })
		waitForTreeCount(t, sess.treeCounter, 0)
	})
}

// TestDriveAtCapacityDoesNotLaunchOrSettle proves the §4/§3 interaction: when the
// tree is at capacity, a drive does NOT launch (driveSubagentNotificationTurn
// returns false), does NOT settle, and the child's durable signal persists so the
// next loop boundary retries (spec §3: the ledger is durable; no retry daemon).
func TestDriveAtCapacityDoesNotLaunchOrSettle(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(_ llm.Request) llm.Response { return finalResponse("coordinator idle") },
			// A drive turn would consume step 1. At capacity it must never run.
			func(_ llm.Request) llm.Response { return finalResponse("must not run") },
		},
	})
	sess := newDelegateTestSession(t, c)

	coord := sess.createDelegate(context.Background(), delegateArgs{
		Task:       "coordinate",
		Background: true,
	})
	if coord.Err != nil {
		t.Fatalf("createDelegate (coordinator): %v", coord.Err)
	}
	waitForShellDone(t, sess.jobManager, coord.JobID)
	waitForTreeCount(t, sess.treeCounter, 0)

	_, coordID, err := decodeRef(coord.TranscriptRef)
	if err != nil {
		t.Fatalf("decodeRef: %v", err)
	}
	coordSub := sess.subagents.get(coordID)
	if coordSub == nil || coordSub.sess == nil {
		t.Fatalf("coordinator subagent %q not found", coordID)
	}
	enqueueCompletedDelegateNotification(t, coordSub.sess, "worker-1")
	if got := coordSub.sess.peekNotifications(); got == 0 {
		t.Fatal("coordinator has no pending notification; test setup failed")
	}

	// Saturate the tree counter directly so the drive cannot claim a slot.
	for i := range 16 {
		if !sess.treeCounter.reserve() {
			t.Fatalf("manual saturating reserve %d failed", i+1)
		}
	}

	if sess.driveSubagentNotificationTurn(coordSub) {
		t.Fatal("driveSubagentNotificationTurn returned true at capacity; the drive must not launch")
	}
	// No slot was consumed by the (rejected) drive — still exactly 16.
	if got := sess.treeCounter.n.Load(); got != 16 {
		t.Fatalf("tree counter = %d after rejected drive, want 16 (drive reserved nothing)", got)
	}
	// The durable signal persists: the coordinator's queued notification is not
	// drained or settled, so a later boundary (with a free slot) can retry.
	if got := coordSub.sess.peekNotifications(); got == 0 {
		t.Fatal("coordinator's pending notification was drained by a drive that should not have launched")
	}

	// Free one slot; the retry now launches and drains the signal.
	sess.treeCounter.release()
	if !sess.driveSubagentNotificationTurn(coordSub) {
		t.Fatal("driveSubagentNotificationTurn returned false after a slot freed; the retry should launch")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && coordSub.sess.peekNotifications() != 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := coordSub.sess.peekNotifications(); got != 0 {
		t.Fatalf("coordinator still has %d pending after the retry drive drained it", got)
	}
}

// TestCounter17thFails proves the tree-wide cap (16): with 16 concurrent running
// delegate turns holding reservations, the 17th spawn returns the exact
// tree_at_capacity error and does NOT launch.
func TestCounter17thFails(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(_ llm.Request) llm.Response {
				<-release
				return communicateWithDefaultOutput("done")
			},
		},
	})
	sess := newDelegateTestSession(t, c)
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	// Launch 16 concurrent background delegates; each holds a running turn (its
	// adapter step blocks on release), so each holds a reservation.
	for i := range 16 {
		res := sess.createDelegate(context.Background(), delegateArgs{
			Task:       "fan-out worker",
			Background: true,
		})
		if res.Err != nil {
			t.Fatalf("createDelegate %d: %v", i+1, res.Err)
		}
	}
	waitForTreeCount(t, sess.treeCounter, 16)

	// The 17th must fail loudly with the exact tree_at_capacity text.
	seventeenth := sess.createDelegate(context.Background(), delegateArgs{
		Task:       "one too many",
		Background: true,
	})
	if seventeenth.Err == nil {
		t.Fatal("17th createDelegate succeeded; want tree_at_capacity error")
	}
	wantErr := "tree_at_capacity: 16 delegate jobs running across this session tree. " +
		"Wait for completions to free slots, job_stop work you no longer need, or narrow your fan-out and retry."
	if got := seventeenth.Err.Error(); !strings.Contains(got, wantErr) {
		t.Fatalf("17th error = %q, want it to contain %q", got, wantErr)
	}
	// The counter must still be exactly 16 — the failed spawn reserved nothing.
	if got := sess.treeCounter.n.Load(); got != 16 {
		t.Fatalf("tree counter = %d after rejected 17th, want 16", got)
	}

	releaseOnce.Do(func() { close(release) })
}

// TestCounterIdleFreesAndRestartRebuild proves two §4 properties:
//   - a delegate whose turn ended holds NO reservation (its slot is freed), so a
//     fresh spawn after it reuses the slot rather than stacking;
//   - a restart rebuilds the counter from the post-reconciliation state (zero),
//     and a descendant re-reserves as it re-attaches/resumes.
func TestCounterIdleFreesAndRestartRebuild(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// Step 0: the first delegate completes and goes idle.
			func(_ llm.Request) llm.Response { return communicateWithDefaultOutput("done") },
			// Step 1: the post-restart re-attach delegate blocks so its reservation
			// against the rebuilt counter is observable.
			func(_ llm.Request) llm.Response {
				<-release
				return communicateWithDefaultOutput("re-attach done")
			},
		},
	})
	sess := newDelegateTestSession(t, c)
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	// A delegate that runs to completion: its turn ends, so it must hold no slot.
	first := sess.createDelegate(context.Background(), delegateArgs{
		Task:           "complete then idle",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if first.Err != nil {
		t.Fatalf("createDelegate: %v", first.Err)
	}
	if first.Status != jobstore.StatusCompleted {
		t.Fatalf("first = %+v, want completed", first)
	}
	waitForTreeCount(t, sess.treeCounter, 0)

	// Restart rebuilds the counter from the post-reconciliation state (zero). The
	// production root mints a fresh counter; model that here.
	rebuilt := newTreeCounter()
	if got := rebuilt.n.Load(); got != 0 {
		t.Fatalf("rebuilt counter = %d, want 0 (root rebuilds from post-reconciliation state)", got)
	}
	sess.treeCounter = rebuilt

	// A descendant re-reserves as it re-attaches: a fresh delegate turn against the
	// rebuilt counter must move it to 1, then back to 0 when its turn ends.
	second := sess.createDelegate(context.Background(), delegateArgs{
		Task:       "re-attach descendant",
		Background: true,
	})
	if second.Err != nil {
		t.Fatalf("createDelegate (re-attach): %v", second.Err)
	}
	waitForTreeCount(t, sess.treeCounter, 1)
	releaseOnce.Do(func() { close(release) })
	waitForShellDone(t, sess.jobManager, second.JobID)
	waitForTreeCount(t, sess.treeCounter, 0)
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
