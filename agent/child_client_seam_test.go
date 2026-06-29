package agent

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/llm"
)

// TestChildClientFactorySeam locks in C1's per-child adapter seam: when a
// childClientFactory is injected, a spawned child session uses the factory's
// client (its own ScriptedAdapter), not the parent's. This is what lets the
// lifecycle harness give each concurrent child a private, pre-recorded response
// script with no shared-Responder draw race.
func TestChildClientFactorySeam(t *testing.T) {
	clk := agenttest.NewFakeClock()
	env := &agenttest.DenyEnv{WorkDir: lifecycleWorkDir}

	var parentCalls atomic.Int64
	parentClient := llm.NewClient()
	parentClient.Register(&agenttest.ScriptedAdapter{Provider: "openai", Responder: func(llm.Request) llm.Response {
		parentCalls.Add(1)
		return agenttest.FinalResponse("parent")
	}})

	var factoryCalls atomic.Int64
	var childCalls atomic.Int64
	factory := func() *llm.Client {
		factoryCalls.Add(1)
		c := llm.NewClient()
		c.Register(&agenttest.ScriptedAdapter{Provider: "openai", Responder: func(llm.Request) llm.Response {
			childCalls.Add(1)
			return agenttest.FinalResponse("child reporting done")
		}})
		return c
	}

	cfg := SessionConfig{
		clock:                 clk,
		MaxSubagentDepth:      1,
		MaxToolRoundsPerInput: 10,
		LLMSleep:              func(_ context.Context, d time.Duration) error { clk.Sleep(d); return nil },
	}
	cfg.testOnly.childClientFactory = factory

	sess, err := NewSession(parentClient, NewOpenAIProfile("gpt-5.2"), env, cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	drainDone := make(chan struct{})
	go func() {
		for range sess.Events() {
		}
		close(drainDone)
	}()
	defer func() {
		sess.Close()
		<-drainDone
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res := sess.createDelegate(ctx, delegateArgs{Task: "do the thing", DelegationAllowance: 0, Background: false})

	if res.Err != nil {
		t.Fatalf("createDelegate returned error: %v (status=%s reason=%s)", res.Err, res.Status, res.Reason)
	}
	if factoryCalls.Load() != 1 {
		t.Fatalf("childClientFactory called %d times, want 1", factoryCalls.Load())
	}
	if childCalls.Load() == 0 {
		t.Fatal("child adapter was never called: the child did not use its own client")
	}
	if parentCalls.Load() != 0 {
		t.Fatalf("parent adapter was called %d times by a child turn; the child must use its own client", parentCalls.Load())
	}
}
