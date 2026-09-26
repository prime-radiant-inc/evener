package agent

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appprojector"
	"primeradiant.com/evener/llm"
)

func TestHistoryEndsInTurnFailure(t *testing.T) {
	t.Parallel()
	turns := func(kinds ...schema.TurnKind) []schema.Turn {
		out := make([]schema.Turn, len(kinds))
		for i, kind := range kinds {
			out[i] = schema.Turn{Kind: kind}
		}
		return out
	}
	cases := []struct {
		name    string
		history []schema.Turn
		want    bool
	}{
		{"empty history", nil, false},
		{"a clean turn", turns(schema.TurnUserInput, schema.TurnAssistant), false},
		{"a failed turn", turns(schema.TurnUserInput, schema.TurnFailure), true},
		{"a failed turn with a salvaged draft and its explanation", turns(schema.TurnUserInput, schema.TurnAssistant, schema.TurnSteering, schema.TurnFailure), true},
		{"bookkeeping after the failure", turns(schema.TurnUserInput, schema.TurnFailure, schema.TurnModelSwitch, schema.TurnHookCompleted, schema.TurnEnvironment, schema.TurnNotesContext, schema.TurnSystem, schema.TurnCheckpoint, schema.TurnSummary, schema.TurnAttentionResolution), true},
		{"the next message after a failure", turns(schema.TurnFailure, schema.TurnUserInput), false},
		{"a steer or interrupt marker after a failure", turns(schema.TurnFailure, schema.TurnSteering), false},
		{"a turn a notification started after a failure", turns(schema.TurnFailure, schema.TurnSystem, schema.TurnAssistant), false},
		{"an assistant response (tool calls included) after a failure", turns(schema.TurnFailure, schema.TurnAssistant), false},
		{"tool results after a failure", turns(schema.TurnFailure, schema.TurnToolResults), false},
		{"legacy tool output after a failure", turns(schema.TurnFailure, schema.TurnTool), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := historyEndsInTurnFailure(c.history); got != c.want {
				t.Fatalf("historyEndsInTurnFailure = %v, want %v", got, c.want)
			}
		})
	}
}

// failingThenRecoveringSession's first model call fails with a provider error
// that is not retried; every later call answers "recovered".
func failingThenRecoveringSession(t *testing.T, dir string) *Session {
	t.Helper()
	c := llm.NewClient()
	c.Register(&fakeErrAdapter{name: "openai", steps: []func(llm.Request) (llm.Response, error){
		func(llm.Request) (llm.Response, error) {
			return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 403, "sign-in rejected", nil, nil)
		},
		func(llm.Request) (llm.Response, error) { return finalResponse("recovered"), nil },
	}})
	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("test-model")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir, LLMRetryPolicy: &policy})
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

// A turn that records a failure leaves the session idle inside (it takes the
// next message) and systemError on the wire, until the next turn starts.
func TestWireState_FailedTurnReadsFailedUntilTheNextTurn(t *testing.T) {
	t.Parallel()
	sess := failingThenRecoveringSession(t, t.TempDir())
	defer sess.Close()
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "first", nil); err == nil {
		t.Fatal("first turn succeeded, want the scripted provider failure")
	}
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("State after a failed turn = %q, want idle: the session still takes input", got)
	}
	if got := sess.WireState(); got != appwire.ThreadStatusSystemError {
		t.Fatalf("WireState after a failed turn = %q, want %q", got, appwire.ThreadStatusSystemError)
	}
	if _, err := sess.ProcessInput(ctx, "second", nil); err != nil {
		t.Fatalf("second turn: %v", err)
	}
	if got := sess.WireState(); got != string(SessionAwaiting) {
		t.Fatalf("WireState after the next clean turn = %q, want awaiting", got)
	}
}

// An interrupt ends a turn without recording a failure: Stop is not Failed.
func TestWireState_InterruptIsNotAFailedTurn(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	blocked := make(chan struct{})
	c.Register(&blockingAdapter{name: "openai", blocked: blocked})
	sess, err := NewSession(c, NewOpenAIProfile("test-model"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	// TRIPWIRE: blockingAdapter is an in-process fake; only fires on a genuine hang.
	outer, outerCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer outerCancel()
	turnCtx, stop := context.WithCancel(outer)
	done := make(chan error, 1)
	go func() {
		_, err := sess.ProcessInput(turnCtx, "hello", nil)
		done <- err
	}()
	<-blocked
	stop()
	<-done
	if got := sess.WireState(); got != string(SessionIdle) {
		t.Fatalf("WireState after Stop = %q, want idle: an interrupt is not a failed turn", got)
	}
}

// A turn that fails while a question is still pending (a human-note carrier's
// turn, which does not answer ask1) stays awaiting on the wire: answering the
// question is what moves the session.
func TestWireState_PendingQuestionOutranksAFailedTurn(t *testing.T) {
	t.Parallel()
	ask := askUserCall("ask1", askUserArgsValid())
	c := llm.NewClient()
	c.Register(&fakeErrAdapter{name: "openai", steps: []func(llm.Request) (llm.Response, error){
		func(llm.Request) (llm.Response, error) { return toolCallResponse(ask), nil },
		func(llm.Request) (llm.Response, error) {
			return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 403, "carrier provider failure", nil, nil)
		},
	}})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if _, err := sess.SetHumanNote("note-question-outranks-failure", "watch the ingest path"); err != nil {
		t.Fatalf("SetHumanNote: %v", err)
	}
	if _, ran, err := sess.ProcessPendingUserInput(ctx, nil); err == nil || !ran {
		t.Fatalf("ProcessPendingUserInput: ran=%v err=%v, want a provider failure after the carrier ran", ran, err)
	}
	if got := sess.WireState(); got != string(SessionAwaiting) {
		t.Fatalf("WireState with a pending question after a failed turn = %q, want awaiting", got)
	}
}

// A daemon restarted after a failed turn derives the same Failed state from
// its transcript that the live session published, and stamps it on its
// SessionStart event, so a restart never turns a failure into idle.
func TestRestore_FailedTurnResumesFailed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sess := failingThenRecoveringSession(t, dir)
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "first", nil); err == nil {
		t.Fatal("first turn succeeded, want the scripted provider failure")
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
	eventsPtr, mu, doneCh := collectEvents(restored)
	if got := restored.State(); got != SessionIdle {
		t.Fatalf("restored State = %q, want idle", got)
	}
	if got := restored.WireState(); got != appwire.ThreadStatusSystemError {
		t.Fatalf("restored WireState = %q, want %q", got, appwire.ThreadStatusSystemError)
	}
	restored.Close()
	<-doneCh
	mu.Lock()
	defer mu.Unlock()
	for _, ev := range *eventsPtr {
		if d, ok := ev.Data.(events.SessionStartData); ok && ev.Kind == events.EventSessionStart {
			if d.State != appwire.ThreadStatusSystemError {
				t.Fatalf("restored SessionStart State = %q, want %q", d.State, appwire.ThreadStatusSystemError)
			}
			return
		}
	}
	t.Fatal("restored session emitted no SessionStart")
}

// A restored session resting on a failed turn with claimable queued input
// reports active, on its restored SessionStart and on WireState alike: the
// queued message will start the next turn without the user, and serve and the
// bridge publish that same effective state (#251).
func TestRestore_FailedTurnWithQueuedInputResumesActive(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sess := failingThenRecoveringSession(t, dir)
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "first", nil); err == nil {
		t.Fatal("first turn succeeded, want the scripted provider failure")
	}
	if err := sess.Enqueue(ctx, "queued after the failure"); err != nil {
		t.Fatalf("Enqueue: %v", err)
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
	eventsPtr, mu, doneCh := collectEvents(restored)
	if got := restored.QueueDepth(); got != 1 {
		t.Fatalf("restored QueueDepth = %d, want 1", got)
	}
	if got := restored.RestingWireState(); got != appwire.ThreadStatusSystemError {
		t.Fatalf("restored RestingWireState = %q, want %q", got, appwire.ThreadStatusSystemError)
	}
	if got := restored.WireState(); got != string(SessionProcessing) {
		t.Fatalf("restored WireState = %q, want %q: queued input will start the next turn", got, SessionProcessing)
	}
	restored.Close()
	<-doneCh
	mu.Lock()
	defer mu.Unlock()
	for _, ev := range *eventsPtr {
		if d, ok := ev.Data.(events.SessionStartData); ok && ev.Kind == events.EventSessionStart {
			if d.State != string(SessionProcessing) {
				t.Fatalf("restored SessionStart State = %q, want %q", d.State, SessionProcessing)
			}
			return
		}
	}
	t.Fatal("restored session emitted no SessionStart")
}

// A message queued while a turn runs stays queued when that turn fails: the
// failure exit returns before the drain ladder. Its SessionEnd then carries the
// effective state active (the queued message starts the next turn), and the
// projector announces that as active, never as a close.
func TestSession_FailedTurnWithQueuedInputEndsActiveAndOpen(t *testing.T) {
	t.Parallel()
	var sess *Session
	c := llm.NewClient()
	c.Register(&fakeErrAdapter{name: "openai", steps: []func(llm.Request) (llm.Response, error){
		func(llm.Request) (llm.Response, error) {
			if err := sess.Enqueue(context.Background(), "queued during the failing turn"); err != nil {
				t.Errorf("Enqueue: %v", err)
			}
			return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 403, "sign-in rejected", nil, nil)
		},
	}})
	policy := llm.RetryPolicy{MaxRetries: 0}
	var err error
	sess, err = NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("test-model")), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{LLMRetryPolicy: &policy})
	if err != nil {
		t.Fatal(err)
	}
	eventsPtr, mu, doneCh := collectEvents(sess)
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "first", nil); err == nil {
		t.Fatal("first turn succeeded, want the scripted provider failure")
	}
	if got := sess.QueueDepth(); got != 1 {
		t.Fatalf("QueueDepth after the failed turn = %d, want 1: the queued message survives the failure", got)
	}
	if got := sess.RestingWireState(); got != appwire.ThreadStatusSystemError {
		t.Fatalf("RestingWireState = %q, want %q", got, appwire.ThreadStatusSystemError)
	}
	if got := sess.WireState(); got != string(SessionProcessing) {
		t.Fatalf("WireState = %q, want %q: the queued message starts the next turn", got, SessionProcessing)
	}
	sess.Close()
	<-doneCh

	mu.Lock()
	evs := append([]events.SessionEvent{}, (*eventsPtr)...)
	mu.Unlock()
	var failedEnd *events.SessionEndData
	for _, ev := range evs {
		if d, ok := ev.Data.(events.SessionEndData); ok && ev.Kind == events.EventSessionEnd && d.Reason == "turn_failed" {
			failedEnd = &d
		}
	}
	if failedEnd == nil {
		t.Fatalf("no turn_failed SESSION_END in %+v", evs)
	}
	if failedEnd.State != string(SessionProcessing) {
		t.Fatalf("turn_failed SESSION_END State = %q, want %q", failedEnd.State, SessionProcessing)
	}
	projector := appprojector.NewAppEventProjector(sess.ID(), "local:"+sess.ID())
	for _, ev := range evs {
		for _, n := range projector.Project(ev) {
			if n.Method == appwire.NotifyThreadClosed {
				t.Fatalf("a failed turn with a queued message announced thread/closed: %+v", n)
			}
		}
	}
}
