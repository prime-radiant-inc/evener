package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/internal/delegatestore"
)

// TestSteer_EmptyMessage covers the empty-message path (lines 55-56).
func TestSteer_EmptyMessage(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	_, err := c.Steer(context.Background(), rootDelegateActor("root-session"), "dlg_target", "")
	if !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("expected errDelegateTargetBusy, got %v", err)
	}
	// Whitespace-only message also triggers it.
	_, err = c.Steer(context.Background(), rootDelegateActor("root-session"), "dlg_target", "   ")
	if !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("expected errDelegateTargetBusy for whitespace, got %v", err)
	}
}

// TestSteer_CancelledContext covers the ctx.Err path (lines 52-53).
func TestSteer_CancelledContext(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Steer(ctx, rootDelegateActor("root-session"), "dlg_target", "hello")
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

// TestSteer_AuthorizeMutationFailure covers the BeginSteerPersistence error
// path (lines 59-61) when the delegate doesn't exist.
func TestSteer_AuthorizeMutationFailure(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	_, err := c.Steer(context.Background(), rootDelegateActor("root-session"), "nonexistent", "hello")
	if err == nil {
		t.Fatal("expected error for nonexistent delegate")
	}
}

// TestSteerCaller_EmptyMessage covers the empty-message path in SteerCaller
// (lines 78-79).
func TestSteerCaller_EmptyMessage(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	_, err := c.SteerCaller(context.Background(), rootDelegateActor("root-session"), "", nil)
	if err == nil {
		t.Fatal("expected error for empty message in SteerCaller")
	}
}

// TestSteerCaller_CancelledContext covers the ctx.Err path in SteerCaller
// (lines 75-76).
func TestSteerCaller_CancelledContext(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.SteerCaller(ctx, rootDelegateActor("root-session"), "hello", nil)
	if err == nil {
		t.Fatal("expected error for cancelled context in SteerCaller")
	}
}

// TestSteerCaller_NoLease covers the no-lease path in beginCallerSteerPersistence
// (lines 111-112).
func TestSteerCaller_NoLease(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	actor := rootDelegateActor("root-session")
	_, err := c.SteerCaller(context.Background(), actor, "hello", nil)
	if err == nil {
		t.Fatal("expected error for caller without lease")
	}
}

// TestBeginSteerPersistence_NoLiveBinding covers the no-live-binding path
// (lines 133-135).
func TestBeginSteerPersistence_NoLiveBinding(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_idle", "")
	_, err := c.BeginSteerPersistence(rootDelegateActor("root-session"), "dlg_idle")
	if !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("expected errDelegateTargetBusy, got %v", err)
	}
}

// TestBeginSteerPersistence_AuthorizeFailure covers the authorizeMutation failure
// path (lines 102-103).
func TestBeginSteerPersistence_AuthorizeFailure(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	_, err := c.BeginSteerPersistence(rootDelegateActor("root-session"), "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent delegate")
	}
}

// TestSteer_Success covers the full Steer happy path (lines 63-68).
func TestSteer_Success(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	attachDelegateSteerRuntime(t, c, "dlg_target", afero.NewMemMapFs())
	plans, err := c.Steer(context.Background(), rootDelegateActor("root-session"), "dlg_target", "steer message")
	if err != nil {
		t.Fatalf("Steer: %v", err)
	}
	if len(plans.updates) != 1 {
		t.Fatalf("expected 1 update, got %d", len(plans.updates))
	}
}

// TestSteer_AppendFailure covers the appendDelegateSteeringDurably error path
// (lines 64-66).
func TestSteer_AppendFailure(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	fs := &transcriptWriteFailFS{Fs: afero.NewMemMapFs()}
	attachDelegateSteerRuntime(t, c, "dlg_target", fs)
	fs.fail = true
	_, err := c.Steer(context.Background(), rootDelegateActor("root-session"), "dlg_target", "must fail")
	if err == nil {
		t.Fatal("expected error for append failure")
	}
}

// TestDelegateDepthLocked covers delegateDepthLocked for nested and
// non-existent delegates.
func TestDelegateDepthLocked(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	c.mu.Lock()
	defer c.mu.Unlock()
	// Root delegate has depth 1.
	if got := c.delegateDepthLocked("dlg_target"); got != 1 {
		t.Fatalf("delegateDepthLocked(root) = %d, want 1", got)
	}
	// Non-existent delegate has depth 0.
	if got := c.delegateDepthLocked("nonexistent"); got != 0 {
		t.Fatalf("delegateDepthLocked(nonexistent) = %d, want 0", got)
	}
}

// TestAdmitLeaseLocked_NilDurable covers the nil-durable path in
// admitLeaseLocked.
func TestAdmitLeaseLocked_NilDurable(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	c.mu.Lock()
	defer c.mu.Unlock()
	_, _, err := c.admitLeaseLocked(delegateLease{delegateID: "nonexistent", generation: 1}, delegatestore.PhaseRunning)
	if err == nil {
		t.Fatal("expected error for nonexistent delegate")
	}
}
