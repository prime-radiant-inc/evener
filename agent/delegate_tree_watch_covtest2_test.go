package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// TestAcquireWatchDelivery_NewDelivery covers the new-delivery path
// (lines 156-164) where a delivery is created from scratch.
func TestAcquireWatchDelivery_NewDelivery(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_source", "")
	seedDelegateControllerRunning(t, c, "dlg_recv", "")

	receipt, err := c.AcquireWatchDelivery("dlg_source", 1, "dlg_recv", "delivery-1", 1, false)
	if err != nil {
		t.Fatalf("AcquireWatchDelivery: %v", err)
	}
	if receipt == nil || receipt.deliveryID != "delivery-1" {
		t.Fatalf("unexpected receipt: %#v", receipt)
	}
	if receipt.sourceDelegateID != "dlg_source" || receipt.receiverDelegateID != "dlg_recv" {
		t.Fatalf("receipt source/receiver wrong: %#v", receipt)
	}
}

// TestAcquireWatchDelivery_Idempotent covers the idempotent return path
// (lines 145-152) where the same delivery is requested again.
func TestAcquireWatchDelivery_Idempotent(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_source", "")
	seedDelegateControllerRunning(t, c, "dlg_recv", "")

	first, err := c.AcquireWatchDelivery("dlg_source", 1, "dlg_recv", "delivery-1", 1, false)
	if err != nil {
		t.Fatalf("first AcquireWatchDelivery: %v", err)
	}
	second, err := c.AcquireWatchDelivery("dlg_source", 1, "dlg_recv", "delivery-1", 1, false)
	if err != nil {
		t.Fatalf("second AcquireWatchDelivery: %v", err)
	}
	if first != second {
		t.Fatalf("expected idempotent return of same receipt, got %#v vs %#v", first, second)
	}
}

// TestAcquireWatchDelivery_StaleLeaseMismatch covers the stale-lease path
// (lines 147-149) where a matching deliveryID/updateSeq has different source.
func TestAcquireWatchDelivery_StaleLeaseMismatch(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_source", "")
	seedDelegateControllerRunning(t, c, "dlg_recv", "")
	seedDelegateControllerRunning(t, c, "dlg_other", "")

	_, err := c.AcquireWatchDelivery("dlg_source", 1, "dlg_recv", "delivery-1", 1, false)
	if err != nil {
		t.Fatalf("first AcquireWatchDelivery: %v", err)
	}
	// Same deliveryID and updateSeq but different source — should return stale lease error.
	_, err = c.AcquireWatchDelivery("dlg_other", 1, "dlg_recv", "delivery-1", 1, false)
	if err == nil {
		t.Fatal("expected stale lease error for mismatched source")
	}
}

// TestAcquireWatchDelivery_StopIntersects covers the stop-intersection path
// (lines 153-155).
func TestAcquireWatchDelivery_StopIntersects(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_source", "")
	seedDelegateControllerRunning(t, c, "dlg_recv", "")

	// Start a stop that covers the source.
	_, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_source")
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	_, err = c.AcquireWatchDelivery("dlg_source", 1, "dlg_recv", "delivery-1", 1, false)
	if err == nil {
		t.Fatal("expected target busy error for stop intersection")
	}
}

// TestAcquireWatchDelivery_ValidationError covers the validation error path
// (lines 142-143).
func TestAcquireWatchDelivery_ValidationError(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	// No delegates seeded — source not found.
	_, err := c.AcquireWatchDelivery("dlg_nonexistent", 1, "", "delivery-1", 1, false)
	if err == nil {
		t.Fatal("expected validation error for nonexistent source")
	}
}

// TestRepairStableWatchDeliveriesForBootstrap_NoStoreFile covers the
// not-exist skip path (lines 326-328).
func TestRepairStableWatchDeliveriesForBootstrap_NoStoreFile(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	// No jobs.jsonl exists for any delegate — all are skipped.
	if err := repairStableWatchDeliveriesForBootstrap(c); err != nil {
		t.Fatalf("expected nil when no store files exist, got %v", err)
	}
}

// TestRepairStableWatchDeliveriesForBootstrap_DirectoryStoreFile covers the
// not-a-regular-file error path (lines 332-334).
func TestRepairStableWatchDeliveriesForBootstrap_DirectoryStoreFile(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	storePath := filepath.Join(jobsDir(c.stateDir, "child-dlg_target"), "jobs.jsonl")
	if err := os.MkdirAll(storePath, 0o755); err != nil {
		t.Fatalf("mkdir store path: %v", err)
	}
	err := repairStableWatchDeliveriesForBootstrap(c)
	if err == nil {
		t.Fatal("expected error for directory store file")
	}
}

// TestRepairStableWatchDeliveriesForBootstrap_ValidStoreFile covers the valid
// empty store path (lines 335-339).
func TestRepairStableWatchDeliveriesForBootstrap_ValidStoreFile(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	storePath := filepath.Join(jobsDir(c.stateDir, "child-dlg_target"), "jobs.jsonl")
	if err := os.MkdirAll(filepath.Dir(storePath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(storePath, []byte{}, 0o644); err != nil {
		t.Fatalf("write empty file: %v", err)
	}
	// Should succeed (empty store, nothing to repair).
	if err := repairStableWatchDeliveriesForBootstrap(c); err != nil {
		t.Fatalf("expected nil for empty valid store, got %v", err)
	}
}

// TestStableWatchReceiver_RootReceiver covers the root receiver path
// (lines 188-193).
func TestStableWatchReceiver_RootReceiver(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	// Root runtime is not set up in the test harness, so it should fail.
	_, err := c.stableWatchReceiver("root-session", "")
	if err == nil {
		t.Fatal("expected error for root receiver without root runtime")
	}
}

// TestStableWatchReceiver_NonRootEmptyID covers the non-root empty-id path
// (lines 188-191).
func TestStableWatchReceiver_NonRootEmptyID(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	_, err := c.stableWatchReceiver("other-session", "")
	if err == nil {
		t.Fatal("expected error for non-root empty receiver ID")
	}
}

// TestStableWatchReceiver_DelegateNotFound covers the delegate-not-found path
// (lines 194-197).
func TestStableWatchReceiver_DelegateNotFound(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	_, err := c.stableWatchReceiver("any-session", "dlg_nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent delegate receiver")
	}
}

// TestStableWatchReceiver_SessionMismatch covers the session-mismatch path
// (lines 195-197).
func TestStableWatchReceiver_SessionMismatch(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	// The child session ID is "child-dlg_target", so a different session ID should fail.
	_, err := c.stableWatchReceiver("wrong-session", "dlg_target")
	if err == nil {
		t.Fatal("expected error for session mismatch")
	}
}

// TestStableWatchBootstrapSnapshot covers the snapshot function (lines 342-374).
func TestStableWatchBootstrapSnapshot(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	snapshot := c.stableWatchBootstrapSnapshot()
	if snapshot.stateDir == "" {
		t.Fatal("expected non-empty stateDir")
	}
	if len(snapshot.storePaths) == 0 {
		t.Fatal("expected at least one store path")
	}
	// Should include root session and the delegate.
	if _, ok := snapshot.receiverByID[""]; !ok {
		t.Fatal("expected root receiver")
	}
	if _, ok := snapshot.receiverByID["dlg_target"]; !ok {
		t.Fatal("expected delegate receiver")
	}
}

// TestStableWatchBootstrapSnapshot_NilAggregate covers the nil-aggregate skip
// (line 352-353).
func TestStableWatchBootstrapSnapshot_NilAggregate(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	c.mu.Lock()
	c.durable["dlg_nil"] = nil
	c.mu.Unlock()
	snapshot := c.stableWatchBootstrapSnapshot()
	// The nil aggregate should be skipped — no receiver for it.
	if _, ok := snapshot.receiverByID["dlg_nil"]; ok {
		t.Fatal("expected nil aggregate to be skipped")
	}
}

// TestRepairStableWatchDeliveriesForBootstrap_NilController covers the nil-controller
// guard (lines 320-321).
func TestRepairStableWatchDeliveriesForBootstrap_NilController(t *testing.T) {
	var c *delegateTreeController
	err := repairStableWatchDeliveriesForBootstrap(c)
	if err == nil || !contains(err.Error(), "controller is nil") {
		t.Fatalf("expected nil controller error, got %v", err)
	}
}
