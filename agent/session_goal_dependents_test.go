package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/goal"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// --- Goal engine wake-pending-dependents hold ---
//
// A goal continuation turn that only polls (read-only tools + the result tool)
// while the session owns work guaranteed to deliver a future wake — a running
// delegate, a non-detached background job — is WAITING, not stalling. Before
// the hold, three such turns in a row tripped the no-progress breaker and
// blocked the goal mid-wait, stranding multi-phase programs whose next phase
// was meant to start when the last delegate reported (the gate had stopped
// driving turns, and nothing else woke the session after the final
// communicate(end_turn=true)).
//
// The hold mirrors the ask_user hold (spec §5.3): while wake-pending
// dependents exist, a non-progressed continuation gate skips the no-progress
// fold AND does not re-arm; the notification machinery wakes the session, and
// the notification turn's own settle re-arms the goal. Dependents that can
// never deliver a wake — idle delegates awaiting delegate_send, detached
// processes — must NOT hold, or the goal strands the other way.

// seedSessionDelegate seeds one delegate owned by sess. running=true appends
// the run-started event (PhaseRunning: a report or terminal notification is
// guaranteed to wake the session); running=false leaves it created-but-never-
// started (PhaseIdle: no autonomous wake, must not hold the goal).
func seedSessionDelegate(t *testing.T, sess *Session, c *delegateTreeController, id string, running bool) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	evs := []delegatestore.Event{{
		Kind:       delegatestore.EventDelegateCreated,
		DelegateID: id,
		Created: &delegatestore.DelegateCreated{Descriptor: delegatestore.Descriptor{
			ChildSessionID: "child-" + id,
			TranscriptRef:  "local:child-" + id,
			OwnerSessionID: sess.ID(),
			Task:           "wake-pending hold test delegate",
			AgentType:      "general",
			ToolNameCeiling: []string{
				"communicate",
			},
			Resumable: true,
		}},
	}}
	if running {
		evs = append(evs, delegateControllerRunStartedEvent(id, 1, delegatestore.TriggerInitial, time.Unix(10, 0).UTC()))
	}
	if _, err := c.appendLocked(evs...); err != nil {
		t.Fatalf("seed delegate %s (running=%v): %v", id, running, err)
	}
}

// seedSessionRunningDelegate seeds the controller with one delegate owned by
// sess in PhaseRunning (created + run started), the state whose terminal or
// report notification is guaranteed to wake the session.
func seedSessionRunningDelegate(t *testing.T, sess *Session, c *delegateTreeController, id string) {
	t.Helper()
	seedSessionDelegate(t, sess, c, id, true)
}

// seedSessionIdleDelegate seeds a delegate that was created but never started:
// PhaseIdle delivers no autonomous wake, so it must not hold the goal.
func seedSessionIdleDelegate(t *testing.T, sess *Session, c *delegateTreeController, id string) {
	t.Helper()
	seedSessionDelegate(t, sess, c, id, false)
}

func attachDelegateController(t *testing.T, sess *Session) *delegateTreeController {
	t.Helper()
	c, _ := newDelegateControllerTestHarness(t, 10, 10)
	c.rootRuntime = sess
	sess.delegateController = c
	return c
}

// seedRunningBackgroundJob registers one live job in the job manager. Detached
// processes deliberately stay out of jm.running, so any entry there is a
// non-detached job whose terminal notification is guaranteed.
func seedRunningBackgroundJob(t *testing.T, sess *Session, jobID string) {
	t.Helper()
	jm := sess.jobManager
	jm.mu.Lock()
	defer jm.mu.Unlock()
	jm.running[jobID] = &runningJob{rec: &jobstore.JobRecord{JobID: jobID, Status: jobstore.StatusRunning}, done: make(chan struct{})}
}

// seedProgressWatch installs an active progress-interval watch on a job, the
// shape that keeps waking the session with periodic ticks even if the job
// never exits — the supervised-job contract the goal hold counts on.
func seedProgressWatch(t *testing.T, sess *Session, jobID string) {
	t.Helper()
	jm := sess.jobManager
	jm.mu.Lock()
	defer jm.mu.Unlock()
	jm.watches[watchKey{VisibleSessionID: sess.ID(), Target: jobID}] = &watchConfig{
		target:             jobID,
		progressIntervalMS: 120000,
	}
}

// wireKickAndNotify mirrors serve.go's bridgeSession pairing: the idle kick and
// the notification wake are always installed together, and the goal hold
// legitimately depends on both.
func wireKickAndNotify(sess *Session, kicks *int) {
	sess.SetKickFunc(func(string) {
		if kicks != nil {
			*kicks++
		}
	})
	sess.SetNotifyFunc(func() {})
}

func goalSnapshot(t *testing.T, sess *Session) goal.Snapshot {
	t.Helper()
	snap, ok := sess.getOrCreateGoalStore().Snapshot()
	if !ok {
		t.Fatal("precondition: a goal should be set")
	}
	return snap
}

// TestGoalHoldRunningDelegateSkipsFoldAndRearm pins the core hold: a
// non-progressed continuation gate with a running delegate neither folds into
// the no-progress breaker nor re-arms — no matter how many times it is asked.
func TestGoalHoldRunningDelegateSkipsFoldAndRearm(t *testing.T) {
	t.Parallel()
	sess := newGoalMethodSession(t)
	defer sess.Close()
	wireKickAndNotify(sess, nil)
	attachDelegateController(t, sess)
	c := sess.delegateController
	seedSessionRunningDelegate(t, sess, c, "dlg_hold")

	sess.getOrCreateGoalStore().Set("wait for the triage fleet", time.Now())
	// One progressed fold so the two-tier breaker is in its strict regime
	// (NoProgressLimit, not NeverProgressedLimit).
	if _, ok := sess.armGoalContinuation(true, true); !ok {
		t.Fatal("progressed continuation should keep the goal active")
	}

	for i := range goal.NoProgressLimit + 2 {
		prompt, ok := sess.armGoalContinuation(false, true)
		if ok || prompt != "" {
			t.Fatalf("held gate %d = (%q, %v), want (\"\", false): waiting on a running delegate must not re-arm", i, prompt, ok)
		}
	}
	snap := goalSnapshot(t, sess)
	if snap.Status != goal.StatusActive {
		t.Fatalf("goal status = %q, want active: waiting on wake-pending dependents is not stalling", snap.Status)
	}
	if snap.Iterations != 1 || snap.NoProgressStreak != 0 {
		t.Fatalf("snapshot = %+v, want Iterations=1 NoProgressStreak=0 (held turns must not fold)", snap)
	}
}

// TestGoalHoldProgressedTurnFoldsNormally pins the ordering: a continuation
// that made a mutating call folds (and resets the streak) even while
// dependents run — the hold only shields non-progress turns.
func TestGoalHoldProgressedTurnFoldsNormally(t *testing.T) {
	t.Parallel()
	sess := newGoalMethodSession(t)
	defer sess.Close()
	wireKickAndNotify(sess, nil)
	attachDelegateController(t, sess)
	seedSessionRunningDelegate(t, sess, sess.delegateController, "dlg_hold")

	sess.getOrCreateGoalStore().Set("interleave fix waves", time.Now())
	prompt, ok := sess.armGoalContinuation(true, true)
	if !ok || prompt == "" {
		t.Fatal("progressed continuation with dependents must fold and re-arm normally")
	}
	snap := goalSnapshot(t, sess)
	if snap.Iterations != 1 || snap.NoProgressStreak != 0 || snap.Status != goal.StatusActive {
		t.Fatalf("snapshot = %+v, want one progressed fold, active goal", snap)
	}
}

// TestGoalHoldIdleDelegateDoesNotHold pins the exclusion: an idle delegate
// (reported, awaiting delegate_send) delivers no autonomous wake, so the gate
// must fold normally — otherwise the goal strands with no wake source.
func TestGoalHoldIdleDelegateDoesNotHold(t *testing.T) {
	t.Parallel()
	sess := newGoalMethodSession(t)
	defer sess.Close()
	wireKickAndNotify(sess, nil)
	attachDelegateController(t, sess)
	seedSessionIdleDelegate(t, sess, sess.delegateController, "dlg_idle")

	sess.getOrCreateGoalStore().Set("no wake will come", time.Now())
	if _, ok := sess.armGoalContinuation(true, true); !ok {
		t.Fatal("progressed continuation should keep the goal active")
	}
	if _, ok := sess.armGoalContinuation(false, true); !ok {
		t.Fatal("non-progressed continuation with only an idle delegate must fold and re-arm (no hold)")
	}
	snap := goalSnapshot(t, sess)
	if snap.Iterations != 2 || snap.NoProgressStreak != 1 {
		t.Fatalf("snapshot = %+v, want the no-progress turn folded (streak 1)", snap)
	}
}

// TestGoalHoldRunningBackgroundJobHolds pins the job half of the predicate: a
// live jm.running entry covered by a progress-interval watch keeps waking the
// session with periodic ticks even if the job never exits, so holding the
// goal on it is liveness-safe.
func TestGoalHoldRunningBackgroundJobHolds(t *testing.T) {
	t.Parallel()
	sess := newGoalMethodSession(t)
	defer sess.Close()
	wireKickAndNotify(sess, nil)
	seedRunningBackgroundJob(t, sess, "job_hold")
	seedProgressWatch(t, sess, "job_hold")

	sess.getOrCreateGoalStore().Set("wait for the build", time.Now())
	if _, ok := sess.armGoalContinuation(true, true); !ok {
		t.Fatal("progressed continuation should keep the goal active")
	}
	for i := range goal.NoProgressLimit + 1 {
		if prompt, ok := sess.armGoalContinuation(false, true); ok || prompt != "" {
			t.Fatalf("gate %d with a watched running job = (%q, %v), want held", i, prompt, ok)
		}
	}
	if snap := goalSnapshot(t, sess); snap.Status != goal.StatusActive {
		t.Fatalf("goal status = %q, want active while a watched background job runs", snap.Status)
	}
}

// TestGoalHoldUnwatchedBackgroundJobDoesNotHold pins the liveness bound: a
// bare running job delivers nothing until it exits (jobs have no watchdog),
// so it must NOT hold — a never-exiting unwatched job would otherwise park
// the goal forever with the breaker unreachable.
func TestGoalHoldUnwatchedBackgroundJobDoesNotHold(t *testing.T) {
	t.Parallel()
	sess := newGoalMethodSession(t)
	defer sess.Close()
	wireKickAndNotify(sess, nil)
	seedRunningBackgroundJob(t, sess, "job_unwatched")

	sess.getOrCreateGoalStore().Set("wait for the build", time.Now())
	if _, ok := sess.armGoalContinuation(true, true); !ok {
		t.Fatal("progressed continuation should keep the goal active")
	}
	if _, ok := sess.armGoalContinuation(false, true); !ok {
		t.Fatal("non-progressed continuation with only an unwatched job must fold and re-arm (no hold)")
	}
	if snap := goalSnapshot(t, sess); snap.Iterations != 2 || snap.NoProgressStreak != 1 {
		t.Fatalf("snapshot = %+v, want the no-progress turn folded (streak 1)", snap)
	}
}

// TestGoalHoldNoNotifyFuncDoesNotHold pins the pairing precondition: the
// hold's liveness depends on the notification wake (serve.go wires it
// alongside the kick), so with only the kick wired the gate must fold as
// before rather than park the goal on a wake that can never arrive.
func TestGoalHoldNoNotifyFuncDoesNotHold(t *testing.T) {
	t.Parallel()
	sess := newGoalMethodSession(t)
	defer sess.Close()
	sess.SetKickFunc(func(string) {})
	attachDelegateController(t, sess)
	seedSessionRunningDelegate(t, sess, sess.delegateController, "dlg_hold")

	sess.getOrCreateGoalStore().Set("kick but no notify", time.Now())
	if _, ok := sess.armGoalContinuation(false, true); !ok {
		t.Fatal("with notifyFunc unset the gate must not hold")
	}
	if snap := goalSnapshot(t, sess); snap.Iterations != 1 || snap.NoProgressStreak != 1 {
		t.Fatalf("snapshot = %+v, want the no-progress turn folded", snap)
	}
}

// TestGoalHoldNoKickFuncDoesNotHold pins the one-shot boundary: with no kick
// callback (e.g. `evener run`), the goal advances only through the drain's
// defer chain, so the gate must keep folding and re-arming exactly as before
// — holding there would strand the goal after the first notification.
func TestGoalHoldNoKickFuncDoesNotHold(t *testing.T) {
	t.Parallel()
	sess := newGoalMethodSession(t)
	defer sess.Close()
	attachDelegateController(t, sess)
	seedSessionRunningDelegate(t, sess, sess.delegateController, "dlg_hold")

	sess.getOrCreateGoalStore().Set("one-shot run", time.Now())
	if _, ok := sess.armGoalContinuation(false, true); !ok {
		t.Fatal("with kickFunc unset the gate must not hold: the defer chain is the only driver")
	}
	if snap := goalSnapshot(t, sess); snap.Iterations != 1 || snap.NoProgressStreak != 1 {
		t.Fatalf("snapshot = %+v, want the no-progress turn folded", snap)
	}
}

// TestGoalHoldSettleSuppressesKickWhileDependentsPending pins the settle half
// of the hold: when the gate just held (flag set) and dependents still pend,
// the idle-settle must not immediately re-kick the same goal past the wait.
func TestGoalHoldSettleSuppressesKickWhileDependentsPending(t *testing.T) {
	t.Parallel()
	sess := newGoalMethodSession(t)
	defer sess.Close()
	kicks := 0
	wireKickAndNotify(sess, &kicks)
	attachDelegateController(t, sess)
	seedSessionRunningDelegate(t, sess, sess.delegateController, "dlg_hold")
	sess.getOrCreateGoalStore().Set("held goal", time.Now())

	sess.mu.Lock()
	sess.goalDependentsHeld = true
	sess.mu.Unlock()

	if sess.settleGoalOnIdle() {
		t.Fatal("settle after a hold with dependents still pending must not kick")
	}
	if kicks != 0 {
		t.Fatalf("kicks = %d, want 0 while the hold stands", kicks)
	}
	sess.mu.Lock()
	held := sess.goalDependentsHeld
	sess.mu.Unlock()
	if held {
		t.Fatal("the settle must consume the hold flag")
	}
}

// TestGoalHoldSettleKicksOnceDependentsDrain pins the liveness re-check: a
// stale hold (the last delegate terminated between the gate read and the
// settle, its notification already queued) must not strand the goal — the
// settle recomputes the predicate and kicks.
func TestGoalHoldSettleKicksOnceDependentsDrain(t *testing.T) {
	t.Parallel()
	sess := newGoalMethodSession(t)
	defer sess.Close()
	kicks := 0
	wireKickAndNotify(sess, &kicks)
	sess.getOrCreateGoalStore().Set("held goal", time.Now())

	sess.mu.Lock()
	sess.goalDependentsHeld = true
	sess.mu.Unlock()

	if !sess.settleGoalOnIdle() {
		t.Fatal("settle after a hold whose dependents are gone must kick the goal back in")
	}
	if kicks != 1 {
		t.Fatalf("kicks = %d, want 1 once no wake-pending dependents remain", kicks)
	}
}

// TestGoalHoldSettleWithoutHoldKicksDespiteDependents pins the notification
// path: a settle that did NOT just hold (flag clear — e.g. after a
// notification turn) kicks the goal normally even though dependents still
// run, so adjudication follow-up work can continue between reports.
func TestGoalHoldSettleWithoutHoldKicksDespiteDependents(t *testing.T) {
	t.Parallel()
	sess := newGoalMethodSession(t)
	defer sess.Close()
	kicks := 0
	wireKickAndNotify(sess, &kicks)
	attachDelegateController(t, sess)
	seedSessionRunningDelegate(t, sess, sess.delegateController, "dlg_hold")
	sess.getOrCreateGoalStore().Set("adjudicate batch", time.Now())

	if !sess.settleGoalOnIdle() {
		t.Fatal("a settle that did not just hold must kick even with dependents running")
	}
	if kicks != 1 {
		t.Fatalf("kicks = %d, want 1", kicks)
	}
}

// TestGoalHoldSetGoalVoidsPendingHold: a fresh objective invalidates a
// pending hold — the retargeted goal must not inherit the old goal's wait.
func TestGoalHoldSetGoalVoidsPendingHold(t *testing.T) {
	t.Parallel()
	sess := newGoalMethodSession(t)
	defer sess.Close()
	wireKickAndNotify(sess, nil)

	sess.mu.Lock()
	sess.goalDependentsHeld = true
	sess.mu.Unlock()

	if _, err := sess.SetGoal(context.Background(), "new objective"); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	sess.mu.Lock()
	held := sess.goalDependentsHeld
	sess.mu.Unlock()
	if held {
		t.Fatal("SetGoal must clear a pending dependents hold (the new objective never waited)")
	}
}

// TestGoalHoldContinuationWaitsOnRunningDelegate drives the full drain loop:
// a communicate-only (no-progress) continuation turn with a running delegate
// must end the session idle with the goal active and unfolded — before the
// hold, three such turns blocked the goal mid-wait.
func TestGoalHoldContinuationWaitsOnRunningDelegate(t *testing.T) {
	t.Parallel()
	sess := newSession(t, withSteps(
		func(req llm.Request) llm.Response {
			return toolCallResponse(communicateCall("c1", "delegates still running"))
		},
		func(req llm.Request) llm.Response {
			t.Fatalf("reached a second LLM call: a held goal must not drive another continuation while dependents run")
			return llm.Response{}
		},
	))
	attachDelegateController(t, sess)
	seedSessionRunningDelegate(t, sess, sess.delegateController, "dlg_hold")

	kicks := 0
	wireKickAndNotify(sess, &kicks)
	sess.getOrCreateGoalStore().Set("triage the fleet", time.Now())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	if _, err := sess.ProcessInputKind(ctx, "continue toward the goal", nil, EntryContinuation); err != nil {
		t.Fatalf("ProcessInputKind: %v", err)
	}

	snap := goalSnapshot(t, sess)
	if snap.Status != goal.StatusActive || snap.Iterations != 0 || snap.NoProgressStreak != 0 {
		t.Fatalf("snapshot = %+v, want active and unfolded after a held wait-turn", snap)
	}
	if kicks != 0 {
		t.Fatalf("kicks = %d, want 0 (the settle must not re-kick past the wait)", kicks)
	}
}

// TestGoalHoldNotificationTurnRearmsGoal pins the resume half end to end:
// after the hold parks the session, the next notification turn's settle
// re-arms the goal (kick) so the program continues when reports land.
func TestGoalHoldNotificationTurnRearmsGoal(t *testing.T) {
	t.Parallel()
	sess := newSession(t, withSteps(
		func(req llm.Request) llm.Response { return toolCallResponse(communicateCall("c1", "waiting")) },
		func(req llm.Request) llm.Response {
			return toolCallResponse(communicateCall("c2", "batch adjudicated"))
		},
	))
	attachDelegateController(t, sess)
	seedSessionRunningDelegate(t, sess, sess.delegateController, "dlg_hold")

	kicks := 0
	wireKickAndNotify(sess, &kicks)
	sess.getOrCreateGoalStore().Set("triage the fleet", time.Now())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	if _, err := sess.ProcessInputKind(ctx, "continue toward the goal", nil, EntryContinuation); err != nil {
		t.Fatalf("continuation: %v", err)
	}
	if kicks != 0 {
		t.Fatalf("kicks after the held continuation = %d, want 0", kicks)
	}

	sess.enqueueJobNotification(watchNotification("job_batch1", "output_match: done"))
	if _, err := sess.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
		t.Fatalf("notification turn: %v", err)
	}
	if kicks != 1 {
		t.Fatalf("kicks after the notification turn = %d, want 1 (the goal must re-arm when reports land)", kicks)
	}
	if snap := goalSnapshot(t, sess); snap.Status != goal.StatusActive {
		t.Fatalf("goal status = %q, want active", snap.Status)
	}
}

// TestGoalBreakerSystemTurnAppendedOnce pins the visibility companion: when
// the no-progress breaker fires, one steering turn records the stall in the
// transcript (and the live context). Before this, the block existed only in
// meta.json and the live event stream — the transcript showed a session that
// simply stopped. The note rides the steering channel (user-role), not a
// system-role message, so provider adapters cannot fold it into persistent
// system instructions and the appwire projection carries it on reload.
func TestGoalBreakerSystemTurnAppendedOnce(t *testing.T) {
	t.Parallel()
	// One drive cascades: each communicate-only continuation folds and re-arms
	// inline until the breaker trips. A goal that never made a mutating call
	// gets the larger NeverProgressedLimit before blocking.
	steps := make([]func(req llm.Request) llm.Response, 0, goal.NeverProgressedLimit+1)
	for i := range goal.NeverProgressedLimit {
		steps = append(steps, func(req llm.Request) llm.Response {
			return toolCallResponse(communicateCall(fmt.Sprintf("c%d", i), "poll"))
		})
	}
	steps = append(steps, func(req llm.Request) llm.Response {
		t.Fatalf("reached a further LLM call: the blocked goal must stop driving continuations")
		return llm.Response{}
	})
	sess := newSession(t, withSteps(steps...))
	wireKickAndNotify(sess, nil)
	sess.getOrCreateGoalStore().Set("doomed polling loop", time.Now())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	if _, err := sess.ProcessInputKind(ctx, "continue toward the goal", nil, EntryContinuation); err != nil {
		t.Fatalf("ProcessInputKind: %v", err)
	}

	snap := goalSnapshot(t, sess)
	if snap.Status != goal.StatusBlocked {
		t.Fatalf("goal status = %q, want blocked after %d never-progressed continuations", snap.Status, goal.NeverProgressedLimit)
	}
	sess.mu.Lock()
	breakerNotes := 0
	for _, turn := range sess.history {
		if turn.Kind == schema.TurnSteering && strings.Contains(turn.Message.Text(), "goal-no-progress-breaker") {
			breakerNotes++
		}
	}
	sess.mu.Unlock()
	if breakerNotes != 1 {
		t.Fatalf("breaker steering notes in history = %d, want exactly 1", breakerNotes)
	}
}
