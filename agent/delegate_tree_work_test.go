package agent

import (
	"context"
	"errors"
	"testing"
)

func TestDelegateControllerShellReceiptHoldsStopOpen(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	lease := delegateLease{delegateID: "dlg_target", generation: 1}
	token, err := c.BeginShellWork(lease)
	if err != nil {
		t.Fatalf("BeginShellWork: %v", err)
	}
	result, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	if _, err := c.Reconcile(emptyDelegateReconcileEvidence(c)); err != nil {
		t.Fatalf("Reconcile with receipt: %v", err)
	}
	select {
	case <-result.done:
		t.Fatal("stop completed while a shell receipt was pending")
	default:
	}
	if err := c.AbortShellWork(token); err != nil {
		t.Fatalf("AbortShellWork: %v", err)
	}
}

func TestDelegateControllerCommitShellWorkAfterStopCancelsImmediately(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	token, err := c.BeginShellWork(delegateLease{delegateID: "dlg_target", generation: 1})
	if err != nil {
		t.Fatalf("BeginShellWork: %v", err)
	}
	result, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	cancelled := false
	cancelNow, err := c.CommitShellWork(token, "job-shell", func() { cancelled = true })
	if err != nil {
		t.Fatalf("CommitShellWork: %v", err)
	}
	if !cancelNow {
		t.Fatal("CommitShellWork after stop did not request immediate cancellation")
	}
	if cancelled {
		t.Fatal("CommitShellWork invoked process cancellation while holding controller ownership")
	}
	if _, err := c.FinishGeneration(delegateLease{delegateID: "dlg_target", generation: 1}, delegateFinish{}); err != nil {
		t.Fatalf("FinishGeneration: %v", err)
	}
	if _, err := c.ReportShellFinished(token, "job-other"); !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("wrong shell finish error = %v, want stale lease", err)
	}
	if _, err := c.Reconcile(emptyDelegateReconcileEvidence(c)); err != nil {
		t.Fatalf("Reconcile before exact shell finish: %v", err)
	}
	select {
	case <-result.done:
		t.Fatal("stop completed before exact shell finish")
	default:
	}
	if _, err := c.ReportShellFinished(token, "job-shell"); err != nil {
		t.Fatalf("ReportShellFinished exact: %v", err)
	}
	if _, err := c.Reconcile(emptyDelegateReconcileEvidence(c)); err != nil {
		t.Fatalf("Reconcile after exact shell finish: %v", err)
	}
	select {
	case <-result.done:
	default:
		t.Fatal("stop remained pending after exact shell finish")
	}
}

func TestDelegateControllerShellFinishRequiresTokenAndJobID(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	token, err := c.BeginShellWork(delegateLease{delegateID: "dlg_target", generation: 1})
	if err != nil {
		t.Fatalf("BeginShellWork: %v", err)
	}
	if cancelNow, err := c.CommitShellWork(token, "job-shell", context.CancelFunc(func() {})); err != nil || cancelNow {
		t.Fatalf("CommitShellWork = cancel:%t err:%v", cancelNow, err)
	}
	for name, forged := range map[string]struct {
		token delegateWorkToken
		jobID string
	}{
		"token": {token: delegateWorkToken{processID: token.processID + 1}, jobID: "job-shell"},
		"job":   {token: token, jobID: "job-other"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := c.ReportShellFinished(forged.token, forged.jobID); !errors.Is(err, errDelegateStaleLease) {
				t.Fatalf("ReportShellFinished error = %v, want stale lease", err)
			}
		})
	}
	if _, err := c.ReportShellFinished(token, "job-shell"); err != nil {
		t.Fatalf("ReportShellFinished exact: %v", err)
	}
}

func TestDelegateControllerUnrelatedShellCommitDoesNotJoinStop(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_stopping", "")
	seedDelegateControllerRunning(t, c, "dlg_running", "")
	token, err := c.BeginShellWork(delegateLease{delegateID: "dlg_running", generation: 1})
	if err != nil {
		t.Fatalf("BeginShellWork: %v", err)
	}
	result, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_stopping")
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	if cancelNow, err := c.CommitShellWork(token, "job-unrelated", func() {}); err != nil || cancelNow {
		t.Fatalf("CommitShellWork outside stop = cancel:%t err:%v", cancelNow, err)
	}
	if _, err := c.Reconcile(emptyDelegateReconcileEvidence(c)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	select {
	case <-result.done:
	default:
		t.Fatal("unrelated shell work blocked stop completion")
	}
}

func TestDelegateControllerClosingShellOutsideCurrentStopJoinsLaterRoot(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_stopping", "")
	seedDelegateControllerRunning(t, c, "dlg_running", "")
	token, err := c.BeginShellWork(delegateLease{delegateID: "dlg_running", generation: 1})
	if err != nil {
		t.Fatalf("BeginShellWork: %v", err)
	}
	seedDelegateControllerDelivery(t, c, "dlg_stopping")
	deliveryPlan := c.ReplayDeliveries()[0]
	deliveryToken, admitted, err := c.BeginDelivery(deliveryPlan)
	if err != nil || !admitted {
		t.Fatalf("BeginDelivery = admitted:%t err:%v", admitted, err)
	}
	if _, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_stopping"); err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}

	cancelCount := 0
	var cancellationErr error
	lease := delegateLease{delegateID: "dlg_running", generation: 1}
	shellCancel := func() {
		cancelCount++
		if cancelCount != 2 {
			return
		}
		_, reportErr := c.ReportShellFinished(token, "job-later-root")
		cancellationErr = errors.Join(cancellationErr, reportErr)
	}
	finishRan := false
	c.live["dlg_running"].binding.cancel = func() {
		if finishRan {
			return
		}
		finishRan = true
		_, finishErr := c.FinishGeneration(lease, delegateFinish{})
		cancellationErr = errors.Join(cancellationErr, finishErr)
	}
	ctx := newDelegateStopWaitBarrierContext()
	closeResult := make(chan error, 1)
	go func() { closeResult <- c.Close(ctx) }()
	<-ctx.entered
	cancelNow, err := c.CommitShellWork(token, "job-later-root", shellCancel)
	if err != nil || !cancelNow {
		t.Fatalf("CommitShellWork while closing = cancel:%t err:%v", cancelNow, err)
	}
	shellCancel()
	if _, err := c.CompleteDelivery(deliveryToken, false); err != nil {
		t.Fatalf("CompleteDelivery current stop: %v", err)
	}
	ctx.cancel()
	if err := <-closeResult; err != nil {
		t.Fatalf("Close before later root cancellation = %v", err)
	}
	if cancellationErr != nil {
		t.Fatalf("later root cleanup: %v", cancellationErr)
	}
	if cancelCount != 2 || !finishRan || len(c.work) != 0 {
		t.Fatalf("later root drain cancelCount=%d finish=%t work=%#v", cancelCount, finishRan, c.work)
	}
}

func TestDelegateControllerAbortShellReceiptReleasesOnce(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	token, err := c.BeginShellWork(delegateLease{delegateID: "dlg_target", generation: 1})
	if err != nil {
		t.Fatalf("BeginShellWork: %v", err)
	}
	if err := c.AbortShellWork(token); err != nil {
		t.Fatalf("AbortShellWork: %v", err)
	}
	if err := c.AbortShellWork(token); !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("second AbortShellWork error = %v, want stale lease", err)
	}
}

func emptyDelegateReconcileEvidence(c *delegateTreeController) delegateReconcileEvidence {
	requirements := c.ReconcileRequirements()
	evidence := delegateReconcileEvidence{
		evidenceVersion: requirements.evidenceVersion,
		shells:          map[string]shellRuntimeLossEvidence{},
		attention:       map[string][]string{},
	}
	for id := range requirements.shellStores {
		evidence.shells[id] = shellRuntimeLossEvidence{}
	}
	for id := range requirements.attentionTranscripts {
		evidence.attention[id] = nil
	}
	return evidence
}
