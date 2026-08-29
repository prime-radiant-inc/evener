package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/schema"
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
	runtime := c.live[lease.delegateID].binding.runtime
	c.mu.Unlock()
	if err := c.ReportFinalizationQuiesced(lease, runtime); err != nil {
		t.Fatalf("ReportFinalizationQuiesced: %v", err)
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

func TestDelegateControllerFinishNoActionRequiresExactEligibleClaim(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	claim := eligibleDelegateNoActionClaim(t, c, "dlg_target")
	fallback := stableDelegateFinishFromRun(delegateTerminalRunInputs{result: "bare attention response"})
	prepared, err := c.prepareNoAction(claim, fallback)
	if err != nil {
		t.Fatalf("prepareNoAction: %v", err)
	}
	if !prepared {
		t.Fatal("prepareNoAction rejected exact eligible claim")
	}
	plans, err := c.FinishNoAction(claim)
	if err != nil {
		t.Fatalf("FinishNoAction: %v", err)
	}
	aggregate := c.durable["dlg_target"]
	finished := latestDelegateControllerRunFinished(t, c, "dlg_target")
	if len(plans.deliveries) != 0 || len(aggregate.PendingDeliveries) != 0 || aggregate.LatestOutcome == nil || aggregate.LatestOutcome.Status != delegatestore.OutcomeCompleted ||
		finished.Disposition != delegatestore.DispositionCompletedNoAction || finished.DeliveryID != "" || finished.Packet != nil {
		t.Fatalf("completed-no-action leaked publicly: plans=%#v aggregate=%#v", plans, aggregate)
	}
	c.mu.Lock()
	_, claimLive := c.settlementClaims[claim.token]
	live := c.live["dlg_target"]
	drives := c.drivesInUse
	c.mu.Unlock()
	if claimLive || (live != nil && live.binding != nil) || drives != 0 {
		t.Fatalf("no-action finish retained authority/capacity: claim=%t live=%#v drives=%d", claimLive, live, drives)
	}
}

func TestDelegateControllerFinishNoActionRejectsMissingStaleMismatchedAndUnreadyClaims(t *testing.T) {
	t.Run("missing preparation", func(t *testing.T) {
		c, _ := newDelegateControllerTestHarness(t, 1, 1)
		claim := eligibleDelegateNoActionClaim(t, c, "dlg_target")
		if _, err := c.FinishNoAction(claim); !errors.Is(err, errDelegateTargetBusy) {
			t.Fatalf("FinishNoAction without preparation error = %v, want busy", err)
		}
	})

	t.Run("stale and mismatched", func(t *testing.T) {
		c, _ := newDelegateControllerTestHarness(t, 1, 1)
		claim := eligibleDelegateNoActionClaim(t, c, "dlg_target")
		if prepared, err := c.prepareNoAction(claim, delegateFinish{}); err != nil || !prepared {
			t.Fatalf("prepareNoAction = %t, %v", prepared, err)
		}
		if _, err := c.FinishNoAction(nil); !errors.Is(err, errDelegateStaleLease) {
			t.Fatalf("FinishNoAction(nil) error = %v, want stale lease", err)
		}
		forged := *claim
		forged.lease.generation++
		if _, err := c.FinishNoAction(&forged); !errors.Is(err, errDelegateStaleLease) {
			t.Fatalf("FinishNoAction(mismatched) error = %v, want stale lease", err)
		}
	})

	t.Run("unready", func(t *testing.T) {
		c, _ := newDelegateControllerTestHarness(t, 1, 1)
		claim := eligibleDelegateNoActionClaim(t, c, "dlg_target")
		if prepared, err := c.prepareNoAction(claim, delegateFinish{}); err != nil || !prepared {
			t.Fatalf("prepareNoAction = %t, %v", prepared, err)
		}
		claim.ready = make(chan struct{})
		if _, err := c.FinishNoAction(claim); !errors.Is(err, errDelegateTargetBusy) {
			t.Fatalf("FinishNoAction(unready) error = %v, want busy", err)
		}
	})
}

func TestDelegateControllerFinishNoActionRejectsReportRequiredTerminalAndPreparedState(t *testing.T) {
	t.Run("non-nil run error", func(t *testing.T) {
		for name, runErr := range map[string]error{
			"cancellation": context.Canceled,
			"failure":      errors.New("run failed"),
		} {
			t.Run(name, func(t *testing.T) {
				c, _ := newDelegateControllerTestHarness(t, 1, 1)
				claim := eligibleDelegateNoActionClaimForRun(t, c, "dlg_target", runErr)
				fallback := stableDelegateFinishFromRun(delegateTerminalRunInputs{result: "error fallback", runErr: runErr})
				if prepared, err := c.prepareNoAction(claim, fallback); err != nil || prepared {
					t.Fatalf("prepareNoAction(non-nil run error) = %t, %v, want false/nil", prepared, err)
				}
			})
		}
	})

	t.Run("report required", func(t *testing.T) {
		c, _ := newDelegateControllerTestHarness(t, 1, 1)
		seedDelegateControllerRunning(t, c, "dlg_target", "")
		claim, continued, err := c.BeginSettlement(delegateLease{delegateID: "dlg_target", generation: 1})
		if err != nil || continued {
			t.Fatalf("BeginSettlement = claim:%#v continued:%t err:%v", claim, continued, err)
		}
		if prepared, err := c.prepareNoAction(claim, delegateFinish{}); err != nil || prepared {
			t.Fatalf("prepareNoAction(report required) = %t, %v, want false/nil", prepared, err)
		}
		if _, err := c.FinishNoAction(claim); !errors.Is(err, errDelegateTargetBusy) {
			t.Fatalf("FinishNoAction(report required) error = %v, want busy", err)
		}
	})

	t.Run("terminal claim", func(t *testing.T) {
		c, _ := newDelegateControllerTestHarness(t, 1, 1)
		seedDelegateControllerRunning(t, c, "dlg_target", "")
		lease := delegateLease{delegateID: "dlg_target", generation: 1}
		claim, continued, err := c.BeginFinalization(lease, delegateSettlementTerminal)
		if err != nil || continued {
			t.Fatalf("BeginFinalization(terminal) = claim:%#v continued:%t err:%v", claim, continued, err)
		}
		if prepared, err := c.prepareNoAction(claim, delegateFinish{}); err != nil || prepared {
			t.Fatalf("prepareNoAction(terminal) = %t, %v, want false/nil", prepared, err)
		}
		if _, err := c.FinishNoAction(claim); !errors.Is(err, errDelegateTargetBusy) {
			t.Fatalf("FinishNoAction(terminal) error = %v, want busy", err)
		}
	})

	t.Run("prepared terminal", func(t *testing.T) {
		c, _ := newDelegateControllerTestHarness(t, 1, 1)
		claim := eligibleDelegateNoActionClaim(t, c, "dlg_target")
		c.mu.Lock()
		_, appendErr := c.appendLocked(delegatestore.Event{
			Kind:       delegatestore.EventDelegateTerminalPrepared,
			DelegateID: "dlg_target",
			TerminalPrepared: &delegatestore.TerminalPrepared{
				Generation: 1,
				Packet:     delegateMissingTerminalPacket(),
			},
		})
		c.mu.Unlock()
		if appendErr != nil {
			t.Fatalf("append prepared terminal: %v", appendErr)
		}
		if prepared, err := c.prepareNoAction(claim, delegateFinish{}); err != nil || prepared {
			t.Fatalf("prepareNoAction(prepared terminal) = %t, %v, want false/nil", prepared, err)
		}
		if _, err := c.FinishNoAction(claim); !errors.Is(err, errDelegateTargetBusy) {
			t.Fatalf("FinishNoAction(prepared terminal) error = %v, want busy", err)
		}
	})
}

func TestDelegateControllerFinishNoActionStopUsesRetainedFallback(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	claim := eligibleDelegateNoActionClaim(t, c, "dlg_target")
	startedAt := time.Date(2026, 8, 29, 1, 2, 3, 0, time.UTC)
	endedAt := startedAt.Add(4 * time.Minute)
	activityAt := startedAt.Add(3 * time.Minute)
	fallback := stableDelegateFinishFromRun(delegateTerminalRunInputs{
		result:           "retained fallback",
		descriptor:       delegatestore.Descriptor{Task: "inspect task", Description: "inspect description"},
		startedAt:        startedAt,
		endedAt:          endedAt,
		latestActivityAt: activityAt,
		usage:            schema.CumulativeUsage{InputTokens: 11, OutputTokens: 7, CacheReadTokens: 3, TotalTokens: 21},
		warnings:         []string{"retained warning"},
		worktree:         &delegateWorktreeReport{Path: "/tmp/worktree", Branch: "task-3", HeadSHA: "deadbeef", Ahead: 2, Dirty: true},
		scratchPath:      "/tmp/scratch",
	})
	if prepared, err := c.prepareNoAction(claim, fallback); err != nil || !prepared {
		t.Fatalf("prepareNoAction = %t, %v", prepared, err)
	}
	// The stopping finish must use the controller-retained clone, not this caller value.
	fallback.packet.Warnings[0] = "mutated warning"
	fallback.packet.Metadata[0] = 'X'
	appendDelegateControllerStopRequest(t, c, "dlg_target")

	plans, err := c.FinishNoAction(claim)
	if err != nil {
		t.Fatalf("FinishNoAction under stop: %v", err)
	}
	finished := latestDelegateControllerRunFinished(t, c, "dlg_target")
	if finished.Outcome.Status != delegatestore.OutcomeStopped {
		t.Fatalf("stopped outcome = %q, want %q", finished.Outcome.Status, delegatestore.OutcomeStopped)
	}
	if finished.Outcome.Reason != "stopped_by_parent" {
		t.Fatalf("stopped reason = %q, want stopped_by_parent", finished.Outcome.Reason)
	}
	if finished.Disposition != delegatestore.DispositionTerminalError {
		t.Fatalf("stopped disposition = %q, want %q", finished.Disposition, delegatestore.DispositionTerminalError)
	}
	if finished.DeliveryID == "" {
		t.Fatal("stopped delivery ID is empty")
	}
	if len(plans.deliveries) != 0 {
		t.Fatalf("stopped delivery plans = %#v, want none for covered owner", plans.deliveries)
	}
	if finished.Packet == nil {
		t.Fatal("stopped packet is nil")
	}
	if finished.Packet.Kind != delegatestore.PacketTerminalError {
		t.Fatalf("stopped packet kind = %q, want %q", finished.Packet.Kind, delegatestore.PacketTerminalError)
	}
	var metadata delegateTerminalPacketMetadata
	if err := json.Unmarshal(finished.Packet.Metadata, &metadata); err != nil {
		t.Fatalf("decode retained fallback metadata: %v", err)
	}
	if metadata.Task != "inspect task" {
		t.Fatalf("retained task = %q, want inspect task", metadata.Task)
	}
	if metadata.Worktree == nil {
		t.Fatal("retained worktree is nil")
	}
	if metadata.Worktree.Path != "/tmp/worktree" {
		t.Fatalf("retained worktree path = %q, want /tmp/worktree", metadata.Worktree.Path)
	}
	if metadata.Worktree.Branch != "task-3" {
		t.Fatalf("retained worktree branch = %q, want task-3", metadata.Worktree.Branch)
	}
	if metadata.Worktree.HeadSHA != "deadbeef" {
		t.Fatalf("retained worktree head = %q, want deadbeef", metadata.Worktree.HeadSHA)
	}
	if metadata.Worktree.Ahead != 2 {
		t.Fatalf("retained worktree ahead = %d, want 2", metadata.Worktree.Ahead)
	}
	if !metadata.Worktree.Dirty {
		t.Fatal("retained worktree dirty = false, want true")
	}
	if metadata.ScratchPath != "/tmp/scratch" {
		t.Fatalf("retained scratch path = %q, want /tmp/scratch", metadata.ScratchPath)
	}
	if metadata.CumulativeUsage == nil {
		t.Fatal("retained cumulative usage is nil")
	}
	if metadata.CumulativeUsage.InputTokens != 11 {
		t.Fatalf("retained input tokens = %d, want 11", metadata.CumulativeUsage.InputTokens)
	}
	if metadata.CumulativeUsage.OutputTokens != 7 {
		t.Fatalf("retained output tokens = %d, want 7", metadata.CumulativeUsage.OutputTokens)
	}
	if metadata.CumulativeUsage.CacheReadTokens != 3 {
		t.Fatalf("retained cache-read tokens = %d, want 3", metadata.CumulativeUsage.CacheReadTokens)
	}
	if metadata.CumulativeUsage.TotalTokens != 21 {
		t.Fatalf("retained total tokens = %d, want 21", metadata.CumulativeUsage.TotalTokens)
	}
	if metadata.RunStartedAt != startedAt.Format(time.RFC3339Nano) {
		t.Fatalf("retained start time = %q, want %q", metadata.RunStartedAt, startedAt.Format(time.RFC3339Nano))
	}
	if metadata.RunEndedAt != endedAt.Format(time.RFC3339Nano) {
		t.Fatalf("retained end time = %q, want %q", metadata.RunEndedAt, endedAt.Format(time.RFC3339Nano))
	}
	if metadata.LatestActivityAt != activityAt.Format(time.RFC3339Nano) {
		t.Fatalf("retained latest activity = %q, want %q", metadata.LatestActivityAt, activityAt.Format(time.RFC3339Nano))
	}
	if !reflect.DeepEqual(finished.Packet.Warnings, []string{"retained warning"}) {
		t.Fatalf("retained warnings = %#v, want retained warning", finished.Packet.Warnings)
	}
}

func TestDelegateControllerFinishNoActionAppendFailureRetainsRecoveryState(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	claim := eligibleDelegateNoActionClaim(t, c, "dlg_target")
	if prepared, err := c.prepareNoAction(claim, stableDelegateFinishFromRun(delegateTerminalRunInputs{result: "fallback"})); err != nil || !prepared {
		t.Fatalf("prepareNoAction = %t, %v", prepared, err)
	}
	if err := c.store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if _, err := c.FinishNoAction(claim); err == nil {
		t.Fatal("FinishNoAction succeeded after store close")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	live := c.live["dlg_target"]
	if c.settlementClaims[claim.token] != claim || live == nil || live.binding == nil || live.binding.evidence == nil || live.binding.evidence.fallback == nil ||
		!live.recoveryRequired || !live.finalizationRecoveryRequired || !live.recoveryRunnerPending || c.drivesInUse != 1 || !c.durable["dlg_target"].CurrentRunOpen {
		t.Fatalf("append failure recovery state = claim:%#v live:%#v drives:%d aggregate:%#v", c.settlementClaims[claim.token], live, c.drivesInUse, c.durable["dlg_target"])
	}
}

func TestDelegateControllerFinishGenerationCannotForgeNoAction(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	lease := delegateLease{delegateID: "dlg_target", generation: 1}
	plans, err := c.FinishGeneration(lease, delegateFinish{
		outcome:     delegatestore.OutcomeCompleted,
		disposition: delegatestore.DispositionCompletedNoAction,
	})
	if !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("FinishGeneration forged no-action error = %v, want busy", err)
	}
	if len(plans.deliveries) != 0 || !c.durable["dlg_target"].CurrentRunOpen || c.live["dlg_target"].binding == nil {
		t.Fatalf("forged no-action mutated controller: plans=%#v aggregate=%#v live=%#v", plans, c.durable["dlg_target"], c.live["dlg_target"])
	}
}

func eligibleDelegateNoActionClaim(t *testing.T, c *delegateTreeController, delegateID string) *delegateSettlementClaim {
	return eligibleDelegateNoActionClaimForRun(t, c, delegateID, nil)
}

func eligibleDelegateNoActionClaimForRun(t *testing.T, c *delegateTreeController, delegateID string, runErr error) *delegateSettlementClaim {
	t.Helper()
	seedDelegateControllerIdle(t, c, delegateID, "")
	lease := startDelegateAttentionEvidenceGeneration(t, c, delegateID)
	if recorded, err := c.recordAttentionNoAction(lease); err != nil || !recorded {
		t.Fatalf("recordAttentionNoAction = %t, %v", recorded, err)
	}
	claim, continued, err := c.BeginRunFinalization(lease, delegateSettlementOrdinary, runErr)
	if err != nil || continued {
		t.Fatalf("BeginSettlement = claim:%#v continued:%t err:%v", claim, continued, err)
	}
	<-claim.ready
	c.mu.Lock()
	c.live[delegateID].attentionIDs = nil
	c.mu.Unlock()
	return claim
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
	for _, event := range slices.Backward(events) {
		if event.DelegateID == delegateID && event.RunFinished != nil {
			return *event.RunFinished
		}
	}
	t.Fatalf("no run-finished event for %s", delegateID)
	return delegatestore.RunFinished{}
}
