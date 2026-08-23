package agent

import (
	"context"
	"testing"
)

// TestDelegateRuntimeSend_NilOwner covers the nil-owner path in send
// (line 781-782).
func TestDelegateRuntimeSend_NilOwner(t *testing.T) {
	t.Parallel()
	var runtime delegateRuntime
	outcome := runtime.send(context.Background(), "dlg_1", "hello", 0)
	if outcome.result.Err == nil {
		t.Fatal("expected error for nil owner")
	}
}

// TestDelegateRuntimeSend_NilController covers the nil-controller path
// (line 781-782) with a non-nil owner but nil delegateController.
func TestDelegateRuntimeSend_NilController(t *testing.T) {
	t.Parallel()
	runtime := delegateRuntime{owner: &Session{}}
	outcome := runtime.send(context.Background(), "dlg_1", "hello", 0)
	if outcome.result.Err == nil {
		t.Fatal("expected error for nil controller")
	}
}

// TestDelegateRuntimeSend_EmptyDelegateID covers the empty-delegateID path
// (line 784-785).
func TestDelegateRuntimeSend_EmptyDelegateID(t *testing.T) {
	t.Parallel()
	runtime := delegateRuntime{owner: &Session{}}
	outcome := runtime.send(context.Background(), "", "hello", 0)
	if outcome.result.Err == nil {
		t.Fatal("expected error for empty delegate ID")
	}
}

// TestDelegateRuntimeSend_EmptyMessage covers the empty-message path
// (line 784-785).
func TestDelegateRuntimeSend_EmptyMessage(t *testing.T) {
	t.Parallel()
	runtime := delegateRuntime{owner: &Session{}}
	outcome := runtime.send(context.Background(), "dlg_1", "", 0)
	if outcome.result.Err == nil {
		t.Fatal("expected error for empty message")
	}
}

// TestRuntimeForDelegateOwner_NilController covers the nil-controller guard
// in runtimeForDelegateOwner (lines 1970-1972).
func TestRuntimeForDelegateOwner_NilController(t *testing.T) {
	t.Parallel()
	var c *delegateTreeController
	if got := c.runtimeForDelegateOwner(delegateSnapshot{}); got != nil {
		t.Fatal("expected nil for nil controller")
	}
}

// TestEmitStableDelegateUpdate_NilSession covers the nil-session guard
// in emitStableDelegateUpdate (lines 1944-1946).
func TestEmitStableDelegateUpdate_NilSession(t *testing.T) {
	t.Parallel()
	var s *Session
	s.emitStableDelegateUpdate(delegateUpdatePlan{})
	// Should not panic.
}

// TestEmitStableDelegateUpdate_NilController covers the nil-controller guard
// in emitStableDelegateUpdate (lines 1944-1946).
func TestEmitStableDelegateUpdate_NilController(t *testing.T) {
	t.Parallel()
	s := &Session{}
	s.emitStableDelegateUpdate(delegateUpdatePlan{})
	// Should not panic.
}
