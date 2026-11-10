package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/apilog"
)

func assistantText(s string) llm.ContentPart {
	return llm.ContentPart{Kind: llm.ContentText, Text: s}
}

func toolCall(name, args string) llm.ContentPart {
	return llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
		ID: "tc-" + name, Name: name, Arguments: json.RawMessage(args)}}
}

// rejectedToolCall is toolCall for a call whose arguments were not valid
// JSON: Arguments holds the replay-safe {} placeholder while raw_arguments
// carries the model's bytes.
func rejectedToolCall(name, raw string) llm.ContentPart {
	return rejectedToolCallWithID("tc-"+name, name, raw)
}

// rejectedToolCallWithID is rejectedToolCall with an explicit ID, for fixtures
// holding several distinct rejected calls to the same tool.
func rejectedToolCallWithID(id, name, raw string) llm.ContentPart {
	return llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
		ID: id, Name: name, Arguments: json.RawMessage(`{}`), RawArguments: raw}}
}

func toolResult(name string, content any, isError bool) llm.ContentPart {
	return llm.ContentPart{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
		ToolCallID: "tc-" + name,
		Name:       name,
		Content:    content,
		IsError:    isError,
	}}
}

// writeRichSession writes the semantic transcript, canonical API log, and meta
// file through Evener's durable record types so fixtures match production.
func writeRichSession(t *testing.T, bucket, sid string, turns []schema.Turn, apiRecords []apilog.APILogRecord, meta schema.SessionMeta) {
	t.Helper()
	path := filepath.Join(bucket, "sessions", sid+".transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: sid})
	if err != nil {
		t.Fatal(err)
	}
	for _, turn := range turns {
		if err := w.Append(turn); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if len(apiRecords) > 0 {
		apiPath := filepath.Join(bucket, "sessions", sid+".api.jsonl")
		f, err := os.OpenFile(apiPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		for _, record := range apiRecords {
			line, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.Write(append(line, '\n')); err != nil {
				t.Fatal(err)
			}
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	meta.ID = sid
	if err := schema.SaveSessionMeta(bucket, meta); err != nil {
		t.Fatal(err)
	}
}

func countFixture(t *testing.T) (base, sid string) {
	t.Helper()
	base = t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid = sidA
	turns := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{assistantText("please read the file")}}),
		schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			assistantText("I'll read it. Note: I will not call delegate_send myself."),
			toolCall("read_file", `{"path":"x"}`),
		}}),
		schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			toolCall("communicate", `{"message":"done"}`),
		}}),
	}
	writeRichSession(t, bucket, sid, turns, nil, schema.SessionMeta{})
	return base, sid
}

func TestCount_StructuralVsMentions(t *testing.T) {
	base, sid := countFixture(t)

	rf, err := Count(base, sid, "read_file")
	if err != nil {
		t.Fatal(err)
	}
	if rf.Calls != 1 {
		t.Errorf("read_file calls = %d, want 1", rf.Calls)
	}

	ds, err := Count(base, sid, "delegate_send")
	if err != nil {
		t.Fatal(err)
	}
	if ds.Calls != 0 {
		t.Errorf("delegate_send calls = %d, want 0 (it is only mentioned, never invoked)", ds.Calls)
	}
	if ds.MentionsAssistantText < 1 {
		t.Errorf("delegate_send assistant-text mentions = %d, want >=1", ds.MentionsAssistantText)
	}
}

func TestCount_RenderDisambiguates(t *testing.T) {
	base, sid := countFixture(t)
	ds, _ := Count(base, sid, "delegate_send")
	out := RenderCount(ds)
	if !strings.Contains(out, "0 calls") {
		t.Errorf("render should say 0 calls: %q", out)
	}
	if !strings.Contains(out, "mention") {
		t.Errorf("render should disambiguate with mentions: %q", out)
	}
}

func TestTranscript_OutlineAndElision(t *testing.T) {
	base, sid := countFixture(t)
	r, err := Transcript(base, sid, TranscriptOpts{Format: "outline"})
	if err != nil {
		t.Fatal(err)
	}
	if r.TurnsTotal != 3 {
		t.Errorf("TurnsTotal = %d, want 3", r.TurnsTotal)
	}
	if r.TurnsRendered != 3 || r.Elided != 0 {
		t.Errorf("rendered/elided = %d/%d, want 3/0", r.TurnsRendered, r.Elided)
	}
	out := RenderTranscript(r, "outline")
	if !strings.Contains(out, "USER_INPUT") || !strings.Contains(out, "read_file") {
		t.Errorf("outline missing turns/tools:\n%s", out)
	}
	if !strings.Contains(out, "turns_total=3") {
		t.Errorf("outline missing honest footer:\n%s", out)
	}
}

func TestTranscript_RangeLastN(t *testing.T) {
	base, sid := countFixture(t)
	r, err := Transcript(base, sid, TranscriptOpts{Range: "last:1"})
	if err != nil {
		t.Fatal(err)
	}
	if r.TurnsRendered != 1 || r.Elided != 2 {
		t.Errorf("last:1 rendered/elided = %d/%d, want 1/2", r.TurnsRendered, r.Elided)
	}
	if r.Turns[0].Turn == nil || *r.Turns[0].Turn != 2 {
		t.Errorf("last:1 should show turn 2, got %v", r.Turns[0].Turn)
	}
}

func TestTranscript_ResultToolDefaultsToCommunicate(t *testing.T) {
	base, sid := countFixture(t)
	r, _ := Transcript(base, sid, TranscriptOpts{})
	if r.ResultTool != "communicate" {
		t.Errorf("ResultTool = %q, want communicate", r.ResultTool)
	}
	// The communicate call (turn 3) must be flagged as the result tool.
	last := r.Turns[len(r.Turns)-1]
	if len(last.ToolCalls) != 1 || !last.ToolCalls[0].IsResult {
		t.Errorf("communicate call should be flagged IsResult: %+v", last.ToolCalls)
	}
}

func TestTranscript_RendersToolResultPreviews(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid := sidB
	turns := []schema.Turn{
		schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
			toolResult("job_watch", map[string]any{
				"watching": true,
				"watch_id": "watch_123",
			}, false),
			toolResult("job_list", "target_not_found", true),
		}}),
	}
	writeRichSession(t, bucket, sid, turns, nil, schema.SessionMeta{})

	r, err := Transcript(base, sid, TranscriptOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(r.Turns[0].ToolResults); got != 2 {
		t.Fatalf("tool results = %d, want 2", got)
	}
	if !strings.Contains(r.Turns[0].ToolResults[0].ContentPreview, "watch_123") {
		t.Fatalf("content preview = %q, want watch id", r.Turns[0].ToolResults[0].ContentPreview)
	}

	out := RenderTranscript(r, "markdown")
	if !strings.Contains(out, "← job_watch") || !strings.Contains(out, "← job_list (error)") {
		t.Fatalf("rendered transcript missing tool result previews:\n%s", out)
	}
}

// TestTranscript_ToolCallShowsExplicitIntent pins the current-key path: a
// tool call's stated "intent" surfaces both structurally (ToolCallSummary.
// Intent) and in the rendered markdown card.
func TestTranscript_ToolCallShowsExplicitIntent(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid := sidC
	turns := []schema.Turn{
		schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			toolCall("shell", `{"command":"ls","intent":"list the directory"}`),
		}}),
	}
	writeRichSession(t, bucket, sid, turns, nil, schema.SessionMeta{})

	r, err := Transcript(base, sid, TranscriptOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(r.Turns[0].ToolCalls); got != 1 {
		t.Fatalf("tool calls = %d, want 1", got)
	}
	if got := r.Turns[0].ToolCalls[0].Intent; got != "list the directory" {
		t.Fatalf("Intent = %q, want %q", got, "list the directory")
	}

	out := RenderTranscript(r, "markdown")
	if !strings.Contains(out, "intent: list the directory") {
		t.Fatalf("rendered transcript missing intent line:\n%s", out)
	}
}

// TestTranscript_ToolCallShowsRejectedRawArguments pins the post-mortem
// contract for a call whose arguments were not valid JSON: the record keeps
// the model's raw bytes in raw_arguments while arguments holds the
// replay-safe {} placeholder, and the doctor render must show what the model
// actually sent, not the placeholder.
func TestTranscript_ToolCallShowsRejectedRawArguments(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid := sidC
	const rawRejected = `{"update":[{"id":1},{id: 2, status: "in_progress"}]}`
	turns := []schema.Turn{
		schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			rejectedToolCall("rejected", rawRejected),
		}}),
	}
	writeRichSession(t, bucket, sid, turns, nil, schema.SessionMeta{})

	r, err := Transcript(base, sid, TranscriptOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(r.Turns[0].ToolCalls); got != 1 {
		t.Fatalf("tool calls = %d, want 1", got)
	}
	if got := r.Turns[0].ToolCalls[0].Arguments; got != rawRejected {
		t.Fatalf("Arguments = %q, want the raw bytes verbatim %q", got, rawRejected)
	}
	if got := r.Turns[0].ToolCalls[0].ArgPreview; got != rawRejected {
		t.Fatalf("ArgPreview = %q, want the raw bytes verbatim %q", got, rawRejected)
	}

	out := RenderTranscript(r, "markdown")
	if !strings.Contains(out, rawRejected) {
		t.Fatalf("rendered transcript hides the rejected call's raw arguments:\n%s", out)
	}

	// Old records (written before raw_arguments existed) have no
	// raw_arguments field and render their arguments exactly as before.
	legacy := []schema.Turn{
		schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			toolCall("read_file", `{"path":"x"}`),
		}}),
	}
	writeRichSession(t, bucket, sidB, legacy, nil, schema.SessionMeta{})
	lr, err := Transcript(base, sidB, TranscriptOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if got := lr.Turns[0].ToolCalls[0].Arguments; got != `{"path":"x"}` {
		t.Fatalf("legacy record Arguments = %q, want unchanged %q", got, `{"path":"x"}`)
	}
}

// TestTranscript_ToolCallIntentFallsBackToPurpose (issue #709 round 2,
// roborev Medium): evener-doctor's transcript renderer previously showed
// only a truncated raw ArgPreview, with no dedicated intent line at all, so
// it could not have honored a legacy "purpose" key either. Tool calls
// recorded before the purpose->intent rename (7512a736e, 2026-08-29) carry
// the model's stated reason under "purpose", not "intent" -- doctor must
// surface that reason the same way the hub, the read_transcript agent tool,
// and the TUI already do.
func TestTranscript_ToolCallIntentFallsBackToPurpose(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid := sidC
	turns := []schema.Turn{
		schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			toolCall("shell", `{"command":"ls","purpose":"list the directory"}`),
		}}),
	}
	writeRichSession(t, bucket, sid, turns, nil, schema.SessionMeta{})

	r, err := Transcript(base, sid, TranscriptOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(r.Turns[0].ToolCalls); got != 1 {
		t.Fatalf("tool calls = %d, want 1", got)
	}
	if got := r.Turns[0].ToolCalls[0].Intent; got != "list the directory" {
		t.Fatalf("Intent = %q, want legacy purpose value %q", got, "list the directory")
	}

	out := RenderTranscript(r, "markdown")
	if !strings.Contains(out, "intent: list the directory") {
		t.Fatalf("rendered transcript missing intent line for legacy purpose arg:\n%s", out)
	}
}

// salvagedLoopFixture writes a session whose one assistant turn carries text
// far past DefaultTextMax, plus an equally long tool result. This is the shape
// of a salvaged partial response (agent/salvage.go): the model's repeated tool
// calls survive as *text*, never as structural tool-call parts, so no
// tool-call metric can see the repetition and only the full turn text can.
func salvagedLoopFixture(t *testing.T) (base, sid, turnText, resultText string) {
	t.Helper()
	base = t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid = sidB
	turnText = strings.Repeat(`manage_worktree({"op":"list"}) `, 60) + "END_OF_SALVAGED_TEXT"
	resultText = strings.Repeat("worktree-row ", 40) + "END_OF_RESULT"
	turns := []schema.Turn{
		schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			assistantText(turnText),
		}}),
		schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
			toolResult("manage_worktree", resultText, false),
		}}),
	}
	writeRichSession(t, bucket, sid, turns, nil, schema.SessionMeta{})
	return base, sid, turnText, resultText
}

func TestTranscript_TextMaxDefaultTruncates(t *testing.T) {
	base, sid, turnText, resultText := salvagedLoopFixture(t)

	r, err := Transcript(base, sid, TranscriptOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := r.Turns[0].Text, turnText[:DefaultTextMax]+"…"; got != want {
		t.Errorf("default turn text = %q, want the first %d bytes plus an ellipsis (%q)",
			got, DefaultTextMax, want)
	}
	if got, want := r.Turns[1].ToolResults[0].ContentPreview, resultText[:DefaultTextMax]+"…"; got != want {
		t.Errorf("default tool-result preview = %q, want %q", got, want)
	}
}

func TestTranscript_TextMaxFullRendersEveryByte(t *testing.T) {
	base, sid, turnText, resultText := salvagedLoopFixture(t)

	r, err := Transcript(base, sid, TranscriptOpts{TextMax: TextMaxFull})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Turns[0].Text; got != turnText {
		t.Errorf("TextMaxFull turn text is not the whole turn: got %d bytes, want %d\ngot:  %q\nwant: %q",
			len(got), len(turnText), got, turnText)
	}
	if got := r.Turns[1].ToolResults[0].ContentPreview; got != resultText {
		t.Errorf("TextMaxFull tool-result preview is not the whole result: got %d bytes, want %d",
			len(got), len(resultText))
	}
	// The rendered output is what an operator actually reads.
	if out := RenderTranscript(r, "markdown"); !strings.Contains(out, "END_OF_SALVAGED_TEXT") {
		t.Errorf("markdown render dropped the tail of the turn text:\n%s", out)
	}
}

func TestTranscript_TextMaxHonoursAnExplicitCap(t *testing.T) {
	base, sid, turnText, _ := salvagedLoopFixture(t)
	const textMax = 512

	r, err := Transcript(base, sid, TranscriptOpts{TextMax: textMax})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := r.Turns[0].Text, turnText[:textMax]+"…"; got != want {
		t.Errorf("TextMax=%d turn text = %d bytes (%q), want the first %d bytes plus an ellipsis",
			textMax, len(got), got, textMax)
	}
}

// A tool result's ContentPreview reports the result's bytes, not a tidied
// version of them: trimming edge whitespace made --full-text (which promises
// the result whole) lie about leading/trailing newlines, and made a
// whitespace-only result vanish entirely (omitempty). Turn text keeps its
// trim — that happens at summarizeTurn's call site, not inside Truncate.
func TestTranscript_ToolResultPreviewKeepsEdgeWhitespace(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid := sidB
	resultText := "\n\nleading newlines kept\n"
	whitespaceOnly := "\n\t "
	turns := []schema.Turn{
		schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
			toolResult("read_file", resultText, false),
			toolResult("shell", whitespaceOnly, false),
		}}),
	}
	writeRichSession(t, bucket, sid, turns, nil, schema.SessionMeta{})

	// --full-text: byte-exact, whole.
	full, err := Transcript(base, sid, TranscriptOpts{TextMax: TextMaxFull})
	if err != nil {
		t.Fatal(err)
	}
	if got := full.Turns[0].ToolResults[0].ContentPreview; got != resultText {
		t.Errorf("full-text preview = %q, want the result byte-exact %q", got, resultText)
	}
	if got := full.Turns[0].ToolResults[1].ContentPreview; got != whitespaceOnly {
		t.Errorf("full-text whitespace-only preview = %q, want %q (must not vanish)", got, whitespaceOnly)
	}

	// Default cap: same fidelity — the cap elides the tail, it does not tidy.
	capped, err := Transcript(base, sid, TranscriptOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if got := capped.Turns[0].ToolResults[0].ContentPreview; got != resultText {
		t.Errorf("default preview = %q, want %q", got, resultText)
	}
	if got := capped.Turns[0].ToolResults[1].ContentPreview; got != whitespaceOnly {
		t.Errorf("default whitespace-only preview = %q, want %q", got, whitespaceOnly)
	}
}

// Truncate's cap is a byte budget, but the cut must land on a rune boundary:
// slicing mid-rune emits invalid UTF-8, which --json then rewrites to U+FFFD.
func TestTruncate_NeverCutsMidRune(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"ascii cut", "abcdef", 4, "abcd…"},
		{"cjk cut mid-rune backs off", "日本語", 4, "日…"},
		{"cjk cut on boundary", "日本語", 6, "日本…"},
		{"emoji cut mid-rune backs off", "a👍b", 3, "a…"},
		{"emoji fits whole", "👍", 4, "👍"},
		{"cut inside first rune backs off to empty", "👍👍", 3, "…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Truncate(tc.in, tc.max)
			if got != tc.want {
				t.Errorf("Truncate(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("Truncate(%q, %d) = %q is not valid UTF-8", tc.in, tc.max, got)
			}
			if kept := strings.TrimSuffix(got, "…"); len(kept) > tc.max {
				t.Errorf("Truncate(%q, %d) kept %d bytes before the ellipsis, over the byte budget", tc.in, tc.max, len(kept))
			}
		})
	}
}

func TestTranscript_ResultToolFromMeta(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid := sidB
	meta := schema.SessionMeta{Config: schema.ConfigSnapshot{ResultToolName: "submit_answer"}}
	turns := []schema.Turn{
		schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			toolCall("submit_answer", `{"ok":true}`),
		}}),
	}
	writeRichSession(t, bucket, sid, turns, nil, meta)

	r, err := Transcript(base, sid, TranscriptOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if r.ResultTool != "submit_answer" {
		t.Errorf("ResultTool = %q, want submit_answer (from meta)", r.ResultTool)
	}
	if !r.Turns[0].ToolCalls[0].IsResult {
		t.Error("submit_answer should be flagged as the result tool")
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
		{" 1-2 ", 2, 1},  // surrounding whitespace is ignored, as in read_transcript
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
// transcript renders, and the result and its rendering say why.
func TestTranscriptRange_MalformedSpecFallsBackWithWarning(t *testing.T) {
	base, sid := countFixture(t)
	r, err := Transcript(base, sid, TranscriptOpts{Range: "garbage"})
	if err != nil {
		t.Fatal(err)
	}
	if r.RangeWarning == "" || r.TurnsRendered != 3 || len(r.Turns) != 3 {
		t.Fatalf("garbage range = warning %q, %d rows; want a warning over all 3 turns", r.RangeWarning, len(r.Turns))
	}
	if out := RenderTranscript(r, "outline"); !strings.Contains(out, r.RangeWarning) {
		t.Fatalf("rendered transcript does not surface the warning:\n%s", out)
	}
}

// The outline lists each turn's tool results, marking a result with no name
// and an error result so neither reads as a plain named success.
func TestRenderTranscript_OutlineToolResults(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	turns := []schema.Turn{
		schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
			toolResult("job_watch", "ok", false),
		}}),
	}
	writeRichSession(t, bucket, sidB, turns, nil, schema.SessionMeta{})
	r, err := Transcript(base, sidB, TranscriptOpts{Format: "outline"})
	if err != nil {
		t.Fatal(err)
	}
	if out := RenderTranscript(r, "outline"); !strings.Contains(out, "job_watch") {
		t.Errorf("outline should list tool results:\n%s", out)
	}
	unnamed := toolResultNames([]ToolResultSummary{{}})
	unnamedError := toolResultNames([]ToolResultSummary{{IsError: true}})
	if unnamed == "" || unnamedError == unnamed {
		t.Errorf("unnamed result = %q, unnamed error = %q; want both marked and distinct", unnamed, unnamedError)
	}
}

// The markdown view marks the session's result-tool call apart from an
// ordinary tool call.
func TestRenderTranscript_MarkdownResultLabel(t *testing.T) {
	base, sid := countFixture(t)
	r, err := Transcript(base, sid, TranscriptOpts{})
	if err != nil {
		t.Fatal(err)
	}
	var result, other bool
	for _, turn := range r.Turns {
		for _, tc := range turn.ToolCalls {
			result = result || (tc.Name == "communicate" && tc.IsResult)
			other = other || (tc.Name == "read_file" && !tc.IsResult)
		}
	}
	if !result || !other {
		t.Fatalf("tool calls flagged result=%v other=%v, want communicate flagged and read_file not", result, other)
	}
	marked := RenderTranscript(r, "markdown")
	for i := range r.Turns {
		for j := range r.Turns[i].ToolCalls {
			r.Turns[i].ToolCalls[j].IsResult = false
		}
	}
	if RenderTranscript(r, "markdown") == marked {
		t.Error("markdown renders the result call the same as an ordinary call")
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
	if got := toolResultContentText(map[string]any{"a": 1}); got != `{"a":1}` {
		t.Errorf("map = %q, want its JSON", got)
	}
}

// A count of one renders differently from the same text with any other count.
func TestRenderCount_Singular(t *testing.T) {
	one := RenderCount(CountResult{Tool: "read_file", Calls: 1})
	two := RenderCount(CountResult{Tool: "read_file", Calls: 2})
	if strings.Replace(two, "2", "1", 1) == one {
		t.Errorf("one call renders as %q, the plural form with its count swapped", one)
	}
}
