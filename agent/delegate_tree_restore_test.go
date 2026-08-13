package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/delegatestore"
	"primeradiant.com/serf/agent/internal/jobstore"
)

func TestDelegateControllerRestartThreeLevelTreeIsProviderFree(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 3, 1)
	seedDelegateControllerRunning(t, c, "dlg_parent", "")
	seedDelegateControllerRunning(t, c, "dlg_child", "dlg_parent")
	seedDelegateControllerRunning(t, c, "dlg_grandchild", "dlg_child")
	restarted := reopenDelegateController(t, c, path)
	for _, id := range []string{"dlg_parent", "dlg_child", "dlg_grandchild"} {
		if !restarted.durable[id].CurrentRunOpen {
			t.Fatalf("%s was reconciled before external evidence collection", id)
		}
	}
	evidence, err := collectDelegateReconcileEvidence(restarted.stateDir, restarted.ReconcileRequirements())
	if err != nil {
		t.Fatalf("collectDelegateReconcileEvidence: %v", err)
	}
	if _, err := restarted.Reconcile(evidence); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	for _, id := range []string{"dlg_parent", "dlg_child", "dlg_grandchild"} {
		aggregate := restarted.durable[id]
		if aggregate.CurrentRunOpen || aggregate.LatestOutcome == nil || aggregate.LatestOutcome.Status != delegatestore.OutcomeFailed || aggregate.LatestOutcome.Reason != "runtime_lost" {
			t.Fatalf("%s restart aggregate = %#v", id, aggregate)
		}
	}
	if len(restarted.live) != 0 {
		t.Fatalf("restart constructed live runtimes: %#v", restarted.live)
	}
}

func TestDelegateControllerRestartRepairsPreparedTerminalOnce(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	packet := delegateControllerReportedPacket("prepared")
	if _, _, err := c.BeginSettlement(delegateLease{delegateID: "dlg_target", generation: 1}, &packet); err != nil {
		t.Fatalf("BeginSettlement: %v", err)
	}
	restarted := reopenDelegateController(t, c, path)
	if aggregate := restarted.durable["dlg_target"]; !aggregate.CurrentRunOpen || aggregate.Phase != delegatestore.PhaseSettling {
		t.Fatalf("prepared run changed before Reconcile: %#v", aggregate)
	}
	if _, err := restarted.Reconcile(emptyDelegateReconcileEvidence(restarted)); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	firstCount := countDelegateRunFinished(t, restarted, "dlg_target")
	if firstCount != 1 || restarted.durable["dlg_target"].LatestOutcome == nil || restarted.durable["dlg_target"].LatestOutcome.Status != delegatestore.OutcomeCompleted {
		t.Fatalf("prepared repair count=%d aggregate=%#v", firstCount, restarted.durable["dlg_target"])
	}
	if _, err := restarted.Reconcile(emptyDelegateReconcileEvidence(restarted)); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if got := countDelegateRunFinished(t, restarted, "dlg_target"); got != firstCount {
		t.Fatalf("run-finished count after second reconcile = %d, want %d", got, firstCount)
	}
}

func TestDelegateControllerRestartCompletesStopBeforeAdmission(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	requestSeq := appendDelegateControllerStopRequest(t, c, "dlg_target")
	restarted := reopenDelegateController(t, c, path)
	if restarted.stop == nil || restarted.stop.requestSeq != requestSeq {
		t.Fatalf("restored stop = %#v, want request %d", restarted.stop, requestSeq)
	}
	if _, err := restarted.ReserveStart(rootDelegateActor("root-session"), "dlg_target"); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("ReserveStart before reconcile = %v, want busy", err)
	}
	if _, err := restarted.Reconcile(emptyDelegateReconcileEvidence(restarted)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if restarted.stop != nil || restarted.durable["dlg_target"].PendingStopSeq != 0 {
		t.Fatalf("restart stop remained pending: stop=%#v aggregate=%#v", restarted.stop, restarted.durable["dlg_target"])
	}
	if _, err := restarted.ReserveStart(rootDelegateActor("root-session"), "dlg_target"); err != nil {
		t.Fatalf("ReserveStart after completion: %v", err)
	}
}

func TestDelegateControllerRestartAfterStoppedFinishDoesNotFinishTwice(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	if _, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target"); err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	if _, err := c.FinishGeneration(delegateLease{delegateID: "dlg_target", generation: 1}, delegateFinish{}); err != nil {
		t.Fatalf("FinishGeneration: %v", err)
	}
	before := countDelegateRunFinished(t, c, "dlg_target")
	restarted := reopenDelegateController(t, c, path)
	if _, err := restarted.Reconcile(emptyDelegateReconcileEvidence(restarted)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := countDelegateRunFinished(t, restarted, "dlg_target"); got != before {
		t.Fatalf("restart finished stopped generation twice: before=%d after=%d", before, got)
	}
}

func TestDelegateControllerRestartCleansAttentionWithoutRuntime(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	requestSeq := appendDelegateControllerStopRequest(t, c, "dlg_target")
	transcriptPath := filepath.Join(c.stateDir, sessionsSubdir, "child-dlg_target.transcript.jsonl")
	writeRawAttentionTranscript(t, transcriptPath, "child-dlg_target", "attention-1", false)
	restarted := reopenDelegateController(t, c, path)
	requirements := restarted.ReconcileRequirements()
	evidence, err := collectDelegateReconcileEvidence(restarted.stateDir, requirements)
	if err != nil {
		t.Fatalf("collectDelegateReconcileEvidence: %v", err)
	}
	if got := evidence.attention["dlg_target"]; len(got) != 1 || got[0] != "attention-1" {
		t.Fatalf("pending attention = %#v", got)
	}
	plans, err := restarted.Reconcile(evidence)
	if err != nil || len(plans.attention) != 1 || plans.attention[0].runtime != nil {
		t.Fatalf("cold attention plans = %#v err=%v", plans.attention, err)
	}
	writeRawAttentionTranscript(t, transcriptPath, "child-dlg_target", "attention-1", true)
	if _, err := restarted.ReportAttentionResolved(requestSeq, "dlg_target", "attention-1", delegateAttentionDiscarded, nil); err != nil {
		t.Fatalf("ReportAttentionResolved: %v", err)
	}
	evidence, err = collectDelegateReconcileEvidence(restarted.stateDir, restarted.ReconcileRequirements())
	if err != nil {
		t.Fatalf("recollect evidence: %v", err)
	}
	if _, err := restarted.Reconcile(evidence); err != nil {
		t.Fatalf("final Reconcile: %v", err)
	}
	if restarted.stop != nil {
		t.Fatalf("stop remained after attention cleanup: %#v", restarted.stop)
	}
}

func TestDelegateControllerRestartRepairsDescendantShellBeforeStopCompletion(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_parent", "")
	seedDelegateControllerIdle(t, c, "dlg_child", "dlg_parent")
	appendDelegateControllerStopRequest(t, c, "dlg_parent")
	shellPath := filepath.Join(jobsDir(c.stateDir, "child-dlg_child"), "jobs.jsonl")
	seedDelegateShellStoreAt(t, shellPath)
	restarted := reopenDelegateController(t, c, path)
	evidence, err := collectDelegateReconcileEvidence(restarted.stateDir, restarted.ReconcileRequirements())
	if err != nil {
		t.Fatalf("collectDelegateReconcileEvidence: %v", err)
	}
	plans, err := restarted.Reconcile(evidence)
	if err != nil || len(plans.shellRepairs) != 1 {
		t.Fatalf("shell repair plans = %#v err=%v", plans.shellRepairs, err)
	}
	if restarted.stop == nil {
		t.Fatal("stop completed before shell repair")
	}
	if err := executeDelegateShellRepair(plans.shellRepairs[0], time.Unix(20, 0).UTC()); err != nil {
		t.Fatalf("executeDelegateShellRepair: %v", err)
	}
	evidence, err = collectDelegateReconcileEvidence(restarted.stateDir, restarted.ReconcileRequirements())
	if err != nil {
		t.Fatalf("recollect: %v", err)
	}
	if _, err := restarted.Reconcile(evidence); err != nil {
		t.Fatalf("final Reconcile: %v", err)
	}
	if restarted.stop != nil {
		t.Fatalf("stop remained after shell repair: %#v", restarted.stop)
	}
}

func TestDelegateControllerReconcileRejectsStaleExternalEvidence(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	seedDelegateControllerDelivery(t, c, "dlg_target")
	evidence := emptyDelegateReconcileEvidence(c)
	plan := c.ReplayDeliveries()[0]
	token, admitted, err := c.BeginDelivery(plan)
	if err != nil || !admitted {
		t.Fatalf("BeginDelivery = admitted:%t err:%v", admitted, err)
	}
	if _, err := c.Reconcile(evidence); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("stale Reconcile error = %v, want busy", err)
	}
	if _, err := c.CompleteDelivery(token, false); err != nil {
		t.Fatalf("CompleteDelivery: %v", err)
	}
}

func TestDelegateControllerRestartPreservesOrderedDeliveries(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	seedDelegateControllerDelivery(t, c, "dlg_target")
	seedDelegateControllerDelivery(t, c, "dlg_target")
	restarted := reopenDelegateController(t, c, path)
	plans := restarted.ReplayDeliveries()
	if len(plans) != 1 || plans[0].deliveryID != "dlg_target/delivery/1" {
		t.Fatalf("restart delivery head = %#v", plans)
	}
	token, admitted, err := restarted.BeginDelivery(plans[0])
	if err != nil || !admitted {
		t.Fatalf("BeginDelivery = admitted:%t err:%v", admitted, err)
	}
	next, err := restarted.CompleteDelivery(token, true)
	if err != nil || len(next.deliveries) != 1 || next.deliveries[0].deliveryID != "dlg_target/delivery/2" {
		t.Fatalf("next ordered delivery = %#v err=%v", next.deliveries, err)
	}
}

func TestDelegateControllerRestartDefersExternalStopDeliveryUntilCompletion(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	seedDelegateControllerDelivery(t, c, "dlg_target")
	appendDelegateControllerStopRequest(t, c, "dlg_target")
	restarted := reopenDelegateController(t, c, path)
	if plans := restarted.ReplayDeliveries(); len(plans) != 0 {
		t.Fatalf("delivery replayed before stop completion: %#v", plans)
	}
	plans, err := restarted.Reconcile(emptyDelegateReconcileEvidence(restarted))
	if err != nil || len(plans.deliveries) != 1 {
		t.Fatalf("completion deliveries = %#v err=%v", plans.deliveries, err)
	}
}

func TestDelegateControllerRestartIdleStopFencesQueuedCoveredDelivery(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_parent", "")
	seedDelegateControllerIdle(t, c, "dlg_child", "dlg_parent")
	seedDelegateControllerDelivery(t, c, "dlg_child")
	appendDelegateControllerStopRequest(t, c, "dlg_parent")
	restarted := reopenDelegateController(t, c, path)
	plans, err := restarted.Reconcile(emptyDelegateReconcileEvidence(restarted))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(plans.deliveries) != 0 || len(restarted.durable["dlg_child"].PendingDeliveries) != 0 {
		t.Fatalf("covered delivery survived restart stop: plans=%#v pending=%#v", plans.deliveries, restarted.durable["dlg_child"].PendingDeliveries)
	}
}

func TestDelegateControllerRestartCannotCollideStopOrDeliveryIdentity(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	seedDelegateControllerDelivery(t, c, "dlg_target")
	firstSeq := appendDelegateControllerStopRequest(t, c, "dlg_target")
	restarted := reopenDelegateController(t, c, path)
	plans, err := restarted.Reconcile(emptyDelegateReconcileEvidence(restarted))
	if err != nil || len(plans.deliveries) != 1 {
		t.Fatalf("Reconcile: plans=%#v err=%v", plans.deliveries, err)
	}
	token, admitted, err := restarted.BeginDelivery(plans.deliveries[0])
	if err != nil || !admitted || token.deliveryID == "" || token.processID == 0 {
		t.Fatalf("delivery token = %#v admitted=%t err=%v", token, admitted, err)
	}
	if _, err := restarted.CompleteDelivery(token, false); err != nil {
		t.Fatalf("CompleteDelivery: %v", err)
	}
	second, _, _, err := restarted.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("second StopSubtree: %v", err)
	}
	if second.requestSeq <= firstSeq {
		t.Fatalf("new stop seq = %d, want > %d", second.requestSeq, firstSeq)
	}
}

func reopenDelegateController(t *testing.T, c *delegateTreeController, path string) *delegateTreeController {
	t.Helper()
	if err := c.store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	store, err := delegatestore.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	restarted, err := openDelegateTreeController(delegateTreeControllerConfig{
		store:         store,
		rootSessionID: "root-session",
		stateDir:      c.stateDir,
		worktreeRoot:  c.worktreeRoot,
		turnLimit:     c.turnLimit,
		driveLimit:    c.driveLimit,
		now:           c.now,
	})
	if err != nil {
		t.Fatalf("openDelegateTreeController: %v", err)
	}
	return restarted
}

func countDelegateRunFinished(t *testing.T, c *delegateTreeController, delegateID string) int {
	t.Helper()
	events, err := c.store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	count := 0
	for _, event := range events {
		if event.DelegateID == delegateID && event.Kind == delegatestore.EventDelegateRunFinished {
			count++
		}
	}
	return count
}

func writeRawAttentionTranscript(t *testing.T, path, sessionID, attentionID string, resolved bool) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	resolution := ""
	if resolved {
		resolution = fmt.Sprintf("{\"kind\":\"entry\",\"seq\":1,\"turn\":{\"kind\":\"ATTENTION_RESOLUTION\",\"attention_resolution\":{\"attention_id\":%q,\"disposition\":\"discarded\"}}}\n", attentionID)
	}
	body := fmt.Sprintf("{\"kind\":\"header\",\"format_version\":2,\"session_id\":%q}\n{\"kind\":\"entry\",\"seq\":0,\"turn\":{\"kind\":\"STEERING\",\"attention_id\":%q}}\n%s", sessionID, attentionID, resolution)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func seedDelegateShellStoreAt(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	store, err := jobstore.OpenNoSync(path)
	if err != nil {
		t.Fatalf("OpenNoSync: %v", err)
	}
	now := time.Unix(10, 0).UTC()
	if err := store.Append(jobstore.Event{
		Kind:           jobstore.EventJobStarted,
		TS:             now,
		JobID:          "job-shell",
		Type:           jobstore.JobShell,
		OwnerSessionID: "child-dlg_child",
		StartedAt:      &now,
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
