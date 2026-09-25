package atif

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/schema/schematest"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// A trajectory exports the conversation; transcript-only entries were never
// part of it, and one between an assistant's calls and their results must not
// orphan the results.
func TestConvertSkipsTranscriptOnlyEntries(t *testing.T) {
	at := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	stamp := func(turn schema.Turn) schema.Turn { turn.Timestamp = at; return turn }
	plain := []schema.Turn{
		stamp(schema.NewTurn(schema.TurnUserInput, llm.User("read it"))),
		stamp(schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "c1", Name: "read_file", Arguments: json.RawMessage(`{}`)}},
		}})),
		stamp(schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
			{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "c1", Name: "read_file", Content: "ok", DurationMS: 4}},
		}})),
		stamp(schema.NewTurn(schema.TurnAssistant, llm.Assistant("done"))),
	}
	entries := func(turns []schema.Turn) []transcript.Entry {
		out := make([]transcript.Entry, len(turns))
		for i, turn := range turns {
			out[i] = transcript.Entry{Kind: "entry", Seq: i, Turn: turn}
		}
		return out
	}
	header := transcript.Header{SessionID: "s", CreatedAt: at}
	want := Convert(header, entries(plain))
	got := Convert(header, entries(schematest.InterleaveTranscriptOnly(plain)))
	if !reflect.DeepEqual(got, want) {
		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(want)
		t.Fatalf("trajectory differs:\n got %s\nwant %s", gotJSON, wantJSON)
	}
}
