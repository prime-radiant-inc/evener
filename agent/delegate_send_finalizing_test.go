package agent

import (
	"context"
	"encoding/json"
	"errors"
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
	// quiesced closes once the child's finalize tail has reported its
	// generation quiesced, after release.
	quiesced <-chan struct{}
}

// holdDelegateFinalizing starts a delegate and holds its first generation in
// that window. The parent's delivery hook runs on the child's finalize path
// after FinishGeneration has made the aggregate idle and before the child
// clears finalizing, so holding it there pins the window with no timing.
func holdDelegateFinalizing(t *testing.T) finalizingDelegate {
	t.Helper()
	stateDir := realTempDirForTest(t)
	workspace := realTempDirForTest(t)
	held, hold, quiesced := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var holdOnce, releaseOnce, quiescedOnce sync.Once
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
			subagentAfterFinalizationQuiesced: func(*subagent) {
				quiescedOnce.Do(func() { close(quiesced) })
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
	return finalizingDelegate{s: s, delegateID: receipt.DelegateID, child: child, release: release, quiesced: quiesced}
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

		// Once the child has finished finalizing, the same send is taken.
		held.release()
		select {
		case <-held.quiesced:
		// TRIPWIRE: a hang guard only; the released finalize tail reports
		// quiescence within milliseconds.
		case <-time.After(30 * time.Second):
			t.Fatal("the released child never reported quiescence")
		}
		retry := executeDelegateTool(t, held.s, "call_retry", "delegate_send", map[string]any{"to": held.delegateID, "message": "Is drain ordering fine?"})
		if retry.IsError || !strings.Contains(retry.Output, "started") {
			t.Fatalf("max_wait_ms %d: the send after the child quiesced = %q (error %v), want it started", wait, retry.Output, retry.IsError)
		}
		if got := delegateAggregateSnapshot(t, held.s.delegateController, held.delegateID).Generation; got != before.Generation+1 {
			t.Fatalf("max_wait_ms %d: generation after the taken send = %d, want %d", wait, got, before.Generation+1)
		}
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
	// The controller no longer holds the delegate, so a refusal can only
	// come from the send's own look at the child.
	assertStartAdmitted(t, held.s.delegateController, held.delegateID, "once the controller has heard the runtime quiesced")
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

// finishedDelegateStillFinalizing finishes a delegate's generation the way a
// run's finalize tail does, up to (not including) its quiescence report.
func finishedDelegateStillFinalizing(t *testing.T) (*delegateTreeController, delegateLease, *Session) {
	t.Helper()
	c, _ := newDelegateControllerTestHarness(t, 2, 2)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	started, runtime := commitAttachedDelegateControllerStart(t, c, "dlg_target")
	if _, err := c.AdmitStartInput(started.lease, func() error { return nil }); err != nil {
		t.Fatalf("AdmitStartInput: %v", err)
	}
	if _, err := c.FinishGeneration(started.lease, delegateFinish{outcome: delegatestore.OutcomeCompleted, reason: "completed"}); err != nil {
		t.Fatalf("FinishGeneration: %v", err)
	}
	return c, started.lease, runtime
}

func assertStartRefused(t *testing.T, c *delegateTreeController, delegateID, why string) {
	t.Helper()
	if _, err := c.ReserveStart(rootDelegateActor(c.rootSessionID), delegateID); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("ReserveStart %s = %v, want target busy", why, err)
	}
}

func assertStartAdmitted(t *testing.T, c *delegateTreeController, delegateID, why string) {
	t.Helper()
	reservation, err := c.ReserveStart(rootDelegateActor(c.rootSessionID), delegateID)
	if err != nil {
		t.Fatalf("ReserveStart %s: %v", why, err)
	}
	if err := c.AbortStart(reservation); err != nil {
		t.Fatalf("AbortStart: %v", err)
	}
}

// The controller refuses a start from the moment FinishGeneration makes the
// delegate idle until the finished generation's runtime reports quiescence.
func TestDelegateControllerRefusesAStartUntilTheFinishedRuntimeQuiesces(t *testing.T) {
	c, lease, runtime := finishedDelegateStillFinalizing(t)
	assertStartRefused(t, c, "dlg_target", "while the finished runtime finalizes")
	if err := c.ReportFinalizationQuiesced(lease, runtime); err != nil {
		t.Fatalf("ReportFinalizationQuiesced: %v", err)
	}
	assertStartAdmitted(t, c, "dlg_target", "once the finished runtime has quiesced")
}

// A report for an earlier generation can't release a later generation that
// is still finalizing on the same runtime.
func TestDelegateControllerIgnoresAnEarlierGenerationsQuiescence(t *testing.T) {
	c, first, runtime := finishedDelegateStillFinalizing(t)
	if err := c.ReportFinalizationQuiesced(first, runtime); err != nil {
		t.Fatalf("ReportFinalizationQuiesced first: %v", err)
	}
	reservation, err := c.ReserveStart(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("ReserveStart second: %v", err)
	}
	second, err := c.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart second: %v", err)
	}
	if err := c.AttachRuntime(second.lease, runtime); err != nil {
		t.Fatalf("AttachRuntime second: %v", err)
	}
	if _, err := c.AdmitStartInput(second.lease, func() error { return nil }); err != nil {
		t.Fatalf("AdmitStartInput second: %v", err)
	}
	if _, err := c.FinishGeneration(second.lease, delegateFinish{outcome: delegatestore.OutcomeCompleted, reason: "completed"}); err != nil {
		t.Fatalf("FinishGeneration second: %v", err)
	}
	if err := c.ReportFinalizationQuiesced(first, runtime); err != nil {
		t.Fatalf("late ReportFinalizationQuiesced first: %v", err)
	}
	assertStartRefused(t, c, "dlg_target", "after only the earlier generation's report")
	if err := c.ReportFinalizationQuiesced(second.lease, runtime); err != nil {
		t.Fatalf("ReportFinalizationQuiesced second: %v", err)
	}
	assertStartAdmitted(t, c, "dlg_target", "once the later generation has quiesced")
}

// A finished runtime that is no longer the resident one (a restore replaced
// it) no longer holds the delegate: the gate is about that runtime.
func TestDelegateControllerAdmitsAStartOnceTheFinishedRuntimeIsReplaced(t *testing.T) {
	c, _, _ := finishedDelegateStillFinalizing(t)
	assertStartRefused(t, c, "dlg_target", "while the finished runtime is resident")
	c.mu.Lock()
	c.live["dlg_target"].runtime = &Session{}
	c.mu.Unlock()
	assertStartAdmitted(t, c, "dlg_target", "once another runtime is resident")
}

// A generation whose input was admitted but whose run never launched has no
// finalize tail, so its failure reports the runtime quiesced itself and the
// delegate takes its next start.
func TestDelegateControllerAUnlaunchedGenerationLeavesTheDelegateStartable(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 2, 2)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	started, runtime := commitAttachedDelegateControllerStart(t, c, "dlg_target")
	if _, err := c.AdmitStartInput(started.lease, func() error { return nil }); err != nil {
		t.Fatalf("AdmitStartInput: %v", err)
	}
	if _, err := c.finishUnlaunchedGeneration(started.lease, runtime, errors.New("plans failed")); err != nil {
		t.Fatalf("finishUnlaunchedGeneration: %v", err)
	}
	if got := delegateAggregateSnapshot(t, c, "dlg_target"); got.LatestOutcome == nil || got.LatestOutcome.Reason != "launch_failed" {
		t.Fatalf("latest outcome = %+v, want launch_failed", got.LatestOutcome)
	}
	assertStartAdmitted(t, c, "dlg_target", "after an unlaunched generation failed")
}
