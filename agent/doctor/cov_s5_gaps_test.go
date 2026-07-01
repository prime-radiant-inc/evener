package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

// The existing statedir_test covers flag/SERF/XDG precedence; this covers the
// default ~/.local/state fallback when both env knobs are unset.
func TestResolveStateBase_DefaultFallback(t *testing.T) {
	t.Setenv("SERF_STATE_DIR", "")
	t.Setenv("XDG_STATE_HOME", "")
	got := ResolveStateBase("")
	if !strings.HasSuffix(got, filepath.Join(".local", "state")) {
		t.Errorf("default should end in .local/state, got %q", got)
	}
}

// applyRange start:N and A-B windows, plus the lo>hi clamp.
func TestApplyRange_StartAndSpan(t *testing.T) {
	base, sid := countFixture(t) // 3 turns
	// start:2 → skip the first turn, render 2 & 3.
	r, err := Transcript(base, sid, TranscriptOpts{Range: "start:2"})
	if err != nil {
		t.Fatal(err)
	}
	if r.TurnsRendered != 2 || r.Turns[0].Index != 2 {
		t.Errorf("start:2 rendered=%d first=%d, want 2 starting at index 2", r.TurnsRendered, r.Turns[0].Index)
	}
	// A-B span "2-3" → turns 2 and 3.
	r, err = Transcript(base, sid, TranscriptOpts{Range: "2-3"})
	if err != nil {
		t.Fatal(err)
	}
	if r.TurnsRendered != 2 || r.Turns[0].Index != 2 {
		t.Errorf("2-3 rendered=%d first=%d, want 2 starting at index 2", r.TurnsRendered, r.Turns[0].Index)
	}
}

func TestApplyRange_Directly(t *testing.T) {
	// start with lo>hi possibility: start:100 clamps lo to total, hi=total → empty.
	lo, hi := applyRange("start:100", 3)
	if lo != hi {
		t.Errorf("start past end should clamp to empty window, got [%d,%d)", lo, hi)
	}
	// A-B with an out-of-range hi keeps total.
	if lo, hi := applyRange("2-99", 3); lo != 1 || hi != 3 {
		t.Errorf("2-99 = [%d,%d), want [1,3)", lo, hi)
	}
	// Unrecognized token → whole transcript.
	if lo, hi := applyRange("garbage", 3); lo != 0 || hi != 3 {
		t.Errorf("garbage range = [%d,%d), want [0,3)", lo, hi)
	}
}

// Outline render exercises toolResultNames (including <unnamed> and (error)).
func TestRenderTranscript_OutlineToolResults(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid := sidB
	turns := []schema.Turn{
		schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
			toolResult("job_watch", "ok", false),
			toolResult("", "boom", true), // unnamed + error
		}}),
	}
	writeRichSession(t, bucket, sid, turns, nil, schema.SessionMeta{})
	r, err := Transcript(base, sid, TranscriptOpts{Format: "outline"})
	if err != nil {
		t.Fatal(err)
	}
	out := RenderTranscript(r, "outline")
	if !strings.Contains(out, "results:") || !strings.Contains(out, "job_watch") {
		t.Errorf("outline should list tool results:\n%s", out)
	}
	if !strings.Contains(out, "<unnamed>(error)") {
		t.Errorf("outline should mark the unnamed error result:\n%s", out)
	}
}

// Markdown render with a result-tool call exercises the ⇒ (result) label branch.
func TestRenderTranscript_MarkdownResultLabel(t *testing.T) {
	base, sid := countFixture(t)
	r, err := Transcript(base, sid, TranscriptOpts{})
	if err != nil {
		t.Fatal(err)
	}
	out := RenderTranscript(r, "markdown")
	if !strings.Contains(out, "⇒ communicate (result)") {
		t.Errorf("markdown should flag the communicate result tool:\n%s", out)
	}
	if !strings.Contains(out, "→ read_file") {
		t.Errorf("markdown should show the non-result tool call:\n%s", out)
	}
}

// summarizeTurn must skip content parts whose ToolCall/ToolResult pointer is nil.
func TestSummarizeTurn_SkipsNilParts(t *testing.T) {
	e := transcript.Entry{Turn: schema.NewTurn(schema.TurnAssistant, llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.ContentPart{
			{Kind: llm.ContentToolCall, ToolCall: nil},
			{Kind: llm.ContentToolResult, ToolResult: nil},
			{Kind: llm.ContentText, Text: "hi"},
		},
	})}
	ts := summarizeTurn(1, e, "communicate")
	if len(ts.ToolCalls) != 0 || len(ts.ToolResults) != 0 {
		t.Errorf("nil parts should be skipped: %+v", ts)
	}
	if ts.Text != "hi" {
		t.Errorf("text = %q, want hi", ts.Text)
	}
}

func TestToolResultContentText_Kinds(t *testing.T) {
	if got := toolResultContentText(nil); got != "" {
		t.Errorf("nil = %q, want empty", got)
	}
	if got := toolResultContentText("plain"); got != "plain" {
		t.Errorf("string = %q, want plain", got)
	}
	if got := toolResultContentText(map[string]any{"a": 1}); !strings.Contains(got, "\"a\"") {
		t.Errorf("map should marshal to json, got %q", got)
	}
}

// loadTranscript tolerates a partial trailing line but rejects a malformed
// interior line.
func TestLoadTranscript_PartialTrailingTolerated(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid := sidA
	sess := filepath.Join(bucket, "sessions")
	writeFile(t, filepath.Join(sess, sid+".transcript.jsonl"),
		`{"kind":"header","session_id":"`+sid+`"}`+"\n"+`{"kind":"api_call"`)
	doc, err := loadTranscript(filepath.Join(sess, sid+".transcript.jsonl"))
	if err != nil {
		t.Fatalf("partial trailing line should be tolerated: %v", err)
	}
	if doc.Header.SessionID != sid {
		t.Errorf("header not parsed: %+v", doc.Header)
	}
}

func TestLoadTranscript_MalformedInteriorLineErrors(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid := sidA
	sess := filepath.Join(bucket, "sessions")
	writeFile(t, filepath.Join(sess, sid+".transcript.jsonl"),
		`{not json`+"\n"+`{"kind":"header"}`+"\n")
	if _, err := loadTranscript(filepath.Join(sess, sid+".transcript.jsonl")); err == nil {
		t.Fatal("malformed interior line should error")
	}
}

func TestLoadTranscript_BadEntryErrors(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid := sidA
	sess := filepath.Join(bucket, "sessions")
	// An entry line whose "turn" field is the wrong shape fails entry unmarshal.
	writeFile(t, filepath.Join(sess, sid+".transcript.jsonl"),
		`{"kind":"entry","turn":"not-an-object"}`+"\n"+`{"kind":"header"}`+"\n")
	if _, err := loadTranscript(filepath.Join(sess, sid+".transcript.jsonl")); err == nil {
		t.Fatal("malformed entry should error")
	}
}

// locateInBucket falls through to constructing the bucket path when the hash was
// not enumerated, and reports not-found for an unknown hash.
func TestLocate_ProjRefUnknownHash(t *testing.T) {
	base := t.TempDir()
	writeSession(t, stateHomeBucket(base, hash1), sidA) // projects/ exists, but hash2 does not
	if _, err := Locate(base, "proj:"+hash2+":"+sidA); err == nil {
		t.Fatal("proj ref to an unenumerated/missing bucket should error")
	}
}

// Watches surfaces the jobs-read error rather than a partial report.
func TestWatches_JobsUnreadable(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid := sidA
	writeSession(t, bucket, sid)
	// Replace jobs.jsonl with a directory so ReadEvents fails.
	jobs := filepath.Join(bucket, "sessions", sid, "jobs.jsonl")
	if err := writeDirAt(jobs); err != nil {
		t.Fatal(err)
	}
	if _, err := Watches(base, sid, WatchOpts{}); err == nil {
		t.Fatal("unreadable jobs should surface an error")
	}
}

func TestTerminalKind_Default(t *testing.T) {
	if got := terminalKind(jobstore.EventKind("something_else")); got != "something_else" {
		t.Errorf("terminalKind fallback = %q, want the raw kind", got)
	}
}

// expandNode records a note (not an error) when a node's jobs.jsonl is unreadable.
func TestTree_JobsUnreadable(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid := sidA
	writeSession(t, bucket, sid)
	jobs := filepath.Join(bucket, "sessions", sid, "jobs.jsonl")
	if err := writeDirAt(jobs); err != nil {
		t.Fatal(err)
	}
	root, err := Tree(base, sid, TreeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(root.Note, "jobs unreadable") {
		t.Errorf("expected jobs-unreadable note, got %q", root.Note)
	}
}

// A delegate whose child transcript is missing is listed with a "transcript not
// found" note rather than being dropped.
func TestTree_DelegateChildTranscriptMissing(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid := sidA
	writeSession(t, bucket, sid)
	jobs := filepath.Join(bucket, "sessions", sid, "jobs.jsonl")
	writeJobsEvents(t, jobs, []jobstore.Event{
		{Kind: jobstore.EventDelegateCreated, DelegateID: "d1", Delegate: &jobstore.DelegateEvent{
			ChildSessionID: "01MISSINGCHILDSESSIONXXXXXX", AgentType: "ghost"}},
	})
	root, err := Tree(base, sid, TreeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(root.Children) != 1 || !strings.Contains(root.Children[0].Note, "transcript not found") {
		t.Errorf("missing child should be noted, got %+v", root.Children)
	}
}

func TestTruncateAndAtoi(t *testing.T) {
	if got := truncate("short", 80); got != "short" {
		t.Errorf("short string unchanged, got %q", got)
	}
	long := strings.Repeat("x", 100)
	got := truncate(long, 10)
	if len([]rune(got)) != 11 || !strings.HasSuffix(got, "…") {
		t.Errorf("truncate should cut to 10 + ellipsis, got %q", got)
	}
	if atoi("12x3") != 0 {
		t.Error("atoi with a non-digit should return 0")
	}
	if atoi("042") != 42 {
		t.Error("atoi should parse digits")
	}
}

// RenderCount singular ("1 call") branch.
func TestRenderCount_Singular(t *testing.T) {
	out := RenderCount(CountResult{Tool: "read_file", Calls: 1})
	if !strings.Contains(out, "1 call") || strings.Contains(out, "1 calls") {
		t.Errorf("single call should render singular, got %q", out)
	}
}

// writeDirAt removes any file at path and creates a directory there, so a
// subsequent os.ReadFile of that path fails.
func writeDirAt(path string) error {
	_ = os.Remove(path)
	return os.MkdirAll(path, 0o755)
}
