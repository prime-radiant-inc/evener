package agent

import (
	"context"
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
	if n.Reason != string(SubagentCompleted) {
		t.Errorf("notification Reason = %q, want %q", n.Reason, SubagentCompleted)
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

	sess.enqueueNotification(subagentNotification{
		AgentID:       "01CHILD",
		Status:        "completed",
		Reason:        "completed",
		TurnsUsed:     4,
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
// seeded mid-chain preempts the NEXT goal continuation: it runs as an
// EntryNotification turn ahead of the next continuation, the reminder reaches the
// model, the notification turn does NOT perturb the goal's iteration/no-progress
// accounting, and the goal stays active and resumes (to completion) via the normal
// settleGoalOnIdle kick path.
//
// It drives the real ProcessInputKind drain loop with a fake model scripted so:
//   - turn 1 (user "begin"): mutate (progress) then end. The gate (ranKind != a
//     continuation) resumes the goal and arms continuation #1 without folding the
//     user turn into the streak — Iterations stays 0.
//   - turn 2 (continuation #1): mutate (progress) then end, AND enqueue a
//     notification during the turn. At the tail the notification peek runs BEFORE
//     the goal gate, so it preempts: the next iteration is an EntryNotification
//     turn and this continuation's own fold is deferred to the resume (Iterations
//     still 0).
//   - turn 3 (notification): drains the queue, frames the <subagent-notification>
//     reminder, drives a real model request. Its step snapshots the goal counters;
//     because ranKind==EntryNotification short-circuits the gate, no
//     RecordContinuation runs and the counters are unchanged across it.
//     settleGoalOnIdle then kicks the still-active goal (recorded here).
//
// Finally the recorded kick prompt is re-fed as a fresh
// ProcessInputKind(EntryContinuation) — exactly as production's serve loop does —
// which advances the goal (Iterations bumps) and completes it. This proves the
// notification left the goal fully resumable.
//
// Load-bearing assertions: a model request carries the <subagent-notification>
// block (the reminder interleaved into the chain); the goal's Iterations and
// NoProgressStreak are identical at the start of the notification turn and at the
// end of the call (the notification neither advanced nor terminated the goal);
// exactly ONE EventGoalContinuation fired before the notification (continuation #2
// was preempted); the goal is still active afterwards and the kick fired; the
// re-fed continuation advances and completes the goal.
func TestNotification_InterleavesWithActiveGoal(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	var snapInNotif goalCounters
	var snapCaptured bool

	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// Turn 1 (user "begin"), round 0: mutate (progress).
			func(req llm.Request) llm.Response {
				return toolCallResponse(writeFileCall("u1", "u.txt", "user work"))
			},
			// Turn 1, round 1: end. Gate resumes the goal, arms continuation #1.
			func(req llm.Request) llm.Response { return finalResponse("user turn done") },
			// Turn 2 (continuation #1), round 0: mutate (progress).
			func(req llm.Request) llm.Response {
				return toolCallResponse(writeFileCall("c1", "c.txt", "cont work"))
			},
			// Turn 2, round 1: end. (Wrapped below to enqueue a notification first,
			// so the tail-drain peek preempts the next continuation.)
			func(req llm.Request) llm.Response { return finalResponse("cont 1 done") },
			// Turn 3 (notification), round 0: the reminder reaches the model here.
			// (Wrapped below to snapshot the goal counters.) End the turn.
			func(req llm.Request) llm.Response { return finalResponse("ack reminder") },
			// Resumed continuation #2, round 0: mutate (progress) — this is the
			// deferred fold finally being recorded when the goal resumes.
			func(req llm.Request) llm.Response {
				return toolCallResponse(writeFileCall("c2", "c2.txt", "resumed work"))
			},
			// Resumed continuation #2, round 1: end. The gate folds this turn
			// (Iterations bumps) and arms continuation #3 within the same call.
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

	// Record the settleGoalOnIdle kick so the resume path is observable. Production
	// re-feeds this prompt as a fresh ProcessInputKind(EntryContinuation); the test
	// drives that explicitly below.
	var kickMu sync.Mutex
	var kicked []string
	sess.SetKickFunc(func(prompt string) {
		kickMu.Lock()
		kicked = append(kicked, prompt)
		kickMu.Unlock()
	})

	sess.getOrCreateGoalStore().Set("interleave objective", time.Now())

	// Enqueue the notification as continuation #1 ends (its round-1 step, steps[3]),
	// so the tail-drain peek sees a non-empty queue and preempts continuation #2.
	base3 := adapter.steps[3]
	adapter.steps[3] = func(req llm.Request) llm.Response {
		sess.enqueueNotification(subagentNotification{
			AgentID:       "01CHILD",
			Status:        "completed",
			Reason:        "completed",
			TurnsUsed:     2,
			TranscriptRef: "local:01CHILD",
		})
		return base3(req)
	}
	// Snapshot the goal counters from inside the notification turn (steps[4]).
	base4 := adapter.steps[4]
	adapter.steps[4] = func(req llm.Request) llm.Response {
		snapInNotif = readGoalCounters(t, sess)
		snapCaptured = true
		return base4(req)
	}

	// A live event tap that can be snapshotted mid-run (the post-close drainEvents
	// collector blocks until Close, which is too late — the goal must resume first).
	tap := newEventTap(sess)

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
		t.Fatal("the notification turn's adapter step never ran; an EntryNotification turn did not run ahead of the next continuation")
	}

	// The notification turn must NOT perturb goal accounting: the snapshot taken
	// inside the notification turn must equal the snapshot at the end of the call.
	// If the gate were (mis-)armed for EntryNotification, RecordContinuation would
	// bump Iterations or NoProgressStreak here.
	snapAfterNotif := readGoalCounters(t, sess)
	if snapInNotif != snapAfterNotif {
		t.Fatalf("goal accounting changed across the notification turn: during=%+v after=%+v (the notification must not advance the goal)", snapInNotif, snapAfterNotif)
	}

	// Exactly one EventGoalContinuation fired before the notification preempted the
	// chain: continuation #1 started; continuation #2 was preempted by the
	// notification. Snapshotted live (the session is still open; the goal resumes).
	if got := countGoalContinuations(tap.snapshot()); got != 1 {
		t.Fatalf("count(EventGoalContinuation) before resume = %d, want 1 (the notification preempted the next continuation)", got)
	}

	// The goal survived the notification and is resumable: still active, and the
	// settleGoalOnIdle kick fired with a continuation prompt.
	if snap, ok := sess.getOrCreateGoalStore().Snapshot(); !ok || snap.Status != goal.StatusActive {
		t.Fatalf("goal status after notification = %v (ok=%v), want active (the notification must not terminate the goal)", snapStatus(snap, ok), ok)
	}
	kickMu.Lock()
	kickPrompt, kickedOK := "", len(kicked) > 0
	if kickedOK {
		kickPrompt = kicked[len(kicked)-1]
	}
	kickMu.Unlock()
	if !kickedOK {
		t.Fatal("settleGoalOnIdle did not kick the active goal after the notification turn; the goal would be stranded rather than resumed")
	}

	// Re-feed the kicked continuation exactly as production's serve loop does. This
	// resumes the goal: it advances (Iterations bumps off the deferred fold) and
	// completes via update_goal.
	if _, err := sess.ProcessInputKind(ctx, kickPrompt, nil, EntryContinuation); err != nil {
		t.Fatalf("resumed continuation ProcessInputKind(EntryContinuation): %v", err)
	}
	if got := readGoalCounters(t, sess).iterations; got <= snapAfterNotif.iterations {
		t.Fatalf("resumed continuation did not advance the goal: Iterations %d -> %d (want an increase)", snapAfterNotif.iterations, got)
	}

	sess.Close()
	allEvs := tap.collect()
	if got := countGoalEnded(allEvs); got != 1 {
		t.Fatalf("count(EventGoalEnded) over the whole run = %d, want 1 (the goal completes once, after resuming)", got)
	}
	if d := lastGoalEnded(t, allEvs); d.Status != "complete" {
		t.Fatalf("EventGoalEnded.Status = %q, want complete (the goal resumed and finished)", d.Status)
	}
}

// eventTap drains a session's events into a mutex-guarded slice. Unlike
// drainEvents, snapshot() returns the events seen so far WITHOUT waiting for the
// channel to close, so a test can assert on the stream while the session is still
// running (e.g. a goal that must resume after the assertion). collect() blocks
// until the channel closes and returns the full stream.
type eventTap struct {
	mu   sync.Mutex
	evs  []events.SessionEvent
	done chan struct{}
}

func newEventTap(sess *Session) *eventTap {
	tp := &eventTap{done: make(chan struct{})}
	go func() {
		for ev := range sess.Events() {
			tp.mu.Lock()
			tp.evs = append(tp.evs, ev)
			tp.mu.Unlock()
		}
		close(tp.done)
	}()
	return tp
}

func (tp *eventTap) snapshot() []events.SessionEvent {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	return append([]events.SessionEvent(nil), tp.evs...)
}

func (tp *eventTap) collect() []events.SessionEvent {
	<-tp.done
	tp.mu.Lock()
	defer tp.mu.Unlock()
	return append([]events.SessionEvent(nil), tp.evs...)
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

func snapStatus(snap goal.Snapshot, ok bool) string {
	if !ok {
		return "<no goal>"
	}
	return string(snap.Status)
}
