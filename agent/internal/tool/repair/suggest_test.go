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

func TestTopMatches_ReturnsClosestWithinThreshold(t *testing.T) {
	got := TopMatches("session_tolls.go", []string{"session_tools.go", "session_tools_file.go", "unrelated.go"}, 3)
	if len(got) == 0 || got[0] != "session_tools.go" {
		t.Fatalf("got %v, want first match session_tools.go", got)
	}
}

func TestTopMatches_CapsAtN(t *testing.T) {
	got := TopMatches("file", []string{"file1", "file2", "file3", "file4"}, 2)
	if len(got) != 2 {
		t.Fatalf("got %d matches, want 2: %v", len(got), got)
	}
}

func TestTopMatches_NoneWithinThreshold(t *testing.T) {
	got := TopMatches("zzzzzz", []string{"read_file", "shell"}, 3)
	if len(got) != 0 {
		t.Fatalf("got %v, want empty", got)
	}
}

func TestTopMatches_SortedByDistanceThenLexically(t *testing.T) {
	// "cot" is distance 1 from both "cat" and "cog"; ties break lexically.
	got := TopMatches("cot", []string{"cog", "cat", "dot"}, 3)
	if len(got) != 3 {
		t.Fatalf("got %v, want 3 matches", got)
	}
	if got[0] != "cat" || got[1] != "cog" {
		t.Fatalf("got %v, want [cat cog ...] (lexical tiebreak)", got)
	}
}

func TestUnknownToolMessage_CapsLongList(t *testing.T) {
	names := make([]string, 40)
	for i := range 40 {
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
