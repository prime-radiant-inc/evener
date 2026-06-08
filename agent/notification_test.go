package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/goal"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// TestRun_ArmsOneNotificationOnTerminal proves a child reaching a terminal run
// state arms exactly one metadata notification on the parent. The notification
// carries the child's agent_id, the completed status/reason, a transcript_ref,
// and turns_used — and no child output. Arming happens once per run: a second
// drain returns nothing.
//
// Load-bearing: the arm site in run's finalize is what enqueues the entry. If it
// were removed, the first drainNotifications would return 0 and the len==1
// assertion would fail.
func TestRun_ArmsOneNotificationOnTerminal(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("child done") },
		},
	})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	childID := spawnCompletedChild(t, sess, "n1", "do the thing")

	notifs := sess.drainNotifications()
	if len(notifs) != 1 {
		t.Fatalf("drainNotifications after terminal run = %d entries, want 1", len(notifs))
	}
	n := notifs[0]
	if n.AgentID != childID {
		t.Errorf("notification AgentID = %q, want %q", n.AgentID, childID)
	}
	if n.Status != string(SubagentCompleted) {
		t.Errorf("notification Status = %q, want %q", n.Status, SubagentCompleted)
	}
	if want := encodeRef("", childID); n.TranscriptRef != want {
		t.Errorf("notification TranscriptRef = %q, want %q", n.TranscriptRef, want)
	}
	if n.TurnsUsed < 0 {
		t.Errorf("notification TurnsUsed = %d, want >= 0", n.TurnsUsed)
	}

	// Armed at most once per run: the queue is now empty.
	if again := sess.drainNotifications(); len(again) != 0 {
		t.Fatalf("second drainNotifications = %d entries, want 0 (armed once)", len(again))
	}
}

// requestsContain reports whether any text content part across any recorded
// request message contains every wanted substring.
func requestsContain(reqs []llm.Request, wants ...string) bool {
	for _, r := range reqs {
		for _, m := range r.Messages {
			text := m.Text()
			all := true
			for _, w := range wants {
				if !strings.Contains(text, w) {
					all = false
					break
				}
			}
			if all {
				return true
			}
		}
	}
	return false
}

// TestNotificationTurn_DrivesModelRequestWithReminder proves that a notification
// turn drains the queue, frames the entry as a TurnSteering reminder, and DRIVES
// A REAL MODEL REQUEST carrying that reminder this turn — not merely an append to
// s.history that the model never sees (the rejected v4). The load-bearing
// assertion is (a): the fake adapter recorded a request whose message history
// contains the "<subagent-notification ...>" block, so the notification reached
// the MODEL.
func TestNotificationTurn_DrivesModelRequestWithReminder(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("ack") },
		},
	}
	c.Register(adapter)
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// Back the notification with a real tracked, unconsumed terminal child so the
	// suppress-at-drain filter (Task 4) treats it as deliverable. Without a backing
	// record the manager get returns nil and the entry is dropped as GC-reclaimed.
	trackSyntheticChild(t, sess, "01CHILD", SubagentCompleted, false, false, time.Now(), false)
	sess.enqueueNotification(subagentNotification{
		AgentID: "01CHILD",
		Status:  "completed", TurnsUsed: 4,
		TranscriptRef: "local:01CHILD",
	})

	sess.mu.Lock()
	turnsBefore := sess.turns
	sess.mu.Unlock()

	if _, err := sess.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("ProcessInputKind(EntryNotification): %v", err)
	}

	// (a) The model WAS called — the anti-v4 guard. A tail-append-then-idle would
	// make zero requests.
	reqs := adapter.Requests()
	if len(reqs) == 0 {
		t.Fatal("notification turn made no model request; the notification never reached the model (v4 regression)")
	}
	// (b) The recorded request's message history carries the notification block.
	if !requestsContain(reqs, "<subagent-notification", "01CHILD") {
		t.Fatalf("model request history did not contain the <subagent-notification ...> block for 01CHILD")
	}

	// (c) A notification turn is NOT a user turn: s.turns must not increment.
	sess.mu.Lock()
	turnsAfter := sess.turns
	sess.mu.Unlock()
	if turnsAfter != turnsBefore {
		t.Errorf("s.turns = %d after notification turn, want %d (notification is not a user turn)", turnsAfter, turnsBefore)
	}

	// (d) The reminder was appended as TurnSteering, never as a user bubble.
	sess.mu.Lock()
	var sawSteering bool
	for _, tn := range sess.history {
		if tn.Kind == schema.TurnUserInput && strings.Contains(tn.Message.Text(), "<subagent-notification") {
			sess.mu.Unlock()
			t.Fatal("notification reminder appended as TurnUserInput (user bubble); want TurnSteering")
		}
		if tn.Kind == schema.TurnSteering && strings.Contains(tn.Message.Text(), "<subagent-notification") {
			sawSteering = true
		}
	}
	sess.mu.Unlock()
	if !sawSteering {
		t.Fatal("no TurnSteering entry carrying the <subagent-notification ...> block was appended to history")
	}
}

// TestNotification_EmptyNoOpDoesNotSuppressNextTurnEnd proves that an empty
// notification no-op does NOT poison sessionEndEmitted for a QUEUED user message
// that the same ProcessInputKind call picks up and runs. When the notification
// queue is empty but a user message is queued, the drain-loop must reset
// sessionEndEmitted before running the queued turn so its idle tail can fire a
// legitimate SESSION_END{input_complete}.
//
// Without the fix: acceptNotificationInput sets sessionEndEmitted=true (correct
// for the notification-only case), then the popQueueHead pickup continues WITHOUT
// resetting the flag, the user turn runs, but the idle tail sees the stale true
// and suppresses the SESSION_END.  The session emits zero ends, causing the hub's
// SetProcessing(false) to be skipped and leaving the session stuck.
func TestNotification_EmptyNoOpDoesNotSuppressNextTurnEnd(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("queued reply") },
		},
	}
	c.Register(adapter)
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	collect := drainEvents(sess)

	// No enqueueNotification: the notification queue is empty.
	// Enqueue a real user message so popQueueHead picks it up after the no-op.
	if err := sess.Enqueue(context.Background(), "queued user message"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	if _, err := sess.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("ProcessInputKind(EntryNotification): %v", err)
	}

	sess.Close()
	evs := collect()

	var sessionEnds []events.SessionEndData
	for _, ev := range evs {
		if ev.Kind != events.EventSessionEnd {
			continue
		}
		if d, ok := ev.Data.(events.SessionEndData); ok && d.Reason == "input_complete" {
			sessionEnds = append(sessionEnds, d)
		}
	}
	if len(sessionEnds) != 1 {
		t.Fatalf("SESSION_END{input_complete} count = %d, want 1 (queued user turn must emit its own end)", len(sessionEnds))
	}
}

// TestNotificationTurn_EmptyQueueIsNoOp proves that a notification turn with an
// empty queue is a true no-op: it makes NO model request and emits NO phantom
// SESSION_END{input_complete}. The suppression works because acceptNotificationInput
// sets s.sessionEndEmitted=true before returning false, so the drain loop's idle
// tail skips the SESSION_END emit.
func TestNotificationTurn_EmptyQueueIsNoOp(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("should not be called") },
		},
	}
	c.Register(adapter)
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	collect := drainEvents(sess)

	// No enqueueNotification: the queue is empty.
	if _, err := sess.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("ProcessInputKind(EntryNotification): %v", err)
	}

	if got := len(adapter.Requests()); got != 0 {
		t.Fatalf("empty-queue notification turn made %d model request(s), want 0", got)
	}

	sess.Close()
	evs := collect()
	for _, ev := range evs {
		if ev.Kind != events.EventSessionEnd {
			continue
		}
		d, ok := ev.Data.(events.SessionEndData)
		if ok && d.Reason == "input_complete" {
			t.Fatalf("empty-queue notification turn emitted a phantom SESSION_END{input_complete}; want none")
		}
	}
}

// TestNotification_InterleavesWithActiveGoal proves that a pending notification
// seeded mid-chain interleaves between goal continuations: it runs as an
// EntryNotification turn AFTER the just-finished continuation folds at its own gate
// and BEFORE the next (deferred) continuation runs inline, the reminder reaches the
// model, and the notification turn does NOT perturb the goal's iteration/no-progress
// accounting. With the fold-then-interleave-then-continue-inline fix the whole goal
// completes within the single ProcessInput call (continuations run inline, no
// kickFunc re-feed needed).
//
// It drives the real ProcessInputKind drain loop with a fake model scripted so:
//   - turn 1 (user "begin"): mutate (progress) then end. The gate (ranKind != a
//     continuation) resumes the goal and DEFERS continuation #1 without folding the
//     user turn into the streak — Iterations stays 0. No notification pending yet,
//     so continuation #1 runs inline.
//   - turn 2 (continuation #1): mutate (progress) then end, AND enqueue a
//     notification during the turn. At the tail the gate folds continuation #1
//     FIRST (Iterations -> 1) and defers continuation #2; THEN the notification peek
//     interleaves an EntryNotification turn ahead of continuation #2.
//   - turn 3 (notification): drains the queue, frames the <subagent-notification>
//     reminder, drives a real model request. Its step snapshots the goal counters
//     (Iterations already 1, folded at continuation #1's own gate); because
//     ranKind==EntryNotification short-circuits the gate, no RecordContinuation runs
//     and the counters are unchanged across it. The deferred continuation #2 then
//     runs inline.
//   - continuation #2: mutate (progress) then end (Iterations -> 2), defer
//     continuation #3, run it inline.
//   - continuation #3: declare the goal complete; the gate sees the terminal status
//     and stops, emitting EventGoalEnded{complete}.
//
// Load-bearing assertions: a model request carries the <subagent-notification>
// block (the reminder interleaved into the chain); the goal's Iterations and
// NoProgressStreak are identical at the start of the notification turn and at the
// start of the deferred continuation that runs right after it (the notification
// turn itself neither advanced nor terminated the goal); the goal completes once
// (EventGoalEnded{complete}) within the single call.
func TestNotification_InterleavesWithActiveGoal(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	var snapInNotif goalCounters
	var snapCaptured bool
	var snapAfterNotifTurn goalCounters
	var snapAfterCaptured bool

	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// Turn 1 (user "begin"), round 0: mutate (progress).
			func(req llm.Request) llm.Response {
				return toolCallResponse(writeFileCall("u1", "u.txt", "user work"))
			},
			// Turn 1, round 1: end. Gate resumes the goal, defers continuation #1.
			func(req llm.Request) llm.Response { return finalResponse("user turn done") },
			// Turn 2 (continuation #1), round 0: mutate (progress).
			func(req llm.Request) llm.Response {
				return toolCallResponse(writeFileCall("c1", "c.txt", "cont work"))
			},
			// Turn 2, round 1: end. (Wrapped below to enqueue a notification first, so
			// the tail interleaves a notification turn after continuation #1's fold.)
			func(req llm.Request) llm.Response { return finalResponse("cont 1 done") },
			// Turn 3 (notification), round 0: the reminder reaches the model here.
			// (Wrapped below to snapshot the goal counters.) End the turn.
			func(req llm.Request) llm.Response { return finalResponse("ack reminder") },
			// Deferred continuation #2, round 0: mutate (progress). (Wrapped below to
			// snapshot the counters right after the notification turn ended.)
			func(req llm.Request) llm.Response {
				return toolCallResponse(writeFileCall("c2", "c2.txt", "resumed work"))
			},
			// Continuation #2, round 1: end. The gate folds this turn (Iterations
			// bumps) and defers continuation #3 within the same call.
			func(req llm.Request) llm.Response { return finalResponse("resumed work done") },
			// Continuation #3, round 0: declare the goal complete.
			func(req llm.Request) llm.Response {
				return toolCallResponse(updateGoalCall("g1", "complete"))
			},
			// Continuation #3, round 1: end. The gate sees the terminal status and
			// stops the loop, emitting EventGoalEnded{complete}.
			func(req llm.Request) llm.Response { return finalResponse("goal achieved") },
		},
	}
	c.Register(adapter)
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	sess.getOrCreateGoalStore().Set("interleave objective", time.Now())

	// Back the interleaved notification with a real tracked, unconsumed terminal
	// child so the suppress-at-drain filter (Task 4) treats it as deliverable.
	trackSyntheticChild(t, sess, "01CHILD", SubagentCompleted, false, false, time.Now(), false)

	// Enqueue the notification as continuation #1 ends (its round-1 step, steps[3]),
	// so after continuation #1 folds at its gate the tail interleaves a notification
	// turn ahead of the deferred continuation #2.
	base3 := adapter.steps[3]
	adapter.steps[3] = func(req llm.Request) llm.Response {
		sess.enqueueNotification(subagentNotification{
			AgentID: "01CHILD",
			Status:  "completed", TurnsUsed: 2,
			TranscriptRef: "local:01CHILD",
		})
		return base3(req)
	}
	// Snapshot the goal counters from inside the notification turn (steps[4]):
	// continuation #1 has already folded at its own gate, so Iterations == 1 here.
	base4 := adapter.steps[4]
	adapter.steps[4] = func(req llm.Request) llm.Response {
		snapInNotif = readGoalCounters(t, sess)
		snapCaptured = true
		return base4(req)
	}
	// Snapshot the counters at the START of the deferred continuation #2 (steps[5]),
	// i.e. right after the notification turn ended but before continuation #2 folds.
	// The notification turn must have folded nothing, so this equals snapInNotif.
	base5 := adapter.steps[5]
	adapter.steps[5] = func(req llm.Request) llm.Response {
		snapAfterNotifTurn = readGoalCounters(t, sess)
		snapAfterCaptured = true
		return base5(req)
	}

	collect := drainEvents(sess)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "begin", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	// The notification request carries the reminder block — it reached the model,
	// interleaved into the chain rather than waiting for the goal to finish.
	if !requestsContain(adapter.Requests(), "<subagent-notification", "01CHILD") {
		t.Fatal("model never saw the <subagent-notification ...> block; the notification did not interleave into the chain")
	}
	if !snapCaptured {
		t.Fatal("the notification turn's adapter step never ran; an EntryNotification turn did not interleave between continuations")
	}
	if !snapAfterCaptured {
		t.Fatal("the deferred continuation #2 step never ran; the goal did not resume inline after the notification turn")
	}

	// Fold-at-own-gate: continuation #1 folded BEFORE the notification turn, so
	// Iterations is already 1 during the notification turn.
	if snapInNotif.iterations != 1 {
		t.Fatalf("Iterations during the notification turn = %d, want 1 (continuation #1 folds at its own gate before the notification interleaves)", snapInNotif.iterations)
	}

	// The notification turn itself must NOT perturb goal accounting: the snapshot
	// taken inside the notification turn must equal the snapshot at the start of the
	// deferred continuation that runs right after it. If the gate were (mis-)armed
	// for EntryNotification, RecordContinuation would bump Iterations/streak here.
	if snapInNotif != snapAfterNotifTurn {
		t.Fatalf("goal accounting changed across the notification turn: during=%+v after=%+v (the notification turn must not advance the goal)", snapInNotif, snapAfterNotifTurn)
	}

	sess.Close()
	allEvs := collect()

	// All three continuations ran (inline): the user turn deferred #1, #1 folded and
	// deferred #2, #2 folded and deferred #3, #3 completed the goal.
	if got := countGoalContinuations(allEvs); got != 3 {
		t.Fatalf("count(EventGoalContinuation) = %d, want 3 (every continuation runs inline within the single call)", got)
	}
	if got := countGoalEnded(allEvs); got != 1 {
		t.Fatalf("count(EventGoalEnded) over the whole run = %d, want 1 (the goal completes once)", got)
	}
	if d := lastGoalEnded(t, allEvs); d.Status != "complete" {
		t.Fatalf("EventGoalEnded.Status = %q, want complete (the goal ran to completion within the call)", d.Status)
	}
}

// sustainedNotificationAdapter models the central workload of the subagent
// control plane: a goal whose strategy spawns a subagent every work turn. Each
// work turn (the user's "begin" and every goal continuation) makes NO mutating
// tool call — it just ends the turn — and arms one subagent-completion
// notification, so a notification is pending at the tail of every continuation.
// A notification turn (detected by the <subagent-notification ...> reminder being
// the most recent message in the request) does NOT re-arm, so notification turns
// do not chain into one another.
//
// callCeiling is a fail-loud guard: the breaker fires after a bounded number of
// continuations (goal.NeverProgressedLimit, since no turn ever progresses), so a
// correct loop completes in well under the ceiling. The PRE-FIX loop — where the
// notification peek preempts the gate every round so RecordContinuation never
// runs — loops forever; the ceiling turns that hang into a prompt error so the
// test fails fast instead of timing out.
type sustainedNotificationAdapter struct {
	name string
	sess *Session // armed lazily by the test after NewSession

	mu    sync.Mutex
	calls int
	ceil  int
}

var errSustainedNotificationCeiling = errors.New("sustainedNotificationAdapter: call ceiling exceeded (goal loop did not terminate)")

func (a *sustainedNotificationAdapter) Name() string { return a.name }

func (a *sustainedNotificationAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	_ = ctx
	a.mu.Lock()
	a.calls++
	n := a.calls
	a.mu.Unlock()
	if a.ceil > 0 && n > a.ceil {
		return llm.Response{}, errSustainedNotificationCeiling
	}
	// Re-arm a notification on every work turn (the user turn and every goal
	// continuation), but never on a notification turn — otherwise notification
	// turns would chain endlessly and the deferred continuation would never run.
	if a.sess != nil && !lastMessageIsNotification(req) {
		a.sess.enqueueNotification(subagentNotification{
			AgentID: "01CHILD",
			Status:  "completed", TurnsUsed: 1,
			TranscriptRef: "local:01CHILD",
		})
	}
	resp := finalResponse("work turn (no progress)")
	resp.Provider = a.name
	if resp.Model == "" {
		resp.Model = req.Model
	}
	return resp, nil
}

func (a *sustainedNotificationAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	_ = ctx
	_ = req
	return nil, llm.ErrStreamUnsupported
}

// lastMessageIsNotification reports whether the last text-bearing message in the
// request is the <subagent-notification ...> reminder — i.e. this model call is a
// notification turn. acceptNotificationInput appends that reminder as the final
// steering turn before the request, so it is the most recent message text.
func lastMessageIsNotification(req llm.Request) bool {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if t := req.Messages[i].Text(); strings.TrimSpace(t) != "" {
			return strings.Contains(t, "<subagent-notification")
		}
	}
	return false
}

// lastTextMessage returns the text of the last text-bearing message in the request
// (the most recent message the model sees), or "" if none. The continuation prompt
// driving a turn is appended as the final steering message, so this isolates it from
// the historical turns that precede it.
func lastTextMessage(req llm.Request) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if t := req.Messages[i].Text(); strings.TrimSpace(t) != "" {
			return t
		}
	}
	return ""
}

// TestNotification_BreakerFiresUnderSustainedNotifications is the CRITICAL
// regression guard for the notification/goal interleave. The goal engine's
// no-progress breaker is its ONLY automatic stop (there is no iteration cap), so
// a continuation that is preempted by a notification before the gate runs never
// folds its progress signal and the breaker never accrues.
//
// It drives an active goal where every work turn makes no progress AND arms a
// notification before its tail (sustainedNotificationAdapter). With the fix — the
// gate folds the just-finished continuation BEFORE the notification peek — the
// breaker accrues one no-progress turn per continuation and FIRES at
// goal.NeverProgressedLimit, blocking the goal with reason "no progress".
//
// PRE-FIX this FAILS: the notification peek preempts the gate every round,
// RecordContinuation never runs, NoProgressStreak stays pinned at 0, the goal
// never blocks, and the loop runs until the adapter's call ceiling trips
// (surfaced as a prompt error / non-blocked status). This is the load-bearing
// "goal loops forever under sustained notifications" guard.
func TestNotification_BreakerFiresUnderSustainedNotifications(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	adapter := &sustainedNotificationAdapter{name: "openai", ceil: 60}
	c.Register(adapter)
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	adapter.sess = sess
	collect := drainEvents(sess)

	sess.getOrCreateGoalStore().Set("never-progress objective", time.Now())

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// The call may end in nil (breaker fired, idle) — the goal status is the
	// authoritative assertion, not the call's error, so we don't fail on err here.
	_, _ = sess.ProcessInput(ctx, "begin", nil)

	snap, ok := sess.getOrCreateGoalStore().Snapshot()
	if !ok {
		t.Fatal("goal snapshot missing after run")
	}
	if snap.Status != goal.StatusBlocked {
		t.Fatalf("goal status = %q (iterations=%d, streak=%d), want %q — the no-progress breaker did not fire; the goal looped under sustained notifications",
			snap.Status, snap.Iterations, snap.NoProgressStreak, goal.StatusBlocked)
	}
	if snap.StopReason != "no progress" {
		t.Fatalf("goal stop reason = %q, want %q (the breaker, not an error/ceiling, must terminate the goal)", snap.StopReason, "no progress")
	}
	// The breaker fires at NeverProgressedLimit (the goal never progresses); it must
	// not run unbounded. Iterations counts folded continuations, one per breaker step.
	if snap.Iterations != goal.NeverProgressedLimit {
		t.Fatalf("goal Iterations = %d, want %d (one fold per continuation up to the breaker)", snap.Iterations, goal.NeverProgressedLimit)
	}

	sess.Close()
	evs := collect()
	if got := countGoalEnded(evs); got != 1 {
		t.Fatalf("count(EventGoalEnded) = %d, want 1 (the breaker emits exactly one terminal report)", got)
	}
	if d := lastGoalEnded(t, evs); d.Status != string(goal.StatusBlocked) {
		t.Fatalf("EventGoalEnded.Status = %q, want %q", d.Status, goal.StatusBlocked)
	}
}

// TestNotification_GoalContinuesInlineWithoutKickFunc is the IMPORTANT regression
// guard for the kickFunc-nil stranding bug. A one-shot `serf run` (cmd/serf) never
// wires a kickFunc, yet a restored session can carry an active goal and the model
// can spawn a subagent (depth 1 by default). If a notification preempts the gate
// and the design relied on settleGoalOnIdle to re-kick the goal, settleGoalOnIdle
// is a no-op with kickFunc==nil and the goal is stranded active, its continuation
// lost.
//
// With the fix the deferred continuation runs INLINE (via continue, not via the
// idle kick), so the goal advances and completes within the single
// ProcessInputKind call with NO kickFunc wired.
//
// PRE-FIX this FAILS: the notification preempts the gate, settleGoalOnIdle no-ops
// (kickFunc==nil), the call returns with the goal still active and Iterations 0.
func TestNotification_GoalContinuesInlineWithoutKickFunc(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// Turn 1 (user "begin"): mutate (progress), then end. The gate resumes
			// the goal and arms continuation #1 (without folding the user turn).
			func(req llm.Request) llm.Response {
				return toolCallResponse(writeFileCall("u1", "u.txt", "user work"))
			},
			func(req llm.Request) llm.Response { return finalResponse("user turn done") },
			// Turn 2 (continuation #1): mutate (progress), then end — AND arm a
			// notification (wrapped below) so it preempts at this continuation's tail.
			func(req llm.Request) llm.Response {
				return toolCallResponse(writeFileCall("c1", "c.txt", "cont work"))
			},
			func(req llm.Request) llm.Response { return finalResponse("cont 1 done") },
			// Turn 3 (notification): ack the reminder and end.
			func(req llm.Request) llm.Response { return finalResponse("ack reminder") },
			// Turn 4 (deferred continuation #2, run INLINE — no kickFunc): declare complete.
			func(req llm.Request) llm.Response {
				return toolCallResponse(updateGoalCall("g1", "complete"))
			},
			func(req llm.Request) llm.Response { return finalResponse("goal achieved") },
		},
	}
	c.Register(adapter)
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	// Deliberately do NOT call SetKickFunc: kickFunc stays nil, modelling `serf run`.
	collect := drainEvents(sess)

	sess.getOrCreateGoalStore().Set("inline objective", time.Now())

	// Arm the notification as continuation #1 ends (its round-1 step, steps[3]) so the
	// tail-drain peek preempts the next continuation.
	base3 := adapter.steps[3]
	adapter.steps[3] = func(req llm.Request) llm.Response {
		sess.enqueueNotification(subagentNotification{
			AgentID: "01CHILD",
			Status:  "completed", TurnsUsed: 2,
			TranscriptRef: "local:01CHILD",
		})
		return base3(req)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "begin", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	// The goal must have ADVANCED and COMPLETED within this single call — the
	// deferred continuation ran inline, not via a (nil) kick. PRE-FIX it is stranded
	// active with Iterations 0.
	snap, ok := sess.getOrCreateGoalStore().Snapshot()
	if !ok {
		t.Fatal("goal snapshot missing after run")
	}
	if snap.Status != goal.StatusComplete {
		t.Fatalf("goal status = %q (iterations=%d), want %q — the goal was stranded; the deferred continuation did not run inline without a kickFunc",
			snap.Status, snap.Iterations, goal.StatusComplete)
	}
	if snap.Iterations < 1 {
		t.Fatalf("goal Iterations = %d, want >= 1 (the deferred continuation must have folded inline)", snap.Iterations)
	}

	sess.Close()
	evs := collect()
	if got := countGoalEnded(evs); got != 1 {
		t.Fatalf("count(EventGoalEnded) = %d, want 1", got)
	}
	if d := lastGoalEnded(t, evs); d.Status != "complete" {
		t.Fatalf("EventGoalEnded.Status = %q, want complete", d.Status)
	}
}

// TestNotification_GoalClearedDuringInterleaveStops proves that clearing the goal
// DURING an interleaved notification turn drops the gate-time deferred continuation
// rather than running it against a goal that no longer exists. The gate folds
// continuation #1 and captures its next continuation as a string; the notification
// then interleaves; if the user runs `/goal clear` (ClearGoal) during that
// notification turn, the cached render is stale. The fix re-validates the goal at
// the inline-run site: with the goal cleared, the deferred continuation is dropped
// and the session idles.
//
// PRE-FIX this FAILS: the inline site runs the cached string blindly, so one stale
// continuation runs against the cleared goal (countGoalContinuations == 2). The
// no-notification path does not have this bug because its gate re-reads the (empty)
// store; this test closes the gap the notification interleave opened.
func TestNotification_GoalClearedDuringInterleaveStops(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// Turn 1 (user "begin"): mutate (progress), then end. The gate resumes
			// the goal and defers continuation #1.
			func(req llm.Request) llm.Response {
				return toolCallResponse(writeFileCall("u1", "u.txt", "user work"))
			},
			func(req llm.Request) llm.Response { return finalResponse("user turn done") },
			// Turn 2 (continuation #1): mutate (progress), then end — AND arm a
			// notification (wrapped below) so it interleaves at this continuation's tail.
			func(req llm.Request) llm.Response {
				return toolCallResponse(writeFileCall("c1", "c.txt", "cont work"))
			},
			func(req llm.Request) llm.Response { return finalResponse("cont 1 done") },
			// Turn 3 (notification): clear the goal (wrapped below), then ack and end.
			func(req llm.Request) llm.Response { return finalResponse("ack reminder") },
			// PRE-FIX ONLY: the stale continuation #2 would run here and end. Post-fix
			// it is dropped, so this step never runs.
			func(req llm.Request) llm.Response { return finalResponse("stale cont (should not run post-fix)") },
		},
	}
	c.Register(adapter)
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	collect := drainEvents(sess)

	sess.getOrCreateGoalStore().Set("clear-me objective", time.Now())

	// Back the interleaved notification with a real tracked, unconsumed terminal
	// child so the suppress-at-drain filter (Task 4) treats it as deliverable.
	trackSyntheticChild(t, sess, "01CHILD", SubagentCompleted, false, false, time.Now(), false)

	// Arm the notification as continuation #1 ends (steps[3]) so it interleaves ahead
	// of the gate-time deferred continuation #2.
	base3 := adapter.steps[3]
	adapter.steps[3] = func(req llm.Request) llm.Response {
		sess.enqueueNotification(subagentNotification{
			AgentID: "01CHILD",
			Status:  "completed", TurnsUsed: 2,
			TranscriptRef: "local:01CHILD",
		})
		return base3(req)
	}
	// During the notification turn (steps[4]), clear the goal — modelling `/goal clear`
	// landing while the notification interleaves. This makes the gate-time deferred
	// continuation string stale.
	base4 := adapter.steps[4]
	adapter.steps[4] = func(req llm.Request) llm.Response {
		if lastMessageIsNotification(req) {
			sess.ClearGoal()
		}
		return base4(req)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "begin", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	// The goal is gone (cleared), so no further continuation may run after the
	// notification: only continuation #1 ran. Pre-fix the stale continuation #2 runs,
	// giving 2.
	sess.Close()
	evs := collect()
	if got := countGoalContinuations(evs); got != 1 {
		t.Fatalf("count(EventGoalContinuation) = %d, want 1 (the cleared goal must not run a stale continuation after the notification)", got)
	}
	// A cleared goal has no terminal report: it was removed, not completed/blocked.
	if got := countGoalEnded(evs); got != 0 {
		t.Fatalf("count(EventGoalEnded) = %d, want 0 (a cleared goal emits no terminal report)", got)
	}
	if _, ok := sess.getOrCreateGoalStore().Snapshot(); ok {
		t.Fatal("goal snapshot present after ClearGoal; want absent")
	}
}

// TestNotification_GoalRetargetedDuringInterleaveUsesNewObjective proves that
// retargeting the goal DURING an interleaved notification turn makes the continuation
// that runs after the notification pursue the NEW objective, not the abandoned OLD
// one. The gate captures continuation #1's next continuation as a render of the OLD
// objective; the notification interleaves; `/goal <new>` (SetGoal) lands during the
// notification turn. The fix re-validates at the inline site and renders the CURRENT
// objective, so the post-notification continuation carries NEW.
//
// PRE-FIX this FAILS: the inline site runs the cached OLD render, so the continuation
// after the notification pursues the abandoned OLD objective.
func TestNotification_GoalRetargetedDuringInterleaveUsesNewObjective(t *testing.T) {
	const (
		oldObjective = "OLD-abandoned-objective-zzz"
		newObjective = "NEW-retargeted-objective-qqq"
	)
	dir := t.TempDir()
	c := llm.NewClient()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// Turn 1 (user "begin"): mutate (progress), then end. Gate defers cont #1.
			func(req llm.Request) llm.Response {
				return toolCallResponse(writeFileCall("u1", "u.txt", "user work"))
			},
			func(req llm.Request) llm.Response { return finalResponse("user turn done") },
			// Turn 2 (continuation #1, OLD objective): mutate, then end — AND arm a
			// notification (wrapped below).
			func(req llm.Request) llm.Response {
				return toolCallResponse(writeFileCall("c1", "c.txt", "cont work"))
			},
			func(req llm.Request) llm.Response { return finalResponse("cont 1 done") },
			// Turn 3 (notification): retarget to NEW (wrapped below), then ack and end.
			func(req llm.Request) llm.Response { return finalResponse("ack reminder") },
			// Turn 4 (continuation after the notification): its request prompt is
			// captured below; declare the goal complete so the loop stops.
			func(req llm.Request) llm.Response {
				return toolCallResponse(updateGoalCall("g1", "complete"))
			},
			func(req llm.Request) llm.Response { return finalResponse("goal achieved") },
		},
	}
	c.Register(adapter)
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	sess.getOrCreateGoalStore().Set(oldObjective, time.Now())

	// Back the interleaved notification with a real tracked, unconsumed terminal
	// child so the suppress-at-drain filter (Task 4) treats it as deliverable.
	trackSyntheticChild(t, sess, "01CHILD", SubagentCompleted, false, false, time.Now(), false)

	// Arm the notification as continuation #1 ends (steps[3]).
	base3 := adapter.steps[3]
	adapter.steps[3] = func(req llm.Request) llm.Response {
		sess.enqueueNotification(subagentNotification{
			AgentID: "01CHILD",
			Status:  "completed", TurnsUsed: 2,
			TranscriptRef: "local:01CHILD",
		})
		return base3(req)
	}
	// During the notification turn (steps[4]), retarget the goal to NEW — modelling
	// `/goal <new>` landing while the notification interleaves. This makes the
	// gate-time deferred render (of OLD) stale.
	base4 := adapter.steps[4]
	adapter.steps[4] = func(req llm.Request) llm.Response {
		if lastMessageIsNotification(req) {
			if _, err := sess.SetGoal(context.Background(), newObjective); err != nil {
				t.Errorf("SetGoal(new) during notification turn: %v", err)
			}
		}
		return base4(req)
	}
	// Capture the request that drives the post-notification continuation (steps[5]):
	// its history must carry the NEW objective render, not OLD.
	var postNotifReq llm.Request
	var postNotifCaptured bool
	base5 := adapter.steps[5]
	adapter.steps[5] = func(req llm.Request) llm.Response {
		postNotifReq = req
		postNotifCaptured = true
		return base5(req)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "begin", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	if !postNotifCaptured {
		t.Fatal("the post-notification continuation step never ran; the retargeted goal did not resume")
	}
	// The continuation prompt DRIVING this turn is the most recent text-bearing
	// message (acceptContinuationInput appends the render as the final steering turn).
	// It must pursue the NEW objective, not the abandoned OLD one. We assert on the
	// last message specifically rather than the whole history, because the OLD render
	// from continuation #1 legitimately lingers in the transcript as a prior turn.
	lastMsg := lastTextMessage(postNotifReq)
	if !strings.Contains(lastMsg, newObjective) {
		t.Fatalf("post-notification continuation prompt did not carry the NEW objective %q; the stale OLD render ran. last message:\n%s", newObjective, lastMsg)
	}
	if strings.Contains(lastMsg, oldObjective) {
		t.Fatalf("post-notification continuation prompt carried the abandoned OLD objective %q; a stale continuation ran. last message:\n%s", oldObjective, lastMsg)
	}

	snap, ok := sess.getOrCreateGoalStore().Snapshot()
	if !ok {
		t.Fatal("goal snapshot missing after run")
	}
	if snap.Objective != newObjective {
		t.Fatalf("goal objective = %q after run, want %q", snap.Objective, newObjective)
	}
}

// assertNotificationSuppressed runs a single EntryNotification turn and asserts it
// was a true no-op: the fake adapter recorded zero model requests and no
// SESSION_END{input_complete} was emitted. This is the observable signature of a
// notification whose every queued entry was filtered out at drain — identical to
// the empty-queue no-op path. The session is consumed (closed) so its events can
// be collected.
func assertNotificationSuppressed(t *testing.T, sess *Session, adapter *fakeAdapter, collect func() []events.SessionEvent) {
	t.Helper()
	// Count requests made before the notification turn (a spawned child's own run
	// records requests on the shared adapter) so the assertion is "the notification
	// turn added zero", not "the adapter is pristine".
	before := len(adapter.Requests())
	if _, err := sess.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("ProcessInputKind(EntryNotification): %v", err)
	}
	if added := len(adapter.Requests()) - before; added != 0 {
		t.Fatalf("suppressed notification turn made %d model request(s), want 0 (every entry should have been filtered at drain)", added)
	}
	sess.Close()
	for _, ev := range collect() {
		if ev.Kind != events.EventSessionEnd {
			continue
		}
		if d, ok := ev.Data.(events.SessionEndData); ok && d.Reason == "input_complete" {
			t.Fatalf("suppressed notification turn emitted a phantom SESSION_END{input_complete}; want none")
		}
	}
}

// TestNotification_SuppressedWhenConsumed proves a notification armed at terminal
// run end is DROPPED at drain when the child's result was already consumed by a
// blocking wait before the notification turn runs. spawnCompletedChild spawns a
// non-blocking child that completes (arming one notification) and then wait()s on
// it (setting resultConsumed=true). The subsequent notification turn must find
// nothing to render and make NO model request.
//
// Load-bearing: the filter keys on resultConsumed. WITHOUT it, the consumed
// entry would still render a <subagent-notification> reminder and drive a real
// model request — so the len(Requests())==0 assertion is what proves the
// consumed entry was suppressed rather than delivered.
func TestNotification_SuppressedWhenConsumed(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	// One step for the child's single run. The notification turn must make NO
	// further request (so no extra step is needed); if the filter regressed and
	// the turn DID call the model, the adapter falls back to its default "done"
	// response and the len==0 assertion below fails loudly.
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("child done") },
		},
	}
	c.Register(adapter)
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	collect := drainEvents(sess)

	// Spawn + wait: arms a notification AND consumes the result (resultConsumed=true).
	childID := spawnCompletedChild(t, sess, "sc", "do the thing")

	// Sanity: the entry was armed and its backing record is consumed, so the filter
	// must drop it. (We do not drain here — that is the turn's job below.)
	if got := sess.peekNotifications(); got != 1 {
		t.Fatalf("peekNotifications after spawn+wait = %d, want 1 (the terminal run armed one)", got)
	}
	if sub := sess.subagents.get(childID); sub == nil {
		t.Fatalf("child %s not tracked", childID)
	} else {
		sub.mu.Lock()
		consumed := sub.resultConsumed
		sub.mu.Unlock()
		if !consumed {
			t.Fatalf("child %s resultConsumed = false after wait, want true", childID)
		}
	}

	assertNotificationSuppressed(t, sess, adapter, collect)
}

// TestNotification_SuppressedWhenClosedOrAbsent proves a notification is dropped
// at drain when, by delivery time, the child's record is closed (close_agent ran,
// record retained as closed) or absent (GC-reclaimed → manager get returns nil).
// Both are modelled with synthetic tracked records / an untracked agent_id so the
// drain filter is exercised deterministically without spawn-goroutine timing.
func TestNotification_SuppressedWhenClosedOrAbsent(t *testing.T) {
	t.Run("closed", func(t *testing.T) {
		dir := t.TempDir()
		c := llm.NewClient()
		adapter := &fakeAdapter{
			name: "openai",
			steps: []func(req llm.Request) llm.Response{
				func(req llm.Request) llm.Response { return finalResponse("should not be called") },
			},
		}
		c.Register(adapter)
		sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
			MaxSubagentDepth: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		collect := drainEvents(sess)

		// A retained closed record: close_agent leaves closed=true, record tracked.
		trackSyntheticChild(t, sess, "01CLOSED", SubagentCompleted, true /*closed*/, false, time.Now(), false)
		sess.enqueueNotification(subagentNotification{
			AgentID: "01CLOSED",
			Status:  "completed", TurnsUsed: 3,
			TranscriptRef: "local:01CLOSED",
		})

		assertNotificationSuppressed(t, sess, adapter, collect)
	})

	t.Run("absent", func(t *testing.T) {
		dir := t.TempDir()
		c := llm.NewClient()
		adapter := &fakeAdapter{
			name: "openai",
			steps: []func(req llm.Request) llm.Response{
				func(req llm.Request) llm.Response { return finalResponse("should not be called") },
			},
		}
		c.Register(adapter)
		sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
			MaxSubagentDepth: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		collect := drainEvents(sess)

		// No tracking: the manager get returns nil for this agent_id (GC-reclaimed).
		sess.enqueueNotification(subagentNotification{
			AgentID: "01GONE",
			Status:  "completed", TurnsUsed: 5,
			TranscriptRef: "local:01GONE",
		})

		assertNotificationSuppressed(t, sess, adapter, collect)
	})
}

// TestNotification_DeliveredWhenUnconsumedTerminal is the POSITIVE control: the
// filter must NOT over-suppress. A still-tracked, terminal (completed),
// UNconsumed child's notification IS delivered — the notification turn drives a
// real model request carrying the <subagent-notification> reminder. This proves
// the drain filter keeps survivors rather than dropping everything.
func TestNotification_DeliveredWhenUnconsumedTerminal(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("ack") },
		},
	}
	c.Register(adapter)
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// A terminal, unconsumed, tracked child: the filter must KEEP its notification.
	trackSyntheticChild(t, sess, "01LIVE", SubagentCompleted, false, false, time.Now(), false)
	sess.enqueueNotification(subagentNotification{
		AgentID: "01LIVE",
		Status:  "completed", TurnsUsed: 2,
		TranscriptRef: "local:01LIVE",
	})

	if _, err := sess.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("ProcessInputKind(EntryNotification): %v", err)
	}

	reqs := adapter.Requests()
	if len(reqs) == 0 {
		t.Fatal("unconsumed-terminal notification made no model request; the filter over-suppressed a deliverable entry")
	}
	if !requestsContain(reqs, "<subagent-notification", "01LIVE") {
		t.Fatal("model request history did not carry the <subagent-notification ...> block for the unconsumed terminal child")
	}
}

// goalCounters is the subset of the goal snapshot whose invariance across a
// notification turn proves the notification did not advance/terminate the goal.
type goalCounters struct {
	iterations       int
	noProgressStreak int
}

func readGoalCounters(t *testing.T, sess *Session) goalCounters {
	t.Helper()
	snap, ok := sess.getOrCreateGoalStore().Snapshot()
	if !ok {
		t.Fatal("goal snapshot missing")
	}
	return goalCounters{iterations: snap.Iterations, noProgressStreak: snap.NoProgressStreak}
}
