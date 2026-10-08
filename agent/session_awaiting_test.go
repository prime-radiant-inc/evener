package agent

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// newTestSessionForState builds a fresh idle session (no events drained) for
// direct testing of the settle-state helpers, mirroring the construction
// pattern used by session_lifecycle_test.go / session_goal_test.go.
func newTestSessionForState(t *testing.T) *Session {
	t.Helper()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{}})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("test-model")), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

func TestSettleTerminalState(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name                                                             string
		hadOutput, goalKicked, notifsPending, queuePending, childrenLive bool
		want                                                             SessionState
	}{
		{"clean turn with output arms awaiting", true, false, false, false, false, SessionAwaiting},
		{"no user-visible output stays idle", false, false, false, false, false, SessionIdle},
		{"goal kick suppresses", true, true, false, false, false, SessionIdle},
		{"pending notifications suppress", true, false, true, false, false, SessionIdle},
		{"queued input suppresses", true, false, false, true, false, SessionIdle},
		{"live children suppress", true, false, false, false, true, SessionIdle},
		{"all suppressors at once", true, true, true, true, true, SessionIdle},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := settleTerminalState(c.hadOutput, c.goalKicked, c.notifsPending, c.queuePending, c.childrenLive)
			if got != c.want {
				t.Fatalf("settleTerminalState(%v,%v,%v,%v,%v) = %q, want %q",
					c.hadOutput, c.goalKicked, c.notifsPending, c.queuePending, c.childrenLive, got, c.want)
			}
		})
	}
}

func TestSettleGoalOnIdle_ReportsKick(t *testing.T) {
	t.Parallel()
	sess := newTestSessionForState(t)
	// No goal set: settle must report kicked=false.
	if kicked := sess.settleGoalOnIdle(); kicked {
		t.Fatal("settleGoalOnIdle with no goal reported kicked=true")
	}
	// Active goal + wired kick: settle must kick and report it.
	kickCh := make(chan string, 1)
	sess.SetKickFunc(func(p string) { kickCh <- p })
	if _, err := sess.SetGoal(context.Background(), "test objective"); err != nil {
		t.Fatal(err)
	}
	<-kickCh // drain the SetGoal idle-kick itself
	sess.mu.Lock()
	sess.goalInTurn = true // simulate the turn-tail window
	sess.mu.Unlock()
	if kicked := sess.settleGoalOnIdle(); !kicked {
		t.Fatal("settleGoalOnIdle with an active goal did not report kicked=true")
	}
	select {
	case <-kickCh:
	default:
		t.Fatal("settleGoalOnIdle reported kicked but no kick arrived")
	}
}

// endReasonResponse is a turn-ending communicate call that states reason
// (none when reason is empty).
func endReasonResponse(message, reason string) llm.Response {
	args := map[string]any{"message": message}
	if reason != "" {
		args["end_reason"] = reason
	}
	return toolCallResponse(communicateCallArgs("communicate_test_call", args))
}

// A clean completion rests by the reason its communicate gave: awaiting only
// when the agent needs a response, idle for a plain reply and for a wait on
// work. The SessionEnd event carries the same state.
func TestProcessInput_CleanCompletionRestsByEndReason(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		reason string
		want   SessionState
	}{
		{reason: "", want: SessionIdle},
		{reason: "done", want: SessionIdle},
		{reason: "waiting_on_work", want: SessionIdle},
		{reason: "needs_response", want: SessionAwaiting},
	} {
		t.Run("reason="+tc.reason, func(t *testing.T) {
			t.Parallel()
			c := llm.NewClient()
			c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
				func(req llm.Request) llm.Response { return endReasonResponse("done", tc.reason) },
			}})
			sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("test-model")), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
			if err != nil {
				t.Fatal(err)
			}
			eventsPtr, mu, doneCh := collectEvents(sess)
			// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
				t.Fatal(err)
			}
			if got := sess.State(); got != tc.want {
				t.Fatalf("state after clean completion = %q, want %q", got, tc.want)
			}
			sess.Close()
			<-doneCh
			mu.Lock()
			defer mu.Unlock()
			ended := false
			for _, ev := range *eventsPtr {
				if d, ok := ev.Data.(events.SessionEndData); ok && ev.Kind == events.EventSessionEnd && d.Reason == "input_complete" {
					ended = true
					if d.State != string(tc.want) {
						t.Fatalf("SessionEnd.State = %q, want %q", d.State, tc.want)
					}
				}
			}
			if !ended {
				t.Fatal("no input_complete SessionEnd")
			}
		})
	}
}

func TestProcessInput_InterruptStaysIdle(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	blocker := make(chan struct{})
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { <-blocker; return finalResponse("late") },
	}})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("test-model")), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _, _ = sess.ProcessInput(ctx, "hello", nil); close(done) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	close(blocker)
	<-done
	if got := sess.State(); got == SessionAwaiting {
		t.Fatalf("interrupted turn must not arm awaiting; state = %q", got)
	}
}

func TestProcessInput_NextInputClearsAwaiting(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return endReasonResponse("one", "needs_response") },
		func(req llm.Request) llm.Response { return endReasonResponse("two", "needs_response") },
	}})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("test-model")), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "first", nil); err != nil {
		t.Fatal(err)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state = %q, want awaiting before second input", got)
	}
	if _, err := sess.ProcessInput(ctx, "second", nil); err != nil {
		t.Fatal(err)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state after second clean turn = %q, want awaiting again", got)
	}
}

func TestWireState_LiveChildDoesNotMakeIdleParentActive(t *testing.T) {
	t.Parallel()
	parent := newTestSessionForState(t)
	child := newTestSessionForState(t)
	parent.subagents.mu.Lock()
	parent.subagents.subs[child.ID()] = &subagent{id: child.ID(), sess: child}
	parent.subagents.mu.Unlock()

	if got := parent.WireState(); got != string(SessionIdle) {
		t.Fatalf("WireState with only live child = %q, want %q", got, SessionIdle)
	}
	if !parent.autonomyInFlight() {
		t.Fatal("live child must remain autonomy in flight for settle and restore")
	}
}

func TestWireState_PendingParentWorkMakesIdleParentActive(t *testing.T) {
	t.Parallel()
	withNotification := newTestSessionForState(t)
	// Idle with no autonomy: wire state == raw state.
	if got := withNotification.WireState(); got != string(SessionIdle) {
		t.Fatalf("WireState idle = %q", got)
	}
	withNotification.enqueueJobNotification(jobNotification{})
	if got := withNotification.WireState(); got != string(SessionProcessing) {
		t.Fatalf("WireState with pending notification = %q, want %q", got, SessionProcessing)
	}

	withQueuedInput := newTestSessionForState(t)
	if err := withQueuedInput.Enqueue(context.Background(), "queued follow-up"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if got := withQueuedInput.WireState(); got != string(SessionProcessing) {
		t.Fatalf("WireState with queued input = %q, want %q", got, SessionProcessing)
	}
}

// TestWireState_AwaitingOutranksAutonomy pins the projection precedence rule:
// the autonomy-in-flight override upgrades idle ONLY — awaiting is never
// masked as "active". Today the settle suppressors keep the two from
// coexisting at the settle itself, but a notification can land after the
// session has settled awaiting, and the ask-user-question design makes the
// coexistence routine: an asking session rests awaiting while its children
// run and their completion notifications queue behind the entry gate. If the
// override ever outranked awaiting, the question would read as "working"
// forever while the wakes that could clear it stay gated on the very answer
// the user was never told to give.
func TestWireState_AwaitingOutranksAutonomy(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return endReasonResponse("done", "needs_response") },
	}})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("test-model")), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatal(err)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state after clean completion = %q, want %q", got, SessionAwaiting)
	}
	// A job notification lands after the session settled awaiting.
	sess.enqueueJobNotification(jobNotification{})
	if got := sess.WireState(); got != string(SessionAwaiting) {
		t.Fatalf("WireState awaiting+pending notification = %q, want %q (awaiting outranks autonomy)", got, SessionAwaiting)
	}
}

// A restored session rests by the reason its last communicate gave, exactly
// as the live settle did: awaiting only when the agent needs a response.
func TestRestore_RestsByEndReason(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		reason string
		want   SessionState
	}{
		{reason: "", want: SessionIdle},
		{reason: "done", want: SessionIdle},
		{reason: "waiting_on_work", want: SessionIdle},
		{reason: "needs_response", want: SessionAwaiting},
	} {
		t.Run("reason="+tc.reason, func(t *testing.T) {
			t.Parallel()
			c := llm.NewClient()
			c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
				func(req llm.Request) llm.Response { return endReasonResponse("answer", tc.reason) },
			}})
			dir := t.TempDir()
			sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("test-model")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := sess.ProcessInput(ctx, "question", nil); err != nil {
				t.Fatal(err)
			}
			id := sess.ID()
			sess.Close()

			meta, err := schema.LoadSessionMeta(dir, id)
			if err != nil {
				t.Fatalf("LoadSessionMeta: %v", err)
			}
			c2 := llm.NewClient()
			c2.Register(&fakeAdapter{name: "openai"})
			restored, err := RestoreSessionFromMeta(c2, withTestSessionNamer(c2, NewOpenAIProfile("test-model")), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
			if err != nil {
				t.Fatalf("RestoreSessionFromMeta: %v", err)
			}
			defer restored.Close()
			if got := restored.State(); got != tc.want {
				t.Fatalf("restored state = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRestore_UserLastTurnStaysIdle is the companion negative case for
// TestRestore_AgentLastTurnResumesAwaiting: a restored session whose persisted
// transcript ends with the user's turn (the daemon stopped before the agent
// replied — simulated here the same way TestProcessInput_InterruptStaysIdle
// does, by cancelling before the adapter responds) must stay idle. Only an
// agent-last transcript upgrades; this guards recomputeRestoredState's
// backward-walk against over-triggering on a dangling user turn.
func TestRestore_UserLastTurnStaysIdle(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	blocker := make(chan struct{})
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { <-blocker; return finalResponse("late") },
	}})
	dir := t.TempDir()
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("test-model")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _, _ = sess.ProcessInput(ctx, "dangling question", nil); close(done) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	close(blocker)
	<-done
	id := sess.ID()
	sess.Close()

	meta, err := schema.LoadSessionMeta(dir, id)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}

	c2 := llm.NewClient()
	c2.Register(&fakeAdapter{name: "openai"})
	restored, err := RestoreSessionFromMeta(c2, withTestSessionNamer(c2, NewOpenAIProfile("test-model")), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()
	if got := restored.State(); got != SessionIdle {
		t.Fatalf("restored user-last session state = %q, want idle", got)
	}
}

// Live settle and restore read the same call: the first turn-ending
// communicate the input accepted. A later needs_response call in the same
// round loses the capture, so neither rests awaiting on it; the other order
// rests awaiting on both sides.
func TestSettleAndRestoreReadTheAcceptedCommunicate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		first, second string
		want          SessionState
	}{
		{name: "done wins", first: "done", second: "needs_response", want: SessionIdle},
		{name: "needs_response wins", first: "needs_response", second: "done", want: SessionAwaiting},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := llm.NewClient()
			c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
				func(req llm.Request) llm.Response {
					return toolCallResponse(
						communicateCallArgs("c1", map[string]any{"message": "one", "end_reason": tc.first}),
						communicateCallArgs("c2", map[string]any{"message": "two", "end_reason": tc.second}),
					)
				},
			}})
			dir := t.TempDir()
			sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("test-model")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := sess.ProcessInput(ctx, "question", nil); err != nil {
				t.Fatal(err)
			}
			if got := sess.State(); got != tc.want {
				t.Fatalf("live state = %q, want %q", got, tc.want)
			}
			id := sess.ID()
			sess.Close()
			meta, err := schema.LoadSessionMeta(dir, id)
			if err != nil {
				t.Fatalf("LoadSessionMeta: %v", err)
			}
			c2 := llm.NewClient()
			c2.Register(&fakeAdapter{name: "openai"})
			restored, err := RestoreSessionFromMeta(c2, withTestSessionNamer(c2, NewOpenAIProfile("test-model")), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
			if err != nil {
				t.Fatalf("RestoreSessionFromMeta: %v", err)
			}
			defer restored.Close()
			if got := restored.State(); got != tc.want {
				t.Fatalf("restored state = %q, want %q", got, tc.want)
			}
		})
	}
}

// needs_response rests awaiting only when nothing autonomous is in flight: a
// live child keeps the session idle, since its report will move it.
func TestProcessInput_NeedsResponseWithLiveChildRestsIdle(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return endReasonResponse("which?", "needs_response") },
	}})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("test-model")), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	child := newTestSessionForState(t)
	sess.subagents.mu.Lock()
	sess.subagents.subs[child.ID()] = &subagent{id: child.ID(), sess: child, running: true}
	sess.subagents.mu.Unlock()
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatal(err)
	}
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state = %q, want idle while a child runs", got)
	}
}

// restoreAfter runs inputs through a fresh session scripted with steps, then
// restores it, returning the live and restored states.
func restoreAfter(t *testing.T, steps []func(llm.Request) llm.Response, run func(context.Context, *Session)) (live, restored SessionState) {
	t.Helper()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: steps})
	dir := t.TempDir()
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("test-model")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	run(ctx, sess)
	live = sess.State()
	id := sess.ID()
	sess.Close()
	meta, err := schema.LoadSessionMeta(dir, id)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	c2 := llm.NewClient()
	c2.Register(&fakeAdapter{name: "openai"})
	again, err := RestoreSessionFromMeta(c2, withTestSessionNamer(c2, NewOpenAIProfile("test-model")), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer again.Close()
	return live, again.State()
}

// A model re-asking the same question makes a byte-identical communicate
// call, which the repeated-call breaker answers with a nudge appended to its
// result text. Restore still reads the accepted needs_response.
func TestRestore_RepeatedNeedsResponseQuestionRestsAwaiting(t *testing.T) {
	t.Parallel()
	ask := func(llm.Request) llm.Response { return endReasonResponse("Merge it?", "needs_response") }
	live, restored := restoreAfter(t, []func(llm.Request) llm.Response{ask, ask}, func(ctx context.Context, sess *Session) {
		for _, in := range []string{"q1", "not yet"} {
			if _, err := sess.ProcessInput(ctx, in, nil); err != nil {
				t.Fatal(err)
			}
		}
	})
	if live != SessionAwaiting || restored != SessionAwaiting {
		t.Fatalf("live %q, restored %q; want both awaiting", live, restored)
	}
}

// An input opened by a notification wake reads only its own accepted call,
// never the needs_response of the input before it.
func TestRestore_NotificationAfterNeedsResponseRestsIdle(t *testing.T) {
	t.Parallel()
	live, restored := restoreAfter(t, []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return endReasonResponse("Merge it?", "needs_response") },
		func(llm.Request) llm.Response { return endReasonResponse("noted the job", "") },
	}, func(ctx context.Context, sess *Session) {
		if _, err := sess.ProcessInput(ctx, "q1", nil); err != nil {
			t.Fatal(err)
		}
		sess.enqueueJobNotification(watchNotification("job_wake", "output_match: done"))
		if _, err := sess.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
			t.Fatal(err)
		}
	})
	if live != SessionIdle || restored != SessionIdle {
		t.Fatalf("live %q, restored %q; want both idle", live, restored)
	}
}
