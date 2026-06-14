package contextmgr

import (
	"strings"
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// TestRenderHistoryForElicit_IncludesToolContent is the regression test for the
// blindness bug: the elicitor must see tool-call arguments and tool-result content,
// not just assistant/user prose. In a coding agent the opaque values worth
// preserving (tokens, IDs, paths) arrive via tool output.
func TestRenderHistoryForElicit_IncludesToolContent(t *testing.T) {
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("Find the deploy token.")},
		{Kind: schema.TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
					ID:        "call-1",
					Name:      "read_file",
					Arguments: []byte(`{"path": "/etc/secrets/deploy.env"}`),
				}},
			},
		}},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("call-1", "read_file", "DEPLOY_TOKEN=OPAQUE-9X2Q\nREGION=eu-central-1", false)},
	}

	got := renderHistoryForElicit(history, 80_000)

	for _, want := range []string{
		"OPAQUE-9X2Q",             // the token, which lives ONLY in the tool result
		"eu-central-1",            // another tool-result value
		"/etc/secrets/deploy.env", // the tool-call argument (a path)
		"read_file",               // the tool name
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered elicit history is missing %q — tool content not captured:\n%s", want, got)
		}
	}
}

// TestRenderHistoryForElicit_KeepsRecentOnOverflow verifies the tail-truncation
// direction: when history exceeds the cap, the MOST RECENT content is kept (the
// freshest facts about to be folded), with a truncation marker for the dropped head.
func TestRenderHistoryForElicit_KeepsRecentOnOverflow(t *testing.T) {
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("OLD_HEAD_MARKER "+strings.Repeat("filler ", 4000))),
		schema.NewTurn(schema.TurnUserInput, llm.User("RECENT_TAIL_MARKER stays")),
	}

	got := renderHistoryForElicit(history, 2_000)

	if !strings.Contains(got, "RECENT_TAIL_MARKER") {
		t.Errorf("recent content must be kept on overflow, missing marker:\n%.200s", got)
	}
	if strings.Contains(got, "OLD_HEAD_MARKER") {
		t.Errorf("the oldest content should have been truncated, but the head marker survived")
	}
	if !strings.Contains(got, "truncated") {
		t.Errorf("expected a truncation marker when history overflows the cap")
	}
}
