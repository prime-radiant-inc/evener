package fakellm

import (
	"testing"
)

// TestCovMintToolCallID covers mintToolCallID (fakellm.go:90), which mints
// a unique tool-call id from an atomic counter.
func TestCovMintToolCallID(t *testing.T) {
	id1 := mintToolCallID()
	id2 := mintToolCallID()
	if id1 == id2 {
		t.Fatalf("mintToolCallID should produce unique ids: %q == %q", id1, id2)
	}
	if id1 == "" {
		t.Fatal("mintToolCallID should not return empty")
	}
}

// TestCovNew covers New (fakellm.go:93), which starts a fake provider on
// a kernel-assigned loopback port. NewOn is already 100% covered; New is
// the convenience wrapper.
func TestCovNew(t *testing.T) {
	srv, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()
	if srv.Addr() == "" {
		t.Fatal("server address should not be empty")
	}
	if srv.BaseURL() == "" {
		t.Fatal("base URL should not be empty")
	}
}

// TestCovCallAccessors covers Cancelled, AffinityHeader, and ToolCallID
// (fakellm.go:69, 74, 167), which are simple field accessors on Call.
func TestCovCallAccessors(t *testing.T) {
	cancelled := make(chan struct{})
	close(cancelled)
	c := &Call{
		toolCallID:     "call_test_123",
		affinityHeader: "affinity-abc",
		cancelled:      cancelled,
		reply:          make(chan reply, 1),
	}

	if got := c.Cancelled(); got == nil {
		t.Fatal("Cancelled() returned nil")
	}
	select {
	case <-c.Cancelled():
		// expected: already closed
	default:
		t.Fatal("Cancelled() channel should be closed")
	}

	if got := c.AffinityHeader(); got != "affinity-abc" {
		t.Fatalf("AffinityHeader() = %q, want affinity-abc", got)
	}

	if got := c.ToolCallID(); got != "call_test_123" {
		t.Fatalf("ToolCallID() = %q, want call_test_123", got)
	}
}

// TestCovRespondText covers RespondText (fakellm.go:153), which finishes
// a round with an assistant message and finish_reason "stop".
func TestCovRespondText(t *testing.T) {
	c := &Call{reply: make(chan reply, 1)}
	c.RespondText("hello world")

	select {
	case r := <-c.reply:
		if r.text != "hello world" {
			t.Fatalf("reply.text = %q, want hello world", r.text)
		}
		if r.toolName != "" {
			t.Fatalf("reply.toolName = %q, want empty", r.toolName)
		}
	default:
		t.Fatal("RespondText should send on reply channel")
	}
}

// TestCovRespondToolCall covers RespondToolCall (fakellm.go:160), which
// finishes a round with a single tool call.
func TestCovRespondToolCall(t *testing.T) {
	c := &Call{reply: make(chan reply, 1), toolCallID: "call_tc_1"}
	args := map[string]any{"file_path": "test.txt"}
	c.RespondToolCall("read_file", args)

	select {
	case r := <-c.reply:
		if r.toolName != "read_file" {
			t.Fatalf("reply.toolName = %q, want read_file", r.toolName)
		}
		if r.toolArgs["file_path"] != "test.txt" {
			t.Fatalf("reply.toolArgs = %v, want file_path=test.txt", r.toolArgs)
		}
		if r.toolID != "call_tc_1" {
			t.Fatalf("reply.toolID = %q, want call_tc_1", r.toolID)
		}
		if r.text != "" {
			t.Fatalf("reply.text = %q, want empty", r.text)
		}
	default:
		t.Fatal("RespondToolCall should send on reply channel")
	}
}

// TestCovRespondOnce covers the sync.Once guard in respond (fakellm.go:169):
// a second call is a no-op and does not block.
func TestCovRespondOnce(t *testing.T) {
	c := &Call{reply: make(chan reply, 1)}
	c.RespondText("first")
	// Drain the channel so the second send would block if not guarded.
	<-c.reply
	c.RespondText("second") // must not block
}
