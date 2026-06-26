package msgrender

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/cmd/serf-tui/internal/transcript"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuitheme"
	"primeradiant.com/serf/llm"
)

func TestHistoryToMessages_UserAndCommunicate(t *testing.T) {
	turns := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("what is 2+2?")},
		{Kind: schema.TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
					ID:        "call_1",
					Name:      "communicate",
					Arguments: json.RawMessage(`{"message":"The answer is 4."}`),
				}},
			},
		}},
		{Kind: schema.TurnToolResults, Message: llm.ToolResult("call_1", "ok", false)},
	}

	msgs := historyToMessages(turns)

	// Should have: user message + communicate message.
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d: %+v", len(msgs), msgs)
	}
	if msgs[0].Kind != transcript.MsgUser || msgs[0].Text != "what is 2+2?" {
		t.Errorf("msg[0] = %+v, want user 'what is 2+2?'", msgs[0])
	}
	if msgs[1].Kind != transcript.MsgCommunicate || msgs[1].Text != "The answer is 4." {
		t.Errorf("msg[1] = %+v, want communicate 'The answer is 4.'", msgs[1])
	}
}

func TestHistoryToMessages_ToolCalls(t *testing.T) {
	turns := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("list files")},
		{Kind: schema.TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
					ID:        "call_1",
					Name:      "shell",
					Arguments: json.RawMessage(`{"command":"ls -la"}`),
				}},
			},
		}},
		{Kind: schema.TurnToolResults, Message: llm.ToolResult("call_1", "file1.go\nfile2.go", false)},
		{Kind: schema.TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
					ID:        "call_2",
					Name:      "communicate",
					Arguments: json.RawMessage(`{"message":"Found 2 files."}`),
				}},
			},
		}},
		{Kind: schema.TurnToolResults, Message: llm.ToolResult("call_2", "ok", false)},
	}

	msgs := historyToMessages(turns)

	// Should have: user + tool + communicate.
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d: %+v", len(msgs), msgs)
	}
	if msgs[0].Kind != transcript.MsgUser {
		t.Errorf("msg[0].Kind = %v, want transcript.MsgUser", msgs[0].Kind)
	}
	if msgs[1].Kind != transcript.MsgTool || msgs[1].Tool == nil {
		t.Fatalf("msg[1] should be a tool call, got %+v", msgs[1])
	}
	if msgs[1].Tool.Name != "shell" {
		t.Errorf("tool name = %q, want 'shell'", msgs[1].Tool.Name)
	}
	if msgs[1].Tool.Output != "file1.go\nfile2.go" {
		t.Errorf("tool output = %q, want file listing", msgs[1].Tool.Output)
	}
	if msgs[2].Kind != transcript.MsgCommunicate || msgs[2].Text != "Found 2 files." {
		t.Errorf("msg[2] = %+v, want communicate 'Found 2 files.'", msgs[2])
	}
}

func TestHistoryToMessages_ThinkingText(t *testing.T) {
	turns := []schema.Turn{
		{Kind: schema.TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentText, Text: "Let me think about this..."},
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
					ID:        "call_1",
					Name:      "communicate",
					Arguments: json.RawMessage(`{"message":"Done."}`),
				}},
			},
		}},
	}

	msgs := historyToMessages(turns)

	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d: %+v", len(msgs), msgs)
	}
	if msgs[0].Kind != transcript.MsgAssistant || msgs[0].Text != "Let me think about this..." {
		t.Errorf("msg[0] = %+v, want assistant thinking", msgs[0])
	}
	if msgs[1].Kind != transcript.MsgCommunicate {
		t.Errorf("msg[1].Kind = %v, want transcript.MsgCommunicate", msgs[1].Kind)
	}
}

func TestHistoryToMessages_Empty(t *testing.T) {
	msgs := historyToMessages(nil)
	if len(msgs) != 0 {
		t.Fatalf("expected 0 messages for nil history, got %d", len(msgs))
	}
}

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
			JobID:         "job_ABCDEFGH1234",
			DelegateID:    "dlg_ABCDEFGH1234",
			Status:        "completed",
			Task:          "inspect billing",
			TranscriptRef: "local:child",
		},
	}, 90, false)

	if !strings.Contains(got, "inspect billing") || !strings.Contains(got, "completed") || !strings.Contains(got, "job job_ABCD") || !strings.Contains(got, "delegate dlg_ABCD") || !strings.Contains(got, "transcript local:child") {
		t.Fatalf("structured subagent body missing metadata: %q", got)
	}
	if strings.Contains(got, "inspect billing (running)") {
		t.Fatalf("structured subagent body should replace fallback delegate body, got %q", got)
	}
}

func TestRenderToolCallShowsPurposeAsFirstBodyLine(t *testing.T) {
	withTestColorProfile(t)
	tc := transcript.ToolCallInfo{
		Name:     "exec_command",
		RawArgs:  `{"command":"go test ./cmd/serf-tui","purpose":"Verify tool renderer purpose display"}`,
		Output:   "ok",
		Duration: 50 * time.Millisecond,
		Done:     true,
		Expanded: true,
	}

	got := RenderToolCall(tc, 100, false)
	lines := strings.Split(got, "\n")
	if len(lines) < 2 {
		t.Fatalf("expected purpose body line under header, got %q", got)
	}
	if !strings.Contains(lines[1], "Verify tool renderer purpose display") {
		t.Fatalf("first body line = %q, want purpose text; full render:\n%q", lines[1], got)
	}
	if !strings.Contains(lines[1], "\x1b[3m") {
		t.Fatalf("first body line should be italic-styled, got %q", lines[1])
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
