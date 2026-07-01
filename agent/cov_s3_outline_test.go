package agent

import (
	"fmt"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// s3cov_resultIndexWith builds a resultIndex mapping the given call IDs to
// results with the supplied error flags and content, so the pure outline
// summarizers can be exercised without a full transcript.
func s3cov_resultIndexWith(results map[string]*llm.ToolResultData) *resultIndex {
	idx := &resultIndex{byCallID: map[string]pairedResult{}, consumed: map[string]bool{}}
	for id, r := range results {
		idx.byCallID[id] = pairedResult{result: r}
	}
	return idx
}

func s3cov_calls(ids ...string) []*llm.ToolCallData {
	out := make([]*llm.ToolCallData, 0, len(ids))
	for _, id := range ids {
		out = append(out, &llm.ToolCallData{ID: id, Name: "read_file"})
	}
	return out
}

func TestS3Cov_CallStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		calls   []*llm.ToolCallData
		results map[string]*llm.ToolResultData
		want    string
	}{
		{
			name:    "all ok",
			calls:   s3cov_calls("a", "b"),
			results: map[string]*llm.ToolResultData{"a": {Content: "ok"}, "b": {Content: "ok"}},
			want:    "ok",
		},
		{
			name:    "one error dominates",
			calls:   s3cov_calls("a", "b"),
			results: map[string]*llm.ToolResultData{"a": {Content: "ok"}, "b": {Content: "boom", IsError: true}},
			want:    "error",
		},
		{
			name:    "missing result is pending",
			calls:   s3cov_calls("a", "b"),
			results: map[string]*llm.ToolResultData{"a": {Content: "ok"}},
			want:    "pending",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := callStatus(tc.calls, s3cov_resultIndexWith(tc.results))
			if got != tc.want {
				t.Fatalf("callStatus = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestS3Cov_ResultSizeNote(t *testing.T) {
	t.Parallel()

	longLine := strings.Repeat("x", resultLineMaxRunes+5)
	manyLines := strings.Repeat("line\n", resultBodyWholeMax+3)

	tests := []struct {
		name    string
		calls   []*llm.ToolCallData
		results map[string]*llm.ToolResultData
		want    string
	}{
		{
			name:    "no results paired",
			calls:   s3cov_calls("a"),
			results: map[string]*llm.ToolResultData{},
			want:    "",
		},
		{
			name:    "small result no truncation",
			calls:   s3cov_calls("a"),
			results: map[string]*llm.ToolResultData{"a": {Content: "one\ntwo\n"}},
			want:    "2 lines",
		},
		{
			name:    "wide line marks truncated",
			calls:   s3cov_calls("a"),
			results: map[string]*llm.ToolResultData{"a": {Content: longLine}},
			want:    "1 lines [truncated]",
		},
		{
			name:    "too many lines marks truncated",
			calls:   s3cov_calls("a"),
			results: map[string]*llm.ToolResultData{"a": {Content: manyLines}},
			want:    fmt.Sprintf("%d lines [truncated]", resultBodyWholeMax+3),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := resultSizeNote(tc.calls, s3cov_resultIndexWith(tc.results))
			if got != tc.want {
				t.Fatalf("resultSizeNote = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestS3Cov_AnyLineWiderThan(t *testing.T) {
	t.Parallel()
	if anyLineWiderThan("short\nalso short", 100) {
		t.Fatal("expected no wide line")
	}
	if !anyLineWiderThan("short\n"+strings.Repeat("y", 50), 20) {
		t.Fatal("expected a wide line")
	}
}

func TestS3Cov_OutlineRoleLabel(t *testing.T) {
	t.Parallel()
	cases := map[schema.TurnKind]string{
		schema.TurnUserInput:   "User",
		schema.TurnAssistant:   "Assistant",
		schema.TurnSteering:    "Steering",
		schema.TurnSummary:     "Summary",
		schema.TurnCheckpoint:  "Checkpoint",
		schema.TurnSystem:      "System",
		schema.TurnToolResults: "ToolResults",
		schema.TurnTool:        "ToolResults",
		schema.TurnKind("odd"): "odd",
	}
	for kind, want := range cases {
		if got := outlineRoleLabel(kind); got != want {
			t.Errorf("outlineRoleLabel(%q) = %q, want %q", kind, got, want)
		}
	}
}

func TestS3Cov_TurnPlainText(t *testing.T) {
	t.Parallel()
	msg := llm.Assistant("visible text")
	msg.Content = append(msg.Content, llm.ContentPart{
		Kind:     llm.ContentThinking,
		Thinking: &llm.ThinkingData{Text: "the reasoning"},
	})
	turn := schema.NewTurn(schema.TurnAssistant, msg)
	got := turnPlainText(turn)
	if !strings.Contains(got, "visible text") || !strings.Contains(got, "the reasoning") {
		t.Fatalf("turnPlainText = %q, want both text and thinking", got)
	}
}

func TestS3Cov_BoundOutline(t *testing.T) {
	t.Parallel()

	t.Run("fits whole", func(t *testing.T) {
		t.Parallel()
		lines := []string{"a", "b", "c"}
		content, truncated, elided := boundOutline(lines)
		if truncated || elided != 0 {
			t.Fatalf("expected no truncation, got truncated=%v elided=%d", truncated, elided)
		}
		if content != "a\nb\nc" {
			t.Fatalf("content = %q", content)
		}
	})

	t.Run("elides middle when over budget", func(t *testing.T) {
		t.Parallel()
		// ~400 lines of 100 chars each = ~40k chars, well over convBudgetChars.
		line := strings.Repeat("x", 100)
		lines := make([]string, 400)
		for i := range lines {
			lines[i] = fmt.Sprintf("%03d-%s", i, line)
		}
		content, truncated, elided := boundOutline(lines)
		if !truncated {
			t.Fatal("expected truncation")
		}
		if elided <= 0 {
			t.Fatalf("expected positive elided count, got %d", elided)
		}
		if len([]rune(content)) > convBudgetChars {
			t.Fatalf("content %d runes exceeds budget %d", len([]rune(content)), convBudgetChars)
		}
		if !strings.Contains(content, "turns elided") {
			t.Fatal("expected elision marker")
		}
		// Head and tail survive.
		if !strings.Contains(content, "000-") || !strings.Contains(content, "399-") {
			t.Fatal("expected head and tail lines to survive")
		}
	})
}
