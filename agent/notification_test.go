package agent

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
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
