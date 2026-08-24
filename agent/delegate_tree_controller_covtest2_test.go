package agent

import (
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// TestExactLeaseLocked_NilAggregate covers the nil-aggregate branch.
func TestExactLeaseLocked_NilAggregate(t *testing.T) {
	c := &delegateTreeController{durable: map[string]*delegatestore.Aggregate{}}
	_, _, err := c.exactLeaseLocked(delegateLease{delegateID: "dlg_1", generation: 1})
	if err == nil {
		t.Fatal("expected error for nil aggregate")
	}
}

// TestExactLeaseLocked_GenerationMismatch covers the generation-mismatch branch.
func TestExactLeaseLocked_GenerationMismatch(t *testing.T) {
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {Generation: 2, CurrentRunOpen: true},
		},
	}
	_, _, err := c.exactLeaseLocked(delegateLease{delegateID: "dlg_1", generation: 1})
	if err == nil {
		t.Fatal("expected error for generation mismatch")
	}
}

// TestExactLeaseLocked_RunNotOpen covers the run-not-open branch.
func TestExactLeaseLocked_RunNotOpen(t *testing.T) {
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {Generation: 1, CurrentRunOpen: false},
		},
	}
	_, _, err := c.exactLeaseLocked(delegateLease{delegateID: "dlg_1", generation: 1})
	if err == nil {
		t.Fatal("expected error for run not open")
	}
}

// TestExactLeaseLocked_NilLive covers the nil-live branch.
func TestExactLeaseLocked_NilLive(t *testing.T) {
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {Generation: 1, CurrentRunOpen: true},
		},
		live: map[string]*delegateLiveState{},
	}
	_, _, err := c.exactLeaseLocked(delegateLease{delegateID: "dlg_1", generation: 1})
	if err == nil {
		t.Fatal("expected error for nil live")
	}
}

// TestExactLeaseLocked_NilBinding covers the nil-binding branch.
func TestExactLeaseLocked_NilBinding(t *testing.T) {
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {Generation: 1, CurrentRunOpen: true},
		},
		live: map[string]*delegateLiveState{
			"dlg_1": {binding: nil},
		},
	}
	_, _, err := c.exactLeaseLocked(delegateLease{delegateID: "dlg_1", generation: 1})
	if err == nil {
		t.Fatal("expected error for nil binding")
	}
}

// TestExactLeaseLocked_LeaseMismatch covers the binding-lease-mismatch branch.
func TestExactLeaseLocked_LeaseMismatch(t *testing.T) {
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {Generation: 1, CurrentRunOpen: true},
		},
		live: map[string]*delegateLiveState{
			"dlg_1": {binding: &delegateRuntimeBinding{lease: delegateLease{delegateID: "dlg_1", generation: 2}}},
		},
	}
	_, _, err := c.exactLeaseLocked(delegateLease{delegateID: "dlg_1", generation: 1})
	if err == nil {
		t.Fatal("expected error for lease mismatch")
	}
}

// TestExactLeaseLocked_Success covers the success path.
func TestExactLeaseLocked_Success(t *testing.T) {
	lease := delegateLease{delegateID: "dlg_1", generation: 1}
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {Generation: 1, CurrentRunOpen: true},
		},
		live: map[string]*delegateLiveState{
			"dlg_1": {binding: &delegateRuntimeBinding{lease: lease}},
		},
	}
	agg, live, err := c.exactLeaseLocked(lease)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if agg == nil || live == nil {
		t.Fatal("expected non-nil aggregate and live")
	}
}

// TestAdmitLeaseLocked_Closing covers the closing branch.
func TestAdmitLeaseLocked_Closing(t *testing.T) {
	c := &delegateTreeController{
		closing: true,
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {Generation: 1, CurrentRunOpen: true},
		},
		live: map[string]*delegateLiveState{
			"dlg_1": {binding: &delegateRuntimeBinding{lease: delegateLease{delegateID: "dlg_1", generation: 1}}},
		},
	}
	_, _, err := c.admitLeaseLocked(delegateLease{delegateID: "dlg_1", generation: 1})
	if err == nil {
		t.Fatal("expected error for closing controller")
	}
}

// TestAdmitLeaseLocked_ReclamationCovers covers the reclamation-covers branch.
func TestAdmitLeaseLocked_ReclamationCovers(t *testing.T) {
	lease := delegateLease{delegateID: "dlg_1", generation: 1}
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {Generation: 1, CurrentRunOpen: true},
		},
		live: map[string]*delegateLiveState{
			"dlg_1": {binding: &delegateRuntimeBinding{lease: lease}},
		},
		reclaiming: map[string]uint64{"dlg_1": 1},
	}
	_, _, err := c.admitLeaseLocked(lease)
	if err == nil {
		t.Fatal("expected error for reclamation covers")
	}
}

// TestAdmitLeaseLocked_RecoveryRequired covers the recovery-required branch.
func TestAdmitLeaseLocked_RecoveryRequired(t *testing.T) {
	lease := delegateLease{delegateID: "dlg_1", generation: 1}
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {Generation: 1, CurrentRunOpen: true, Resumable: true},
		},
		live: map[string]*delegateLiveState{
			"dlg_1": {binding: &delegateRuntimeBinding{lease: lease, ready: true}, recoveryRequired: true},
		},
	}
	_, _, err := c.admitLeaseLocked(lease)
	if err == nil {
		t.Fatal("expected error for recovery required")
	}
}

// TestAdmitLeaseLocked_PendingStopSeq covers the pending-stop-seq branch.
func TestAdmitLeaseLocked_PendingStopSeq(t *testing.T) {
	lease := delegateLease{delegateID: "dlg_1", generation: 1}
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {Generation: 1, CurrentRunOpen: true, Resumable: true, PendingStopSeq: 1},
		},
		live: map[string]*delegateLiveState{
			"dlg_1": {binding: &delegateRuntimeBinding{lease: lease, ready: true}},
		},
	}
	_, _, err := c.admitLeaseLocked(lease)
	if err == nil {
		t.Fatal("expected error for pending stop seq")
	}
}

// TestAdmitLeaseLocked_NotResumable covers the not-resumable branch.
func TestAdmitLeaseLocked_NotResumable(t *testing.T) {
	lease := delegateLease{delegateID: "dlg_1", generation: 1}
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {Generation: 1, CurrentRunOpen: true, Resumable: false},
		},
		live: map[string]*delegateLiveState{
			"dlg_1": {binding: &delegateRuntimeBinding{lease: lease, ready: true}},
		},
	}
	_, _, err := c.admitLeaseLocked(lease)
	if err == nil {
		t.Fatal("expected error for not resumable")
	}
}

// TestAdmitLeaseLocked_BindingNotReady covers the binding-not-ready branch.
func TestAdmitLeaseLocked_BindingNotReady(t *testing.T) {
	lease := delegateLease{delegateID: "dlg_1", generation: 1}
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {Generation: 1, CurrentRunOpen: true, Resumable: true},
		},
		live: map[string]*delegateLiveState{
			"dlg_1": {binding: &delegateRuntimeBinding{lease: lease, ready: false}},
		},
	}
	_, _, err := c.admitLeaseLocked(lease)
	if err == nil {
		t.Fatal("expected error for binding not ready")
	}
}

// TestDelegateActorDescribe covers the describe method on delegateActor.
func TestDelegateActorDescribe(t *testing.T) {
	t.Run("with lease", func(t *testing.T) {
		lease := delegateLease{delegateID: "dlg_1", generation: 1}
		a := delegateActor{lease: &lease}
		if got := a.describe(); got != "delegate dlg_1" {
			t.Fatalf("expected 'delegate dlg_1', got %q", got)
		}
	})
	t.Run("with root session", func(t *testing.T) {
		a := delegateActor{rootSessionID: "root-sess"}
		if got := a.describe(); got != "root session root-sess" {
			t.Fatalf("expected 'root session root-sess', got %q", got)
		}
	})
	t.Run("empty", func(t *testing.T) {
		a := delegateActor{}
		if got := a.describe(); got != "" {
			t.Fatalf("expected empty, got %q", got)
		}
	})
}
