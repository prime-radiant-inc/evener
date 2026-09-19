package research

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// writeTranscript writes a minimal valid v2 transcript: header + entries.
func writeTranscript(t *testing.T, dir, sid string, entries []transcript.Entry) string {
	t.Helper()
	sessions := filepath.Join(dir, "projects", "proj", "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessions, sid+".transcript.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if len(entries) == 0 {
		// Header-only file: still valid for walking.
		return path
	}
	return path
}

func timeNowPlus(seconds int64) time.Time {
	return time.Now().Add(time.Duration(seconds) * time.Second)
}

func TestWalkSessionTranscripts_NewestFirstAndLimit(t *testing.T) {
	dir := t.TempDir()
	a := writeTranscript(t, dir, "aaa", nil)
	b := writeTranscript(t, dir, "bbb", nil)
	if err := os.Chtimes(b, timeNowPlus(0), timeNowPlus(0)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(a, timeNowPlus(-3600), timeNowPlus(-3600)); err != nil {
		t.Fatal(err)
	}
	got, err := walkSessionTranscripts(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].SessionID != "bbb" || got[1].SessionID != "aaa" {
		t.Fatalf("got %+v, want bbb then aaa", got)
	}
	limited, err := walkSessionTranscripts(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 1 || limited[0].SessionID != "bbb" {
		t.Fatalf("limit=1 got %+v", limited)
	}
}

func TestLoadEntries_SkipsBadLines(t *testing.T) {
	dir := t.TempDir()
	headerLine := fmt.Sprintf(`{"kind":"header","format_version":%d}`, transcript.FormatVersion)
	userLine := `{"kind":"entry","seq":1,"turn":{"kind":"USER_INPUT","message":{"role":"user","content":[{"kind":"text","text":"hi"}]}}}`
	badLine := `{not json`
	path := filepath.Join(dir, "s.transcript.jsonl")
	content := headerLine + "\n" + userLine + "\n" + badLine + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, skipped, err := loadEntries(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || skipped != 1 {
		t.Fatalf("entries=%d skipped=%d, want 1/1", len(entries), skipped)
	}
	if entries[0].Turn.Kind != schema.TurnUserInput {
		t.Fatalf("kind = %s", entries[0].Turn.Kind)
	}
}

func TestLoadEntries_EmptyFileIsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.transcript.jsonl")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadEntries(path); err == nil {
		t.Fatal("loadEntries(0-byte file) = nil error, want headerless error")
	}
}

func assistantToolCallTurn(id string, calls []llm.ToolCallData, in, out int) transcript.Entry {
	content := make([]llm.ContentPart, 0, len(calls))
	for _, c := range calls {
		c := c
		content = append(content, llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &c})
	}
	return transcript.Entry{Kind: "entry", Turn: schema.Turn{
		Kind:    schema.TurnAssistant,
		Message: llm.Message{Role: "assistant", Content: content},
		Usage:   llm.Usage{InputTokens: in, OutputTokens: out},
	}}
}

func mutationCall(name string) llm.ToolCallData {
	return llm.ToolCallData{ID: "c_" + name, Name: name, Arguments: json.RawMessage(`{}`)}
}

func shellCall(command string) llm.ToolCallData {
	return llm.ToolCallData{
		ID:        "c_shell",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"` + command + `","description":"d"}`),
	}
}

func TestMeasureAdjacency_CountsEditThenTestCycles(t *testing.T) {
	entries := []transcript.Entry{
		assistantToolCallTurn("u1", []llm.ToolCallData{mutationCall("edit_file")}, 1000, 50),
		// result turn; adjacency scanner looks past it
		assistantToolCallTurn("u2", []llm.ToolCallData{shellCall("go test ./...")}, 2200, 80),
		assistantToolCallTurn("u3", []llm.ToolCallData{shellCall("go build ./...")}, 2400, 60),
		assistantToolCallTurn("u4", []llm.ToolCallData{mutationCall("apply_patch")}, 2500, 70),
		assistantToolCallTurn("u5", []llm.ToolCallData{shellCall("ls -la")}, 2600, 40), // not a test command
	}
	got := measureAdjacency(entries)
	if got.Cycles != 1 {
		t.Fatalf("Cycles = %d, want 1 (edit_file then go test)", got.Cycles)
	}
	if got.SavedPromptTokens != 2200 || got.SavedCompletionTokens != 80 {
		t.Fatalf("saved tokens = %d/%d, want 2200/80", got.SavedPromptTokens, got.SavedCompletionTokens)
	}
}

func toolResultsTurn(results ...llm.ToolResultData) transcript.Entry {
	content := make([]llm.ContentPart, 0, len(results))
	for _, r := range results {
		r := r
		content = append(content, llm.ContentPart{Kind: llm.ContentToolResult, ToolResult: &r})
	}
	return transcript.Entry{Kind: "entry", Turn: schema.Turn{
		Kind:    schema.TurnToolResults,
		Message: llm.Message{Role: "tool", Content: content},
	}}
}

func bigResult(name, text string) llm.ToolResultData {
	return llm.ToolResultData{ToolCallID: "c_" + name, Name: name, Content: text}
}

func TestMeasureLargeObservations_ResendAfterFirstTwo(t *testing.T) {
	big := strings.Repeat("x", 11*1024)
	small := "ok"
	entries := []transcript.Entry{
		toolResultsTurn(bigResult("shell", big), bigResult("read_file", small)),
		assistantToolCallTurn("r1", nil, 5000, 10), // request 1 sees it (free)
		assistantToolCallTurn("r2", nil, 5200, 10), // request 2 sees it (free)
		assistantToolCallTurn("r3", nil, 5400, 10), // request 3 pays: +len(big)
		// compaction boundary ends residency accounting
		{Kind: "entry", Turn: schema.Turn{Kind: schema.TurnCheckpoint}},
		assistantToolCallTurn("r4", nil, 2000, 10), // after boundary: not counted
	}
	got := measureLargeObservations(entries, 10*1024)
	if got.Results != 1 || got.TotalBytes != len(big) {
		t.Fatalf("results=%d bytes=%d, want 1/%d", got.Results, got.TotalBytes, len(big))
	}
	if got.ResendBytes != len(big) || got.ResendRequests != 1 {
		t.Fatalf("resend=%d over %d requests, want %d over 1", got.ResendBytes, got.ResendRequests, len(big))
	}
}

func TestMeasureLogVolume_OnlyDeclaredCommands(t *testing.T) {
	log := strings.Repeat("FAIL line\n", 600) // ~5.4 KiB, from go test
	entries := []transcript.Entry{
		assistantToolCallTurn("p", []llm.ToolCallData{shellCall("go test ./...")}, 10, 5),
		toolResultsTurn(bigResult("shell", log)),
		assistantToolCallTurn("r1", nil, 5000, 10),
		assistantToolCallTurn("r2", nil, 5200, 10),
		assistantToolCallTurn("r3", nil, 5400, 10),
	}
	vol := measureLogVolume(entries, 4*1024)
	if vol.Results != 1 || vol.TotalBytes != len(log) {
		t.Fatalf("vol results=%d bytes=%d", vol.Results, vol.TotalBytes)
	}
	if vol.ResendBytes != len(log) {
		t.Fatalf("resend = %d, want %d", vol.ResendBytes, len(log))
	}
}

func TestBuildReport_RanksProjections(t *testing.T) {
	adj := AdjacencyStats{Cycles: 3, SavedPromptTokens: 9000, SavedCompletionTokens: 300}
	obs := ObsResendStats{Results: 2, TotalBytes: 40 * 1024, ResendBytes: 20 * 1024, ResendRequests: 5}
	logs := LogVolumeStats{Results: 1, TotalBytes: 12 * 1024, ResendBytes: 8 * 1024, ResendRequests: 2}
	r := buildReport("/x", 5, 100, 2, adj, obs, logs, CompactionStats{}, 200000, 40000)
	if r.Sessions != 5 || r.Entries != 100 || r.SkippedLines != 2 {
		t.Fatalf("report header wrong: %+v", r)
	}
	if r.TotalInputTokens != 200000 || r.TotalOutputTokens != 40000 {
		t.Fatalf("token totals wrong")
	}
	// Traffic pct is saved tokens over recorded traffic (input+output).
	wantAdj := float64(9300) / float64(240000) * 100
	var gotAdj *MechanismProjection
	for i := range r.Projections {
		if r.Projections[i].Mechanism == "action-fusion" {
			gotAdj = &r.Projections[i]
		}
	}
	if gotAdj == nil {
		t.Fatal("no action-fusion projection")
	}
	if gotAdj.SavedTokens != 9300 {
		t.Fatalf("action-fusion saved = %d, want 9300", gotAdj.SavedTokens)
	}
	if math.Abs(gotAdj.TrafficPct-wantAdj) > 0.001 {
		t.Fatalf("action-fusion pct = %f, want %f", gotAdj.TrafficPct, wantAdj)
	}
	// Byte-based projections estimate tokens at bytes/4 (the contextmgr
	// char/4 estimator) over input tokens only.
	wantObs := float64(20*1024/4) / float64(200000) * 100
	var gotObs *MechanismProjection
	for i := range r.Projections {
		if r.Projections[i].Mechanism == "observation-pack" {
			gotObs = &r.Projections[i]
		}
	}
	if gotObs == nil {
		t.Fatal("no observation-pack projection")
	}
	if math.Abs(gotObs.TrafficPct-wantObs) > 0.001 {
		t.Fatalf("observation-pack pct = %f, want %f", gotObs.TrafficPct, wantObs)
	}
	// Compaction reminder is informational in slice 1.
	for _, p := range r.Projections {
		if p.Mechanism == "compaction-reminder" && !p.Informational {
			t.Fatal("compaction-reminder must be informational in slice 1")
		}
	}
}

func TestRenderReport_IncludesStopOrGoVerdict(t *testing.T) {
	// A report whose best projection is under 5% must say so plainly.
	r := buildReport("/x", 1, 1, 0, AdjacencyStats{}, ObsResendStats{}, LogVolumeStats{}, CompactionStats{}, 1000, 100)
	text := renderReport(r)
	if !strings.Contains(text, "under 5%") && !strings.Contains(text, "5%") {
		t.Fatalf("rendered report lacks stop-or-go statement:\n%s", text)
	}
}
