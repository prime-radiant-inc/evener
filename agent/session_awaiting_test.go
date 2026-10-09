package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/clock"
	"primeradiant.com/evener/agent/internal/tool"
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
		name                                                 string
		hadOutput, goalKicked, queuePending, autonomyPending bool
		want                                                 SessionState
	}{
		{"clean turn with output arms awaiting", true, false, false, false, SessionAwaiting},
		{"no user-visible output stays idle", false, false, false, false, SessionIdle},
		{"goal kick suppresses", true, true, false, false, SessionIdle},
		{"queued input suppresses", true, false, true, false, SessionIdle},
		{"autonomy in flight suppresses", true, false, false, true, SessionIdle},
		{"all suppressors at once", true, true, true, true, SessionIdle},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := settleTerminalState(c.hadOutput, c.goalKicked, c.queuePending, c.autonomyPending)
			if got != c.want {
				t.Fatalf("settleTerminalState(%v,%v,%v,%v) = %q, want %q",
					c.hadOutput, c.goalKicked, c.queuePending, c.autonomyPending, got, c.want)
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

func TestWireState_RunningChildDoesNotMakeIdleParentActive(t *testing.T) {
	t.Parallel()
	parent := newTestSessionForState(t)
	child := newTestSessionForState(t)
	parent.subagents.track(&subagent{id: child.ID(), sess: child, running: true})

	if got := parent.WireState(); got != string(SessionIdle) {
		t.Fatalf("WireState with only a running child = %q, want %q", got, SessionIdle)
	}
	if !parent.autonomyInFlight() {
		t.Fatal("a running child must remain autonomy in flight for settle and restore")
	}
}

// Only a child doing work counts as autonomy: running, being driven, or
// finalizing a generation. A finished child's runtime kept warm for a quick
// follow-up will not move its parent, nor will a closed one.
func TestAutonomyInFlight_CountsOnlyWorkingChildren(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                                 string
		running, driving, finalizing, closed bool
		want                                 bool
	}{
		{name: "running", running: true, want: true},
		{name: "driving", driving: true, want: true},
		{name: "finalizing", finalizing: true, want: true},
		{name: "warm idle", want: false},
		{name: "closed", running: true, closed: true, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			parent := newTestSessionForState(t)
			child := newTestSessionForState(t)
			parent.subagents.track(&subagent{id: child.ID(), sess: child, running: tc.running, driving: tc.driving, finalizing: tc.finalizing, closed: tc.closed})
			if got := parent.autonomyInFlight(); got != tc.want {
				t.Fatalf("autonomyInFlight = %v, want %v", got, tc.want)
			}
		})
	}
}

// A child that has stopped finalizing locally still counts as work until its
// finalize tail releases the delegate's finalization: its report may not have
// reached the parent yet.
func TestAutonomyInFlight_CountsChildInFinalizeTail(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 4, 2)
	parent := newTestSessionForState(t)
	parent.delegateController = c
	child := newTestSessionForState(t)
	child.owningDelegateID = "dlg_tail"
	parent.subagents.track(&subagent{id: child.ID(), sess: child})
	if parent.autonomyInFlight() {
		t.Fatal("a warm idle child with no finalization counted as work")
	}
	c.mu.Lock()
	c.live["dlg_tail"] = &delegateLiveState{runtime: child, finalizing: &delegateFinalization{delegateID: "dlg_tail", runtime: child, released: make(chan struct{})}}
	c.mu.Unlock()
	if !parent.autonomyInFlight() {
		t.Fatal("a child in its finalize tail must count as work")
	}
}

// A finished delegate whose runtime is still warm does not keep its parent
// from resting awaiting after a needs_response turn (#4093).
func TestProcessInput_NeedsResponseWithWarmIdleChildRestsAwaiting(t *testing.T) {
	t.Parallel()
	sess := newSession(t, withSteps(func(llm.Request) llm.Response { return endReasonResponse("which?", "needs_response") }))
	child := newTestSessionForState(t)
	sess.subagents.track(&subagent{id: child.ID(), sess: child})
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatal(err)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state = %q, want awaiting beside a warm idle child", got)
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
			_, restored := restoreAfter(t, []func(llm.Request) llm.Response{
				func(llm.Request) llm.Response { return endReasonResponse("answer", tc.reason) },
			}, func(ctx context.Context, sess *Session) {
				if _, err := sess.ProcessInput(ctx, "question", nil); err != nil {
					t.Fatal(err)
				}
			})
			if restored != tc.want {
				t.Fatalf("restored state = %q, want %q", restored, tc.want)
			}
		})
	}
}

// TestRestore_UserLastTurnStaysIdle is the companion negative case for
// TestRestore_RestsByEndReason: a restored session whose persisted transcript
// ends with the user's turn (the daemon stopped before the agent replied —
// simulated here the same way TestProcessInput_InterruptStaysIdle does, by
// cancelling before the adapter responds) must stay idle, even though the
// input before it ended on needs_response. This guards
// recomputeRestoredState's backward-walk against reading past a dangling
// user turn to an earlier needs_response.
func TestRestore_UserLastTurnStaysIdle(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	blocker := make(chan struct{})
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return endReasonResponse("Merge it?", "needs_response") },
		func(req llm.Request) llm.Response { <-blocker; return finalResponse("late") },
	}})
	dir := t.TempDir()
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("test-model")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := sess.ProcessInput(ctx, "question", nil); err != nil {
		t.Fatal(err)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state after needs_response = %q, want %q (test setup broken)", got, SessionAwaiting)
	}
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
			live, restored := restoreAfter(t, []func(llm.Request) llm.Response{
				func(llm.Request) llm.Response {
					return toolCallResponse(
						communicateCallArgs("c1", map[string]any{"message": "one", "end_reason": tc.first}),
						communicateCallArgs("c2", map[string]any{"message": "two", "end_reason": tc.second}),
					)
				},
			}, func(ctx context.Context, sess *Session) {
				if _, err := sess.ProcessInput(ctx, "question", nil); err != nil {
					t.Fatal(err)
				}
			})
			if live != tc.want || restored != tc.want {
				t.Fatalf("live %q, restored %q; want both %q", live, restored, tc.want)
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
		// The second, identical call's result carries the breaker's nudge.
		sess.mu.Lock()
		last := sess.history[len(sess.history)-1]
		sess.mu.Unlock()
		if text, _ := last.Message.Content[0].ToolResult.Content.(string); !strings.Contains(text, "same call") {
			t.Fatalf("second result = %q, want the repeated-call nudge", text)
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

// A result replaced because its PostToolUse hook context could not be
// delivered keeps its tool state: a turn-ending communicate's records the end
// reason restore reads.
func TestExecTool_HookContextFailureKeepsToolState(t *testing.T) {
	t.Parallel()
	sess, _ := intg_hookSession(t, `{
		"hooks": {
			"PostToolUse": [
				{"matcher": "*", "hooks": [{"type": "command", "command": "echo '{\"hookSpecificOutput\":{\"hookEventName\":\"PostToolUse\",\"additionalContext\":\"post-context-note\"}}'"}]}
			]
		}
	}`)
	defer sess.Close()
	sess.RegisterTool("stateful_probe", "returns tool state", map[string]any{"type": "object"}, func(context.Context, any) (any, error) {
		return tool.StateResult{Output: "ok", State: communicateEndState{EndReason: "needs_response"}}, nil
	})
	// A retiring session refuses the hook context's steering.
	retirement, err := NewRetirementController(0, clock.Real())
	if err != nil {
		t.Fatal(err)
	}
	retirement.mu.Lock()
	retirement.phase = "claimed"
	retirement.mu.Unlock()
	sess.retirementController.Store(retirement)

	res := sess.execTool(context.Background(), llm.ToolCallData{ID: "call_state", Name: "stateful_probe", Arguments: json.RawMessage(`{}`)}, "")
	if !res.IsError {
		t.Fatalf("result = %+v, want the hook-failure error", res)
	}
	if string(res.ToolState) != `{"communicate_end_reason":"needs_response"}` {
		t.Fatalf("tool state = %s, want the original kept", res.ToolState)
	}
}

// A delegate's report already armed as root attention moves the parent on,
// so a needs_response settle rests idle even once the reporter's runtime is
// only warm: settle reads the same pending work restore does.
func TestArmAwaitingAtSettle_PendingDelegateReportRestsIdle(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	f := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return endReasonResponse("which?", "needs_response") },
	}}
	sess := newSession(t, withDir(stateDir), withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1}), withAdapter(f))
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatal(err)
	}
	// Settle again from idle, now beside a warm idle reporter whose report is
	// armed: the delegate finished while the turn was writing its question.
	sess.mu.Lock()
	sess.state = SessionIdle
	sess.mu.Unlock()
	child := newTestSessionForState(t)
	sess.subagents.track(&subagent{id: child.ID(), sess: child})
	const id = "delegate:dlg_x/delivery/1"
	if ok, err := sess.appendDelegateNotificationDurably(id, `<delegate-notification delegate_id="dlg_x">done</delegate-notification>`); err != nil || !ok {
		t.Fatalf("append delegate notification: %v %v", ok, err)
	}
	if err := sess.armDelegateAttention(id); err != nil {
		t.Fatal(err)
	}
	sess.armAwaitingAtSettle(true, false)
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state = %q, want idle while a delegate report is pending", got)
	}
}
