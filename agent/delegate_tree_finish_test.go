package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/serf/agent/internal/delegatestore"
)

func (c *delegateTreeController) prepareSettlementForTest(lease delegateLease, packet *delegatestore.TerminalPacket) (bool, delegateMutationPlans, error) {
	claim, continueRun, err := c.BeginSettlement(lease)
	if err != nil || continueRun {
		return continueRun, delegateMutationPlans{}, err
	}
	plans, err := c.CompleteSettlement(claim, packet)
	return false, plans, err
}

func TestDelegateControllerNormalSettlementDefersEarlierSteer(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	attachDelegateSteerRuntime(t, c, "dlg_target", afero.NewMemMapFs())
	lease := delegateLease{delegateID: "dlg_target", generation: 1}
	if _, err := c.Steer(context.Background(), rootDelegateActor("root-session"), "dlg_target", "earlier"); err != nil {
		t.Fatalf("Steer: %v", err)
	}

	continueRun, plans, err := c.prepareSettlementForTest(lease, nil)
	if err != nil {
		t.Fatalf("BeginSettlement: %v", err)
	}
	if !continueRun || len(plans.updates) != 0 || c.durable["dlg_target"].Phase != delegatestore.PhaseRunning || c.durable["dlg_target"].PreparedTerminal != nil {
		t.Fatalf("settlement with earlier steer = continue:%t plans:%#v aggregate:%#v", continueRun, plans, c.durable["dlg_target"])
	}
}

func TestDelegateControllerCommunicateSettlementDefersEarlierSteer(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	attachDelegateSteerRuntime(t, c, "dlg_target", afero.NewMemMapFs())
	lease := delegateLease{delegateID: "dlg_target", generation: 1}
	if _, err := c.Steer(context.Background(), rootDelegateActor("root-session"), "dlg_target", "earlier"); err != nil {
		t.Fatalf("Steer: %v", err)
	}
	packet := delegateControllerReportedPacket("must defer")

	continueRun, plans, err := c.prepareSettlementForTest(lease, &packet)
	if err != nil {
		t.Fatalf("BeginSettlement: %v", err)
	}
	if !continueRun || len(plans.updates) != 0 || c.durable["dlg_target"].PreparedTerminal != nil {
		t.Fatalf("communicate settlement with earlier steer = continue:%t plans:%#v aggregate:%#v", continueRun, plans, c.durable["dlg_target"])
	}
}

func TestDelegateControllerSettlementClaimFencesNewWorkUntilPreparation(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	attachDelegateSteerRuntime(t, c, "dlg_target", afero.NewMemMapFs())
	lease := delegateLease{delegateID: "dlg_target", generation: 1}

	claim, continueRun, err := c.BeginSettlement(lease)
	if err != nil {
		t.Fatalf("BeginSettlement: %v", err)
	}
	if claim == nil || continueRun {
		t.Fatalf("BeginSettlement = claim:%#v continue:%t, want claim", claim, continueRun)
	}
	aggregate := c.durable[lease.delegateID]
	if aggregate.Phase != delegatestore.PhaseRunning || aggregate.PreparedTerminal != nil {
		t.Fatalf("claimed settlement changed durable state: %#v", aggregate)
	}
	if _, err := c.BeginSteerPersistence(rootDelegateActor("root-session"), lease.delegateID); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("BeginSteerPersistence after settlement claim error = %v, want target busy", err)
	}
	if _, err := c.BeginModelRequest(lease); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("BeginModelRequest after settlement claim error = %v, want target busy", err)
	}
	packet := delegateControllerReportedPacket("sampled after cleanup")
	plans, err := c.CompleteSettlement(claim, &packet)
	if err != nil {
		t.Fatalf("CompleteSettlement: %v", err)
	}
	aggregate = c.durable[lease.delegateID]
	if len(plans.updates) != 1 || aggregate.Phase != delegatestore.PhaseSettling || !reflect.DeepEqual(aggregate.PreparedTerminal, &packet) {
		t.Fatalf("completed settlement = plans:%#v aggregate:%#v", plans, aggregate)
	}
}

func TestDelegateControllerNormalSettlementPreparesMissingTerminal(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	lease := delegateLease{delegateID: "dlg_target", generation: 1}

	continueRun, plans, err := c.prepareSettlementForTest(lease, nil)
	if err != nil {
		t.Fatalf("BeginSettlement: %v", err)
	}
	aggregate := c.durable["dlg_target"]
	if continueRun || len(plans.updates) != 1 || aggregate.Phase != delegatestore.PhaseSettling || aggregate.PreparedTerminal == nil || aggregate.PreparedTerminal.Kind != delegatestore.PacketTerminalError {
		t.Fatalf("missing-terminal settlement = continue:%t plans:%#v aggregate:%#v", continueRun, plans, aggregate)
	}
	if got := string(aggregate.PreparedTerminal.Message); got != `"delegate completed without an accepted communicate result"` {
		t.Fatalf("missing-terminal message = %s", got)
	}
}

func TestDelegateControllerCrashAfterNormalSettlementFinishesPreparedOnce(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	lease := delegateLease{delegateID: "dlg_target", generation: 1}
	if _, _, err := c.prepareSettlementForTest(lease, nil); err != nil {
		t.Fatalf("BeginSettlement: %v", err)
	}
	if err := c.store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := delegatestore.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restarted, err := openDelegateTreeController(delegateTreeControllerConfig{store: reopened, rootSessionID: "root-session", now: c.now})
	if err != nil {
		t.Fatalf("openDelegateTreeController: %v", err)
	}
	if _, err := restarted.Reconcile(emptyDelegateReconcileEvidence(restarted)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	aggregate := restarted.durable["dlg_target"]
	if aggregate.Phase != delegatestore.PhaseIdle || aggregate.CurrentRunOpen || aggregate.PreparedTerminal != nil || len(aggregate.PendingDeliveries) != 1 {
		t.Fatalf("reconciled aggregate = %#v", aggregate)
	}
	raw := readDelegateControllerFile(t, path)
	if got := bytes.Count(raw, []byte(`"delegate_terminal_prepared"`)); got != 1 {
		t.Fatalf("terminal-prepared event count = %d, want 1\n%s", got, raw)
	}
}

func TestDelegateControllerCrashAfterReportedSettlementPreservesPacket(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	lease := delegateLease{delegateID: "dlg_target", generation: 1}
	want := delegateControllerReportedPacket("accepted")
	if _, _, err := c.prepareSettlementForTest(lease, &want); err != nil {
		t.Fatalf("BeginSettlement: %v", err)
	}
	if err := c.store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := delegatestore.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restarted, err := openDelegateTreeController(delegateTreeControllerConfig{store: reopened, rootSessionID: "root-session", now: c.now})
	if err != nil {
		t.Fatalf("openDelegateTreeController: %v", err)
	}
	if _, err := restarted.Reconcile(emptyDelegateReconcileEvidence(restarted)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	aggregate := restarted.durable["dlg_target"]
	if len(aggregate.PendingDeliveries) != 1 || !reflect.DeepEqual(aggregate.PendingDeliveries[0].Packet, want) {
		t.Fatalf("reported crash delivery = %#v, want %#v", aggregate.PendingDeliveries, want)
	}
}

func TestDelegateControllerSettlingReportedPacketOverridesLaterRuntimeError(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	lease := delegateLease{delegateID: "dlg_target", generation: 1}
	wantPacket := delegateControllerReportedPacket("accepted before runtime error")
	if _, _, err := c.prepareSettlementForTest(lease, &wantPacket); err != nil {
		t.Fatalf("BeginSettlement: %v", err)
	}
	endedAt := time.Unix(250, 0).UTC()
	misleadingPacket := delegateTerminalErrorPacket("late runtime error")
	plans, err := c.FinishGeneration(lease, delegateFinish{
		outcome:     delegatestore.OutcomeFailed,
		disposition: delegatestore.DispositionTerminalError,
		reason:      "late_runtime_error",
		packet:      &misleadingPacket,
		endedAt:     endedAt,
	})
	if err != nil {
		t.Fatalf("FinishGeneration: %v", err)
	}
	aggregate := c.durable["dlg_target"]
	if aggregate.LatestOutcome == nil || aggregate.LatestOutcome.Status != delegatestore.OutcomeCompleted || aggregate.LatestOutcome.Reason != "" || !aggregate.LatestOutcome.EndedAt.Equal(endedAt) {
		t.Fatalf("reported prepared outcome = %#v, want completed at supplied end time", aggregate.LatestOutcome)
	}
	if len(plans.deliveries) != 1 || len(aggregate.PendingDeliveries) != 1 || !reflect.DeepEqual(aggregate.PendingDeliveries[0].Packet, wantPacket) {
		t.Fatalf("reported prepared delivery plans=%#v pending=%#v", plans, aggregate.PendingDeliveries)
	}
	if finish := latestDelegateControllerRunFinished(t, c, "dlg_target"); finish.Disposition != delegatestore.DispositionReported {
		t.Fatalf("reported prepared disposition = %q", finish.Disposition)
	}
}

func TestDelegateControllerSettlingMissingPacketOverridesMisleadingFinish(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	lease := delegateLease{delegateID: "dlg_target", generation: 1}
	if _, _, err := c.prepareSettlementForTest(lease, nil); err != nil {
		t.Fatalf("BeginSettlement: %v", err)
	}
	endedAt := time.Unix(251, 0).UTC()
	plans, err := c.FinishGeneration(lease, delegateFinish{
		outcome:     delegatestore.OutcomeCompleted,
		disposition: delegatestore.DispositionReported,
		reason:      "misleading_success",
		packet:      &delegatestore.TerminalPacket{Kind: delegatestore.PacketReported, Message: json.RawMessage(`"ignore me"`)},
		endedAt:     endedAt,
	})
	if err != nil {
		t.Fatalf("FinishGeneration: %v", err)
	}
	aggregate := c.durable["dlg_target"]
	if aggregate.LatestOutcome == nil || aggregate.LatestOutcome.Status != delegatestore.OutcomeFailed || aggregate.LatestOutcome.Reason != "missing_terminal" || !aggregate.LatestOutcome.EndedAt.Equal(endedAt) {
		t.Fatalf("missing prepared outcome = %#v", aggregate.LatestOutcome)
	}
	if len(plans.deliveries) != 1 || len(aggregate.PendingDeliveries) != 1 || !delegateIsMissingTerminalPacket(aggregate.PendingDeliveries[0].Packet) {
		t.Fatalf("missing prepared delivery plans=%#v pending=%#v", plans, aggregate.PendingDeliveries)
	}
	if finish := latestDelegateControllerRunFinished(t, c, "dlg_target"); finish.Disposition != delegatestore.DispositionTerminalError {
		t.Fatalf("missing prepared disposition = %q", finish.Disposition)
	}
}

func TestDelegateControllerRestartFinishesEachPreparedPacketShape(t *testing.T) {
	tests := []struct {
		name            string
		packet          *delegatestore.TerminalPacket
		wantPacket      delegatestore.TerminalPacket
		wantOutcome     delegatestore.OutcomeStatus
		wantReason      string
		wantDisposition delegatestore.RunDisposition
	}{
		{
			name: "reported",
			packet: func() *delegatestore.TerminalPacket {
				packet := delegateControllerReportedPacket("accepted")
				return &packet
			}(),
			wantPacket:      delegateControllerReportedPacket("accepted"),
			wantOutcome:     delegatestore.OutcomeCompleted,
			wantDisposition: delegatestore.DispositionReported,
		},
		{
			name:            "missing terminal",
			wantPacket:      delegateMissingTerminalPacket(),
			wantOutcome:     delegatestore.OutcomeFailed,
			wantReason:      "missing_terminal",
			wantDisposition: delegatestore.DispositionTerminalError,
		},
		{
			name: "terminal error",
			packet: func() *delegatestore.TerminalPacket {
				packet := delegateTerminalErrorPacket("provider rejected request")
				return &packet
			}(),
			wantPacket:      delegateTerminalErrorPacket("provider rejected request"),
			wantOutcome:     delegatestore.OutcomeFailed,
			wantReason:      "terminal_error",
			wantDisposition: delegatestore.DispositionTerminalError,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c, path := newDelegateControllerTestHarness(t, 1, 1)
			seedDelegateControllerRunning(t, c, "dlg_target", "")
			lease := delegateLease{delegateID: "dlg_target", generation: 1}
			if _, _, err := c.prepareSettlementForTest(lease, test.packet); err != nil {
				t.Fatalf("BeginSettlement: %v", err)
			}
			if err := c.store.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			reopened, err := delegatestore.Open(path)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			restarted, err := openDelegateTreeController(delegateTreeControllerConfig{store: reopened, rootSessionID: "root-session", now: c.now})
			if err != nil {
				t.Fatalf("openDelegateTreeController: %v", err)
			}
			if _, err := restarted.Reconcile(emptyDelegateReconcileEvidence(restarted)); err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			aggregate := restarted.durable["dlg_target"]
			if aggregate.LatestOutcome == nil || aggregate.LatestOutcome.Status != test.wantOutcome || aggregate.LatestOutcome.Reason != test.wantReason {
				t.Fatalf("restart outcome = %#v, want status=%q reason=%q", aggregate.LatestOutcome, test.wantOutcome, test.wantReason)
			}
			if len(aggregate.PendingDeliveries) != 1 || !reflect.DeepEqual(aggregate.PendingDeliveries[0].Packet, test.wantPacket) {
				t.Fatalf("restart delivery = %#v, want packet %#v", aggregate.PendingDeliveries, test.wantPacket)
			}
			if finish := latestDelegateControllerRunFinished(t, restarted, "dlg_target"); finish.Disposition != test.wantDisposition {
				t.Fatalf("restart disposition = %q, want %q", finish.Disposition, test.wantDisposition)
			}
		})
	}
}

func TestDelegateControllerFatalFinishPreparesAndFinishesAtomically(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	before := bytes.Count(readDelegateControllerFile(t, path), []byte{'\n'})

	plans, err := c.FinishGeneration(delegateLease{delegateID: "dlg_target", generation: 1}, delegateFinish{
		outcome: delegatestore.OutcomeFailed,
		reason:  "provider_failed",
	})
	if err != nil {
		t.Fatalf("FinishGeneration: %v", err)
	}
	raw := readDelegateControllerFile(t, path)
	after := bytes.Count(raw, []byte{'\n'})
	if after != before+1 || !bytes.Contains(raw, []byte(`"delegate_terminal_prepared"`)) || !bytes.Contains(raw, []byte(`"delegate_run_finished"`)) {
		t.Fatalf("fatal finish was not one prepare+finish batch:\n%s", raw)
	}
	if len(plans.updates) != 1 || len(plans.deliveries) != 1 {
		t.Fatalf("fatal finish plans = %#v", plans)
	}
}

func TestDelegateControllerTerminalPreparedAppendFailureKeepsRunning(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	attachDelegateSteerRuntime(t, c, "dlg_target", afero.NewMemMapFs())
	lease := delegateLease{delegateID: "dlg_target", generation: 1}
	claim, continueRun, err := c.BeginSettlement(lease)
	if err != nil || continueRun || claim == nil {
		t.Fatalf("BeginSettlement = claim:%#v continue:%t err:%v", claim, continueRun, err)
	}
	before := readDelegateControllerFile(t, path)
	if err := c.store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	plans, err := c.CompleteSettlement(claim, nil)
	if err == nil {
		t.Fatal("CompleteSettlement succeeded after store close")
	}
	aggregate := c.durable["dlg_target"]
	if len(plans.updates) != 0 || aggregate.Phase != delegatestore.PhaseRunning || aggregate.PreparedTerminal != nil {
		t.Fatalf("failed settlement mutated state = plans:%#v aggregate:%#v", plans, aggregate)
	}
	if got := readDelegateControllerFile(t, path); !bytes.Equal(got, before) {
		t.Fatalf("failed settlement changed bytes:\n got %q\nwant %q", got, before)
	}
	c.mu.Lock()
	claimRetained := c.hasSettlementClaimLocked(lease)
	c.mu.Unlock()
	if !claimRetained {
		t.Fatal("failed settlement released its admission fence")
	}
	if _, err := c.BeginSteerPersistence(rootDelegateActor("root-session"), lease.delegateID); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("BeginSteerPersistence after failed settlement error = %v, want target busy", err)
	}
	if _, err := c.BeginModelRequest(lease); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("BeginModelRequest after failed settlement error = %v, want target busy", err)
	}

	reopened, err := delegatestore.Open(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	c.mu.Lock()
	c.store = reopened
	c.mu.Unlock()
	result, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), lease.delegateID)
	if err != nil {
		t.Fatalf("StopSubtree after failed settlement: %v", err)
	}
	c.mu.Lock()
	claimRetained = c.hasSettlementClaimLocked(lease)
	c.mu.Unlock()
	if !claimRetained {
		t.Fatal("durable stop released failed settlement claim before recovery fsync")
	}
	if _, err := c.Reconcile(emptyDelegateReconcileEvidence(c)); err != nil {
		t.Fatalf("Reconcile failed settlement: %v", err)
	}
	c.mu.Lock()
	claimRetained = c.hasSettlementClaimLocked(lease)
	c.mu.Unlock()
	if claimRetained {
		t.Fatal("successful stopped finish retained failed settlement claim")
	}
	if _, err := c.Reconcile(emptyDelegateReconcileEvidence(c)); err != nil {
		t.Fatalf("Reconcile stop completion: %v", err)
	}
	select {
	case <-result.done:
	default:
		t.Fatal("stop remained pending after failed settlement recovery")
	}
}

func TestDelegateControllerStopOverridesPreparedNormalPacket(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	lease := delegateLease{delegateID: "dlg_target", generation: 1}
	packet := delegateControllerReportedPacket("normal")
	if _, _, err := c.prepareSettlementForTest(lease, &packet); err != nil {
		t.Fatalf("BeginSettlement: %v", err)
	}
	requestSeq := appendDelegateControllerStopRequest(t, c, "dlg_target")

	plans, err := c.FinishGeneration(lease, delegateFinish{outcome: delegatestore.OutcomeCompleted, disposition: delegatestore.DispositionReported})
	if err != nil {
		t.Fatalf("FinishGeneration: %v", err)
	}
	aggregate := c.durable["dlg_target"]
	if aggregate.Phase != delegatestore.PhaseStopping || aggregate.PendingStopSeq != requestSeq || aggregate.LatestOutcome == nil || aggregate.LatestOutcome.Status != delegatestore.OutcomeStopped || !reflect.DeepEqual(aggregate.PreparedTerminal, &packet) {
		t.Fatalf("stop-selected finish aggregate = %#v", aggregate)
	}
	if len(plans.deliveries) != 0 || len(aggregate.PendingDeliveries) != 1 || aggregate.PendingDeliveries[0].Packet.Kind != delegatestore.PacketTerminalError {
		t.Fatalf("stop-selected delivery state plans=%#v aggregate=%#v", plans, aggregate)
	}
}

func TestDelegateControllerStoppedFinishRemainsStoppingWithDiagnostic(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	lease := delegateLease{delegateID: "dlg_target", generation: 1}
	packet := delegateControllerReportedPacket("diagnostic")
	if _, _, err := c.prepareSettlementForTest(lease, &packet); err != nil {
		t.Fatalf("BeginSettlement: %v", err)
	}
	appendDelegateControllerStopRequest(t, c, "dlg_target")
	if _, err := c.FinishGeneration(lease, delegateFinish{outcome: delegatestore.OutcomeCancelled, reason: "cancelled"}); err != nil {
		t.Fatalf("FinishGeneration: %v", err)
	}
	aggregate := c.durable["dlg_target"]
	if aggregate.Phase != delegatestore.PhaseStopping || aggregate.CurrentRunOpen || !reflect.DeepEqual(aggregate.PreparedTerminal, &packet) {
		t.Fatalf("stopped finish discarded diagnostic or left stopping: %#v", aggregate)
	}
}

func TestDelegateControllerRestartStoppingFinishUsesCanonicalPacket(t *testing.T) {
	live, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, live, "dlg_live", "")
	liveLease := delegateLease{delegateID: "dlg_live", generation: 1}
	liveDiagnostic := delegateControllerReportedPacket("live diagnostic")
	if _, _, err := live.prepareSettlementForTest(liveLease, &liveDiagnostic); err != nil {
		t.Fatalf("live BeginSettlement: %v", err)
	}
	appendDelegateControllerStopRequest(t, live, "dlg_live")
	if _, err := live.FinishGeneration(liveLease, delegateFinish{outcome: delegatestore.OutcomeCompleted}); err != nil {
		t.Fatalf("live FinishGeneration: %v", err)
	}
	liveAggregate := live.durable["dlg_live"]
	if len(liveAggregate.PendingDeliveries) != 1 {
		t.Fatalf("live pending deliveries = %#v, want one", liveAggregate.PendingDeliveries)
	}

	restarting, path := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, restarting, "dlg_restart", "")
	restartLease := delegateLease{delegateID: "dlg_restart", generation: 1}
	restartDiagnostic := delegateControllerReportedPacket("restart diagnostic")
	if _, _, err := restarting.prepareSettlementForTest(restartLease, &restartDiagnostic); err != nil {
		t.Fatalf("restart BeginSettlement: %v", err)
	}
	appendDelegateControllerStopRequest(t, restarting, "dlg_restart")
	if err := restarting.store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := delegatestore.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restarted, err := openDelegateTreeController(delegateTreeControllerConfig{store: reopened, rootSessionID: "root-session", now: restarting.now})
	if err != nil {
		t.Fatalf("openDelegateTreeController: %v", err)
	}
	evidence := emptyDelegateReconcileEvidence(restarted)
	evidence.attention["dlg_restart"] = []string{"hold-stop-open"}
	if _, err := restarted.Reconcile(evidence); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	restartedAggregate := restarted.durable["dlg_restart"]
	if len(restartedAggregate.PendingDeliveries) != 1 {
		t.Fatalf("restart pending deliveries = %#v, want one", restartedAggregate.PendingDeliveries)
	}
	if got, want := restartedAggregate.PendingDeliveries[0].Packet, liveAggregate.PendingDeliveries[0].Packet; !reflect.DeepEqual(got, want) {
		t.Fatalf("restart stopped packet = %#v, want live canonical packet %#v", got, want)
	}
	if !reflect.DeepEqual(liveAggregate.PreparedTerminal, &liveDiagnostic) || !reflect.DeepEqual(restartedAggregate.PreparedTerminal, &restartDiagnostic) {
		t.Fatalf("prepared diagnostics changed: live=%#v restart=%#v", liveAggregate.PreparedTerminal, restartedAggregate.PreparedTerminal)
	}
	for name, aggregate := range map[string]*delegatestore.Aggregate{"live": liveAggregate, "restart": restartedAggregate} {
		if aggregate.Phase != delegatestore.PhaseStopping || aggregate.CurrentRunOpen || aggregate.LatestOutcome == nil || aggregate.LatestOutcome.Status != delegatestore.OutcomeStopped || aggregate.LatestOutcome.Reason != "stopped_by_parent" {
			t.Fatalf("%s stopped aggregate = %#v", name, aggregate)
		}
	}
}

func TestDelegateControllerStopCompletedClearsPreparedDiagnostic(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	lease := delegateLease{delegateID: "dlg_target", generation: 1}
	packet := delegateControllerReportedPacket("diagnostic")
	if _, _, err := c.prepareSettlementForTest(lease, &packet); err != nil {
		t.Fatalf("BeginSettlement: %v", err)
	}
	requestSeq := appendDelegateControllerStopRequest(t, c, "dlg_target")
	if _, err := c.FinishGeneration(lease, delegateFinish{outcome: delegatestore.OutcomeCancelled, reason: "cancelled"}); err != nil {
		t.Fatalf("FinishGeneration: %v", err)
	}
	c.mu.Lock()
	_, err := c.appendLocked(delegatestore.Event{
		Kind:       delegatestore.EventDelegateSubtreeStopCompleted,
		DelegateID: "dlg_target",
		SubtreeStopCompleted: &delegatestore.SubtreeStopCompleted{
			RequestSeq: requestSeq,
		},
	})
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("append stop completion: %v", err)
	}
	aggregate := c.durable["dlg_target"]
	if aggregate.Phase != delegatestore.PhaseIdle || aggregate.PreparedTerminal != nil || aggregate.PendingStopSeq != 0 {
		t.Fatalf("stop-completed aggregate = %#v", aggregate)
	}
}

func TestDelegateControllerAttentionCompletedNoActionStaysPrivate(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	c.mu.Lock()
	_, err := c.appendLocked(
		delegateControllerCreatedEvent("dlg_target", ""),
		delegateControllerRunStartedEvent("dlg_target", 1, delegatestore.TriggerAttention, c.now()),
	)
	if err == nil {
		c.live["dlg_target"] = &delegateLiveState{binding: &delegateRuntimeBinding{
			lease:  delegateLease{delegateID: "dlg_target", generation: 1},
			cancel: func() {},
			ready:  true,
		}}
		c.drivesInUse = 1
	}
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("seed attention run: %v", err)
	}

	plans, err := c.FinishGeneration(delegateLease{delegateID: "dlg_target", generation: 1}, delegateFinish{
		outcome:     delegatestore.OutcomeCompleted,
		disposition: delegatestore.DispositionCompletedNoAction,
		reason:      "attention_consumed_without_report",
	})
	if err != nil {
		t.Fatalf("FinishGeneration: %v", err)
	}
	aggregate := c.durable["dlg_target"]
	if len(plans.deliveries) != 0 || len(aggregate.PendingDeliveries) != 0 || aggregate.LatestOutcome == nil || aggregate.LatestOutcome.Status != delegatestore.OutcomeCompleted || string(aggregate.LatestOutcome.Status) == string(delegatestore.DispositionCompletedNoAction) {
		t.Fatalf("completed-no-action leaked publicly: plans=%#v aggregate=%#v", plans, aggregate)
	}
}

func TestDelegateControllerOwnerInputWithoutCommunicateFailsMissingTerminal(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	lease := delegateLease{delegateID: "dlg_target", generation: 1}
	if _, _, err := c.prepareSettlementForTest(lease, nil); err != nil {
		t.Fatalf("BeginSettlement: %v", err)
	}
	plans, err := c.FinishGeneration(lease, delegateFinish{outcome: delegatestore.OutcomeCompleted})
	if err != nil {
		t.Fatalf("FinishGeneration: %v", err)
	}
	aggregate := c.durable["dlg_target"]
	if aggregate.LatestOutcome == nil || aggregate.LatestOutcome.Status != delegatestore.OutcomeFailed || aggregate.LatestOutcome.Reason != "missing_terminal" || len(aggregate.PendingDeliveries) != 1 || aggregate.PendingDeliveries[0].Packet.Kind != delegatestore.PacketTerminalError || len(plans.deliveries) != 1 {
		t.Fatalf("missing-terminal finish = plans:%#v aggregate:%#v", plans, aggregate)
	}
}

func appendDelegateControllerStopRequest(t *testing.T, c *delegateTreeController, delegateID string) uint64 {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	appended, err := c.appendLocked(delegatestore.Event{
		Kind:       delegatestore.EventDelegateSubtreeStopRequested,
		DelegateID: delegateID,
		SubtreeStopRequested: &delegatestore.SubtreeStopRequested{
			TargetDelegateID: delegateID,
		},
	})
	if err != nil {
		t.Fatalf("append stop request: %v", err)
	}
	return appended[0].Seq
}

func delegateControllerReportedPacket(message string) delegatestore.TerminalPacket {
	valid := false
	return delegatestore.TerminalPacket{
		Kind:                   delegatestore.PacketReported,
		Message:                json.RawMessage(`"` + message + `"`),
		StructuredResult:       json.RawMessage(`null`),
		StructuredResultValid:  &valid,
		StructuredResultReason: "schema mismatch",
		Warnings:               []string{"warning"},
		Metadata:               json.RawMessage(`{"source":"test"}`),
	}
}

func latestDelegateControllerRunFinished(t *testing.T, c *delegateTreeController, delegateID string) delegatestore.RunFinished {
	t.Helper()
	events, err := c.store.Load()
	if err != nil {
		t.Fatalf("load delegate events: %v", err)
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].DelegateID == delegateID && events[i].RunFinished != nil {
			return *events[i].RunFinished
		}
	}
	t.Fatalf("no run-finished event for %s", delegateID)
	return delegatestore.RunFinished{}
}
