package agent

import (
	"encoding/json"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/schema/schematest"
	"primeradiant.com/evener/llm"
)

// In-memory history never holds transcript-only entries (resume and every
// writer keep them out). These are the three switches that would send a
// stray one to the model, so each must pass over one exactly as if it were
// not there.

func TestOrphanRepairPassesOverTranscriptOnlyEntries(t *testing.T) {
	plain := transcriptOnlyFixtureTurns()
	_, plainRepairs, plainAt := repairOrphanedToolResultsIndexed(plain)
	repaired, repairs, _ := repairOrphanedToolResultsIndexed(schematest.InterleaveTranscriptOnly(plain))
	if repairs != plainRepairs || len(plainAt) != 1 {
		t.Fatalf("repairs = %d, plain %d (plain insertions %v)", repairs, plainRepairs, plainAt)
	}
	var kept []schema.Turn
	for _, turn := range repaired {
		if !turn.Kind.TranscriptOnly() {
			kept = append(kept, turn)
		}
	}
	plainRepaired, _, _ := repairOrphanedToolResultsIndexed(plain)
	if len(kept) != len(plainRepaired) {
		t.Fatalf("repaired history has %d model turns, plain %d", len(kept), len(plainRepaired))
	}
	for i := range kept {
		if kept[i].Kind != plainRepaired[i].Kind || !reflect.DeepEqual(kept[i].Message, plainRepaired[i].Message) {
			t.Fatalf("turn %d = %s %+v, plain %s %+v", i, kept[i].Kind, kept[i].Message, plainRepaired[i].Kind, plainRepaired[i].Message)
		}
	}
}

func TestExpandHistoryPassesOverTranscriptOnlyEntries(t *testing.T) {
	plain, _, _ := repairOrphanedToolResultsIndexed(transcriptOnlyFixtureTurns())
	want := expandHistory(plain, replayScope{})
	if got := expandHistory(schematest.InterleaveTranscriptOnly(plain), replayScope{}); !reflect.DeepEqual(got, want) {
		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(want)
		t.Fatalf("wire messages differ:\n got %s\nwant %s", gotJSON, wantJSON)
	}
}

func TestResponsesContinuationDeltaPassesOverTranscriptOnlyEntries(t *testing.T) {
	anchor := schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "c1", Name: "read_file", Arguments: json.RawMessage(`{}`)}},
	}})
	delta := []schema.Turn{
		schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
			{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "c1", Name: "read_file", Content: "ok"}},
		}}),
		schema.NewTurn(schema.TurnUserInput, llm.User("next")),
	}
	if reason := responsesContinuationDeltaIneligibleReason(anchor, delta); reason != "" {
		t.Fatalf("the plain delta is ineligible (%s); the fixture no longer tests anything", reason)
	}
	if reason := responsesContinuationDeltaIneligibleReason(anchor, schematest.InterleaveTranscriptOnly(delta)); reason != "" {
		t.Fatalf("transcript-only entries made the delta ineligible: %s", reason)
	}
}
