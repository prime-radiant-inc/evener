package agent

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// TestOpenDelegateTreeController_NilStore covers the nil-store validation
// (lines 189-190).
func TestOpenDelegateTreeController_NilStore(t *testing.T) {
	_, err := openDelegateTreeController(delegateTreeControllerConfig{
		rootSessionID: "root",
		now:           func() time.Time { return testTime },
	})
	if err == nil {
		t.Fatal("expected error for nil store")
	}
}

// TestOpenDelegateTreeController_EmptyRootSessionID covers the empty-root
// validation (lines 192-193).
func TestOpenDelegateTreeController_EmptyRootSessionID(t *testing.T) {
	dir := t.TempDir()
	store, err := delegatestore.Open(dir + "/delegates.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, err = openDelegateTreeController(delegateTreeControllerConfig{
		store: store,
		now:   func() time.Time { return testTime },
	})
	if err == nil {
		t.Fatal("expected error for empty root session ID")
	}
}

// TestOpenDelegateTreeController_Defaults covers the default-value paths
// (lines 195-211): nil now, turnLimit <= 0, driveLimit <= 0, maxRetainedTerminal <= 0,
// nil newDelegateID, nil attentionOpen.
func TestOpenDelegateTreeController_Defaults(t *testing.T) {
	dir := t.TempDir()
	store, err := delegatestore.Open(dir + "/delegates.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	c, err := openDelegateTreeController(delegateTreeControllerConfig{
		store:         store,
		rootSessionID: "root",
		stateDir:      dir,
		worktreeRoot:  dir + "/wt",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.turnLimit <= 0 {
		t.Fatal("expected default turnLimit > 0")
	}
	if c.driveLimit <= 0 {
		t.Fatal("expected default driveLimit > 0")
	}
	if c.maxRetainedTerminal <= 0 {
		t.Fatal("expected default maxRetainedTerminal > 0")
	}
	if c.newDelegateID == nil {
		t.Fatal("expected non-nil newDelegateID")
	}
	if c.attentionOpen == nil {
		t.Fatal("expected non-nil attentionOpen")
	}
}

// TestAuthorizeMutationLocked_NonexistentDelegate covers the error path
// for a delegate that doesn't exist in durable.
func TestAuthorizeMutationLocked_NonexistentDelegate(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	c.mu.Lock()
	defer c.mu.Unlock()
	err := c.authorizeMutationLocked(rootDelegateActor("root-session"), "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent delegate")
	}
}

// TestAuthorizeMutationLocked_WrongOwner covers the error path for a
// delegate owned by a different session.
func TestAuthorizeMutationLocked_WrongOwner(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	c.mu.Lock()
	defer c.mu.Unlock()
	// Use an actor from a different session.
	err := c.authorizeMutationLocked(delegateActor{rootSessionID: "wrong-session"}, "dlg_target")
	if err == nil {
		t.Fatal("expected error for wrong owner")
	}
}

// TestAuthorizeMutationLocked_WithLease covers the path where the actor
// has a lease (nested delegate). The actor's lease must be for the parent
// and the target must be the child.
func TestAuthorizeMutationLocked_WithLease(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_parent", "")
	seedDelegateControllerRunning(t, c, "dlg_child", "dlg_parent")
	c.mu.Lock()
	defer c.mu.Unlock()
	lease := delegateLease{delegateID: "dlg_parent", generation: 1}
	actor := delegateActor{rootSessionID: "root-session", lease: &lease}
	err := c.authorizeMutationLocked(actor, "dlg_child")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestHasSettlementClaimLocked covers the hasSettlementClaimLocked method.
func TestHasSettlementClaimLocked(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hasSettlementClaimLocked(delegateLease{delegateID: "dlg_target", generation: 1}) {
		t.Fatal("expected false with no settlement claim")
	}
}

// TestHasSteeringClaimLocked covers the hasSteeringClaimLocked method.
func TestHasSteeringClaimLocked(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hasSteeringClaimLocked(delegateLease{delegateID: "dlg_target", generation: 1}) {
		t.Fatal("expected false with no steering claim")
	}
}

// TestSnapshot covers the Snapshot method returns rows.
func TestSnapshot(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	snap := c.Snapshot()
	if len(snap.rows) == 0 {
		t.Fatal("expected non-empty snapshot")
	}
}

// TestStableDelegateOwnerRuntime covers the stableDelegateOwnerRuntime method.
func TestStableDelegateOwnerRuntime(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	// Root delegate (no parent): should return rootRuntime (nil in test harness).
	owner := c.stableDelegateOwnerRuntime(delegateLease{delegateID: "dlg_target", generation: 1})
	// rootRuntime is nil in the test harness.
	if owner != nil {
		t.Fatalf("expected nil rootRuntime, got %v", owner)
	}
}
