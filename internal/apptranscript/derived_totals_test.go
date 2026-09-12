package apptranscript

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func filepathJoinDerivedTotals(t testing.TB, name string) string {
	t.Helper()
	return filepath.Join(t.TempDir(), name)
}

// renderDerivedTotalsTranscript renders a header plus entries into the
// on-disk transcript bytes. writeDerivedTotalsTranscript writes them to a
// file; the differential fuzz seeds them directly.
func renderDerivedTotalsTranscript(t testing.TB, entries []schema.Turn) []byte {
	t.Helper()
	records := []any{transcript.Header{Kind: "header", FormatVersion: transcript.FormatVersion, SessionID: "derived"}}
	for i, turn := range entries {
		turn.Timestamp = time.Unix(1_700_000_000+int64(i), 0).UTC()
		records = append(records, transcript.Entry{Kind: "entry", Seq: i + 1, Turn: turn})
	}
	var data []byte
	for _, record := range records {
		line, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, line...)
		data = append(data, '\n')
	}
	return data
}

func marshalDerivedTotalsToolState(t testing.TB, state map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// writeDerivedTotalsTranscript writes a transcript whose entries carry both
// usage and tool calls/results, so a test can name the exact token sum and
// failure count it expects the combined scan to produce.
func writeDerivedTotalsTranscript(t testing.TB, entries []schema.Turn) string {
	t.Helper()
	path := filepathJoinDerivedTotals(t, "derived.transcript.jsonl")
	if err := os.WriteFile(path, renderDerivedTotalsTranscript(t, entries), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// requireDerivedTotalsFromFile is DerivedTotalsFromFile's test helper.
func requireDerivedTotalsFromFile(t testing.TB, cache *TurnCache, path string, maxLineBytes, fromEntryOrdinal int) (*appwire.EvenerUsage, int) {
	t.Helper()
	usage, failures, err := cache.DerivedTotalsFromFile(path, maxLineBytes, fromEntryOrdinal)
	if err != nil {
		t.Fatalf("DerivedTotalsFromFile: %v", err)
	}
	return usage, failures
}

// The combined scan answers exactly what the two individual scans answer:
// one transcript, both figures, one pass.
func TestDerivedTotalsFromFileMatchesIndividualScans(t *testing.T) {
	path := writeDerivedTotalsTranscript(t, []schema.Turn{
		{Kind: schema.TurnAssistant, Message: llm.Assistant("first"), Usage: llm.Usage{InputTokens: 100, OutputTokens: 10, TotalTokens: 110}},
		{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "c1", Name: "shell"}},
		}}},
		{Kind: schema.TurnToolResults, Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
			{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "c1", Name: "shell", IsError: true}},
		}}},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("after failure"), Usage: llm.Usage{InputTokens: 200, OutputTokens: 20, TotalTokens: 220}},
		{Kind: schema.TurnToolResults, Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
			{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "c2", Name: "shell", ToolState: marshalDerivedTotalsToolState(t, map[string]any{"exit_code": 1})}},
		}}},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("clean"), Usage: llm.Usage{InputTokens: 50, OutputTokens: 5, TotalTokens: 55}},
		{Kind: schema.TurnToolResults, Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
			{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "c3", Name: "shell", ToolState: marshalDerivedTotalsToolState(t, map[string]any{"exit_code": 0})}},
		}}},
	})

	cache := NewTurnCache()
	usage, failures := requireDerivedTotalsFromFile(t, cache, path, testMaxLineBytes, 0)
	wantUsage := &appwire.EvenerUsage{InputTokens: 350, OutputTokens: 35, TotalTokens: 385}
	if usage == nil || *usage != *wantUsage {
		t.Fatalf("combined usage = %+v, want %+v", usage, wantUsage)
	}
	if failures != 2 {
		t.Fatalf("combined failures = %d, want 2 (IsError + nonzero exit)", failures)
	}

	// The individual scans over the same file must agree with the combined one.
	individualUsage := requireUsageTotalFromFile(t, NewTurnCache(), path, testMaxLineBytes, 0)
	if individualUsage == nil || *individualUsage != *wantUsage {
		t.Fatalf("individual usage = %+v, want %+v", individualUsage, wantUsage)
	}
	individualFailures := requireFailedToolCalls(t, NewTurnCache(), path, 0)
	if individualFailures != 2 {
		t.Fatalf("individual failures = %d, want 2", individualFailures)
	}
}

// A fork child's inherited prefix contributes to neither figure, and the
// failure rule still resolves tool names from inherited calls.
func TestDerivedTotalsFromFileSkipsInheritedForkPrefix(t *testing.T) {
	path := writeDerivedTotalsTranscript(t, []schema.Turn{
		// Parent's prefix: entry 1 announces a call, entry 2 answers it
		// with a failure, entry 3 spends tokens.
		{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "p1", Name: "shell"}},
		}}},
		{Kind: schema.TurnToolResults, Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
			{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "p1", IsError: true}}, // nameless: resolved from p1's call
		}}},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("parent spend"), Usage: llm.Usage{InputTokens: 999, OutputTokens: 99, TotalTokens: 1098}},
		// Child's own history: entry 4 answers the inherited call with a
		// failure, entry 5 spends its own tokens.
		{Kind: schema.TurnToolResults, Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
			{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "p1", IsError: true}},
		}}},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("child spend"), Usage: llm.Usage{InputTokens: 100, OutputTokens: 10, TotalTokens: 110}},
	})

	usage, failures := requireDerivedTotalsFromFile(t, NewTurnCache(), path, testMaxLineBytes, 4)
	wantUsage := &appwire.EvenerUsage{InputTokens: 100, OutputTokens: 10, TotalTokens: 110}
	if usage == nil || *usage != *wantUsage {
		t.Fatalf("post-divergence usage = %+v, want %+v (never the parent's prefix)", usage, wantUsage)
	}
	if failures != 1 {
		t.Fatalf("post-divergence failures = %d, want 1 (the child's own; the parent's is excluded but its call still names the result)", failures)
	}
}

// An empty own span reports absent usage, not zero, and a zero failure count is
// still a real measurement.
func TestDerivedTotalsFromFileAbsentUsageForEmptySpan(t *testing.T) {
	path := writeDerivedTotalsTranscript(t, []schema.Turn{
		{Kind: schema.TurnAssistant, Message: llm.Assistant("parent only"), Usage: llm.Usage{InputTokens: 100, OutputTokens: 10, TotalTokens: 110}},
	})

	usage, failures := requireDerivedTotalsFromFile(t, NewTurnCache(), path, testMaxLineBytes, 3)
	if usage != nil {
		t.Fatalf("usage = %+v, want nil (an unopened fork has spent nothing)", usage)
	}
	if failures != 0 {
		t.Fatalf("failures = %d, want 0", failures)
	}
}

// The combined scan memoizes on the same file-identity gate the individual
// memos use, so a repeat read does not rescan; a grown file does.
func TestDerivedTotalsFromFileMemoizesByFileIdentity(t *testing.T) {
	path := writeDerivedTotalsTranscript(t, []schema.Turn{
		{Kind: schema.TurnAssistant, Message: llm.Assistant("one"), Usage: llm.Usage{InputTokens: 100, OutputTokens: 10, TotalTokens: 110}},
		{Kind: schema.TurnToolResults, Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
			{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "c1", Name: "shell", IsError: true}},
		}}},
	})
	cache := NewTurnCache()

	var scans int64
	restore := InstallReadObserverForTesting(func(stats ReadStats) { scans += stats.derivedScans })
	t.Cleanup(restore)

	first, firstFailures := requireDerivedTotalsFromFile(t, cache, path, testMaxLineBytes, 0)
	second, secondFailures := requireDerivedTotalsFromFile(t, cache, path, testMaxLineBytes, 0)
	if first == nil || second == nil || *first != *second {
		t.Fatalf("memoized usage differs: %+v vs %+v", first, second)
	}
	if firstFailures != secondFailures {
		t.Fatalf("memoized failures differ: %d vs %d", firstFailures, secondFailures)
	}
	if scans != 1 {
		t.Fatalf("transcript derived scans = %d, want 1 (second read served from the memo)", scans)
	}
}

// A missing or legacy transcript is unknown, not a fabricated zero.
func TestDerivedTotalsFromFilePropagatesErrors(t *testing.T) {
	missing := filepathJoinDerivedTotals(t, "absent.transcript.jsonl")
	if _, _, err := NewTurnCache().DerivedTotalsFromFile(missing, testMaxLineBytes, 0); err == nil {
		t.Fatal("DerivedTotalsFromFile on a missing transcript returned no error")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error = %v, want it to wrap os.ErrNotExist", err)
	}

	legacy := filepathJoinDerivedTotals(t, "legacy.transcript.jsonl")
	if err := os.WriteFile(legacy, []byte(`{"kind":"header","format_version":1}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewTurnCache().DerivedTotalsFromFile(legacy, testMaxLineBytes, 0); err == nil {
		t.Fatal("DerivedTotalsFromFile on a v1 transcript returned no error; want unknown, not a fabricated figure")
	}
}
