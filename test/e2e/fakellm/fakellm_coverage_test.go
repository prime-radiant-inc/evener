package fakellm

import (
	"testing"
)

// TestContentTextString covers the string content path.
func TestContentTextString(t *testing.T) {
	if got := contentText("hello"); got != "hello" {
		t.Fatalf("contentText(string) = %q, want hello", got)
	}
}

// TestContentTextArray covers the array content path.
func TestContentTextArray(t *testing.T) {
	content := []any{
		map[string]any{"type": "text", "text": "hello "},
		map[string]any{"type": "text", "text": "world"},
	}
	if got := contentText(content); got != "hello world" {
		t.Fatalf("contentText(array) = %q, want 'hello world'", got)
	}
}

// TestContentTextArrayWithNonMap covers the array-with-non-map path.
func TestContentTextArrayWithNonMap(t *testing.T) {
	content := []any{
		"not a map",
		map[string]any{"text": "good"},
	}
	if got := contentText(content); got != "good" {
		t.Fatalf("contentText(array with non-map) = %q, want 'good'", got)
	}
}

// TestContentTextArrayWithNonText covers the array-without-text-key path.
func TestContentTextArrayWithNonText(t *testing.T) {
	content := []any{
		map[string]any{"type": "image"},
	}
	if got := contentText(content); got != "" {
		t.Fatalf("contentText(array without text) = %q, want empty", got)
	}
}

// TestContentTextUnknown covers the unknown-type path.
func TestContentTextUnknown(t *testing.T) {
	if got := contentText(42); got != "" {
		t.Fatalf("contentText(int) = %q, want empty", got)
	}
	if got := contentText(nil); got != "" {
		t.Fatalf("contentText(nil) = %q, want empty", got)
	}
}

// TestCallTexts covers the Call.Texts method.
func TestCallTexts(t *testing.T) {
	call := &Call{
		Body: map[string]any{
			"messages": []any{
				map[string]any{"role": "user", "content": "hello"},
				map[string]any{"role": "assistant", "content": []any{
					map[string]any{"type": "text", "text": "hi there"},
				}},
				"not a map",
			},
		},
	}
	texts := call.Texts()
	if len(texts) != 2 {
		t.Fatalf("Texts() = %v, want 2 entries", texts)
	}
	if texts[0] != "user: hello" {
		t.Fatalf("texts[0] = %q, want 'user: hello'", texts[0])
	}
	if texts[1] != "assistant: hi there" {
		t.Fatalf("texts[1] = %q, want 'assistant: hi there'", texts[1])
	}
}

// TestCallTextsEmpty covers the no-messages path.
func TestCallTextsEmpty(t *testing.T) {
	call := &Call{Body: map[string]any{}}
	texts := call.Texts()
	if len(texts) != 0 {
		t.Fatalf("Texts() = %v, want empty", texts)
	}
}

// TestCallPreviousToolCallID covers the PreviousToolCallID method.
func TestCallPreviousToolCallID(t *testing.T) {
	call := &Call{
		Body: map[string]any{
			"messages": []any{
				map[string]any{"role": "assistant", "tool_calls": []any{
					map[string]any{"id": "call-1", "type": "function"},
				}},
				map[string]any{"role": "tool", "tool_call_id": "call-1"},
				map[string]any{"role": "assistant", "tool_calls": []any{
					map[string]any{"id": "call-2", "type": "function"},
				}},
			},
		},
	}
	if got := call.PreviousToolCallID(); got != "call-2" {
		t.Fatalf("PreviousToolCallID() = %q, want call-2", got)
	}
}

// TestCallPreviousToolCallIDEmpty covers the no-ids path.
func TestCallPreviousToolCallIDEmpty(t *testing.T) {
	call := &Call{Body: map[string]any{}}
	if got := call.PreviousToolCallID(); got != "" {
		t.Fatalf("PreviousToolCallID() = %q, want empty", got)
	}
}

// TestCallPreviousToolCallIDWithNonMap covers the non-map-skip path.
func TestCallPreviousToolCallIDWithNonMap(t *testing.T) {
	call := &Call{
		Body: map[string]any{
			"messages": []any{
				"not a map",
				map[string]any{"tool_call_id": "call-x"},
			},
		},
	}
	if got := call.PreviousToolCallID(); got != "call-x" {
		t.Fatalf("PreviousToolCallID() = %q, want call-x", got)
	}
}

// TestCallContains covers the Contains method.
func TestCallContains(t *testing.T) {
	call := &Call{
		Body: map[string]any{
			"messages": []any{
				map[string]any{"role": "user", "content": "hello world"},
			},
		},
	}
	if !call.Contains("world") {
		t.Fatalf("Contains('world') should be true")
	}
	if call.Contains("missing") {
		t.Fatalf("Contains('missing') should be false")
	}
}

// TestNextOnClosedServer covers the closed-server path.
func TestNextOnClosedServer(t *testing.T) {
	srv, err := NewOn("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv.Close()
	_, err = srv.Next(nil)
	if err == nil {
		t.Fatalf("Next on closed server should error")
	}
}

// TestNextWithDoneChannel covers the done-channel path.
func TestNextWithDoneChannel(t *testing.T) {
	srv, err := NewOn("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	done := make(chan struct{})
	close(done)
	_, err = srv.Next(done)
	if err == nil {
		t.Fatalf("Next with closed done should error")
	}
}
