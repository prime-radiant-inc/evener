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

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

// finalizingDelegate is a stable delegate whose first generation is held
// part-way through its finalize tail: its result is being handed to its
// parent and the controller has not yet released the delegate for a start.
// release lets it finish.
type finalizingDelegate struct {
	s          *Session
	delegateID string
	child      *subagent
	release    func()
}

// holdDelegateFinalizing starts a delegate and holds its first generation in
// that window: the parent's delivery hook runs while the tail announces the
// generation, before it releases the finalization, so holding it there pins
// the window with no timing.
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
	create := executeDelegateTool(s, "call_create", "delegate", map[string]any{"prompt": "Find the race.", "name": "settle-race"})
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
// the call's id in its context. It never fails the test itself, so a test may
// call it off its own goroutine.
func executeDelegateTool(s *Session, id, name string, args map[string]any) tool.ExecResult {
	// The arguments are plain maps of strings and numbers, which always
	// marshal.
	raw, _ := json.Marshal(args)
	ctx := context.WithValue(context.Background(), ctxToolCallID, id)
	return s.reg.ExecuteCall(ctx, s.currentEnv(), llm.ToolCallData{ID: id, Name: name, Arguments: raw})
}

// sendTaken reports whether a delegate_send result took the send: it started
// a generation, or (with a wait) the generation it started completed.
func sendTaken(res tool.ExecResult) bool {
	return !res.IsError && (strings.Contains(res.Output, "started") || strings.Contains(res.Output, "completed"))
}

// A send issued the moment the parent is handed a finished generation's
// result, while the child's finalize tail is still announcing it, waits out
// the tail's release and is taken. Before, it was refused (target_busy), or
// earlier still recorded as a failed start, though the parent had just been
// told the delegate was done.
func TestDelegateSendAtTheResultIsTaken(t *testing.T) {
	t.Parallel()
	for _, wait := range []int{0, 60_000} {
		held := holdDelegateFinalizing(t)
		before := delegateAggregateSnapshot(t, held.s.delegateController, held.delegateID)
		waiting := make(chan struct{})
		var waitingOnce sync.Once
		updateSessionTestConfig(held.s, func(cfg *testConfig) {
			cfg.delegateSendAwaitingFinalization = func() { waitingOnce.Do(func() { close(waiting) }) }
		})
		args := map[string]any{"to": held.delegateID, "message": "Is drain ordering fine?"}
		if wait > 0 {
			args["max_wait_ms"] = wait
		}
		result := make(chan tool.ExecResult, 1)
		go func() { result <- executeDelegateTool(held.s, "call_send", "delegate_send", args) }()
		select {
		case <-waiting:
			held.release()
		case res := <-result:
			t.Fatalf("max_wait_ms %d: the send returned %q (error %v) without waiting for the release", wait, res.Output, res.IsError)
		// TRIPWIRE: a hang guard only; the send reaches its wait within
		// milliseconds.
		case <-time.After(30 * time.Second):
			t.Fatal("the send never reached its wait")
		}
		var res tool.ExecResult
		select {
		case res = <-result:
		// TRIPWIRE: a hang guard only; the released tail lets the send
		// through within milliseconds.
		case <-time.After(30 * time.Second):
			t.Fatal("the send never finished after the release")
		}
		if !sendTaken(res) {
			t.Fatalf("max_wait_ms %d: the send at the result = %q (error %v), want it taken", wait, res.Output, res.IsError)
		}
		if got := delegateAggregateSnapshot(t, held.s.delegateController, held.delegateID).Generation; got != before.Generation+1 {
			t.Fatalf("max_wait_ms %d: generation after the taken send = %d, want %d", wait, got, before.Generation+1)
		}
	}
}

// A tail that never releases can't hang a send: past the wait ceiling the
// send is refused cleanly, committing nothing and leaving the delegate's
// latest outcome as its run reported it.
func TestDelegateSendThatOutwaitsTheReleaseIsARefusal(t *testing.T) {
	t.Parallel()
	for _, wait := range []int{0, 60_000} {
		held := holdDelegateFinalizing(t)
		ceiling := 50 * time.Millisecond
		updateSessionTestConfig(held.s, func(cfg *testConfig) { cfg.delegateFinalizationWaitCeiling = &ceiling })
		before := delegateAggregateSnapshot(t, held.s.delegateController, held.delegateID)
		args := map[string]any{"to": held.delegateID, "message": "Is drain ordering fine?"}
		if wait > 0 {
			args["max_wait_ms"] = wait
		}
		res := executeDelegateTool(held.s, "call_send", "delegate_send", args)
		if !res.IsError || !strings.Contains(res.Output, "target_busy") {
			t.Fatalf("max_wait_ms %d: a send past the ceiling = %q (error %v), want a target_busy refusal", wait, res.Output, res.IsError)
		}
		after := delegateAggregateSnapshot(t, held.s.delegateController, held.delegateID)
		if after.Generation != before.Generation || !reflect.DeepEqual(after.LatestOutcome, before.LatestOutcome) {
			t.Fatalf("max_wait_ms %d: the refused send changed the delegate: generation %d → %d, outcome %+v → %+v", wait, before.Generation, after.Generation, before.LatestOutcome, after.LatestOutcome)
		}
		held.release()
	}
}

// A busy child the controller can't see (here: the controller has released
// the finalization, and the child is marked driving, standing in for a drive
// in flight on an idle child) is refused before anything is committed, by
// the send's own look at the child.
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
	assertStartAdmitted(t, held.s.delegateController, held.delegateID, "once the controller has released the finalization")
	held.child.mu.Lock()
	held.child.driving = true
	held.child.mu.Unlock()
	defer func() {
		held.child.mu.Lock()
		held.child.driving = false
		held.child.mu.Unlock()
	}()
	before := delegateAggregateSnapshot(t, held.s.delegateController, held.delegateID)
	res := executeDelegateTool(held.s, "call_send", "delegate_send", map[string]any{"to": held.delegateID, "message": "Is drain ordering fine?"})
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
	c.setResidentRuntimeLocked(c.live["dlg_target"], &Session{})
	c.mu.Unlock()
	assertStartAdmitted(t, c, "dlg_target", "once another runtime is resident")
}

// A generation whose input was admitted but whose run never launched has no
// finalize tail: its failure hands back the generation's announcements for
// the caller to execute, and its release lets the delegate take its next
// start.
func TestDelegateControllerAUnlaunchedGenerationLeavesTheDelegateStartable(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 2, 2)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	started, runtime := commitAttachedDelegateControllerStart(t, c, "dlg_target")
	if _, err := c.AdmitStartInput(started.lease, func() error { return nil }); err != nil {
		t.Fatalf("AdmitStartInput: %v", err)
	}
	plans, err := c.finishUnlaunchedGeneration(started.lease, errors.New("plans failed"))
	if err != nil {
		t.Fatalf("finishUnlaunchedGeneration: %v", err)
	}
	if len(plans.updates) == 0 {
		t.Fatal("the unlaunched generation's plans carry no idle snapshot to announce")
	}
	assertStartRefused(t, c, "dlg_target", "before the unlaunched generation is released")
	if err := c.ReportFinalizationQuiesced(started.lease, runtime); err != nil {
		t.Fatalf("ReportFinalizationQuiesced: %v", err)
	}
	if got := delegateAggregateSnapshot(t, c, "dlg_target"); got.LatestOutcome == nil || got.LatestOutcome.Reason != "launch_failed" {
		t.Fatalf("latest outcome = %+v, want launch_failed", got.LatestOutcome)
	}
	assertStartAdmitted(t, c, "dlg_target", "after an unlaunched generation failed")
}

// A finalize tail whose announcement fails still releases the delegate: the
// release doesn't depend on the result reaching anyone.
func TestDelegateTailReleasesADelegateWhoseAnnouncementFails(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 2, 2)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	runtime := sessionOnHarness(t, c)
	reservation, err := c.ReserveStart(rootDelegateActor(c.rootSessionID), "dlg_target")
	if err != nil {
		t.Fatalf("ReserveStart: %v", err)
	}
	started, err := c.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart: %v", err)
	}
	if err := c.AttachRuntime(started.lease, runtime); err != nil {
		t.Fatalf("AttachRuntime: %v", err)
	}
	if _, err := c.AdmitStartInput(started.lease, func() error { return nil }); err != nil {
		t.Fatalf("AdmitStartInput: %v", err)
	}
	if _, err := c.FinishGeneration(started.lease, delegateFinish{outcome: delegatestore.OutcomeCompleted, reason: "completed"}); err != nil {
		t.Fatalf("FinishGeneration: %v", err)
	}
	assertStartRefused(t, c, "dlg_target", "before the tail announces")
	// A delivery with no controller fails outright (stale lease).
	failing := delegateMutationPlans{deliveries: []delegateDeliveryPlan{{receiver: committedCallerDeliveryReceiver{}, deliveryID: "dlg_target/delivery/1"}}}
	if err := runtime.executeDelegateMutationPlans(failing); err == nil {
		t.Fatal("the failing announcement didn't fail")
	}
	(&subagent{sess: runtime}).announceFinishedGeneration(started.lease, failing)
	assertStartAdmitted(t, c, "dlg_target", "after the tail released, though announcing failed")
}

// While a finished generation is still finalizing, its delegate is not
// eligible for an attention wake either, so an attention successor can't
// start ahead of the result; the release makes it eligible again.
func TestDelegateControllerRefusesAnAttentionWakeWhileFinalizing(t *testing.T) {
	c, lease, runtime := finishedDelegateStillFinalizing(t)
	eligible := func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.delegateAttentionWakeEligibleLocked("dlg_target")
	}
	if eligible() {
		t.Fatal("an attention wake was eligible while the finished generation finalized")
	}
	if err := c.ReportFinalizationQuiesced(lease, runtime); err != nil {
		t.Fatalf("ReportFinalizationQuiesced: %v", err)
	}
	if !eligible() {
		t.Fatal("an attention wake was still refused after the release")
	}
}

// A send waiting for the release gives up when its caller does, with the
// caller's own error, and commits nothing.
func TestDelegateSendWaitingForTheReleaseStopsWithItsContext(t *testing.T) {
	t.Parallel()
	held := holdDelegateFinalizing(t)
	defer held.release()
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), ctxToolCallID, "call_send"))
	defer cancel()
	updateSessionTestConfig(held.s, func(cfg *testConfig) { cfg.delegateSendAwaitingFinalization = cancel })
	before := delegateAggregateSnapshot(t, held.s.delegateController, held.delegateID)
	raw, err := json.Marshal(map[string]any{"to": held.delegateID, "message": "Is drain ordering fine?"})
	if err != nil {
		t.Fatal(err)
	}
	res := held.s.reg.ExecuteCall(ctx, held.s.currentEnv(), llm.ToolCallData{ID: "call_send", Name: "delegate_send", Arguments: raw})
	if !res.IsError || !strings.Contains(res.Output, context.Canceled.Error()) {
		t.Fatalf("a cancelled waiting send = %q (error %v), want the cancellation", res.Output, res.IsError)
	}
	if after := delegateAggregateSnapshot(t, held.s.delegateController, held.delegateID); after.Generation != before.Generation || !reflect.DeepEqual(after.LatestOutcome, before.LatestOutcome) {
		t.Fatalf("the cancelled send changed the delegate: generation %d → %d, outcome %+v → %+v", before.Generation, after.Generation, before.LatestOutcome, after.LatestOutcome)
	}
}

// A client that sends as soon as it sees the delegate go idle
// (DELEGATE_UPDATED, from the real finalize tail) is taken: the idle snapshot
// goes out as the tail announces the generation, and the send waits out the
// release that follows.
func TestDelegateSendOnTheIdleEventIsTaken(t *testing.T) {
	t.Parallel()
	held := holdDelegateFinalizing(t)
	before := delegateAggregateSnapshot(t, held.s.delegateController, held.delegateID)
	result := make(chan tool.ExecResult, 1)
	stream := held.s.Events()
	go func() {
		for ev := range stream {
			if data, ok := ev.Data.(events.DelegateUpdatedData); ok && data.DelegateID == held.delegateID && data.Lifecycle == string(delegateLifecycleIdle) {
				result <- executeDelegateTool(held.s, "call_send", "delegate_send", map[string]any{"to": held.delegateID, "message": "Is drain ordering fine?"})
				return
			}
		}
	}()
	held.release()
	var res tool.ExecResult
	select {
	case res = <-result:
	// TRIPWIRE: a hang guard only; the idle event and the release follow
	// within milliseconds.
	case <-time.After(30 * time.Second):
		t.Fatal("no send followed the idle event")
	}
	if !sendTaken(res) {
		t.Fatalf("the send on the idle event = %q (error %v), want it taken", res.Output, res.IsError)
	}
	if got := delegateAggregateSnapshot(t, held.s.delegateController, held.delegateID).Generation; got != before.Generation+1 {
		t.Fatalf("generation after the send = %d, want %d", got, before.Generation+1)
	}
}

// A finalizing runtime that stops being the delegate's resident runtime no
// longer holds it, and a send already waiting on its release is let go at
// once rather than at the wait's ceiling.
func TestDelegateControllerReleasesAFinalizationWhoseRuntimeIsReplaced(t *testing.T) {
	c, _, _ := finishedDelegateStillFinalizing(t)
	_, err := c.ReserveStart(rootDelegateActor(c.rootSessionID), "dlg_target")
	finalizing, ok := errors.AsType[delegateFinalizingError](err)
	if !ok {
		t.Fatalf("ReserveStart while finalizing = %v, want a finalizing refusal", err)
	}
	c.mu.Lock()
	c.setResidentRuntimeLocked(c.live["dlg_target"], &Session{})
	c.mu.Unlock()
	select {
	case <-finalizing.released:
	default:
		t.Fatal("the replaced runtime's finalization was not released")
	}
	assertStartAdmitted(t, c, "dlg_target", "once the finalizing runtime was replaced")
}

// sessionOnHarness is a session whose delegate controller is the harness's,
// for driving the send's reservation directly. Its own controller goes back
// before it closes (cleanups run last-registered first), or closing would
// tear down the harness tree.
func sessionOnHarness(t *testing.T, c *delegateTreeController) *Session {
	t.Helper()
	s := newSession(t)
	own := s.delegateController
	s.delegateController = c
	t.Cleanup(func() { s.delegateController = own })
	return s
}

// finishAnotherGeneration runs one more generation of dlg_target on runtime
// through to FinishGeneration, leaving it finalizing.
func finishAnotherGeneration(t *testing.T, c *delegateTreeController, runtime *Session) delegateLease {
	t.Helper()
	reservation, err := c.ReserveStart(rootDelegateActor(c.rootSessionID), "dlg_target")
	if err != nil {
		t.Fatalf("ReserveStart successor: %v", err)
	}
	started, err := c.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart successor: %v", err)
	}
	if err := c.AttachRuntime(started.lease, runtime); err != nil {
		t.Fatalf("AttachRuntime successor: %v", err)
	}
	if _, err := c.AdmitStartInput(started.lease, func() error { return nil }); err != nil {
		t.Fatalf("AdmitStartInput successor: %v", err)
	}
	if _, err := c.FinishGeneration(started.lease, delegateFinish{outcome: delegatestore.OutcomeCompleted, reason: "completed"}); err != nil {
		t.Fatalf("FinishGeneration successor: %v", err)
	}
	return started.lease
}

// A send that waited out one finalization and finds the delegate finalizing
// again (another generation finished in the gap) waits again rather than
// refusing, within the same ceiling.
func TestDelegateSendWaitsOutAFinalizationThatFollowsTheOneItWaitedFor(t *testing.T) {
	c, first, runtime := finishedDelegateStillFinalizing(t)
	s := sessionOnHarness(t, c)
	var second delegateLease
	waits := 0
	updateSessionTestConfig(s, func(cfg *testConfig) {
		cfg.delegateSendAwaitingFinalization = func() {
			waits++
			switch waits {
			case 1:
				// Release the first generation, and let another run and
				// finish before the send reserves again.
				if err := c.ReportFinalizationQuiesced(first, runtime); err != nil {
					t.Errorf("release first: %v", err)
				}
				second = finishAnotherGeneration(t, c, runtime)
			case 2:
				if err := c.ReportFinalizationQuiesced(second, runtime); err != nil {
					t.Errorf("release second: %v", err)
				}
			}
		}
	})
	reservation, err := s.reserveStartAfterFinalization(context.Background(), rootDelegateActor(c.rootSessionID), "dlg_target")
	if err != nil {
		t.Fatalf("reserve after two finalizations: %v (waited %d times)", err, waits)
	}
	if err := c.AbortStart(reservation); err != nil {
		t.Fatalf("AbortStart: %v", err)
	}
	if waits != 2 {
		t.Fatalf("the send waited %d times, want once for each finalization", waits)
	}
}

// A release that is ready wins over a cancellation (or ceiling) that is
// ready at the same moment: the delegate is sendable, so the send reserves.
func TestDelegateSendTakesAReleaseReadyAlongsideItsCancellation(t *testing.T) {
	c, lease, runtime := finishedDelegateStillFinalizing(t)
	s := sessionOnHarness(t, c)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updateSessionTestConfig(s, func(cfg *testConfig) {
		cfg.delegateSendAwaitingFinalization = func() {
			cancel()
			if err := c.ReportFinalizationQuiesced(lease, runtime); err != nil {
				t.Errorf("release: %v", err)
			}
		}
	})
	reservation, err := s.reserveStartAfterFinalization(ctx, rootDelegateActor(c.rootSessionID), "dlg_target")
	if err != nil {
		t.Fatalf("reserve with the release and the cancellation both ready = %v, want the release taken", err)
	}
	if err := c.AbortStart(reservation); err != nil {
		t.Fatalf("AbortStart: %v", err)
	}
}
