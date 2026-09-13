package contextmgr

import (
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func TestRoundTimingsAreTransparentToContextAccountingAndElicitRender(t *testing.T) {
	base := []schema.Turn{schema.NewTurn(schema.TurnUserInput, llm.User("question"))}
	timing := schema.NewTurn(schema.TurnRoundTimings, llm.System(strings.Repeat("timing marker ", 200)))
	compaction := schema.NewTurn(schema.TurnContextCompaction, llm.System(strings.Repeat("compaction marker ", 200)))
	withMetadata := append(append(append([]schema.Turn{}, base...), timing), compaction)
	if got, want := estimateTokens(withMetadata), estimateTokens(base); got != want {
		t.Errorf("presentational markers changed estimated tokens: got %d, want %d", got, want)
	}
	if got, want := contextTurnCount(withMetadata), contextTurnCount(base); got != want {
		t.Errorf("presentational markers changed recent turn count: got %d, want %d", got, want)
	}
	if got := recentContextCutoff(withMetadata, 1); got != 0 {
		t.Errorf("recent cutoff = %d, want original user input at 0", got)
	}
	if got := contextHistory(withMetadata); !reflect.DeepEqual(got, base) {
		t.Errorf("context history retains presentational metadata: %#v", got)
	}
	if got := checkpoint(withMetadata, 1, nil, "communicate"); !reflect.DeepEqual(got, withMetadata) {
		t.Errorf("presentational metadata triggered compaction of preserved input: %#v", got)
	}
	if got := renderTurnForElicit(timing); got != "" {
		t.Fatalf("timing marker rendered into elicit context: %q", got)
	}
	if got := renderTurnForElicit(compaction); got != "" {
		t.Fatalf("compaction marker rendered into elicit context: %q", got)
	}
}

func TestRoundTimingsDoNotBreakToolResultCutoffTracing(t *testing.T) {
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("before")),
		{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call-1", Name: "lookup", Arguments: []byte(`{}`)}}}}},
		schema.NewTurn(schema.TurnRoundTimings, llm.System("timing")),
		schema.NewTurn(schema.TurnToolResults, llm.ToolResult("call-1", "result", false)),
		schema.NewTurn(schema.TurnUserInput, llm.User("after")),
	}
	if got := safeCutoff(history, 2); got != 1 {
		t.Fatalf("safe cutoff = %d, want 1 so tool result tracing crosses timing marker to assistant call", got)
	}
}
