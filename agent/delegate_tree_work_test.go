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
	if _, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target"); err != nil {
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
	return delegateReconcileEvidence{
		evidenceVersion: requirements.evidenceVersion,
		shells:          map[string]shellRuntimeLossEvidence{},
		attention:       map[string][]string{},
	}
}
