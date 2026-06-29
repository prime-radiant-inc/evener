package agent

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/llm"
)

// lifecycleTestSession builds a session matching the lifecycle harness config
// (fake clock, deny env, per-child factory), returning it plus the fake clock and
// an events-drain join. It is the shared rig for the C1/C2 op tests below.
func lifecycleTestSession(t *testing.T, parentResponder func(llm.Request) llm.Response, factory func() *llm.Client) (*Session, *agenttest.FakeClock) {
	t.Helper()
	clk := agenttest.NewFakeClock()
	env := &agenttest.DenyEnv{WorkDir: lifecycleWorkDir}

	client := llm.NewClient()
	client.Register(&agenttest.ScriptedAdapter{Provider: "openai", Responder: parentResponder})
	cfg := SessionConfig{
		clock:                 clk,
		MaxSubagentDepth:      1,
		MaxToolRoundsPerInput: 10,
		LLMSleep:              func(_ context.Context, d time.Duration) error { clk.Sleep(d); return nil },
	}
	cfg.testOnly.childClientFactory = factory

	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), env, cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	drainDone := make(chan struct{})
	go func() {
		for range sess.Events() {
		}
		close(drainDone)
	}()
	t.Cleanup(func() {
		sess.Close()
		<-drainDone
	})
	return sess, clk
}

// TestLifecycleBackgroundShellQuiesces proves the C2 path: a turn that issues a
// background shell tool call actually starts a background job, and quiesceJobs
// drives it to a terminal status deterministically.
func TestLifecycleBackgroundShellQuiesces(t *testing.T) {
	var round atomic.Int64
	responder := func(llm.Request) llm.Response {
		if round.Add(1) == 1 {
			return buildResponse(kindShellBackground, 0)
		}
		return agenttest.FinalResponse("done")
	}
	sess, clk := lifecycleTestSession(t, responder, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "run a background job", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	quiesceJobs(sess, clk)

	if running := sess.jobManager.runningJobIDs(); len(running) != 0 {
		t.Fatalf("after quiesce, %d jobs still running, want 0", len(running))
	}
	jobs := sess.jobManager.list(listFilter{})
	var shellJobs int
	for _, rec := range jobs {
		if rec.Type == jobstore.JobShell {
			shellJobs++
			if !rec.Status.IsTerminal() {
				t.Fatalf("background shell job %s status = %s, want terminal", rec.JobID, rec.Status)
			}
		}
	}
	if shellJobs != 1 {
		t.Fatalf("found %d shell jobs, want exactly 1 (background job must actually start)", shellJobs)
	}
}

// TestLifecycleDelegateViaTurn proves the C1 path end-to-end: a parent turn that
// issues a delegate tool call spawns a child that runs on its OWN adapter (from
// the factory) to completion, with the parent's adapter never serving a child
// turn.
func TestLifecycleDelegateViaTurn(t *testing.T) {
	var parentCalls atomic.Int64
	var childCalls atomic.Int64
	var factoryCalls atomic.Int64

	responder := func(llm.Request) llm.Response {
		if parentCalls.Add(1) == 1 {
			return buildResponse(kindDelegate, 0)
		}
		return agenttest.FinalResponse("done")
	}
	factory := func() *llm.Client {
		factoryCalls.Add(1)
		c := llm.NewClient()
		c.Register(&agenttest.ScriptedAdapter{Provider: "openai", Responder: func(llm.Request) llm.Response {
			childCalls.Add(1)
			return agenttest.FinalResponse("child done")
		}})
		return c
	}
	sess, _ := lifecycleTestSession(t, responder, factory)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "delegate something", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	if factoryCalls.Load() != 1 {
		t.Fatalf("childClientFactory called %d times, want exactly 1", factoryCalls.Load())
	}
	if childCalls.Load() == 0 {
		t.Fatal("child adapter never served a turn: the delegate child did not run on its own client")
	}
}

// TestLifecycleBackgroundDelegateQuiesces proves the C3 path: a parent turn that
// issues a delegate tool call (background by default — no max_wait_ms) spawns a
// child on its OWN client and returns immediately; the fire-and-forget finalize
// bridge drives the delegate job to terminal off the parent goroutine; and the
// harness quiesce (advance the fake clock + join the job's done channel) followed
// by a notification-rail drain leaves no running job, a terminal delegate job, and
// an empty notification queue — deterministically, with no wall-time sleep.
func TestLifecycleBackgroundDelegateQuiesces(t *testing.T) {
	var parentCalls, childCalls, factoryCalls atomic.Int64
	responder := func(llm.Request) llm.Response {
		if parentCalls.Add(1) == 1 {
			return buildResponse(kindDelegate, 0) // background: the delegate tool defaults to background when no max_wait_ms
		}
		return agenttest.FinalResponse("done")
	}
	factory := func() *llm.Client {
		factoryCalls.Add(1)
		c := llm.NewClient()
		c.Register(&agenttest.ScriptedAdapter{Provider: "openai", Responder: func(llm.Request) llm.Response {
			childCalls.Add(1)
			return agenttest.FinalResponse("child done")
		}})
		return c
	}
	sess, clk := lifecycleTestSession(t, responder, factory)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "delegate in background", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	quiesceJobs(sess, clk)
	drainJobNotificationTurns(sess)

	if factoryCalls.Load() != 1 {
		t.Fatalf("childClientFactory called %d times, want exactly 1", factoryCalls.Load())
	}
	if childCalls.Load() == 0 {
		t.Fatal("child adapter never served a turn: the background delegate child did not run")
	}
	if running := sess.jobManager.runningJobIDs(); len(running) != 0 {
		t.Fatalf("after quiesce, %d jobs still running, want 0", len(running))
	}
	var delegateJobs int
	for _, rec := range sess.jobManager.list(listFilter{}) {
		if rec.Type == jobstore.JobDelegate {
			delegateJobs++
			if !rec.Status.IsTerminal() {
				t.Fatalf("background delegate job %s status = %s, want terminal", rec.JobID, rec.Status)
			}
		}
	}
	if delegateJobs != 1 {
		t.Fatalf("found %d delegate jobs, want exactly 1 (the background delegate must actually start)", delegateJobs)
	}
	if pending := sess.peekNotifications(); pending != 0 {
		t.Fatalf("after notification drain, %d notifications pending, want 0", pending)
	}
}

// TestLifecycleBackgroundDelegateWatchdogFires proves the quiet-watchdog ticker is
// driven by the harness's injected fake clock. A background delegate is held
// running (its child gated so it cannot finish); advancing virtual time past the
// quiet window fires the watchdog exactly once, enqueuing a quiet owner
// notification. Releasing the gate then lets the finalize bridge settle the
// delegate, and the harness quiesce + drain leaves it terminal. BlockUntil is the
// deterministic handshake that the watchdog has armed its ticker before the advance.
func TestLifecycleBackgroundDelegateWatchdogFires(t *testing.T) {
	gate := make(chan struct{})
	var gateOnce sync.Once
	releaseGate := func() { gateOnce.Do(func() { close(gate) }) }
	t.Cleanup(releaseGate) // never strand the gated child if an assertion fails early

	var parentCalls atomic.Int64
	responder := func(llm.Request) llm.Response {
		if parentCalls.Add(1) == 1 {
			return buildResponse(kindDelegate, 0) // spawn exactly one background delegate
		}
		return agenttest.FinalResponse("done")
	}
	factory := func() *llm.Client {
		c := llm.NewClient()
		c.Register(&agenttest.ScriptedAdapter{Provider: "openai", Responder: func(llm.Request) llm.Response {
			<-gate // hold the child running until the test releases it
			return agenttest.FinalResponse("child done")
		}})
		return c
	}
	sess, clk := lifecycleTestSession(t, responder, factory)
	// Tighten the quiet window BEFORE the spawn so the watchdog snapshots the small
	// values at startQuietWatchdog time and a one-second advance is clearly past it.
	scaleQuietWatchdog(sess.jobManager, 200*time.Millisecond, 50*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "delegate in background", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	// The delegate is running with its child gated. Wait for the watchdog goroutine
	// to arm its ticker on the fake clock, then advance past the quiet window.
	clk.BlockUntil(1)
	clk.Advance(time.Second)

	// The watchdog must enqueue exactly one quiet owner notification.
	deadline := time.Now().Add(2 * time.Second)
	for sess.peekNotifications() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	notifs := sess.drainJobNotifications()
	if len(notifs) != 1 {
		t.Fatalf("quiet watchdog enqueued %d notifications, want exactly 1", len(notifs))
	}
	if !strings.Contains(notifs[0].Reason, "quiet") {
		t.Fatalf("watchdog notification reason = %q, want it to mention quiet", notifs[0].Reason)
	}

	// Release the child; the bridge finalizes the delegate, quiesce settles it.
	releaseGate()
	quiesceJobs(sess, clk)
	drainJobNotificationTurns(sess)

	if running := sess.jobManager.runningJobIDs(); len(running) != 0 {
		t.Fatalf("after release+quiesce, %d jobs still running, want 0", len(running))
	}
}
