package contextmgr

import (
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func TestRoundTimingsAreTransparentToContextAccountingAndElicitRender(t *testing.T) {
	base := []schema.Turn{schema.NewTurn(schema.TurnUserInput, llm.User("question"))}
	timing := schema.NewTurn(schema.TurnRoundTimings, llm.System(strings.Repeat("timing marker ", 200)))
	withTiming := append(append([]schema.Turn{}, base...), timing)
	if got, want := estimateTokens(withTiming), estimateTokens(base); got != want {
		t.Fatalf("timing marker changed estimated tokens: got %d, want %d", got, want)
	}
	if got, want := attentionTransparentTurnCount(withTiming), attentionTransparentTurnCount(base); got != want {
		t.Fatalf("timing marker changed recent turn count: got %d, want %d", got, want)
	}
	if got := renderTurnForElicit(timing); got != "" {
		t.Fatalf("timing marker rendered into elicit context: %q", got)
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
