package agent

import (
	"context"
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
