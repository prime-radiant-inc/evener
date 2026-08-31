package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/llm"
)

type serveDrainWedgeEnvironment struct {
	execenv.ExecutionEnvironment
	entered     chan struct{}
	release     chan struct{}
	enterOnce   sync.Once
	releaseOnce sync.Once
}

func (e *serveDrainWedgeEnvironment) ReadFile(string, *int, *int) (string, error) {
	e.enterOnce.Do(func() { close(e.entered) })
	<-e.release
	return "", nil
}

func (e *serveDrainWedgeEnvironment) releaseRead() {
	e.releaseOnce.Do(func() { close(e.release) })
}

// TestServeDrainAbandonsARealStopPendingDelegate proves the interactive policy
// bounds a delegate whose real stop request remains pending inside an
// uncancellable tool call. The delegate is created through createDelegate, the
// read_file wedge is reached through the real tool path, and StopSubtree is the
// same mutation job_stop uses; no durable/controller state is hand-seeded.
func TestServeDrainAbandonsARealStopPendingDelegate(t *testing.T) {
	oldBudget := LaneClosePassBudget
	LaneClosePassBudget = 10 * time.Millisecond
	t.Cleanup(func() { LaneClosePassBudget = oldBudget })

	clk := agenttest.NewFakeClock()
	wedge := &serveDrainWedgeEnvironment{
		ExecutionEnvironment: execenv.NewLocalExecutionEnvironment(t.TempDir()),
		entered:              make(chan struct{}),
		release:              make(chan struct{}),
	}
	client := llm.NewClient()
	client.Register(&fakeAdapter{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response {
				args, _ := json.Marshal(map[string]any{"file_path": "WEDGED-REAL-SERVE"})
				return toolCallResponse(llm.ToolCallData{ID: "read-1", Name: "read_file", Arguments: args, Type: "function"})
			},
		},
	})
	root, err := NewSession(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")), wedge, SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		TurnEndsProcess:  false,
		clock:            clk,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(root.Close)
	t.Cleanup(wedge.releaseRead)

	created := root.createDelegate(context.Background(), delegateArgs{Task: "read the wedged path"})
	if created.Err != nil {
		t.Fatalf("createDelegate: %v", created.Err)
	}
	if created.DelegateID == "" || created.ChildSessionID == "" {
		t.Fatalf("createDelegate result = %#v, want live delegate and child session", created)
	}
	<-wedge.entered

	_, cancelPlan, stopPlans, err := root.delegateController.StopSubtree(rootDelegateActor(root.ID()), created.DelegateID)
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	executeDelegateCancelPlan(cancelPlan)
	if err := root.executeDelegateMutationPlans(stopPlans); err != nil {
		t.Fatalf("execute stop plans: %v", err)
	}
	row, ok := root.directStableDelegateForChildSession(created.ChildSessionID)
	if !ok || row.pendingStopSeq == 0 {
		t.Fatalf("real stop did not leave pending stop: ok=%v row=%#v", ok, row)
	}

	ctx, cancel := context.WithCancel(context.Background())
	d := newStallDriver(ctx, root)
	d.releaseKick(t)
	d.assertParked(t, "serve drain should initially wait on the real stopped delegate")

	clk.Advance(DrainStallTimeout)
	d.recheck <- time.Time{}
	d.releaseKick(t)
	d.assertParked(t, "first interactive grace window must not abandon the real stopped delegate")

	clk.Advance(DrainStallTimeout + time.Nanosecond)
	d.recheck <- time.Time{}
	d.releaseKick(t)
	<-d.done
	cancel()

	warnings := collectStallWarnings(root)
	if len(warnings) != 1 {
		t.Fatalf("serve abandonment warnings = %d, want 1: %+v", len(warnings), warnings)
	}
	warning, ok := warnings[0].Data.(events.WarningData)
	if !ok || warning.Code != events.WarningCodeDelegateAbandonedByDrain || warning.DelegateID != created.DelegateID {
		t.Fatalf("serve abandonment warning = %#v, want structured abandonment for delegate %s", warnings[0].Data, created.DelegateID)
	}
}
