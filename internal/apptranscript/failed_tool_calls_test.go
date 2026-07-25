package apptranscript

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

// toolCall describes one tool result to write into a fixture transcript: the tool
// that produced it, whether the result itself errored, and the process exit
// code its ToolState carries (nil for a tool that has none).
type toolCall struct {
	tool     string
	isError  bool
	exitCode *int64
}

func shellExit(code int64) *int64 { return &code }

// writeToolCallTranscript writes one ASSISTANT entry announcing the calls
// followed by one TOOL_RESULTS entry carrying their results, per element of
// `rounds`. That is the real on-disk shape: a tool result's name is resolvable
// from its own record, and also from the announcing call it answers.
func writeToolCallTranscript(t testing.TB, rounds [][]toolCall) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "failures.transcript.jsonl")
	records := []any{transcript.Header{Kind: "header", FormatVersion: transcript.FormatVersion, SessionID: "failures"}}
	seq := 0
	callID := 0
	for _, round := range rounds {
		announce := llm.Message{Role: llm.RoleAssistant}
		results := llm.Message{Role: llm.RoleTool}
		for _, c := range round {
			callID++
			id := "call_" + itoa(callID)
			announce.Content = append(announce.Content, llm.ContentPart{
				Kind:     llm.ContentToolCall,
				ToolCall: &llm.ToolCallData{ID: id, Name: c.tool, Arguments: json.RawMessage(`{}`)},
			})
			result := &llm.ToolResultData{ToolCallID: id, Name: c.tool, Content: "output", IsError: c.isError}
			if c.exitCode != nil {
				state, err := json.Marshal(map[string]any{"exit_code": *c.exitCode})
				if err != nil {
					t.Fatal(err)
				}
				result.ToolState = state
			}
			results.Content = append(results.Content, llm.ContentPart{Kind: llm.ContentToolResult, ToolResult: result})
		}
		stamp := time.Unix(1_700_000_000+int64(seq), 0).UTC()
		seq++
		records = append(records, transcript.Entry{Kind: "entry", Seq: seq, Turn: schema.Turn{
			Kind: schema.TurnAssistant, Message: announce, Timestamp: stamp,
		}})
		seq++
		records = append(records, transcript.Entry{Kind: "entry", Seq: seq, Turn: schema.Turn{
			Kind: schema.TurnToolResults, Message: results, Timestamp: stamp,
		}})
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
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf []byte
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	return string(buf)
}

func requireFailedToolCalls(t testing.TB, cache *TurnCache, path string, fromEntryOrdinal int) int {
	t.Helper()
	got, err := cache.FailedToolCallsFromFile(path, testMaxLineBytes, fromEntryOrdinal)
	if err != nil {
		t.Fatalf("FailedToolCallsFromFile: %v", err)
	}
	return got
}

// The whole point of the full scan: it covers rounds no windowed read ever
// loaded. Ordinal 0 means "no divergence cut", i.e. the entire transcript.
func TestFailedToolCallsCountsEveryFailureInTheTranscript(t *testing.T) {
	path := writeToolCallTranscript(t, [][]toolCall{
		{{tool: "shell", exitCode: shellExit(0)}},
		{{tool: "read_file", isError: true}},
		{{tool: "shell", exitCode: shellExit(0)}, {tool: "shell", exitCode: shellExit(1)}},
		{{tool: "grep"}},
		{{tool: "shell", exitCode: shellExit(127)}},
	})

	if got := requireFailedToolCalls(t, NewTurnCache(), path, 0); got != 3 {
		t.Fatalf("failed tool calls = %d, want 3", got)
	}
}

// A clean session is a real measurement, not an absence: the transcript records
// every tool result and none of them failed. The count is 0 and the read
// succeeds — it is the CLIENT's job to render a zero as nothing.
func TestFailedToolCallsReportsZeroForACleanSession(t *testing.T) {
	path := writeToolCallTranscript(t, [][]toolCall{
		{{tool: "shell", exitCode: shellExit(0)}},
		{{tool: "read_file"}},
	})

	if got := requireFailedToolCalls(t, NewTurnCache(), path, 0); got != 0 {
		t.Fatalf("failed tool calls = %d, want 0", got)
	}
}

// A nonzero shell exit is NOT a tool error (the result is clean, is_error is
// false), and it is still a failure the transcript marks with a --danger glyph.
// Counting only is_error would have reported 1 for the real session measured in
// kata hw2n, which shows 6 glyphs.
func TestFailedToolCallsCountsANonzeroShellExitEvenThoughTheToolResultIsClean(t *testing.T) {
	path := writeToolCallTranscript(t, [][]toolCall{{{tool: "shell", exitCode: shellExit(254)}}})

	if got := requireFailedToolCalls(t, NewTurnCache(), path, 0); got != 1 {
		t.Fatalf("failed tool calls = %d, want 1", got)
	}
}

// A nonzero exit code on a NON-shell tool is not a failure signal: only the
// shell descriptor reads exitCode as failure (transcript renderer's
// tools/shellTool.tsx), so counting it elsewhere would mark rows the reader
// sees no glyph on.
func TestFailedToolCallsIgnoresAnExitCodeOnANonShellTool(t *testing.T) {
	path := writeToolCallTranscript(t, [][]toolCall{{{tool: "read_file", exitCode: shellExit(1)}}})

	if got := requireFailedToolCalls(t, NewTurnCache(), path, 0); got != 0 {
		t.Fatalf("failed tool calls = %d, want 0", got)
	}
}

// A shell call with NO exit code recorded is still running, backgrounded, or
// from a producer that never captured one. Absent is not zero and it is not a
// failure either.
func TestFailedToolCallsIgnoresAShellCallWithNoExitCodeRecorded(t *testing.T) {
	path := writeToolCallTranscript(t, [][]toolCall{{{tool: "shell"}}})

	if got := requireFailedToolCalls(t, NewTurnCache(), path, 0); got != 0 {
		t.Fatalf("failed tool calls = %d, want 0", got)
	}
}

// Every shell alias the transcript renderer treats as a shell gets the same
// exit-code reading, or the count would disagree with the glyphs on exactly
// the providers that rename the tool.
func TestFailedToolCallsReadsTheExitCodeOfEveryShellAlias(t *testing.T) {
	path := writeToolCallTranscript(t, [][]toolCall{{
		{tool: "shell", exitCode: shellExit(1)},
		{tool: "exec_command", exitCode: shellExit(2)},
		{tool: "run_shell_command", exitCode: shellExit(3)},
	}})

	if got := requireFailedToolCalls(t, NewTurnCache(), path, 0); got != 3 {
		t.Fatalf("failed tool calls = %d, want 3", got)
	}
}

// communicate results render no tool row at all (ProjectTurn drops them), so a
// failed one carries no glyph and must not be counted.
func TestFailedToolCallsIgnoresACommunicateResult(t *testing.T) {
	path := writeToolCallTranscript(t, [][]toolCall{{{tool: "communicate", isError: true}}})

	if got := requireFailedToolCalls(t, NewTurnCache(), path, 0); got != 0 {
		t.Fatalf("failed tool calls = %d, want 0", got)
	}
}

// A tool result whose own record carries no name is resolved from the call that
// announced it, exactly as ProjectTurn resolves it — otherwise an unnamed shell
// result would lose its exit-code reading and an unnamed communicate result
// would be counted.
func TestFailedToolCallsResolvesAnUnnamedResultFromTheCallThatAnnouncedIt(t *testing.T) {
	path := writeToolCallTranscript(t, [][]toolCall{{
		{tool: "shell", exitCode: shellExit(1)},
		{tool: "communicate", isError: true},
	}})
	stripResultNames(t, path)

	if got := requireFailedToolCalls(t, NewTurnCache(), path, 0); got != 1 {
		t.Fatalf("failed tool calls = %d, want 1 (the shell exit, not the communicate)", got)
	}
}

// A fork child's transcript OPENS with a verbatim copy of the parent's prefix.
// Those failures were the PARENT's, so counting them charges another session's
// mistakes to this one — the same attribution bug DivergenceTurn fixes for
// tokens (kata 5tdg: a naive whole-file sum doubled a fork's reported spend).
func TestFailedToolCallsSkipsTheInheritedForkPrefix(t *testing.T) {
	path := writeToolCallTranscript(t, [][]toolCall{
		{{tool: "shell", exitCode: shellExit(1)}}, // entries 1-2: the parent's
		{{tool: "shell", exitCode: shellExit(1)}}, // entries 3-4: the parent's
		{{tool: "shell", exitCode: shellExit(2)}}, // entries 5-6: the child's own
	})

	if got := requireFailedToolCalls(t, NewTurnCache(), path, 5); got != 1 {
		t.Fatalf("failed tool calls from ordinal 5 = %d, want 1 (the child's own)", got)
	}
}

// An unopened aside fork has an empty own span. Zero is the true count for it —
// it has run nothing, so it has failed nothing.
func TestFailedToolCallsReportsZeroForAnEmptyOwnSpan(t *testing.T) {
	path := writeToolCallTranscript(t, [][]toolCall{{{tool: "shell", exitCode: shellExit(1)}}})

	if got := requireFailedToolCalls(t, NewTurnCache(), path, 99); got != 0 {
		t.Fatalf("failed tool calls beyond the transcript = %d, want 0", got)
	}
}

// A legacy format_version 1 transcript is unreadable to every semantic reader
// here, so the count is UNKNOWN and the error says so. Returning 0 would report
// a clean session for one nobody can read.
func TestFailedToolCallsPropagatesUnsupportedFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1.transcript.jsonl")
	if err := os.WriteFile(path, []byte(`{"kind":"header","format_version":1,"session_id":"old"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := NewTurnCache().FailedToolCallsFromFile(path, testMaxLineBytes, 0); err == nil {
		t.Fatal("FailedToolCallsFromFile on a v1 transcript returned no error; want unknown, not a fabricated 0")
	}
}

func TestFailedToolCallsReportsMissingTranscript(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.transcript.jsonl")

	_, err := NewTurnCache().FailedToolCallsFromFile(path, testMaxLineBytes, 0)
	if err == nil {
		t.Fatal("FailedToolCallsFromFile on a missing transcript returned no error")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error = %v, want it to wrap os.ErrNotExist", err)
	}
}

// The scan reads the whole file, so a repeat read of an unchanged transcript
// must not pay for it twice — the same file-identity gate UsageTotalFromFile
// memoizes on.
func TestFailedToolCallsMemoizesByFileIdentity(t *testing.T) {
	path := writeToolCallTranscript(t, [][]toolCall{{{tool: "shell", exitCode: shellExit(1)}}})
	cache := NewTurnCache()

	scans := countFailureScans(t, func() {
		requireFailedToolCalls(t, cache, path, 0)
		requireFailedToolCalls(t, cache, path, 0)
		requireFailedToolCalls(t, cache, path, 0)
	})
	if scans != 1 {
		t.Fatalf("transcript scans = %d, want 1 (two of three reads served from the memo)", scans)
	}
}

// A live session's transcript grows. The memo is keyed on file identity, so a
// grown file must be rescanned rather than answered from a stale count.
func TestFailedToolCallsRescansAfterTheTranscriptGrows(t *testing.T) {
	path := writeToolCallTranscript(t, [][]toolCall{{{tool: "shell", exitCode: shellExit(1)}}})
	cache := NewTurnCache()
	if got := requireFailedToolCalls(t, cache, path, 0); got != 1 {
		t.Fatalf("failed tool calls = %d, want 1", got)
	}

	grown := writeToolCallTranscript(t, [][]toolCall{
		{{tool: "shell", exitCode: shellExit(1)}},
		{{tool: "shell", exitCode: shellExit(1)}},
	})
	data, err := os.ReadFile(grown)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	if got := requireFailedToolCalls(t, cache, path, 0); got != 2 {
		t.Fatalf("failed tool calls after growth = %d, want 2", got)
	}
}

// Two divergence ordinals over one file are two different answers, so the memo
// key has to carry the ordinal or the second read returns the first's count.
func TestFailedToolCallsMemoDistinguishesDivergenceOrdinals(t *testing.T) {
	path := writeToolCallTranscript(t, [][]toolCall{
		{{tool: "shell", exitCode: shellExit(1)}},
		{{tool: "shell", exitCode: shellExit(1)}},
	})
	cache := NewTurnCache()

	if got := requireFailedToolCalls(t, cache, path, 0); got != 2 {
		t.Fatalf("failed tool calls from ordinal 0 = %d, want 2", got)
	}
	if got := requireFailedToolCalls(t, cache, path, 3); got != 1 {
		t.Fatalf("failed tool calls from ordinal 3 = %d, want 1", got)
	}
}

// countFailureScans runs fn with the package read observer installed and
// reports how many full transcript scans it performed.
func countFailureScans(t testing.TB, fn func()) int64 {
	t.Helper()
	var scans int64
	restore := InstallReadObserverForTesting(func(stats ReadStats) { scans += stats.failureScans })
	defer restore()
	fn()
	return scans
}

// stripResultNames rewrites every tool_result's own `name` to empty, leaving
// only the announcing tool_call to resolve it from — the shape a producer that
// omits the redundant name writes.
func stripResultNames(t testing.TB, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []byte
	for _, line := range splitLines(data) {
		if len(line) == 0 {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatal(err)
		}
		if turn, ok := rec["turn"].(map[string]any); ok {
			if msg, ok := turn["message"].(map[string]any); ok {
				if content, ok := msg["content"].([]any); ok {
					for _, part := range content {
						p, ok := part.(map[string]any)
						if !ok {
							continue
						}
						if result, ok := p["tool_result"].(map[string]any); ok {
							delete(result, "name")
						}
					}
				}
			}
		}
		encoded, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, encoded...)
		out = append(out, '\n')
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			lines = append(lines, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}
