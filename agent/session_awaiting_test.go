package agent

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// newTestSessionForState builds a fresh idle session (no events drained) for
// direct testing of the settle-state helpers, mirroring the construction
// pattern used by session_lifecycle_test.go / session_goal_test.go.
func newTestSessionForState(t *testing.T) *Session {
	t.Helper()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{}})
	sess, err := NewSession(c, NewOpenAIProfile("test-model"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

func TestSettleTerminalState(t *testing.T) {
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
	sess := newTestSessionForState(t)
	// No goal set: settle must report kicked=false.
	if kicked := sess.settleGoalOnIdle(); kicked {
		t.Fatal("settleGoalOnIdle with no goal reported kicked=true")
	}
	// Active goal + wired kick: settle must kick and report it.
	kickCh := make(chan string, 1)
	sess.SetKickFunc(func(p string) { kickCh <- p })
	if _, err := sess.SetGoal(nil, "test objective"); err != nil { //nolint:staticcheck // ctx unused by SetGoal
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

func TestProcessInput_CleanCompletionArmsAwaiting(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return finalResponse("done") },
	}})
	sess, err := NewSession(c, NewOpenAIProfile("test-model"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	eventsPtr, mu, doneCh := collectEvents(sess)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatal(err)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state after clean completion = %q, want %q", got, SessionAwaiting)
	}
	sess.Close()
	<-doneCh
	mu.Lock()
	defer mu.Unlock()
	for _, ev := range *eventsPtr {
		if ev.Kind == events.EventSessionEnd {
			d, ok := ev.Data.(events.SessionEndData)
			if !ok {
				t.Fatal("SessionEnd data type")
			}
			if d.Reason == "input_complete" && d.State != string(SessionAwaiting) {
				t.Fatalf("SessionEnd.State = %q, want %q", d.State, SessionAwaiting)
			}
		}
	}
}

func TestProcessInput_InterruptStaysIdle(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	blocker := make(chan struct{})
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { <-blocker; return finalResponse("late") },
	}})
	sess, err := NewSession(c, NewOpenAIProfile("test-model"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
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
		func(req llm.Request) llm.Response { return finalResponse("one") },
		func(req llm.Request) llm.Response { return finalResponse("two") },
	}})
	sess, err := NewSession(c, NewOpenAIProfile("test-model"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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

func TestWireState_ChildrenInFlightReadsActive(t *testing.T) {
	sess := newTestSessionForState(t)
	// Idle with no autonomy: wire state == raw state.
	if got := sess.WireState(); got != string(SessionIdle) {
		t.Fatalf("WireState idle = %q", got)
	}
	// Simulate a pending job notification (autonomy in flight while idle):
	sess.enqueueJobNotification(jobNotification{})
	if got := sess.WireState(); got != string(SessionProcessing) {
		t.Fatalf("WireState with pending notification = %q, want %q", got, SessionProcessing)
	}
}

// TestRestore_AgentLastTurnResumesAwaiting pins the resume-recompute contract
// (spec v5, round-3 A2): a restored session whose persisted transcript ends
// with the agent having moved last resumes `awaiting`, not `idle`. Queues and
// notification buffers are never persisted and goals are deliberately not
// re-kicked on restore ("loaded but idle"), so without this recompute a
// restored mid-goal or post-reply session would silently read idle even
// though the ball is in the user's court.
func TestRestore_AgentLastTurnResumesAwaiting(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return finalResponse("answer") },
	}})
	dir := t.TempDir()
	sess, err := NewSession(c, NewOpenAIProfile("test-model"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	restored, err := RestoreSessionFromMeta(c2, NewOpenAIProfile("test-model"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()
	if got := restored.State(); got != SessionAwaiting {
		t.Fatalf("restored agent-last session state = %q, want awaiting", got)
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
	sess, err := NewSession(c, NewOpenAIProfile("test-model"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
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
	restored, err := RestoreSessionFromMeta(c2, NewOpenAIProfile("test-model"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()
	if got := restored.State(); got != SessionIdle {
		t.Fatalf("restored user-last session state = %q, want idle", got)
	}
}
