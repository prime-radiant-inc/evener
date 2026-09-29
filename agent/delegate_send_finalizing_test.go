package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

// finalizingDelegate is a stable delegate whose first generation is held
// part-way through its finalize block: its aggregate is idle and its parent
// has been handed the result, but the child is still finalizing. release
// lets it finish.
type finalizingDelegate struct {
	s          *Session
	delegateID string
	child      *subagent
	release    func()
}

// holdDelegateFinalizing starts a delegate and holds its first generation in
// that window. The parent's delivery hook runs on the child's finalize path
// after FinishGeneration has made the aggregate idle and before the child
// clears finalizing, so holding it there pins the window with no timing.
func holdDelegateFinalizing(t *testing.T) finalizingDelegate {
	t.Helper()
	stateDir := realTempDirForTest(t)
	workspace := realTempDirForTest(t)
	held, hold := make(chan struct{}), make(chan struct{})
	var holdOnce, releaseOnce sync.Once
	var childMu sync.Mutex
	var child *subagent
	client := llm.NewClient()
	client.Register(&agenttest.ScriptedAdapter{Provider: "openai", Responder: func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("done")}
	}})
	profile := withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2"))
	s, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(workspace), SessionConfig{
		StateDir:         stateDir,
		MaxSubagentDepth: 2,
		ForceRealIO:      true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			sandboxProber:       bwrapCapableProber(workspace),
			subagentAfterFinalStatePublish: func(a *subagent) {
				childMu.Lock()
				child = a
				childMu.Unlock()
			},
			delegateDeliveryClassified: func(*Session, bool) {
				holdOnce.Do(func() {
					close(held)
					<-hold
				})
			},
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	release := func() { releaseOnce.Do(func() { close(hold) }) }
	// Cleanups run last-registered first: the held finalize path is let go
	// before the session closes.
	t.Cleanup(s.Close)
	t.Cleanup(release)
	create := executeDelegateTool(t, s, "call_create", "delegate", map[string]any{"prompt": "Find the race.", "name": "settle-race"})
	var receipt stableDelegateCreateResult
	if err := json.Unmarshal([]byte(create.Output), &receipt); err != nil || receipt.DelegateID == "" {
		t.Fatalf("delegate receipt %q names no delegate: %v", create.Output, err)
	}
	select {
	case <-held:
	// TRIPWIRE: a hang guard only; the scripted child answers at once and its
	// finalize path reaches the delivery hook within milliseconds.
	case <-time.After(30 * time.Second):
		t.Fatal("the delegate never reached its finalize path")
	}
	childMu.Lock()
	defer childMu.Unlock()
	return finalizingDelegate{s: s, delegateID: receipt.DelegateID, child: child, release: release}
}

// executeDelegateTool runs one tool call as a session's tool round does, with
// the call's id in its context.
func executeDelegateTool(t *testing.T, s *Session, id, name string, args map[string]any) tool.ExecResult {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("%s arguments: %v", id, err)
	}
	ctx := context.WithValue(context.Background(), ctxToolCallID, id)
	return s.reg.ExecuteCall(ctx, s.currentEnv(), llm.ToolCallData{ID: id, Name: name, Arguments: raw})
}

// A send that reaches a delegate the parent has just heard is idle, while
// its child is still finalizing, is refused: nothing is committed, and the
// delegate's latest outcome is still the one its run reported. It used to
// commit a new generation, find the child busy, and record that generation
// as a failed start (construction_failed), so the coordinator, the web and
// the phone read a working delegate as failed.
func TestDelegateSendWhileTheChildFinalizesIsARefusal(t *testing.T) {
	t.Parallel()
	for _, wait := range []int{0, 60_000} {
		held := holdDelegateFinalizing(t)
		before := delegateAggregateSnapshot(t, held.s.delegateController, held.delegateID)
		if before.Phase != delegatestore.PhaseIdle {
			t.Fatalf("held delegate phase = %q, want idle while its child finalizes", before.Phase)
		}
		args := map[string]any{"to": held.delegateID, "message": "Is drain ordering fine?"}
		if wait > 0 {
			args["max_wait_ms"] = wait
		}
		res := executeDelegateTool(t, held.s, "call_send", "delegate_send", args)
		if !res.IsError || !strings.Contains(res.Output, "target_busy") {
			t.Fatalf("max_wait_ms %d: a send while the child finalizes = %q (error %v), want a target_busy refusal", wait, res.Output, res.IsError)
		}
		after := delegateAggregateSnapshot(t, held.s.delegateController, held.delegateID)
		if after.Generation != before.Generation {
			t.Fatalf("max_wait_ms %d: the refused send committed generation %d over %d", wait, after.Generation, before.Generation)
		}
		if !reflect.DeepEqual(after.LatestOutcome, before.LatestOutcome) {
			t.Fatalf("max_wait_ms %d: the refused send changed the latest outcome from %+v to %+v", wait, before.LatestOutcome, after.LatestOutcome)
		}
		held.release()
	}
}

// A busy child the controller can't see (here: the controller has heard the
// runtime is quiesced, but the child itself is still finalizing, standing in
// for a drive in flight on an idle child) is refused before anything is
// committed, by the send's own look at the child.
func TestDelegateSendToABusyChildTheControllerCantSeeIsARefusal(t *testing.T) {
	t.Parallel()
	held := holdDelegateFinalizing(t)
	defer held.release()
	if held.child == nil {
		t.Fatal("the held delegate's child was never published")
	}
	lease := delegateLease{delegateID: held.delegateID, generation: delegateAggregateSnapshot(t, held.s.delegateController, held.delegateID).Generation}
	if err := held.s.delegateController.ReportFinalizationQuiesced(lease, held.child.sess); err != nil {
		t.Fatalf("report quiescence: %v", err)
	}
	before := delegateAggregateSnapshot(t, held.s.delegateController, held.delegateID)
	res := executeDelegateTool(t, held.s, "call_send", "delegate_send", map[string]any{"to": held.delegateID, "message": "Is drain ordering fine?"})
	if !res.IsError || !strings.Contains(res.Output, "target_busy") {
		t.Fatalf("a send to a busy child = %q (error %v), want a target_busy refusal", res.Output, res.IsError)
	}
	after := delegateAggregateSnapshot(t, held.s.delegateController, held.delegateID)
	if after.Generation != before.Generation || !reflect.DeepEqual(after.LatestOutcome, before.LatestOutcome) {
		t.Fatalf("the refused send changed the delegate: generation %d → %d, outcome %+v → %+v", before.Generation, after.Generation, before.LatestOutcome, after.LatestOutcome)
	}
}
