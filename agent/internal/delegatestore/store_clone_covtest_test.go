package delegatestore

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestAppendBatch_CloneStateError covers the cloneState error path in
// AppendBatch (line 83-86): a state with a nil aggregate should cause
// cloneState to return an error, which AppendBatch wraps.
func TestAppendBatch_CloneStateError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delegates.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Pass a state with a nil aggregate — cloneState should reject it.
	state := State{
		"dlg_nil": nil,
	}
	_, _, err = store.AppendBatch(state, []Event{createdEvent("dlg_test", "")})
	if err == nil {
		t.Fatal("expected cloneState error, got nil")
	}
	if !strings.Contains(err.Error(), "aggregate is nil") {
		t.Errorf("expected nil-aggregate error, got: %v", err)
	}
}
