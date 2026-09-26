package apptranscript

import (
	"encoding/json"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func TestProjectTurnPartsReportsTheContentPartOfEachItem(t *testing.T) {
	communicate := json.RawMessage(`{"message":"hello"}`)
	assistant := schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentText, Text: ""}, // empty: no item, still part 0
		{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "think"}},
		{Kind: llm.ContentText, Text: "hello"},
		{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "c1", Name: "communicate", Arguments: communicate}}, // deferred to its result turn: hidden
		{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "c2", Name: "read_file", Arguments: json.RawMessage(`{}`)}},
	}}}
	items, parts := ProjectTurnParts("turn_1", 1, assistant, NewToolCallRegistry(), nil, nil)
	if !reflect.DeepEqual(parts, []int{1, 2, 4}) {
		t.Fatalf("parts = %v, want [1 2 4]", parts)
	}
	if want := ProjectTurn("turn_1", 1, assistant, NewToolCallRegistry(), nil, nil); !reflect.DeepEqual(items, want) {
		t.Fatalf("ProjectTurnParts items differ from ProjectTurn:\n got %+v\nwant %+v", items, want)
	}

	results := schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
		{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "c1", Name: "communicate"}},
		{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "c2", Name: "read_file", Content: "ok"}},
	}}}
	// c1's result carries no seeded CommRawArgs (a fresh registry, not the
	// paired assistant turn's), so the deferred communicate healed branch
	// finds nothing to render — same "hidden" outcome as the assistant turn,
	// for the same reason ProjectTurnParts must track (part 0 of this entry
	// projects no item).
	if _, parts := ProjectTurnParts("turn_2", 2, results, NewToolCallRegistry(), nil, nil); !reflect.DeepEqual(parts, []int{1}) {
		t.Fatalf("tool result parts = %v, want [1]", parts)
	}

	user := schema.NewTurn(schema.TurnUserInput, llm.User("hi"))
	if _, parts := ProjectTurnParts("turn_3", 3, user, nil, nil, nil); !reflect.DeepEqual(parts, []int{0}) {
		t.Fatalf("user parts = %v, want [0]", parts)
	}
	empty := schema.NewTurn(schema.TurnModelSwitch, llm.User(""))
	if items, parts := ProjectTurnParts("turn_4", 4, empty, nil, nil, nil); len(items) != 0 || len(parts) != 0 {
		t.Fatalf("empty model switch projected %v / %v", items, parts)
	}
}
