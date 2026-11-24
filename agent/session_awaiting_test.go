package agent

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
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
	c.Register(newFakeAdapter("openai"))
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("test-model")), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{testOnly: sharingHostProbe(testConfig{})})
	if err != nil {
		t.Fatal(err)
	}
	return sess
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

func TestProcessInput_InterruptStaysIdle(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	blocker := make(chan struct{})
	c.Register(newFakeAdapter("openai", func(req llm.Request) llm.Response { <-blocker; return agenttest.FinalResponse("late") }))
	sess := newSession(t, withClient(c), withProfile(NewOpenAIProfile("test-model")), withDir(t.TempDir()), withConfig(SessionConfig{}))
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
	c.Register(newFakeAdapter("openai", slices.Repeat([]func(llm.Request) llm.Response{askAwaitingResponse}, 2)...))
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("test-model")), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := tripwireCtx(t)
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
	c.Register(newFakeAdapter("openai", askAwaitingResponse))
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("test-model")), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := tripwireCtx(t)
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

func TestRestore_UserLastTurnStaysIdle(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	blocker := make(chan struct{})
	c.Register(newFakeAdapter("openai", askAwaitingResponse, func(req llm.Request) llm.Response { <-blocker; return agenttest.FinalResponse("late") }))
	dir := t.TempDir()
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("test-model")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir, testOnly: sharingHostProbe(testConfig{})})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := sess.ProcessInput(ctx, "question", nil); err != nil {
		t.Fatal(err)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state after ask_user = %q, want %q (test setup broken)", got, SessionAwaiting)
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

// A live child must not replace the pending ask as the resting state.
func TestAwaitingWithLiveChild(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(newFakeAdapter("openai", askAwaitingResponse))
	sess := newSession(t, withClient(c), withProfile(NewOpenAIProfile("test-model")), withDir(t.TempDir()), withConfig(SessionConfig{}))
	defer sess.Close()
	child := newTestSessionForState(t)
	sess.subagents.mu.Lock()
	sess.subagents.subs[child.ID()] = &subagent{id: child.ID(), sess: child, running: true}
	sess.subagents.mu.Unlock()
	ctx := tripwireCtx(t)
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatal(err)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state = %q, want awaiting while the question remains pending", got)
	}
}

// restoreAfter runs inputs through a fresh session scripted with steps, then
// restores it, returning the live and restored states.
func restoreAfter(t *testing.T, steps []func(llm.Request) llm.Response, run func(context.Context, *Session)) (live, restored SessionState) {
	t.Helper()
	dir, meta, live := saveAfter(t, steps, run)
	again := restoreSaved(t, dir, meta, RestoreSessionConfig{})
	defer again.Close()
	return live, again.State()
}

// saveAfter runs inputs through a fresh session scripted with steps, closes
// it, and returns its state dir, saved meta, and the state it closed in.
func saveAfter(t *testing.T, steps []func(llm.Request) llm.Response, run func(context.Context, *Session)) (string, schema.SessionMeta, SessionState) {
	t.Helper()
	c := llm.NewClient()
	c.Register(newFakeAdapter("openai", steps...))
	dir := t.TempDir()
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("test-model")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir, testOnly: sharingHostProbe(testConfig{})})
	if err != nil {
		t.Fatal(err)
	}
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	run(ctx, sess)
	live := sess.State()
	id := sess.ID()
	sess.Close()
	meta, err := schema.LoadSessionMeta(dir, id)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	return dir, meta, live
}

// restoreSaved restores a session saveAfter saved, with cfg's runtime-only
// restore configuration. The caller closes it.
func restoreSaved(t *testing.T, dir string, meta schema.SessionMeta, cfg RestoreSessionConfig) *Session {
	t.Helper()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	cfg.StateDir = dir
	cfg.testOnly = sharingHostProbe(cfg.testOnly)
	sess, err := RestoreSessionFromMetaWithConfig(c, withTestSessionNamer(c, NewOpenAIProfile("test-model")), execenv.NewLocalExecutionEnvironment(dir), meta, cfg)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	return sess
}

// A tool result replaced by a failed PostToolUse hook keeps its typed state.
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
	sess.registerTool("stateful_probe", "returns tool state", map[string]any{"type": "object"}, func(context.Context, any) (any, error) {
		quiet := true
		return tool.StateResult{Output: "ok", State: communicateEndState{Quiet: &quiet}}, nil
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
	if string(res.ToolState) != `{"communicate_quiet":true}` {
		t.Fatalf("tool state = %s, want the original kept", res.ToolState)
	}
}

// A delegate report must not displace a pending question.
func TestAwaitingAtSettleWithPendingDelegateReport(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	f := newFakeAdapter("openai", askAwaitingResponse)
	sess := newSession(t, withDir(stateDir), withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1}), withAdapter(f))
	ctx := tripwireCtx(t)
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
	sess.armAwaitingAtSettle()
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state = %q, want awaiting with a pending ask and delegate report", got)
	}
}

func askAwaitingResponse(llm.Request) llm.Response {
	return agenttest.ToolCallResponse(askUserCall("ask-awaiting", askUserArgsValid()))
}

func TestPendingQuestionRestsAwaitingWithInputQueued(t *testing.T) {
	t.Parallel()
	sess := newSession(t, withSteps(askAwaitingResponse))
	ctx := tripwireCtx(t)
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatal(err)
	}
	// Settle again from idle with a message queued, as a human-note carrier
	// round that left the question pending would.
	sess.mu.Lock()
	sess.state = SessionIdle
	sess.mu.Unlock()
	if err := sess.Enqueue(ctx, "later"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	sess.armAwaitingAtSettle()
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state = %q, want awaiting while the question is pending", got)
	}
}

// A pending question upgrades only an open, idle session: a settle that
// finds the session still processing, or closing, leaves that state alone.
func TestPendingQuestionUpgradesOnlyAnOpenIdleSession(t *testing.T) {
	t.Parallel()
	sess := newSession(t, withSteps(askAwaitingResponse))
	ctx := tripwireCtx(t)
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatal(err)
	}
	sess.mu.Lock()
	sess.state = SessionProcessing
	sess.mu.Unlock()
	sess.armAwaitingAtSettle()
	if got := sess.State(); got != SessionProcessing {
		t.Fatalf("state = %q, want processing left alone", got)
	}
	// A retiring session is closing while its state still reads idle, so
	// only the closing check keeps the settle from upgrading it.
	sess.mu.Lock()
	sess.state = SessionIdle
	sess.closing = true
	sess.mu.Unlock()
	sess.armAwaitingAtSettle()
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state = %q while closing, want idle left alone", got)
	}
}

func TestFilteredWakeKeepsAnIdleRest(t *testing.T) {
	t.Parallel()
	sess := newSession(t, withSteps(func(llm.Request) llm.Response { return agenttest.FinalResponse("done") }))
	ctx := tripwireCtx(t)
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
		t.Fatalf("filtered wake: %v", err)
	}
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state after a filtered wake = %q, want idle", got)
	}
}
