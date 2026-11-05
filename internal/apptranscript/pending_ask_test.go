package apptranscript

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func TestVoicePendingAsk_ResultMergeRetainsOrigin(t *testing.T) {
	origin := &schema.ConversationOutputProvenance{Version: 1, InstanceID: "instance", RunGeneration: 9, RequestID: "request-a", FrontierID: "frontier-a"}
	turn := schema.Turn{Format: schema.TurnFormatIdentity, Kind: schema.TurnAssistant, TurnID: "turn-a", ConversationProvenance: origin, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call-a", Name: "ask_user", Arguments: json.RawMessage(`{"private":"original"}`)}}}}}
	calls, _ := ProjectEntryParts("turn-a", 17, turn, nil, nil, nil)
	if len(calls) != 1 {
		t.Fatalf("calls=%+v", calls)
	}
	canonical := json.RawMessage(`{"version":1,"questions":[{"question":"executed-719","options":[{"label":"2","detail":"d2"},{"label":"1","detail":"d1"}]}]}`)
	result := schema.Turn{Format: schema.TurnFormatIdentity, Kind: schema.TurnToolResults, TurnID: "turn-a", Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{Name: "ask_user", ToolCallID: "call-a", Content: "ack", ToolState: canonical}}}}}
	results, _ := ProjectEntryParts("turn-a", 18, result, nil, nil, nil)
	if len(results) != 1 {
		t.Fatalf("results=%+v", results)
	}
	merged := MergeThreadItems(calls[0], results[0])
	p := merged.ConversationProvenance
	if p == nil || p.RequestID != origin.RequestID || p.FrontierID != origin.FrontierID || p.RunGeneration != origin.RunGeneration || p.SourceItemID != merged.ID {
		t.Fatalf("matched result lost origin or final source ID: item=%+v provenance=%+v", merged, p)
	}
	if merged.Status != appwire.TurnStatusCompleted || string(merged.Raw) != string(canonical) || merged.CallID != "call-a" {
		t.Fatalf("result lost canonical content: %+v", merged)
	}
	turn.ConversationProvenance = nil
	legacy, _ := ProjectEntryParts("turn-a", 19, turn, nil, nil, nil)
	if MergeThreadItems(legacy[0], results[0]).ConversationProvenance != nil {
		t.Fatal("legacy provenance synthesized")
	}
}
