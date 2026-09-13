package apptranscript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// writeFoldedRoundTranscript writes one real round — an assistant entry with
// usage announcing a tool call, and the failed result answering it — then the
// fold's copies of both, tagged the way publishFoldTransaction tags them, and
// the marker that claims them. That is the on-disk shape after a fold: the
// originals are still there, and the copies are there too.
func writeFoldedRoundTranscript(t testing.TB) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "folded.transcript.jsonl")
	announce := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
		Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_1", Name: "exec_command", Arguments: json.RawMessage(`{}`)},
	}}}
	// The result names no tool of its own, so counting it at all depends on
	// the call above resolving its name — including through the copy.
	results := llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{
		Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_1", Content: "boom", IsError: true},
	}}}
	stamp := func(i int) time.Time { return time.Unix(1_700_000_000+int64(i), 0).UTC() }
	usage := llm.Usage{InputTokens: 100, OutputTokens: 10, TotalTokens: 110}
	assistant := schema.Turn{Kind: schema.TurnAssistant, Message: announce, Usage: usage, Timestamp: stamp(0)}
	toolResults := schema.Turn{Kind: schema.TurnToolResults, Message: results, Timestamp: stamp(1)}
	copyOf := func(turn schema.Turn, at int) schema.Turn {
		turn.ContextReplay = true
		turn.CompactionFoldID = "fold_totals"
		turn.Timestamp = stamp(at)
		return turn
	}
	marker := schema.Turn{Kind: schema.TurnSummary, Message: llm.System("[CONTEXT SUMMARY]\nsummary\n[END SUMMARY]"), CompactionFoldID: "fold_totals", OwningTurnID: "turn_fold", Timestamp: stamp(4)}
	records := []any{
		transcript.Header{Kind: "header", FormatVersion: transcript.FormatVersion, SessionID: "folded"},
		transcript.Entry{Kind: "entry", Seq: 1, Turn: assistant},
		transcript.Entry{Kind: "entry", Seq: 2, Turn: toolResults},
		transcript.Entry{Kind: "entry", Seq: 3, Turn: copyOf(assistant, 2)},
		transcript.Entry{Kind: "entry", Seq: 4, Turn: copyOf(toolResults, 3)},
		transcript.Entry{Kind: "entry", Seq: 5, Turn: marker},
	}
	var data []byte
	for _, record := range records {
		line, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		data = append(append(data, line...), '\n')
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A fold's replay copies are the same work written down twice. Counting them
// charges a session for tokens it never spent and reports failures that never
// happened, and every later fold widens the gap.
func TestAggregateTotalsIgnoreReplayCopies(t *testing.T) {
	path := writeFoldedRoundTranscript(t)

	usage := requireUsageTotalFromFile(t, NewTurnCache(), path, testMaxLineBytes, 0)
	want := &appwire.EvenerUsage{InputTokens: 100, OutputTokens: 10, TotalTokens: 110}
	if usage == nil || *usage != *want {
		t.Fatalf("usage total = %+v, want the round counted once %+v", usage, want)
	}

	failures, err := NewTurnCache().FailedToolCallsFromFile(path, testMaxLineBytes, 0)
	if err != nil {
		t.Fatalf("FailedToolCallsFromFile: %v", err)
	}
	if failures != 1 {
		t.Fatalf("failed tool calls = %d, want the one failure counted once", failures)
	}

	derivedUsage, derivedFailures, err := NewTurnCache().DerivedTotalsFromFile(path, testMaxLineBytes, 0)
	if err != nil {
		t.Fatalf("DerivedTotalsFromFile: %v", err)
	}
	if derivedUsage == nil || *derivedUsage != *want || derivedFailures != 1 {
		t.Fatalf("derived totals = %+v / %d failures, want the same figures the two single-purpose scans report", derivedUsage, derivedFailures)
	}
}
