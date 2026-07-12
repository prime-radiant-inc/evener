package msgrender

import (
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/glamour"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/cmd/serf-tui/internal/transcript"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuitheme"
	"primeradiant.com/serf/llm"
)

var renderReplayOnce sync.Once

// FuzzRenderToolCall drives the package's real render-input decode seam:
// RenderToolCall parses the untrusted tool ArgumentsJSON via toolArgsFromJSON
// (json.Unmarshal into ToolArgs) and routes it through the per-tool renderer's
// Verb/Target/Result/Body funcs. Fuzzing (toolName, rawArgs, output, error)
// exercises the decoder plus the known-tool, MCP-fallback ("provider__op"), and
// unknown-tool renderers. Oracle: no-panic floor — a malformed args payload or
// surprising output must never crash the transcript renderer.
func FuzzRenderToolCall(f *testing.F) {
	seeds := []struct {
		name, args, output, errStr string
	}{
		{"read_file", `{"file_path":"/a/b.go","offset":1,"limit":5}`, "line\nline", ""},
		{"shell", `{"command":"ls -la","purpose":"list"}`, "out", ""},
		{"edit_file", `{"file_path":"/a.go","old_string":"x","new_string":"y"}`, "", ""},
		{"grep", `{"pattern":"TODO"}`, "match", ""},
		{"glob", `{"pattern":"**/*"}`, "", "boom"},
		{"prov__operation", `{"q":"hi","n":3}`, `{"data":1}`, ""},
		{"weird-unknown", `not-json`, "", ""},
		{"delegate", `{"task":"do\nthing"}`, "", ""},
		{"", "", "", ""},
		{"write_file", `{"file_path":"/x","content":"a\nb"}`, "", ""},
	}
	for _, s := range seeds {
		f.Add(s.name, s.args, s.output, s.errStr)
	}

	f.Fuzz(func(t *testing.T, name, args, output, errStr string) {
		renderReplayOnce.Do(replayRenderSurface)

		// toolArgsFromJSON is the raw decode seam; assert it never returns nil.
		if a := toolArgsFromJSON(args); a == nil {
			t.Fatalf("toolArgsFromJSON(%q) returned nil map", args)
		}

		tc := transcript.ToolCallInfo{
			Name:     name,
			RawArgs:  args,
			Output:   output,
			Error:    errStr,
			Done:     true,
			Expanded: true,
		}
		// Two widths exercise the wrap/indent math; both must not panic.
		_ = RenderToolCall(tc, 80, false)
		_ = RenderToolCall(tc, 12, true)
	})
}

// replayRenderSurface covers the package's other pure transcript render fronts
// without making every active-fuzz iteration pay for markdown and syntax setup.
func replayRenderSurface() {
	for _, theme := range []string{"dark", "light"} {
		tuitheme.ApplyThemeName(theme)
		_ = chromaStyleForActiveTheme()
		InitMarkdownRenderer(0)
		InitMarkdownRenderer(40)
		_ = themedGlamourStyle()
		_ = renderMarkdown("plain", 40)
		_ = renderMarkdown("# heading\n\n**bold**", 40)
		_ = markdownRendererCached()
		resetMarkdownRenderer()
		_ = renderMarkdown("`code`", 41)
	}
	tuitheme.ApplyThemeName("dark")
	originalNewMarkdownRenderer := newMarkdownRenderer
	newMarkdownRenderer = func(...glamour.TermRendererOption) (*glamour.TermRenderer, error) {
		return nil, errors.New("forced")
	}
	resetMarkdownRenderer()
	_ = renderMarkdown("**raw**", 40)
	newMarkdownRenderer = originalNewMarkdownRenderer
	InitMarkdownRenderer(40)
	originalRenderMarkdownWith := renderMarkdownWith
	renderMarkdownWith = func(*glamour.TermRenderer, string) (string, error) { return "", errors.New("forced") }
	_ = renderMarkdown("**raw**", 40)
	renderMarkdownWith = originalRenderMarkdownWith

	for _, text := range []string{
		"", "plain", "`code`", "_italic_", "[link]", "# h", "## h", "### h",
		"> quote", "- item", "+ item", "1. item", "123", "1.",
	} {
		_ = containsMarkdownSyntax(text)
		_ = isOrderedMarkdownListItem(text)
	}
	_ = reasoningGist("\n  \n")
	_ = reasoningGist("  first   line  \nsecond")
	_ = reasoningGist("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-extra-long-gist")

	messages := []transcript.ChatMessage{
		{Kind: transcript.MsgUser, Text: "hello", Pending: true},
		{Kind: transcript.MsgUser, Text: "hello", Failed: true, Reason: "nope"},
		{Kind: transcript.MsgUser, Text: "hello", Failed: true},
		{Kind: transcript.MsgAssistant, Text: " "},
		{Kind: transcript.MsgAssistant, Text: "**answer**"},
		{Kind: transcript.MsgReasoning, Text: " "},
		{Kind: transcript.MsgReasoning, Text: "thinking"},
		{Kind: transcript.MsgReasoning, Text: "thinking", Done: true},
		{Kind: transcript.MsgReasoning, Text: "thinking", Done: true, Expanded: true},
		{Kind: transcript.MsgCommunicate, Text: "# response"},
		{Kind: transcript.MsgTool},
		{Kind: transcript.MsgTool, Tool: &transcript.ToolCallInfo{Hidden: true}},
		{Kind: transcript.MsgTool, Tool: &transcript.ToolCallInfo{Name: "read_file", Done: true}},
		{Kind: transcript.MsgSystem, Text: "system"},
		{Kind: transcript.MsgSteering, Text: "steer"},
		{Kind: transcript.MessageKind(255), Text: "unknown"},
	}
	for _, msg := range messages {
		_ = RenderMessage(msg, 40, false)
		_ = RenderMessage(msg, 1, true)
	}
	_ = RenderSelectedMessage("", true)
	_ = RenderSelectedMessage("plain", false)
	_ = RenderSelectedMessage("first\nsecond", true)

	toolCases := []transcript.ToolCallInfo{
		{Name: "unknown", RawArgs: `{"purpose":"why"}`, Expanded: false},
		{Name: "read_file", Description: ` {"file_path":"x.go"}`, Done: true, Duration: 500 * time.Microsecond},
		{Name: "read_file", RawArgs: `{"file_path":"x.go","purpose":"inspect"}`, Output: "a\nb\nc\nd\ne\nf\n", Done: true, Expanded: true, Duration: 250 * time.Millisecond},
		{Name: "read_file", RawArgs: `{"file_path":"x.go"}`, Detail: "detail", Output: "output", Error: "boom", Expanded: true},
		{Name: "read_file", RawArgs: `{"file_path":"x.go"}`, Detail: "detail", Error: "boom", Expanded: true},
		{Name: "delegate", RawArgs: `{"task":"inspect"}`, Error: "boom", Expanded: true, Subagent: &transcript.SubagentRunInfo{Task: "inspect", Status: "done"}},
		{Name: "delegate_send", RawArgs: `{}`, Done: true, Expanded: true, Subagent: &transcript.SubagentRunInfo{}},
		{Name: "unknown", RawArgs: `{}`, Done: false, Expanded: false},
		{Name: "unknown", RawArgs: `{}`, Output: `{"ok":true}`, Error: "boom", Expanded: true},
		{Name: "unknown", RawArgs: `{}`, Detail: "detail", Output: "out", Error: "boom", Expanded: true},
		{Name: "prov__op", RawArgs: `{"a":"abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ","b":"two","c":"three","d":"four","n":3}`, Output: `{"ok":true}`, Done: true, Expanded: true, Duration: 1500 * time.Millisecond},
	}
	for _, tc := range toolCases {
		_ = RenderToolCall(tc, 80, false)
		_ = RenderToolCall(tc, 12, true)
	}

	args := ToolArgs{
		"command": "first line\nsecond line", "pattern": "TODO", "path": "/tmp", "file_path": "x.go",
		"purpose": "why", "query": "find", "task": "task", "job_id": "123456789", "status": "done",
		"patch": "*** Update File: x.go\n@@ -1 +1 @@\n-old\n+new", "content": "a\nb",
	}
	_ = args.Str("missing")
	_ = ToolArgs{"x": 1}.Str("x")
	_ = toolArgsFromJSON("")
	_ = toolArgsFromJSON("bad")
	for name, renderer := range toolRenderers {
		_ = name
		_ = renderer.Verb(args)
		_ = renderer.Target(args)
		_ = renderer.Result(args, "one\ntwo", "", 0)
		_ = renderer.Result(args, "", "boom", time.Second)
		if renderer.Body != nil {
			_ = renderer.Body(args, "one\ntwo", 80)
			_ = renderer.Body(args, "", 10)
		}
	}
	for _, patch := range []string{"*** Add File: a", "*** Delete File: d", "+++ b/u", "none"} {
		_ = toolRenderers["apply_patch"].Target(ToolArgs{"patch": patch})
	}
	_ = toolRenderers["shell"].Target(ToolArgs{"command": "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"})
	_ = toolRenderers["grep"].Result(nil, "", "", 0)
	_ = toolRenderers["list_dir"].Result(nil, `[{"name":"x"}]`, "", 0)
	_ = toolRenderers["delegate"].Target(ToolArgs{"task": "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"})
	_ = toolRenderers["use_skill"].Target(ToolArgs{"skill_name": "named"})
	_ = formatLineCount(1)
	mcp, _ := lookupToolRenderer("provider__operation")
	_ = mcp.Result(nil, "", "boom", 0)

	_ = diffResultText(nil, "+a\n-b", "", 0)
	_ = diffResultText(nil, "+a", "", 0)
	_ = diffResultText(nil, "-b", "", 0)
	_ = diffResultText(nil, "+++ h\n--- h", "", 0)
	_ = diffBody(nil, "", 1)
	_ = diffBody(nil, "@@ h\n+a\n-b\n+++ h\n--- h\n context", 80)
	_ = fileBody(args, "", 80)
	_ = fileBody(args, "a\nb\nc\nd\ne\nf\n", 80)
	_ = taskListBody(nil, "", 80)
	_ = taskListBody(nil, "bad", 80)
	_ = taskListBody(nil, `[{"description":"done","status":"done"},{"name":"work","status":"in_progress"},{"name":"wait","status":"pending"}]`, 80)
	_ = delegateBody(ToolArgs{"task": " task ", "job_id": "123456789", "status": ""}, "", 10)
	_ = delegateBody(nil, `{"job_id":"abcdefghi","status":"completed"}`, 80)
	_ = delegateBody(ToolArgs{"job_id": "fallback", "status": "queued"}, "bad", 80)
	_ = SubagentRunBody(transcript.SubagentRunInfo{}, 10)
	_ = SubagentRunBody(transcript.SubagentRunInfo{Task: "task", Status: "done", JobID: "123456789", DelegateID: "abcdefghi", TranscriptRef: "ref"}, 80)

	_ = RenderSubagentRail(nil, 80)
	_ = RenderSubagentRail([]transcript.SubagentRunInfo{
		{Task: "", Command: "", Status: "running", Activity: "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ-extra", Steps: 2},
		{Task: "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ-extra", Status: "running"},
		{Task: "done", Status: "completed", Headline: "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ-extra"},
		{Task: "done error", Status: "cancelled", Headline: "bad", HeadlineError: true},
		{Task: "fold", Status: "stopped"},
		{Task: "fail", Status: "failed", Reason: "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRST-extra"},
		{Task: "error", Status: "error"},
	}, 10)
	for _, status := range []string{"failed", "error", "completed", "done", "succeeded", "cancelled", "stopped", "other"} {
		_ = subagentRailClass(status)
	}
	_ = ShellBody(nil, "", 80)
	_ = ShellBody(args, "echo hi", 80)
	_ = webSearchBody(nil, "result", 80)
	_ = jsonBody(nil, "", 80)
	_ = jsonBody(nil, "bad", 80)
	_ = jsonBody(nil, `{"x":1}`, 80)
	_ = highlightBlock("x", "unknown-language")
	_ = chromaHighlight("x", "unknown.extension")
	replayChromaFailures()

	questionArgs := toolArgsFromJSON(`{"questions":[{"header":"Mode","question":"Choose","multi_select":true,"options":[{"label":"A","detail":"detail","recommended":true},{"label":"B"}],"why":"reason","if_unanswered":"default"},{"header":"Next","question":"Then"}]}`)
	_ = decodeAskUserQuestions(nil)
	_ = decodeAskUserQuestions(ToolArgs{"questions": func() {}})
	_ = decodeAskUserQuestions(ToolArgs{"questions": "bad"})
	_ = askUserTarget(questionArgs)
	_ = askUserBody(nil, "", 80)
	_ = askUserBody(questionArgs, "", 80)

	_ = wrapText("", 4, 4)
	_ = wrapText("fit", 4, 4)
	_ = wrapText("word boundary next", 12, 4)
	_ = wrapText("abcdefgh", 4, 4)
	_ = argsJSONFromDescription("plain")
	_ = argsJSONFromDescription(" {\"x\":1}")

	turns := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User(" ")},
		{Kind: schema.TurnUserInput, Message: llm.User("user")},
		{Kind: schema.TurnAssistant, Message: llm.Message{Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: " "},
			{Kind: llm.ContentText, Text: "assistant"},
			{Kind: llm.ContentToolCall},
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "comm-empty", Name: "communicate", Arguments: json.RawMessage(`{}`)}},
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "comm", Name: "communicate", Arguments: json.RawMessage(`{"output":{"message":"hello"}}`)}},
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "tool", Name: "shell", Arguments: json.RawMessage(`{"command":"ls"}`)}},
		}}},
		{Kind: schema.TurnTool, Message: llm.ToolResult("tool", "failed", true)},
		{Kind: schema.TurnToolResults, Message: llm.ToolResult("other", "ok", false)},
	}
	_ = historyToMessages(turns)
	_ = extractCommunicate(&llm.ToolCallData{Arguments: json.RawMessage("bad")})
	_ = extractCommunicate(&llm.ToolCallData{Arguments: json.RawMessage(`{"message":"direct"}`)})
}

func replayChromaFailures() {
	originalMatch, originalGetLexer := matchChromaLexer, getChromaLexer
	originalGetStyle, originalGetFormatter := getChromaStyle, getChromaFormatter
	originalTokenise, originalFormat := tokeniseChroma, formatChroma
	defer func() {
		matchChromaLexer, getChromaLexer = originalMatch, originalGetLexer
		getChromaStyle, getChromaFormatter = originalGetStyle, originalGetFormatter
		tokeniseChroma, formatChroma = originalTokenise, originalFormat
	}()

	matchChromaLexer = func(string) chroma.Lexer { return nil }
	_ = highlightBlockByFilename("x", "x.none")
	matchChromaLexer = originalMatch
	getChromaStyle = func(string) *chroma.Style { return nil }
	_ = highlightBlockByFilename("x", "x.go")
	_ = highlightBlock("x", "go")
	getChromaStyle = originalGetStyle
	getChromaFormatter = func(string) chroma.Formatter { return nil }
	_ = highlightBlockByFilename("x", "x.go")
	_ = highlightBlock("x", "go")
	getChromaFormatter = originalGetFormatter
	tokeniseChroma = func(chroma.Lexer, string) (chroma.Iterator, error) { return nil, errors.New("forced") }
	_ = highlightBlockByFilename("x", "x.go")
	_ = highlightBlock("x", "go")
	tokeniseChroma = originalTokenise
	formatChroma = func(chroma.Formatter, io.Writer, *chroma.Style, chroma.Iterator) error { return errors.New("forced") }
	_ = highlightBlockByFilename("x", "x.go")
	_ = highlightBlock("x", "go")
	_ = chromaHighlight("x", "x.go")
	_ = ShellBody(nil, "x", 80)
	_ = jsonBody(nil, `{"x":1}`, 80)
}
