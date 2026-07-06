package repair

import (
	"fmt"
	"strings"
	"testing"
)

func TestSuggestToolName_CloseMatch(t *testing.T) {
	got := SuggestToolName("reed_file", []string{"read_file", "write_file", "shell"})
	if got != "read_file" {
		t.Fatalf("got %q", got)
	}
}

func TestSuggestToolName_NoMatchBeyondThreshold(t *testing.T) {
	got := SuggestToolName("zzzzzz", []string{"read_file", "shell"})
	if got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestUnknownToolMessage_IncludesSuggestionAndList(t *testing.T) {
	msg := UnknownToolMessage("reed_file", []string{"read_file", "shell"})
	if !strings.Contains(msg, `unknown tool: "reed_file"`) {
		t.Fatalf("msg = %q", msg)
	}
	if !strings.Contains(msg, `Did you mean "read_file"`) {
		t.Fatalf("msg missing suggestion: %q", msg)
	}
	if !strings.Contains(msg, "read_file") || !strings.Contains(msg, "shell") {
		t.Fatalf("msg missing available list: %q", msg)
	}
}

func TestUnknownToolMessage_NoSuggestionWhenFar(t *testing.T) {
	msg := UnknownToolMessage("zzzzzz", []string{"read_file", "shell"})
	if strings.Contains(msg, "Did you mean") {
		t.Fatalf("unexpected suggestion: %q", msg)
	}
}

func TestUnknownToolMessage_CapsLongList(t *testing.T) {
	names := make([]string, 40)
	for i := 0; i < 40; i++ {
		names[i] = fmt.Sprintf("tool_%02d", i)
	}
	msg := UnknownToolMessage("zzzzzz", names)
	if !strings.Contains(msg, "tool_00") {
		t.Fatalf("msg missing first tool: %q", msg)
	}
	if strings.Contains(msg, "tool_39") {
		t.Fatalf("msg should not contain tool_39 (beyond cap): %q", msg)
	}
}
