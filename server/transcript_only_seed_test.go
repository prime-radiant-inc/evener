package server

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/schema/schematest"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// A restarted daemon seeds its turn snapshot from the transcript's entries.
// Transcript-only entries change nothing there except the identifiers today's
// projection derives from an entry's line index.
func TestRestoredSeedIgnoresTranscriptOnlyEntries(t *testing.T) {
	plain := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("first"), StableTurnID: "turn_m1"},
		{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: "reading"},
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "c1", Name: "read_file", Arguments: json.RawMessage(`{}`)}},
		}}},
		{Kind: schema.TurnToolResults, Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
			{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "c1", Name: "read_file", Content: "ok"}},
		}}},
		{Kind: schema.TurnHookCompleted, Message: llm.User("hook"), Hook: &schema.HookInfo{Event: "Stop"}},
		{Kind: schema.TurnUserInput, Message: llm.User("second")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("done")},
	}
	entries := func(turns []schema.Turn) []transcript.Entry {
		out := make([]transcript.Entry, len(turns))
		for i, turn := range turns {
			out[i] = transcript.Entry{Kind: "entry", Seq: i, Turn: turn}
		}
		return out
	}
	header := transcript.Header{SessionID: "s"}
	want, err := appTurnProjectionFromEntries(header, entries(plain))
	if err != nil {
		t.Fatal(err)
	}
	interleaved := entries(schematest.InterleaveTranscriptOnly(plain))
	got, err := appTurnProjectionFromEntries(header, interleaved)
	if err != nil {
		t.Fatal(err)
	}
	// The live turn-id floor is every persisted entry, transcript-only ones
	// included: the next live turn's id must not collide with, or fall short
	// of, an entry index the file already holds.
	if got.persistedEntries != len(interleaved) {
		t.Fatalf("persisted entries = %d, want all %d", got.persistedEntries, len(interleaved))
	}
	path := filepath.Join(t.TempDir(), "s.transcript.jsonl")
	w, err := transcript.NewWriterNoSync(path, header)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range interleaved {
		if _, err := w.Record(entry.Turn, transcript.RecordOptions{Place: transcript.PlaceVerbatim}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	fromFile, err := appTurnProjectionFromTranscriptFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if fromFile.persistedEntries != len(interleaved) {
		t.Fatalf("file persisted entries = %d, want all %d", fromFile.persistedEntries, len(interleaved))
	}
	if got.nextEntry != want.nextEntry || len(got.turns) != len(want.turns) {
		t.Fatalf("seed = %d turns, next entry %d; want %d turns, next entry %d", len(got.turns), got.nextEntry, len(want.turns), want.nextEntry)
	}
	indexDerived := map[string]bool{"id": true, "turnId": true, "transcriptEntryIndex": true, "transcriptKey": true}
	for divergence := range diffParity(want.turns, got.turns) {
		if divergence.Class == "item-field" && indexDerived[divergence.Field] || divergence.Class == "turn-field" && divergence.Field == "id" {
			continue
		}
		t.Errorf("seed diverges beyond index-derived identity: %s", divergence)
	}
	if got.turns[0].ID != "turn_m1" {
		t.Fatalf("a stable turn id changed: %q", got.turns[0].ID)
	}
}
