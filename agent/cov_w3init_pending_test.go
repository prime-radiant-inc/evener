package agent

import (
	"context"
	"testing"

	"primeradiant.com/serf/agent/plugin"
)

// TestW3Init_PendingSessionStart_NilReceiver covers the nil-receiver guard.
func TestW3Init_PendingSessionStart_NilReceiver(t *testing.T) {
	t.Parallel()
	var s *Session
	_, _, gotResult, gotKind := s.pendingSessionStartForUserTurn(context.Background())
	if gotResult || gotKind {
		t.Fatalf("nil receiver returned result=%v kind=%v, want both false", gotResult, gotKind)
	}
}

// TestW3Init_PendingSessionStart_NilContext covers the nil-context defaulting arm.
// With no pending hook in flight the call returns immediately after adopting a
// background context.
func TestW3Init_PendingSessionStart_NilContext(t *testing.T) {
	t.Parallel()
	s := newSession(t)
	//nolint:staticcheck // deliberately passing a nil context to exercise the guard.
	_, _, gotResult, gotKind := s.pendingSessionStartForUserTurn(nil)
	if gotResult || gotKind {
		t.Fatalf("no-pending call returned result=%v kind=%v, want both false", gotResult, gotKind)
	}
}

// TestW3Init_PendingSessionStart_CtxCancelledInLoop covers the in-loop context
// cancellation arm: an in-flight hook plus an already-cancelled context returns
// without a result.
func TestW3Init_PendingSessionStart_CtxCancelledInLoop(t *testing.T) {
	t.Parallel()
	s := newSession(t)
	kind := plugin.SessionStartKindStartup
	s.mu.Lock()
	s.pendingSessionStartInFlight = true
	s.pendingSessionStartKind = &kind
	s.pendingSessionStartResult = nil
	s.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, gotResult, gotKind := s.pendingSessionStartForUserTurn(ctx)
	if gotResult || gotKind {
		t.Fatalf("cancelled-in-loop returned result=%v kind=%v, want both false", gotResult, gotKind)
	}
}

// TestW3Init_PendingSessionStart_AfterFuncBroadcast covers the AfterFunc
// registration and its cancel-triggered broadcast: the waiter blocks, the
// wait-entered seam cancels the context, and the AfterFunc wakes the cond so the
// loop re-checks the context and returns.
func TestW3Init_PendingSessionStart_AfterFuncBroadcast(t *testing.T) {
	t.Parallel()
	s := newSession(t)
	kind := plugin.SessionStartKindStartup
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.pendingSessionStartInFlight = true
	s.pendingSessionStartKind = &kind
	s.pendingSessionStartResult = nil
	// Called while holding s.mu, just before the cond wait. Cancelling here makes
	// the freshly registered AfterFunc fire and broadcast, waking the wait.
	s.pendingSessionStartWaitEntered = cancel
	s.mu.Unlock()

	_, _, gotResult, gotKind := s.pendingSessionStartForUserTurn(ctx)
	if gotResult || gotKind {
		t.Fatalf("afterfunc-broadcast returned result=%v kind=%v, want both false", gotResult, gotKind)
	}
}
