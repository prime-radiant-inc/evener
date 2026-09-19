package contextmgr

import (
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func managedAssistant(group, callID string) schema.Turn {
	return schema.Turn{Kind: schema.TurnAssistant, AttemptGroupID: group, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: callID, Name: "managed"}}}}}
}

func managedResult(group, callID string) schema.Turn {
	return schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
		ManagedInvocationID: "invocation-" + group, ManagedAttemptGroupID: group,
		ToolCallID: callID, Name: "managed", Content: "resolved",
	}}}})
}

func TestManagedResultCutoffProtectsOverlappingOccurrenceSpans(t *testing.T) {
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("older prefix")),
		managedAssistant("first", "same-provider-id"),
		managedAssistant("second", "same-provider-id"),
		schema.NewTurn(schema.TurnUserInput, llm.User("accepted while pending")),
		managedResult("first", "same-provider-id"),
		schema.NewTurn(schema.TurnUserInput, llm.User("later input")),
		managedResult("second", "same-provider-id"),
	}
	// Keeping the second result pulls its assistant into the preserved tail;
	// that also retains the first result, which must keep its own occurrence.
	for _, boundary := range []int{5, 6} {
		if got := safeCutoff(history, boundary); got != 1 {
			t.Fatalf("boundary=%d cutoff=%d, want both complete occurrences from 1", boundary, got)
		}
	}
	if got := safeCutoff(history[1:], 4); got != -1 {
		t.Fatalf("cutoff=%d, want refusal when no prefix can be removed safely", got)
	}
}

func TestManagedResultCutoffRequiresExactAnnotation(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*llm.ToolResultData)
	}{
		{"ordinary", func(r *llm.ToolResultData) { r.ManagedInvocationID = ""; r.ManagedAttemptGroupID = "" }},
		{"missing invocation", func(r *llm.ToolResultData) { r.ManagedInvocationID = "" }},
		{"different attempt", func(r *llm.ToolResultData) { r.ManagedAttemptGroupID = "another" }},
		{"different ordinal", func(r *llm.ToolResultData) { r.ManagedToolIndex = 1 }},
		{"negative ordinal", func(r *llm.ToolResultData) { r.ManagedToolIndex = -1 }},
		{"different name", func(r *llm.ToolResultData) { r.Name = "other" }},
		{"different call", func(r *llm.ToolResultData) { r.ToolCallID = "other" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := managedResult("attempt", "call")
			test.change(result.Message.Content[0].ToolResult)
			history := []schema.Turn{
				schema.NewTurn(schema.TurnUserInput, llm.User("older prefix")),
				managedAssistant("attempt", "call"),
				schema.NewTurn(schema.TurnUserInput, llm.User("accepted input")),
				result,
			}
			if got := safeCutoff(history, 3); got != 2 {
				t.Fatalf("cutoff=%d, want existing ordinary boundary 2", got)
			}
		})
	}
}

func TestManagedResultCutoffUsesToolOrdinal(t *testing.T) {
	assistant := managedAssistant("attempt", "first")
	assistant.Message.Content = append([]llm.ContentPart{{Kind: llm.ContentText, Text: "working"}}, assistant.Message.Content...)
	assistant.Message.Content = append(assistant.Message.Content, llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "second", Name: "managed"}})
	result := managedResult("attempt", "second")
	result.Message.Content[0].ToolResult.ManagedToolIndex = 1
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("older prefix")), assistant,
		schema.NewTurn(schema.TurnUserInput, llm.User("accepted input")), result,
	}
	if got := safeCutoff(history, 3); got != 1 {
		t.Fatalf("cutoff=%d, want assistant containing the second tool call at 1", got)
	}
}
