package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// The existing statedir_test covers flag/EVENER/XDG precedence; this covers the
// default ~/.local/state fallback when both env knobs are unset.
func TestResolveStateBase_DefaultFallback(t *testing.T) {
	t.Setenv("EVENER_STATE_DIR", "")
	t.Setenv("XDG_STATE_HOME", "")
	got := ResolveStateBase("")
	if !strings.HasSuffix(got, filepath.Join(".local", "state")) {
		t.Errorf("default should end in .local/state, got %q", got)
	}
}

// firstTurn is the turn number of a result's first row, or -1 when it has
// none or that row is unnumbered.
func firstTurn(r TranscriptResult) int {
	if len(r.Turns) == 0 || r.Turns[0].Turn == nil {
		return -1
	}
	return *r.Turns[0].Turn
}

// The range selects read_transcript turn numbers with read_transcript's
// grammar: start:N is the first N turns, N-M is inclusive and clamped.
func TestTranscriptRange_StartAndSpan(t *testing.T) {
	base, sid := countFixture(t) // turns 0, 1, 2
	for _, tc := range []struct {
		spec           string
		rendered, from int
	}{
		{"start:2", 2, 0},
		{"1-2", 2, 1},
		{"1-99", 2, 1},
		{"last:1", 1, 2},
		{"2-1", 0, -1},   // N > M selects nothing
		{"99-98", 0, -1}, // even past the end
	} {
		r, err := Transcript(base, sid, TranscriptOpts{Range: tc.spec})
		if err != nil {
			t.Fatal(err)
		}
		if r.TurnsRendered != tc.rendered || len(r.Turns) != tc.rendered || firstTurn(r) != tc.from {
			t.Errorf("%s rendered=%d rows=%d first=%d, want %d rows from turn %d", tc.spec, r.TurnsRendered, len(r.Turns), firstTurn(r), tc.rendered, tc.from)
		}
	}
}

// A malformed range recovers the way read_transcript's does: the whole
// transcript renders, and the result says why.
func TestTranscriptRange_MalformedSpecFallsBackWithWarning(t *testing.T) {
	base, sid := countFixture(t)
	r, err := Transcript(base, sid, TranscriptOpts{Range: "garbage"})
	if err != nil {
		t.Fatal(err)
	}
	const want = `invalid range "garbage"; rendered the whole transcript instead. Accepted: N-M | last:N | start:N`
	if r.RangeWarning != want || r.TurnsRendered != 3 || len(r.Turns) != 3 {
		t.Fatalf("garbage range = warning %q, %d rows; want %q over all 3 turns", r.RangeWarning, len(r.Turns), want)
	}
	if out := RenderTranscript(r, "outline"); !strings.Contains(out, "range warning: "+want) {
		t.Fatalf("rendered transcript does not surface the warning:\n%s", out)
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
	ts := summarizeTurn(e, "communicate", DefaultTextMax)
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
		`{"kind":"header","format_version":2,"session_id":"`+sid+`"}`+"\n"+`{"kind":"entry"`)
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
		`{"kind":"header","format_version":2,"session_id":"`+sid+`"}`+"\n"+`{not json`+"\n")
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
		`{"kind":"header","format_version":2,"session_id":"`+sid+`"}`+"\n"+`{"kind":"entry","turn":"not-an-object"}`+"\n")
	if _, err := loadTranscript(filepath.Join(sess, sid+".transcript.jsonl")); err == nil {
		t.Fatal("malformed entry should error")
	}
}

func TestLoadTranscript_RejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeFile(t, path,
		`{"kind":"header","format_version":2,"session_id":"`+sidA+`"}`+"\n"+
			`{"kind":"entry","seq":0,"turn":{},"unknown":true}`+"\n")
	if _, err := loadTranscript(path); err == nil {
		t.Fatal("loadTranscript accepted an unknown entry field")
	}
}

// A proj: ref naming a bucket that is not among the enumerated ones is an
// explicit not-found error, not a silent empty result.
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
	delegates := filepath.Join(bucket, "sessions", sid, "delegates.jsonl")
	missing := "01MISSINGCHILDSESSIONXXXXXX"
	writeDelegateEvents(t, delegates, []delegatestore.Event{
		{Kind: delegatestore.EventDelegateCreated, DelegateID: "d1", Created: &delegatestore.DelegateCreated{Descriptor: delegatestore.Descriptor{
			ChildSessionID: missing, TranscriptRef: "local:" + missing,
			OwnerSessionID: sid, VisibleSessionID: sid, Task: "inspect missing child",
			AgentType: "ghost", ToolNameCeiling: []string{"communicate"}, Resumable: true,
		}}},
	})
	root, err := Tree(base, sid, TreeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(root.Children) != 1 || !strings.Contains(root.Children[0].Note, "transcript not found") {
		t.Errorf("missing child should be noted, got %+v", root.Children)
	}
}

func TestTruncate(t *testing.T) {
	if got := Truncate("short", 80); got != "short" {
		t.Errorf("short string unchanged, got %q", got)
	}
	long := strings.Repeat("x", 100)
	got := Truncate(long, 10)
	if len([]rune(got)) != 11 || !strings.HasSuffix(got, "…") {
		t.Errorf("Truncate should cut to 10 + ellipsis, got %q", got)
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
