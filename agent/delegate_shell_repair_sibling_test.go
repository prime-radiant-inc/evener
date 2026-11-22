//go:build unix

package agent

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
)

// TestDelegateShellRepairLeavesALiveSiblingsForwardedJobAlone proves a forwarded
// job_started copy never carries the owner's process identity. The copy lives in
// an ancestor delegate's journal, and the reconcile pass repairs every journal it
// knows at once, so without this a stop of an unrelated sibling branch would
// SIGKILL the nested delegate's live background job (#4843 review).
func TestDelegateShellRepairLeavesALiveSiblingsForwardedJobAlone(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_branch_a", "")
	seedDelegateControllerIdle(t, c, "dlg_branch_b", "")

	sleeper := startProcessGroup(t)
	// One Wait for the whole test: the group leader is this test's own child, so a
	// killed leader stays a zombie that a liveness probe would still report alive.
	exited := make(chan error, 1)
	go func() { exited <- sleeper.cmd.Wait() }()
	started := time.Unix(10, 0).UTC()

	// The nested delegate under branch A reports its background job to its parent,
	// branch A's session, through the live forwarding path.
	const branchASession = "child-dlg_branch_a"
	parentJM, err := newJobManagerNoSync(c.stateDir, branchASession, nil)
	if err != nil {
		t.Fatalf("open branch A job manager: %v", err)
	}
	if err := parentJM.forwardEvent(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               started,
		JobID:            "job_nested_bg",
		Type:             jobstore.JobShell,
		Background:       true,
		OwnerSessionID:   "child-dlg_branch_a1",
		VisibleToSession: branchASession,
		ParentDelegateID: "dlg_branch_a1",
		StartedAt:        &started,
		ProcessGroupID:   sleeper.pid,
		ProcessStartTime: &sleeper.started,
	}); err != nil {
		t.Fatalf("forward the nested job start: %v", err)
	}
	if err := parentJM.store.Close(); err != nil {
		t.Fatalf("close branch A job store: %v", err)
	}

	// Stopping the sibling branch reconciles every journal, branch A's included.
	evidence, err := collectDelegateReconcileEvidence(c.stateDir, c.ReconcileRequirements())
	if err != nil {
		t.Fatalf("collectDelegateReconcileEvidence: %v", err)
	}
	plans, err := c.Reconcile(evidence)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	repaired := false
	for _, plan := range plans.shellRepairs {
		if plan.delegateID == "dlg_branch_a" {
			repaired = true
		}
		if err := executeDelegateShellRepair(plan, time.Unix(30, 0).UTC()); err != nil {
			t.Fatalf("executeDelegateShellRepair: %v", err)
		}
	}
	if !repaired {
		t.Fatal("no repair plan for branch A, so the sibling-stop path was not exercised")
	}
	select {
	case <-exited:
		t.Fatal("repairing a sibling branch killed a nested delegate's live background job")
	// TRIPWIRE: a killed group leader exits immediately on SIGKILL; this brief quiet window proves the sibling repair left it running.
	case <-time.After(500 * time.Millisecond):
	}

	// The owner's own journal still names the group, so crash recovery of that
	// journal reaps it once its runtime is gone.
	const nestedSession = "child-dlg_branch_a1"
	ownerJM, err := newJobManagerNoSync(c.stateDir, nestedSession, nil)
	if err != nil {
		t.Fatalf("open nested job manager: %v", err)
	}
	if err := ownerJM.store.Append(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               started,
		JobID:            "job_nested_bg",
		Type:             jobstore.JobShell,
		Background:       true,
		OwnerSessionID:   nestedSession,
		VisibleToSession: nestedSession,
		StartedAt:        &started,
		ProcessGroupID:   sleeper.pid,
		ProcessStartTime: &sleeper.started,
	}); err != nil {
		t.Fatalf("append the owner's job start: %v", err)
	}
	if err := ownerJM.store.Close(); err != nil {
		t.Fatalf("close nested job store: %v", err)
	}
	if reaped := reapBackgroundJournal(c.stateDir, nestedSession); reaped != 1 {
		t.Fatalf("owner journal reap ended %d groups, want 1", reaped)
	}
	select {
	case <-exited:
	// TRIPWIRE: reapBackgroundJournal sends SIGKILL and the sleeper exits within milliseconds; 10s only fires on a genuine hang.
	case <-time.After(10 * time.Second):
		t.Fatal("crash recovery left the nested job's group running with its owner gone")
	}
}
