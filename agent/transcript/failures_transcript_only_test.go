package transcript

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/schema/schematest"
	"primeradiant.com/evener/llm"
)

func TestFailureCounterIgnoresTranscriptOnlyEntries(t *testing.T) {
	plain := []schema.Turn{
		schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "c1", Name: "read_file", Arguments: json.RawMessage(`{}`)}},
		}}),
		schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
			{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "c1", Name: "read_file", Content: "boom", IsError: true}},
		}}),
	}
	count := func(turns []schema.Turn) int {
		counter := NewFailureCounter(0)
		for _, turn := range turns {
			counter.Observe(turn)
		}
		return counter.Count()
	}
	want := count(plain)
	if want == 0 {
		t.Fatal("the fixture counts no failure")
	}
	if got := count(schematest.InterleaveTranscriptOnly(plain)); got != want {
		t.Fatalf("failures = %d, want %d", got, want)
	}
}
