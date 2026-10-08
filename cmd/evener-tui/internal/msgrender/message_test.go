package msgrender

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"primeradiant.com/evener/cmd/evener-tui/internal/transcript"
	"primeradiant.com/evener/cmd/evener-tui/internal/tuitheme"
)

func TestRenderMessage_RendersAssistantMarkdown(t *testing.T) {
	previous := markdownRenderer
	previousWidth := markdownRendererWidth
	markdownRenderer = nil
	t.Cleanup(func() {
		markdownRenderer = previous
		markdownRendererWidth = previousWidth
	})

	got := RenderMessage(transcript.ChatMessage{Kind: transcript.MsgAssistant, Text: "**bold**\n\n- one"}, 80, false)

	if strings.Contains(got, "**bold**") || strings.Contains(got, "- one") {
		t.Fatalf("assistant markdown rendered raw:\n%q", got)
	}
	if !strings.Contains(got, "bold") || !strings.Contains(got, "one") {
		t.Fatalf("assistant markdown lost content:\n%q", got)
	}
}

func TestRenderMessage_KeepsPlainAssistantTextSearchable(t *testing.T) {
	got := RenderMessage(transcript.ChatMessage{Kind: transcript.MsgAssistant, Text: "main transcript answer"}, 80, false)

	if !strings.Contains(got, "main transcript answer") {
		t.Fatalf("plain assistant text should remain contiguous:\n%q", got)
	}
}

func TestRenderMessage_StreamingReasoningShowsWholeBlock(t *testing.T) {
	body := "weighing the cache eviction options\nthen the retry path"
	got := RenderMessage(transcript.ChatMessage{Kind: transcript.MsgReasoning, Text: body}, 80, false)

	if !strings.Contains(got, "✦") {
		t.Fatalf("live reasoning should carry the ✦ thinking marker:\n%q", got)
	}
	if !strings.Contains(got, "weighing the cache eviction options") || !strings.Contains(got, "then the retry path") {
		t.Fatalf("live reasoning must show the whole thinking block:\n%q", got)
	}
}

func TestRenderMessage_CollapsedReasoningIsAOneLineGist(t *testing.T) {
	body := "weighing the cache eviction options\nthen the retry path\nand a third line"
	got := RenderMessage(transcript.ChatMessage{Kind: transcript.MsgReasoning, Text: body, Done: true}, 80, false)

	if !strings.Contains(got, "✦") {
		t.Fatalf("collapsed reasoning should carry the ✦ marker:\n%q", got)
	}
	if strings.Count(strings.TrimRight(got, "\n"), "\n") != 0 {
		t.Fatalf("collapsed reasoning must be a single line:\n%q", got)
	}
	if strings.Contains(got, "and a third line") {
		t.Fatalf("collapsed reasoning must not show the full body:\n%q", got)
	}
}

func TestRenderMessage_ExpandedReasoningShowsWholeBlock(t *testing.T) {
	body := "weighing the cache eviction options\nthen the retry path"
	got := RenderMessage(transcript.ChatMessage{Kind: transcript.MsgReasoning, Text: body, Done: true, Expanded: true}, 80, false)

	if !strings.Contains(got, "then the retry path") {
		t.Fatalf("a finished thought re-expanded must show the whole block:\n%q", got)
	}
}

func TestRenderMessage_EmptyReasoningRendersNothing(t *testing.T) {
	if got := RenderMessage(transcript.ChatMessage{Kind: transcript.MsgReasoning, Text: "   "}, 80, false); got != "" {
		t.Fatalf("empty reasoning should render nothing, got %q", got)
	}
}

func TestRenderToolCallUsesRegistry(t *testing.T) {
	tc := transcript.ToolCallInfo{
		Name:        "read_file",
		Description: `{"file_path":"src/x.go"}`,
		Output:      "line1\nline2\nline3",
		Duration:    50 * time.Millisecond,
		Done:        true,
	}
	got := RenderToolCall(tc, 100, false)
	if !strings.Contains(got, "read") {
		t.Errorf("output should include verb 'read': %q", got)
	}
	if !strings.Contains(got, "src/x.go") {
		t.Errorf("output should include target: %q", got)
	}
	if !strings.Contains(got, "3 lines") {
		t.Errorf("output should include result: %q", got)
	}
}

func TestRenderToolCallUsesStructuredSubagentBody(t *testing.T) {
	got := RenderToolCall(transcript.ToolCallInfo{
		Name:     "delegate",
		RawArgs:  `{"task":"inspect billing"}`,
		Done:     true,
		Expanded: true,
		Subagent: &transcript.SubagentRunInfo{
			DelegateID:    "dlg_ABCDEFGH1234",
			Status:        "idle",
			Outcome:       "completed",
			Terminal:      true,
			Task:          "inspect billing",
			TranscriptRef: "local:child",
		},
	}, 90, false)

	if !strings.Contains(got, "inspect billing") || !strings.Contains(got, "completed") || !strings.Contains(got, "Delegate dlg_ABCD") || !strings.Contains(got, "transcript local:child") {
		t.Fatalf("structured subagent body missing metadata: %q", got)
	}
	if strings.Contains(got, "job ") {
		t.Fatalf("stable delegate body exposed activation job identity: %q", got)
	}
	if strings.Contains(got, "inspect billing (running)") {
		t.Fatalf("structured subagent body should replace fallback delegate body, got %q", got)
	}
}

func TestRenderToolCallShowsIntentAsFirstBodyLine(t *testing.T) {
	withTestColorProfile(t)
	tc := transcript.ToolCallInfo{
		Name:     "exec_command",
		RawArgs:  `{"command":"go test ./cmd/evener-tui","intent":"Verify tool renderer intent display"}`,
		Output:   "ok",
		Duration: 50 * time.Millisecond,
		Done:     true,
		Expanded: true,
	}

	got := RenderToolCall(tc, 100, false)
	lines := strings.Split(got, "\n")
	if len(lines) < 2 {
		t.Fatalf("expected intent body line under header, got %q", got)
	}
	if !strings.Contains(lines[1], "Verify tool renderer intent display") {
		t.Fatalf("first body line = %q, want intent text; full render:\n%q", lines[1], got)
	}
	if !strings.Contains(lines[1], "\x1b[3m") {
		t.Fatalf("first body line should be italic-styled, got %q", lines[1])
	}
}

// TestRenderToolCallFallsBackToPurposeAsFirstBodyLine (issue #709): tool
// calls recorded before the purpose->intent rename (7512a736e, 2026-08-29)
// carry the model's stated reason under "purpose", not "intent". A resumed
// session's transcript can span both eras, so the intent body line must
// still render for the pre-rename shape instead of silently disappearing.
func TestRenderToolCallFallsBackToPurposeAsFirstBodyLine(t *testing.T) {
	withTestColorProfile(t)
	tc := transcript.ToolCallInfo{
		Name:     "exec_command",
		RawArgs:  `{"command":"go test ./cmd/evener-tui","purpose":"Verify tool renderer intent display"}`,
		Output:   "ok",
		Duration: 50 * time.Millisecond,
		Done:     true,
		Expanded: true,
	}

	got := RenderToolCall(tc, 100, false)
	lines := strings.Split(got, "\n")
	if len(lines) < 2 {
		t.Fatalf("expected intent body line under header, got %q", got)
	}
	if !strings.Contains(lines[1], "Verify tool renderer intent display") {
		t.Fatalf("first body line = %q, want legacy purpose text; full render:\n%q", lines[1], got)
	}
}

// TestWrapText_FitsOnOneLine checks no wrapping when text fits.
func TestWrapText_FitsOnOneLine(t *testing.T) {
	lines := wrapText("hello world", 20, 20)
	if len(lines) != 1 || lines[0] != "hello world" {
		t.Errorf("got %v, want [\"hello world\"]", lines)
	}
}

// TestWrapText_WrapsAtWordBoundary checks wrapping splits on spaces.
func TestWrapText_WrapsAtWordBoundary(t *testing.T) {
	lines := wrapText("hello world foo", 11, 20)
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %v", len(lines), lines)
	}
	if lines[0] != "hello world" {
		t.Errorf("first line = %q, want \"hello world\"", lines[0])
	}
	if lines[1] != "foo" {
		t.Errorf("second line = %q, want \"foo\"", lines[1])
	}
}

// TestWrapText_Empty returns nil for empty input.
func TestWrapText_Empty(t *testing.T) {
	if lines := wrapText("", 20, 20); lines != nil {
		t.Errorf("got %v, want nil", lines)
	}
}

// TestWrapText_MultiLine checks multiple wraps with different first/cont budgets.
func TestWrapText_MultiLine(t *testing.T) {
	lines := wrapText("aaa bbb ccc ddd eee", 7, 7)
	for _, l := range lines {
		if len(l) > 7 {
			t.Errorf("line %q exceeds budget 7", l)
		}
	}
	joined := strings.Join(lines, " ")
	if joined != "aaa bbb ccc ddd eee" {
		t.Errorf("rejoined = %q, want original text", joined)
	}
}

// TestMarkdownInvalidatorIsWired verifies the renderer's reset is wired into
// tuitheme so a theme change drops the color-baked markdown cache.
func TestMarkdownInvalidatorIsWired(t *testing.T) {
	t.Cleanup(func() { tuitheme.ApplyThemeName("dark") })

	_ = renderMarkdown("# hello", 40)
	if markdownRendererCached() == nil {
		t.Fatalf("renderMarkdown did not populate cache")
	}

	tuitheme.ApplyThemeName("light")
	if markdownRendererCached() != nil {
		t.Errorf("ApplyThemeName should have invalidated markdownRenderer cache")
	}
}

func TestContainsMarkdownSyntax(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{"bold asterisk", "**bold**", true},
		{"backtick code", "`code`", true},
		{"underscore italic", "_italic_", true},
		{"bracket link", "[link](url)", true},
		{"h1 heading", "# heading", true},
		{"h2 heading", "## heading", true},
		{"blockquote", "> quote", true},
		{"unordered list", "- item", true},
		{"plus list", "+ item", true},
		{"ordered list", "1. item", true},
		{"ordered list 10", "10. item", true},
		{"plain text", "hello world", false},
		{"empty", "", false},
		{"just numbers", "12345", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := containsMarkdownSyntax(tc.text)
			if got != tc.want {
				t.Errorf("containsMarkdownSyntax(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

func TestIsOrderedMarkdownListItem(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		{"1. item", true},
		{"10. item", true},
		{"99. x", true},
		{"0. item", true},
		{"item", false},
		{"1 item", false},
		{"1.", false},
		{".", false},
		{"a. item", false},
		{"", false},
	}
	for _, tc := range tests {
		got := isOrderedMarkdownListItem(tc.line)
		if got != tc.want {
			t.Errorf("isOrderedMarkdownListItem(%q) = %v, want %v", tc.line, got, tc.want)
		}
	}
}

func TestReasoningGist(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{"first line", "first line\nsecond line", "first line"},
		{"single line", "single", "single"},
		{"empty", "", ""},
		{"whitespace only", "   \n  \n", ""},
		{"long line clipped", strings.Repeat("a", 100), strings.Repeat("a", 72) + "…"},
		{"tabs collapsed", "line\twith\ttabs", "line with tabs"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := reasoningGist(tc.text)
			if got != tc.want {
				t.Errorf("reasoningGist(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}

func TestArgsJSONFromDescription(t *testing.T) {
	tests := []struct {
		s    string
		want string
	}{
		{`{"file_path":"x.go"}`, `{"file_path":"x.go"}`},
		{"human summary", ""},
		{"  {\"a\":1}  ", "{\"a\":1}"},
		{"", ""},
	}
	for _, tc := range tests {
		got := argsJSONFromDescription(tc.s)
		if got != tc.want {
			t.Errorf("argsJSONFromDescription(%q) = %q, want %q", tc.s, got, tc.want)
		}
	}
}

func TestFormatDur(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "<1ms"},
		{500 * time.Microsecond, "<1ms"},
		{1 * time.Millisecond, "1ms"},
		{999 * time.Millisecond, "999ms"},
		{1 * time.Second, "1.0s"},
		{1500 * time.Millisecond, "1.5s"},
		{10 * time.Second, "10.0s"},
	}
	for _, tc := range tests {
		got := formatDur(tc.d)
		if got != tc.want {
			t.Errorf("formatDur(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestJSONBody(t *testing.T) {
	// Empty output returns empty.
	if got := jsonBody(ToolArgs{}, "", 80); got != "" {
		t.Errorf("jsonBody empty = %q, want empty", got)
	}
	// Invalid JSON returns raw output.
	if got := jsonBody(ToolArgs{}, "not json", 80); got != "not json" {
		t.Errorf("jsonBody invalid = %q, want raw", got)
	}
	// Valid JSON is pretty-printed: after stripping any ANSI highlighting,
	// the output must match the json.Indent result with two-space indent,
	// not the raw single-line input.
	got := jsonBody(ToolArgs{}, `{"a":1}`, 80)
	plain := ansiPattern.ReplaceAllString(got, "")
	const wantPretty = "{\n  \"a\": 1\n}"
	if plain != wantPretty {
		t.Errorf("jsonBody valid = %q (stripped %q), want pretty-printed %q", got, plain, wantPretty)
	}
	if plain == `{"a":1}` {
		t.Errorf("jsonBody valid returned raw input, not pretty-printed: %q", got)
	}
}

// ansiPattern matches terminal SGR escape sequences so tests can compare the
// underlying text without syntax-highlighting color codes.
var ansiPattern = regexp.MustCompile("\x1b\\[[0-9;]*m")

func delegateSendToolCall(raw, output string) transcript.ToolCallInfo {
	return transcript.ToolCallInfo{
		Name:     "delegate_send",
		RawArgs:  `{"to":"dlg_ABCDEFGH1234","message":"do the next part","max_wait_ms":60000}`,
		Raw:      raw,
		Output:   output,
		Done:     true,
		Expanded: true,
		Subagent: &transcript.SubagentRunInfo{DelegateID: "dlg_ABCDEFGH1234", Status: "idle", Outcome: "completed", Terminal: true},
	}
}

// A delegate_send that waited shows the delegate's reply under its run row,
// after any earlier results the wait carried, oldest first, each headed as
// the web and phone head them (#3956).
func TestRenderToolCallShowsDelegateSendReplyAndEarlierResults(t *testing.T) {
	raw := `{"delegate_id":"dlg_ABCDEFGH1234","action":"started","running_in_background":false,"status":"completed","output":"LATEST REPLY",` +
		`"earlier_results":[{"delegate_id":"dlg_ABCDEFGH1234","action":"started","running_in_background":false,"status":"completed","output":"FIRST REPLY"},` +
		`{"delegate_id":"dlg_ABCDEFGH1234","action":"started","running_in_background":false,"status":"failed","reason":"it broke"}]}`
	got := ansi.Strip(RenderToolCall(delegateSendToolCall(raw, "printed text"), 100, false))
	assertRenderedLinesInOrder(t, got, "Delegate dlg_ABCD", "earlier reply 1 of 2", "FIRST REPLY", "earlier reply 2 of 2 · failed", "it broke", "reply", "LATEST REPLY")
}

// assertRenderedLinesInOrder requires each of want, in order, as a whole
// rendered line once its indentation is trimmed; the first may be a prefix.
func assertRenderedLinesInOrder(t *testing.T, got string, want ...string) {
	t.Helper()
	at := 0
	for line := range strings.SplitSeq(got, "\n") {
		line = strings.TrimSpace(line)
		if at < len(want) && (line == want[at] || at == 0 && strings.HasPrefix(line, want[at])) {
			at++
		}
	}
	if at != len(want) {
		t.Fatalf("render matched %d of %q as lines in order; got:\n%s", at, want, got)
	}
}

// An earlier result with no text or reason still shows, as "(no reply)";
// with an empty output of its own and no earlier results, the reply is what
// was printed above the footer, and lines printed after the footer are not
// part of it.
func TestRenderToolCallDelegateSendEdgeCases(t *testing.T) {
	raw := `{"delegate_id":"dlg_ABCDEFGH1234","action":"started","running_in_background":false,"output":"LATEST",` +
		`"earlier_results":[{"delegate_id":"dlg_ABCDEFGH1234","action":"started","running_in_background":false,"status":"completed"}]}`
	got := ansi.Strip(RenderToolCall(delegateSendToolCall(raw, ""), 100, false))
	assertRenderedLinesInOrder(t, got, "Delegate dlg_ABCD", "earlier reply 1 of 1", "(no reply)", "reply", "LATEST")

	raw = `{"delegate_id":"dlg_ABCDEFGH1234","action":"started","running_in_background":false,"output":""}`
	got = ansi.Strip(RenderToolCall(delegateSendToolCall(raw, "printed reply\n[delegate_id dlg_ABCDEFGH1234 · started · completed]\nstructured_result: {}"), 100, false))
	assertRenderedLinesInOrder(t, got, "Delegate dlg_ABCD", "reply", "printed reply")
	if strings.Contains(got, "structured_result") {
		t.Fatalf("render = %q, want lines after the footer kept out of the reply", got)
	}

	steer := ansi.Strip(RenderToolCall(delegateSendToolCall("", "[delegate_id dlg_ABCDEFGH1234 · delivered]"), 100, false))
	if strings.Contains(steer, "reply") {
		t.Fatalf("render of a steer = %q, want no reply", steer)
	}
}

// Without a raw state the reply is what the tool printed above its footer.
func TestRenderToolCallShowsDelegateSendReplyFromPrintedOutput(t *testing.T) {
	got := ansi.Strip(RenderToolCall(delegateSendToolCall("", "the printed reply\n[delegate_id dlg_ABCDEFGH1234 · started · completed]"), 100, false))
	if !strings.Contains(got, "the printed reply") {
		t.Fatalf("render = %q, want the printed reply", got)
	}
	if strings.Contains(got, "[delegate_id") {
		t.Fatalf("render = %q, want the footer kept out of the reply", got)
	}
}

// A reply that carried earlier results but has no text of its own shows no
// reply: what it printed is the earlier results, never its reply.
func TestRenderToolCallShowsNoDelegateSendReplyWhenOnlyEarlierResultsHaveText(t *testing.T) {
	raw := `{"delegate_id":"dlg_ABCDEFGH1234","action":"started","running_in_background":false,` +
		`"earlier_results":[{"delegate_id":"dlg_ABCDEFGH1234","action":"started","running_in_background":false,"status":"completed","output":"FIRST REPLY"}]}`
	got := ansi.Strip(RenderToolCall(delegateSendToolCall(raw, "earlier result 1 of 1, not delivered before:\nFIRST REPLY\n[delegate_id dlg_ABCDEFGH1234 · started]"), 100, false))
	if !strings.Contains(got, "earlier reply 1 of 1") || strings.Count(got, "FIRST REPLY") != 1 || strings.Contains(got, "not delivered before") {
		t.Fatalf("render = %q, want the one earlier reply and no reply of its own", got)
	}
}

// As the shared reader does, an earlier entry that isn't an object is
// dropped on its own: the reply and the other entries still show.
func TestRenderToolCallDelegateSendKeepsValidEntriesBesideAMalformedOne(t *testing.T) {
	raw := `{"delegate_id":"dlg_ABCDEFGH1234","action":"started","running_in_background":false,"output":"LATEST",` +
		`"earlier_results":[42,{"delegate_id":"dlg_ABCDEFGH1234","action":"started","running_in_background":false,"status":"completed","output":"FIRST"}]}`
	got := ansi.Strip(RenderToolCall(delegateSendToolCall(raw, ""), 100, false))
	assertRenderedLinesInOrder(t, got, "Delegate dlg_ABCD", "earlier reply 1 of 1", "FIRST", "reply", "LATEST")
}

// Only a valid footer ends the printed reply, as the shared reader's
// delegateSendFooter decides: a bracketed line that isn't one (here with no
// action) is part of the reply. The reply keeps its own indentation.
func TestRenderToolCallDelegateSendFindsOnlyARealFooter(t *testing.T) {
	got := ansi.Strip(RenderToolCall(delegateSendToolCall("", "answer\n[delegate_id dlg_ABCDEFGH1234]"), 100, false))
	assertRenderedLinesInOrder(t, got, "Delegate dlg_ABCD", "reply", "answer", "[delegate_id dlg_ABCDEFGH1234]")

	got = ansi.Strip(RenderToolCall(delegateSendToolCall("", "  indented answer\n[delegate_id dlg_ABCDEFGH1234 · started · completed]"), 100, false))
	heading, body := -1, -1
	for line := range strings.SplitSeq(got, "\n") {
		switch strings.TrimSpace(line) {
		case "reply":
			heading = len(line) - len(strings.TrimLeft(line, " "))
		case "indented answer":
			body = len(line) - len(strings.TrimLeft(line, " "))
		}
	}
	if heading < 0 || body != heading+2 {
		t.Fatalf("render = %q, want the reply indented two spaces past its heading", got)
	}
}

// A raw state that isn't a delegate_send result, as the shared reader's
// isDelegateSendResult decides (here missing action and
// running_in_background), is not trusted: the reply is the printed one.
func TestRenderToolCallDelegateSendIgnoresARawStateThatIsNotASendResult(t *testing.T) {
	got := ansi.Strip(RenderToolCall(delegateSendToolCall(`{"output":"raw text"}`, "printed reply\n[delegate_id dlg_ABCDEFGH1234 · started · completed]"), 100, false))
	assertRenderedLinesInOrder(t, got, "Delegate dlg_ABCD", "reply", "printed reply")
	if strings.Contains(got, "raw text") {
		t.Fatalf("render = %q, want the unvalidated raw output ignored", got)
	}
}

// A padded status is shown as the shared reader's delegateSendEarlierLabel
// shows it: only a status that is exactly "completed" is left off.
func TestRenderToolCallDelegateSendShowsAPaddedEarlierStatus(t *testing.T) {
	raw := `{"delegate_id":"dlg_ABCDEFGH1234","action":"started","running_in_background":false,"output":"LATEST",` +
		`"earlier_results":[{"delegate_id":"dlg_ABCDEFGH1234","action":"started","running_in_background":false,"status":" completed ","output":"FIRST"}]}`
	got := ansi.Strip(RenderToolCall(delegateSendToolCall(raw, ""), 100, false))
	assertRenderedLinesInOrder(t, got, "Delegate dlg_ABCD", "earlier reply 1 of 1 ·  completed", "FIRST")
	blank := strings.Replace(raw, `" completed "`, `"   "`, 1)
	got = ansi.Strip(RenderToolCall(delegateSendToolCall(blank, ""), 100, false))
	assertRenderedLinesInOrder(t, got, "Delegate dlg_ABCD", "earlier reply 1 of 1", "FIRST")
}
