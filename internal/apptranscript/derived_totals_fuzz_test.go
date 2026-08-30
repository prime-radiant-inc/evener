package apptranscript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// derivedTotalsSeedTranscript renders a header plus entries into the on-disk
// transcript shape, for seeding the differential fuzz below with realistic
// files.
func derivedTotalsSeedTranscript(f *testing.F, entries []schema.Turn) []byte {
	f.Helper()
	records := []any{transcript.Header{Kind: "header", FormatVersion: transcript.FormatVersion, SessionID: "derived"}}
	for i, turn := range entries {
		turn.Timestamp = time.Unix(1_700_000_000+int64(i), 0).UTC()
		records = append(records, transcript.Entry{Kind: "entry", Seq: i + 1, Turn: turn})
	}
	var data []byte
	for _, record := range records {
		line, err := json.Marshal(record)
		if err != nil {
			f.Fatal(err)
		}
		data = append(data, line...)
		data = append(data, '\n')
	}
	return data
}

// FuzzDerivedTotalsMatchesIndividualScans pins DerivedTotalsFromFile to the
// two scans it consolidates: for ANY file content and any divergence ordinal,
// the combined single-pass scan must agree with UsageTotalFromFile and
// FailedToolCallsFromFile — the same usage sum, the same failure count, and
// an error on exactly the same inputs. This is the drift alarm for
// derivedTotalsEntry's narrow decode: a field dropped or mistyped there would
// silently undercount relative to the individual scans, which is precisely
// the divergence this differential detects.
func FuzzDerivedTotalsMatchesIndividualScans(f *testing.F) {
	toolState := func(state map[string]any) json.RawMessage {
		raw, err := json.Marshal(state)
		if err != nil {
			f.Fatal(err)
		}
		return raw
	}
	full := derivedTotalsSeedTranscript(f, []schema.Turn{
		{Kind: schema.TurnAssistant, Message: llm.Assistant("first"), Usage: llm.Usage{InputTokens: 100, OutputTokens: 10, TotalTokens: 110}},
		{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "c1", Name: "shell"}},
		}}},
		{Kind: schema.TurnToolResults, Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
			{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "c1", IsError: true}}, // nameless: resolved from c1's call
		}}},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("after failure"), Usage: llm.Usage{InputTokens: 200, OutputTokens: 20, TotalTokens: 220}},
		{Kind: schema.TurnToolResults, Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
			{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "c2", Name: "shell", ToolState: toolState(map[string]any{"exit_code": 1})}},
		}}},
		{Kind: schema.TurnToolResults, Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
			{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "c3", Name: "shell", ToolState: toolState(map[string]any{"exit_code": 0})}},
		}}},
	})
	f.Add(full, 0)
	f.Add(full, 3) // a fork child's divergence cut mid-file
	f.Add(full, 100)
	f.Add(full, -1)
	f.Add(derivedTotalsSeedTranscript(f, nil), 0) // header only
	f.Add(derivedTotalsSeedTranscript(f, []schema.Turn{{Kind: schema.TurnAssistant, Message: llm.Assistant("no usage")}}), 0)
	f.Add([]byte(nil), 0)          // empty file
	f.Add([]byte("not json\n"), 0) // rejected by the format gate
	f.Add([]byte(`{"kind":"header","format_version":1}`+"\n"), 0)

	f.Fuzz(func(t *testing.T, content []byte, fromEntryOrdinal int) {
		path := filepath.Join(t.TempDir(), "derived.jsonl")
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
		usage, failures, combinedErr := NewTurnCache().DerivedTotalsFromFile(path, testMaxLineBytes, fromEntryOrdinal)
		wantUsage, usageErr := NewTurnCache().UsageTotalFromFile(path, testMaxLineBytes, fromEntryOrdinal)
		wantFailures, failuresErr := NewTurnCache().FailedToolCallsFromFile(path, testMaxLineBytes, fromEntryOrdinal)
		if (combinedErr != nil) != (usageErr != nil) || (combinedErr != nil) != (failuresErr != nil) {
			t.Fatalf("error disagreement: combined=%v usage=%v failures=%v", combinedErr, usageErr, failuresErr)
		}
		if combinedErr != nil {
			return
		}
		switch {
		case (usage == nil) != (wantUsage == nil):
			t.Fatalf("usage presence disagreement: combined=%+v individual=%+v", usage, wantUsage)
		case usage != nil && *usage != *wantUsage:
			t.Fatalf("usage disagreement: combined=%+v individual=%+v", *usage, *wantUsage)
		}
		if failures != wantFailures {
			t.Fatalf("failure count disagreement: combined=%d individual=%d", failures, wantFailures)
		}
	})
}
