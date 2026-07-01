package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

func TestS3Cov_FindBuckets(t *testing.T) {
	t.Parallel()

	t.Run("non-all scope is current project", func(t *testing.T) {
		t.Parallel()
		buckets, scope := findBuckets("/state/dir", scopeCurrentProject)
		if scope != scopeCurrentProject || len(buckets) != 1 || buckets[0] != "/state/dir" {
			t.Fatalf("got buckets=%v scope=%q", buckets, scope)
		}
	})

	t.Run("all-projects under flat dir degrades to current", func(t *testing.T) {
		t.Parallel()
		// A dir with no recognizable state-home layout → stateHomeFor == "" →
		// degrade to current project.
		buckets, scope := findBuckets(t.TempDir(), scopeAllProjects)
		if scope != scopeCurrentProject || len(buckets) != 1 {
			t.Fatalf("got buckets=%v scope=%q", buckets, scope)
		}
	})
}

func TestS3Cov_TurnRoleLabel(t *testing.T) {
	t.Parallel()
	cases := map[schema.TurnKind]string{
		schema.TurnUserInput:   "user",
		schema.TurnAssistant:   "assistant",
		schema.TurnToolResults: "tool_result",
		schema.TurnTool:        "tool_result",
		schema.TurnSteering:    "steering",
		schema.TurnSummary:     "summary",
		schema.TurnCheckpoint:  "checkpoint",
		schema.TurnSystem:      "system",
		schema.TurnKind("XyZ"): "xyz",
	}
	for kind, want := range cases {
		if got := turnRoleLabel(kind); got != want {
			t.Errorf("turnRoleLabel(%q) = %q, want %q", kind, got, want)
		}
	}
}

func TestS3Cov_RepoBasename(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"git@github.com:owner/repo.git":     "repo",
		"https://github.com/owner/repo.git": "repo",
		"https://github.com/owner/repo/":    "repo",
		"plainname":                         "plainname",
	}
	for in, want := range cases {
		if got := repoBasename(in); got != want {
			t.Errorf("repoBasename(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestS3Cov_RawEntryText(t *testing.T) {
	t.Parallel()
	msg := llm.Assistant("assistant says")
	msg.Content = append(msg.Content,
		llm.ContentPart{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "thinks"}},
		llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{Name: "grep", Arguments: json.RawMessage(`{"pattern":"needle"}`)}},
		llm.ContentPart{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{Content: "result body"}},
	)
	turn := schema.NewTurn(schema.TurnAssistant, msg)
	got := rawEntryText(turn)
	for _, want := range []string{"assistant says", "thinks", "grep", "needle", "result body"} {
		if !strings.Contains(got, want) {
			t.Errorf("rawEntryText missing %q in %q", want, got)
		}
	}
}

func TestS3Cov_MakeSnippet(t *testing.T) {
	t.Parallel()

	t.Run("centers on match with ellipses", func(t *testing.T) {
		t.Parallel()
		text := strings.Repeat("a ", 100) + "NEEDLE " + strings.Repeat("b ", 100)
		got := makeSnippet(text, "needle", 40)
		if !strings.Contains(strings.ToLower(got), "needle") {
			t.Fatalf("snippet missing needle: %q", got)
		}
		if !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "…") {
			t.Fatalf("expected clipping ellipses on both ends: %q", got)
		}
	})

	t.Run("query not found returns leading width", func(t *testing.T) {
		t.Parallel()
		got := makeSnippet("hello world here", "absent", 5)
		if !strings.HasPrefix(got, "hello") {
			t.Fatalf("got %q, want leading %q", got, "hello")
		}
	})

	t.Run("match near start no leading ellipsis", func(t *testing.T) {
		t.Parallel()
		got := makeSnippet("NEEDLE at the very start of text", "needle", 40)
		if strings.HasPrefix(got, "…") {
			t.Fatalf("did not expect leading ellipsis: %q", got)
		}
	})
}
