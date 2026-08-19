package transcript

import "testing"

func TestApplyAgentMessageDelta_EmptyIsNoOp(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyAgentMessageDelta("t1", "i1", "")
	if len(r.Messages()) != 0 {
		t.Fatalf("empty delta should not append; got %d messages", len(r.Messages()))
	}
}

func TestApplyAgentMessageDelta_CreatesAndExtendsActiveMessage(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)

	// First chunk creates a new assistant message and remembers it as active.
	r.ApplyAgentMessageDelta("1", "item-a", "Hello")
	msgs := r.Messages()
	if len(msgs) != 1 || msgs[0].Kind != MsgAssistant || msgs[0].Text != "Hello" {
		t.Fatalf("after first chunk: %+v", msgs)
	}
	if r.ActiveMessages()["item-a"] != 0 {
		t.Fatalf("item-a should be active at index 0, got %v", r.ActiveMessages())
	}

	// A second chunk for the same item extends the same message in place.
	r.ApplyAgentMessageDelta("1", "item-a", " world")
	msgs = r.Messages()
	if len(msgs) != 1 || msgs[0].Text != "Hello world" {
		t.Fatalf("same-item chunk should extend in place: %+v", msgs)
	}
}

func TestApplyAgentMessageDelta_ExtendsTrailingAssistantWithoutItemID(t *testing.T) {
	// Seed a trailing assistant message with no item id but the same turn.
	seed := []ChatMessage{{Kind: MsgAssistant, Text: "abc", TurnID: "1", TurnIndex: 1}}
	r := NewTranscriptReducer(seed, nil, nil)

	// A delta for the same turn with an itemID merges into the trailing message
	// and then remembers it as active.
	r.ApplyAgentMessageDelta("1", "item-x", "def")
	msgs := r.Messages()
	if len(msgs) != 1 || msgs[0].Text != "abcdef" {
		t.Fatalf("trailing merge failed: %+v", msgs)
	}
	if r.ActiveMessages()["item-x"] != 0 {
		t.Fatalf("item-x should be remembered active: %v", r.ActiveMessages())
	}
}

func TestApplyReasoningSummaryDelta_StreamsThenSupersedes(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)

	r.ApplyReasoningSummaryDelta("1", "think-1", "pondering")
	r.ApplyReasoningSummaryDelta("1", "think-1", "...")
	msgs := r.Messages()
	if len(msgs) != 1 || msgs[0].Kind != MsgReasoning || msgs[0].Text != "pondering..." {
		t.Fatalf("reasoning stream: %+v", msgs)
	}
	if msgs[0].Done {
		t.Fatal("live reasoning should not be Done yet")
	}

	// A new reasoning item collapses the prior live thought.
	r.ApplyReasoningSummaryDelta("1", "think-2", "next")
	msgs = r.Messages()
	if len(msgs) != 2 {
		t.Fatalf("expected two reasoning messages, got %d", len(msgs))
	}
	if !msgs[0].Done {
		t.Fatal("first thought should be finalized (Done) when a new one starts")
	}

	// FinalizeReasoning collapses the still-live second thought.
	r.FinalizeReasoning()
	if !r.Messages()[1].Done {
		t.Fatal("FinalizeReasoning should mark the live thought Done")
	}
}

func TestApplyToolOutputDelta_CreatesAndAppends(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyToolOutputDelta("", "ignored") // empty itemID still creates a tool message
	r.ApplyToolOutputDelta("tool-1", "line1\n")
	r.ApplyToolOutputDelta("tool-1", "line2\n")

	var found *ChatMessage
	for i := range r.Messages() {
		if r.Messages()[i].ItemID == "tool-1" {
			found = &r.Messages()[i]
		}
	}
	if found == nil || found.Tool == nil {
		t.Fatalf("expected a tool message for tool-1: %+v", r.Messages())
	}
	if found.Tool.Output != "line1\nline2\n" {
		t.Fatalf("tool output = %q, want appended", found.Tool.Output)
	}

	// Empty delta is a no-op.
	before := len(r.Messages())
	r.ApplyToolOutputDelta("tool-1", "")
	if len(r.Messages()) != before {
		t.Fatal("empty tool delta should not append")
	}
}
