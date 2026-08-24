package contextmgr

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent/schema"
)

// TestCovWithCompactionTurnCallback covers WithCompactionTurnCallback
// (context_manager.go lines 67-69).
func TestCovWithCompactionTurnCallback(t *testing.T) {
	ctx := context.Background()
	var called bool
	cb := func(turn schema.Turn) {
		called = true
	}
	ctxWithCb := WithCompactionTurnCallback(ctx, cb)
	if ctxWithCb == nil {
		t.Fatal("WithCompactionTurnCallback should return non-nil context")
	}

	// Verify the callback is stored by retrieving it.
	val := ctxWithCb.Value(compactionTurnCallbackKey{})
	if val == nil {
		t.Fatal("context should carry the callback")
	}
	if cbFn, ok := val.(func(schema.Turn)); !ok {
		t.Fatalf("callback type = %T", val)
	} else {
		cbFn(schema.Turn{})
		if !called {
			t.Fatal("callback should have been called")
		}
	}

	// Nil callback still works (returns a context that carries nil).
	ctxNil := WithCompactionTurnCallback(ctx, nil)
	if ctxNil == nil {
		t.Fatal("nil callback should still return a context")
	}
}

// TestCovWithCompactionTurnCallback_NoCallback covers the case where no callback
// is installed — the context value should be nil.
func TestCovWithCompactionTurnCallback_NoCallback(t *testing.T) {
	ctx := context.Background()
	if val := ctx.Value(compactionTurnCallbackKey{}); val != nil {
		t.Fatalf("plain context should not carry callback: %v", val)
	}
}
