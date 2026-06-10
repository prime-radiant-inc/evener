package contextmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// toolResultContent extracts string content from a TurnTool.
func toolResultContent(t schema.Turn) string {
	for _, p := range t.Message.Content {
		if p.Kind == llm.ContentToolResult && p.ToolResult != nil {
			if s, ok := p.ToolResult.Content.(string); ok {
				return s
			}
		}
	}
	return ""
}

// --- Phase 1: Token tracking + estimation ---

func TestEstimateTokens_EmptyHistory(t *testing.T) {
	got := estimateTokens(nil)
	if got != 0 {
		t.Fatalf("EstimateTokens(nil) = %d, want 0", got)
	}
	got = estimateTokens([]schema.Turn{})
	if got != 0 {
		t.Fatalf("EstimateTokens([]) = %d, want 0", got)
	}
}

func TestEstimateTokens_SingleUserTurn(t *testing.T) {
	text := "Hello, world! This is a test message."
	turns := []schema.Turn{{Kind: schema.TurnUserInput, Message: llm.User(text)}}
	got := estimateTokens(turns)
	want := len(text) / 4
	if got != want {
		t.Fatalf("EstimateTokens = %d, want %d (len=%d)", got, want, len(text))
	}
}

func TestEstimateTokens_WithToolResults(t *testing.T) {
	content := "file contents here with lots of text"
	turns := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("read a file")},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c1", "read_file", content, false)},
	}
	got := estimateTokens(turns)
	// messageCharCount counts: message.ToolCallID + part.ToolCallID + part.Name + content
	// Message-level ToolCallID = "c1" (2), part ToolCallID = "c1" (2), part Name = "read_file" (9), content (36)
	// User text = "read a file" (11)
	// Total = 11 + 2 + 2 + 9 + 36 = 60, /4 = 15
	want := 15
	if got != want {
		t.Fatalf("EstimateTokens = %d, want %d", got, want)
	}
}

func TestEstimateTokens_WithThinking(t *testing.T) {
	turns := []schema.Turn{
		{Kind: schema.TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "let me think about this carefully"}},
				{Kind: llm.ContentText, Text: "answer"},
			},
		}},
	}
	got := estimateTokens(turns)
	totalChars := len("let me think about this carefully") + len("answer")
	want := totalChars / 4
	if got != want {
		t.Fatalf("EstimateTokens = %d, want %d", got, want)
	}
}

func TestContextManager_AddUsage_Accumulates(t *testing.T) {
	cm := NewManager(NewOpenAIProfile("gpt-5.2"), nil)
	cm.AddUsage(llm.Usage{InputTokens: 100, OutputTokens: 50, TotalTokens: 150})
	cm.AddUsage(llm.Usage{InputTokens: 200, OutputTokens: 100, TotalTokens: 300})

	got := cm.CumulativeUsage()
	if got.InputTokens != 300 {
		t.Fatalf("InputTokens = %d, want 300", got.InputTokens)
	}
	if got.OutputTokens != 150 {
		t.Fatalf("OutputTokens = %d, want 150", got.OutputTokens)
	}
	if got.TotalTokens != 450 {
		t.Fatalf("TotalTokens = %d, want 450", got.TotalTokens)
	}
}

func TestContextManager_CumulativeUsage_ThreadSafe(t *testing.T) {
	cm := NewManager(NewOpenAIProfile("gpt-5.2"), nil)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cm.AddUsage(llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2})
			_ = cm.CumulativeUsage()
		}()
	}
	wg.Wait()

	got := cm.CumulativeUsage()
	if got.InputTokens != 100 {
		t.Fatalf("InputTokens = %d, want 100", got.InputTokens)
	}
}

// --- Phase 2: Observation masking ---

func TestSummarizeToolResult_ReadFile(t *testing.T) {
	// Simulate a read_file result: line-numbered content.
	lines := "1 | package main\n2 | func main() {}\n"
	got := summarizeToolResult("read_file", lines, json.RawMessage(`{"file_path":"auth.go"}`))
	want := "[read_file: auth.go, 2 lines]"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSummarizeToolResult_Shell_Success(t *testing.T) {
	output := "ok\nexit_code=0 duration_ms=42 timed_out=false\n"
	got := summarizeToolResult("shell", output, json.RawMessage(`{"command":"go test"}`))
	want := `[shell: "go test" → exit 0]`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSummarizeToolResult_Shell_Failure(t *testing.T) {
	output := "FAIL\nexit_code=1 duration_ms=42 timed_out=false\n"
	got := summarizeToolResult("shell", output, json.RawMessage(`{"command":"go test"}`))
	want := `[shell: "go test" → exit 1]`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSummarizeToolResult_Shell_LongCommand(t *testing.T) {
	longCmd := "this is a very long command that exceeds sixty characters in length and should be truncated"
	output := "ok\nexit_code=0 duration_ms=1 timed_out=false\n"
	got := summarizeToolResult("shell", output, json.RawMessage(`{"command":"`+longCmd+`"}`))
	// Command should be truncated to 60 chars.
	if len(got) > 100 {
		t.Fatalf("summary too long: %q", got)
	}
	if got[len(got)-1] != ']' {
		t.Fatalf("summary not properly terminated: %q", got)
	}
}

func TestSummarizeToolResult_Grep(t *testing.T) {
	output := "file1.go:10:TODO fix\nfile2.go:20:TODO cleanup\n"
	got := summarizeToolResult("grep", output, json.RawMessage(`{"pattern":"TODO"}`))
	want := `[grep: "TODO" → 2 matches]`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSummarizeToolResult_Glob(t *testing.T) {
	output := "a.go\nb.go\nc.go\n"
	got := summarizeToolResult("glob", output, json.RawMessage(`{"pattern":"*.go"}`))
	want := `[glob: "*.go" → 3 files]`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSummarizeToolResult_EditFile(t *testing.T) {
	got := summarizeToolResult("edit_file", "OK", json.RawMessage(`{"file_path":"auth.go"}`))
	want := "[edit_file: auth.go → OK]"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSummarizeToolResult_WriteFile(t *testing.T) {
	got := summarizeToolResult("write_file", "OK", json.RawMessage(`{"file_path":"new.go"}`))
	want := "[write_file: new.go → OK]"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSummarizeToolResult_ApplyPatch(t *testing.T) {
	got := summarizeToolResult("apply_patch", "OK", json.RawMessage(`{"patch":"*** Begin Patch\n*** Update File: auth.go\n"}`))
	want := "[apply_patch → OK]"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSummarizeToolResult_WebFetch(t *testing.T) {
	content := "Here is some fetched content from the web page"
	got := summarizeToolResult("web_fetch", content, json.RawMessage(`{"url":"https://example.com"}`))
	want := fmt.Sprintf("[web_fetch: https://example.com → %d chars]", len(content))
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSummarizeToolResult_Delegate(t *testing.T) {
	got := summarizeToolResult("delegate", `{"job_id":"job_abc123"}`, json.RawMessage(`{"task":"do stuff"}`))
	want := "[delegate: job_abc123]"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	got = summarizeToolResult("delegate", `{"status":"running"}`, json.RawMessage(`{"task":"do stuff"}`))
	want = "[delegate: 20 chars]"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSummarizeToolResult_TaskList(t *testing.T) {
	got := summarizeToolResult("task_list", `[{"id":1},{"id":2},{"id":3}]`, json.RawMessage(`{"action":"view"}`))
	want := "[task_list: view → 3 tasks]"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSummarizeToolResult_UseSkill(t *testing.T) {
	body := "This is the skill body with instructions"
	got := summarizeToolResult("use_skill", body, json.RawMessage(`{"skill_name":"tdd"}`))
	want := fmt.Sprintf("[use_skill: tdd → %d chars]", len(body))
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSummarizeToolResult_UnknownTool(t *testing.T) {
	content := "some output"
	got := summarizeToolResult("custom_tool", content, json.RawMessage(`{}`))
	want := "[custom_tool: 11 chars]"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestMaskObservations_PreservesRecentTurns(t *testing.T) {
	// 4 turns: 2 old tool results + 2 recent. With preserveRecent=2, only the first 2 should be masked.
	bigContent := strings.Repeat("x", 200)
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: assistantWithToolCall("c1", "read_file", `{"file_path":"a.go"}`)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c1", "read_file", bigContent, false)},
		{Kind: schema.TurnAssistant, Message: assistantWithToolCall("c2", "read_file", `{"file_path":"b.go"}`)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c2", "read_file", bigContent, false)},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("done")},
	}

	maskObservations(history, 2, "communicate")

	// Last 2 turns (index 4,5) should be untouched.
	toolContent := toolResultContent(history[4])
	if toolContent == "" || toolContent[0] == '[' {
		t.Fatalf("recent tool result should be preserved, got: %q", toolContent)
	}

	// Turn index 2 (older) should be masked.
	masked := toolResultContent(history[2])
	if !startsWith(masked, "[read_file:") {
		t.Fatalf("old tool result should be masked, got: %q", masked)
	}
}

func TestMaskObservations_SkipsAlreadyMasked(t *testing.T) {
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c1", "read_file", "[read_file: a.go, 10 lines]", false)},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("done")},
	}

	maskObservations(history, 0, "communicate")
	got := toolResultContent(history[1])
	if got != "[read_file: a.go, 10 lines]" {
		t.Fatalf("already-masked result should be unchanged, got: %q", got)
	}
}

func TestMaskObservations_SkipsErrorResults(t *testing.T) {
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c1", "shell", "command not found\nexit_code=127 duration_ms=1 timed_out=false\n", true)},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("done")},
	}

	maskObservations(history, 0, "communicate")
	got := toolResultContent(history[1])
	if startsWith(got, "[shell:") {
		t.Fatalf("error result should NOT be masked, got: %q", got)
	}
}

func TestMaskObservations_PreservesCommunicate(t *testing.T) {
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c1", "communicate", `{"delivered":true,"inbox":[]}`, false)},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("done")},
	}

	maskObservations(history, 0, "communicate")
	got := toolResultContent(history[1])
	if startsWith(got, "[communicate:") {
		t.Fatalf("communicate result should NOT be masked, got: %q", got)
	}
}

func TestMaskObservations_EmptyHistory(t *testing.T) {
	maskObservations(nil, 6, "communicate")
	maskObservations([]schema.Turn{}, 6, "communicate")
}

func TestMaskObservations_PreservesAssistantTurns(t *testing.T) {
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("thinking about it")},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c1", "read_file", "1 | content\n", false)},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("done")},
	}

	maskObservations(history, 0, "communicate")
	// Assistant turn text should be unchanged.
	if history[1].Message.Text() != "thinking about it" {
		t.Fatalf("assistant turn text should be preserved, got: %q", history[1].Message.Text())
	}
}

// --- Phase 3: Thinking clearing ---

func TestClearThinking_RemovesOldThinkingText(t *testing.T) {
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "long reasoning about the problem"}},
				{Kind: llm.ContentText, Text: "my answer"},
			},
		}},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("final answer")},
	}

	clearThinking(history, 1)

	// Index 1 is old enough to be cleared. Check thinking was replaced.
	var thinkingText string
	for _, p := range history[1].Message.Content {
		if p.Kind == llm.ContentThinking && p.Thinking != nil {
			thinkingText = p.Thinking.Text
		}
	}
	want := fmt.Sprintf("[thinking: %d chars]", len("long reasoning about the problem"))
	if thinkingText != want {
		t.Fatalf("thinking text = %q, want %q", thinkingText, want)
	}

	// Text content should be preserved.
	if history[1].Message.Text() != "my answer" {
		t.Fatalf("text content should be preserved, got: %q", history[1].Message.Text())
	}
}

func TestClearThinking_PreservesRecentThinking(t *testing.T) {
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "recent thinking"}},
				{Kind: llm.ContentText, Text: "answer"},
			},
		}},
	}

	clearThinking(history, 2)

	// All turns are within the recent window, so thinking should be preserved.
	var thinkingText string
	for _, p := range history[1].Message.Content {
		if p.Kind == llm.ContentThinking && p.Thinking != nil {
			thinkingText = p.Thinking.Text
		}
	}
	if thinkingText != "recent thinking" {
		t.Fatalf("recent thinking should be preserved, got: %q", thinkingText)
	}
}

func TestClearThinking_PreservesRedactedThinking(t *testing.T) {
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentRedThinking, Thinking: &llm.ThinkingData{
					Text:     "",
					Redacted: true,
				}},
				{Kind: llm.ContentText, Text: "answer"},
			},
		}},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("final")},
	}

	clearThinking(history, 0)

	// Redacted thinking should remain untouched.
	for _, p := range history[1].Message.Content {
		if p.Kind == llm.ContentRedThinking && p.Thinking != nil {
			if p.Thinking.Redacted != true {
				t.Fatalf("redacted thinking should be preserved")
			}
		}
	}
}

func TestClearThinking_NoThinkingBlocks(t *testing.T) {
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("answer")},
	}

	clearThinking(history, 0)
}

func TestClearThinking_MixedContent(t *testing.T) {
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "thought one"}},
				{Kind: llm.ContentText, Text: "text one"},
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "c1", Name: "read_file", Arguments: json.RawMessage(`{"file_path":"a.go"}`)}},
			},
		}},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c1", "read_file", "content", false)},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("done")},
	}

	clearThinking(history, 1)

	// Thinking should be cleared in turn 1 (old).
	for _, p := range history[1].Message.Content {
		if p.Kind == llm.ContentThinking && p.Thinking != nil {
			if p.Thinking.Text == "thought one" {
				t.Fatalf("old thinking should be cleared")
			}
		}
	}
	// Text and tool call should be preserved.
	if history[1].Message.Text() != "text one" {
		t.Fatalf("text should be preserved, got: %q", history[1].Message.Text())
	}
	found := false
	for _, p := range history[1].Message.Content {
		if p.Kind == llm.ContentToolCall {
			found = true
		}
	}
	if !found {
		t.Fatalf("tool call should be preserved")
	}
}

// --- Phase 4: Deterministic checkpoint ---

func TestCheckpoint_CreatesValidMessage(t *testing.T) {
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("Fix the auth bug in login.go")},
		{Kind: schema.TurnAssistant, Message: assistantWithToolCall("c1", "read_file", `{"file_path":"login.go"}`)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c1", "read_file", "1 | package main\n", false)},
		{Kind: schema.TurnAssistant, Message: assistantWithToolCall("c2", "edit_file", `{"file_path":"login.go","old_string":"old","new_string":"new"}`)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c2", "edit_file", "OK", false)},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("done")},
	}

	result := checkpoint(history, 2, nil, "communicate")

	// Should have checkpoint message + preserved turns.
	// safeCutoff may back up the cutoff to avoid orphaned TurnTool, so we
	// may get more than 2 preserved turns.
	if len(result) < 3 {
		t.Fatalf("expected at least 3 turns, got %d", len(result))
	}
	// First turn should be the checkpoint.
	if result[0].Kind != schema.TurnCheckpoint {
		t.Fatalf("checkpoint should be TurnCheckpoint, got %s", result[0].Kind)
	}
	text := result[0].Message.Text()
	if !strings.Contains(text, "[CONTEXT CHECKPOINT]") {
		t.Fatalf("checkpoint missing header: %q", text)
	}
	if !strings.Contains(text, "[END CHECKPOINT]") {
		t.Fatalf("checkpoint missing footer: %q", text)
	}
}

// TestCheckpoint_DoesNotFreezeStaleTaskState is a regression test for the
// frozen-state staleness bug: compaction metadata captures the task snapshot at
// turn start, so a task completed during the round that triggers compaction
// would otherwise be embedded in the checkpoint as still in_progress. The
// checkpoint must not carry task state at all — live reminders are the canonical
// post-compaction source, so they cannot contradict it.
// TestCheckpoint_RendersOnlyFromMetaAndHistory proves checkpoint draws its
// content solely from the CompactionMeta it is handed and the turn history. The
// session deliberately captures task-free compaction meta at turn start
// (buildCompactionMeta records only the session id — see the agent-side
// TestBuildCompactionMeta_ExcludesTaskState), so state that lives outside meta
// and history — e.g. a task description sitting in the session's task store —
// must never surface in the checkpoint.
func TestCheckpoint_RendersOnlyFromMetaAndHistory(t *testing.T) {
	// Meta as the session freezes it at turn start: a session id, no tasks.
	meta := CompactionMeta{SessionID: "XSESS"}

	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("do the work")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("working")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent1")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent2")},
	}
	result := checkpoint(history, 2, &meta, "communicate")
	text := result[0].Message.Text()
	// "Frobnicate the gizmo" appears in neither meta nor history; it stands in for
	// live task state the session must not leak into the checkpoint.
	if strings.Contains(text, "Frobnicate the gizmo") {
		t.Fatalf("checkpoint surfaced state absent from meta and history: %q", text)
	}
}

func TestCheckpoint_IncludesOriginalPrompt(t *testing.T) {
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("Fix the auth bug in login.go")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("on it")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("done")},
	}

	result := checkpoint(history, 1, nil, "communicate")
	text := result[0].Message.Text()
	if !strings.Contains(text, "Fix the auth bug in login.go") {
		t.Fatalf("checkpoint missing original prompt: %q", text)
	}
}

func TestCheckpoint_TracksModifiedFiles(t *testing.T) {
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("prompt")},
		{Kind: schema.TurnAssistant, Message: assistantWithToolCall("c1", "edit_file", `{"file_path":"auth.go"}`)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c1", "edit_file", "OK", false)},
		{Kind: schema.TurnAssistant, Message: assistantWithToolCall("c2", "write_file", `{"file_path":"user.go"}`)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c2", "write_file", "OK", false)},
		{Kind: schema.TurnAssistant, Message: assistantWithToolCall("c3", "apply_patch", `{"patch":"*** Begin Patch\n*** Update File: test.go\n*** End Patch"}`)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c3", "apply_patch", "OK", false)},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("done")},
	}

	result := checkpoint(history, 1, nil, "communicate")
	text := result[0].Message.Text()
	if !strings.Contains(text, "Files modified:") {
		t.Fatalf("checkpoint missing files modified: %q", text)
	}
	for _, f := range []string{"auth.go", "user.go"} {
		if !strings.Contains(text, f) {
			t.Fatalf("checkpoint missing file %q: %q", f, text)
		}
	}
}

func TestCheckpoint_SummarizesActions(t *testing.T) {
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: assistantWithToolCall("c1", "read_file", `{"file_path":"a.go"}`)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c1", "read_file", "content", false)},
		{Kind: schema.TurnAssistant, Message: assistantWithToolCall("c2", "read_file", `{"file_path":"b.go"}`)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c2", "read_file", "content", false)},
		{Kind: schema.TurnAssistant, Message: assistantWithToolCall("c3", "shell", `{"command":"go test"}`)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c3", "shell", "ok\nexit_code=0 duration_ms=1 timed_out=false\n", false)},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("done")},
	}

	result := checkpoint(history, 1, nil, "communicate")
	text := result[0].Message.Text()
	if !strings.Contains(text, "3 tool calls") {
		t.Fatalf("checkpoint missing action summary: %q", text)
	}
	if !strings.Contains(text, "2 read_file") {
		t.Fatalf("checkpoint missing read_file count: %q", text)
	}
	if !strings.Contains(text, "1 shell") {
		t.Fatalf("checkpoint missing shell count: %q", text)
	}
}

func TestCheckpoint_PreservesRecentTurns(t *testing.T) {
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("old answer")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent1")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent2")},
	}

	result := checkpoint(history, 2, nil, "communicate")
	// Checkpoint + 2 recent.
	if len(result) != 3 {
		t.Fatalf("expected 3 turns, got %d", len(result))
	}
	if result[1].Message.Text() != "recent1" {
		t.Fatalf("expected recent1, got %q", result[1].Message.Text())
	}
	if result[2].Message.Text() != "recent2" {
		t.Fatalf("expected recent2, got %q", result[2].Message.Text())
	}
}

func TestCheckpoint_NoHistoryToCheckpoint(t *testing.T) {
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("answer")},
	}

	result := checkpoint(history, 6, nil, "communicate")
	if len(result) != len(history) {
		t.Fatalf("expected unchanged history length %d, got %d", len(history), len(result))
	}
}

// --- Phase 5: LLM summarization ---

func TestSummarizeWithLLM_DefaultsToActiveModel(t *testing.T) {
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				// Without an explicitly configured compaction model, summarize on
				// the active session model. This avoids provider-default auxiliary
				// models that may be unavailable for the account type.
				if req.Model != "gpt-5.2" {
					t.Errorf("expected active model gpt-5.2, got %q", req.Model)
				}
				// Verify the prompt asks for compaction/summarization.
				found := false
				for _, m := range req.Messages {
					text := m.Text()
					if strings.Contains(text, "COMPACTION") || strings.Contains(text, "handoff summary") {
						found = true
					}
				}
				if !found {
					t.Error("expected compaction/handoff prompt")
				}
				return llm.Response{Message: llm.Assistant("Summary: fixed auth bug")}
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	cm := NewManager(NewOpenAIProfile("gpt-5.2"), client)

	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("Fix the auth bug")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("I'll fix it")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent1")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent2")},
	}

	ctx := context.Background()
	result, err := cm.summarizeWithLLM(ctx, history, 2)
	if err != nil {
		t.Fatalf("summarizeWithLLM: %v", err)
	}

	// Should have summary + 2 preserved turns.
	if len(result) != 3 {
		t.Fatalf("expected 3 turns, got %d", len(result))
	}
}

func TestSummarizeWithLLM_UsesConfiguredCompactionModel(t *testing.T) {
	adapter := &fakeAdapter{name: "openai"}
	client := llm.NewClient()
	client.Register(adapter)

	profile := provider.WithCheapModel(NewOpenAIProfile("gpt-5.2"), "gpt-5-mini")
	cm := NewManager(profile, client)

	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("old")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent")},
	}

	if _, err := cm.summarizeWithLLM(context.Background(), history, 1); err != nil {
		t.Fatalf("summarizeWithLLM: %v", err)
	}
	reqs := adapter.Requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(reqs))
	}
	if reqs[0].Model != "gpt-5-mini" {
		t.Fatalf("summary model = %q, want gpt-5-mini", reqs[0].Model)
	}
}

func TestSummarizeWithLLM_FallsBackToActiveModelWhenConfiguredModelFails(t *testing.T) {
	adapter := &fallbackSummaryAdapter{name: "openai", t: t}
	client := llm.NewClient()
	client.Register(adapter)

	profile := provider.WithCheapModel(NewOpenAIProfile("gpt-5.2"), "gpt-5-mini")
	cm := NewManager(profile, client)

	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("old")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent")},
	}

	result, err := cm.summarizeWithLLM(context.Background(), history, 1)
	if err != nil {
		t.Fatalf("summarizeWithLLM: %v", err)
	}
	if !strings.Contains(result[0].Message.Text(), "fallback summary") {
		t.Fatalf("summary did not come from fallback response: %q", result[0].Message.Text())
	}
	if len(adapter.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(adapter.requests))
	}
}

type fallbackSummaryAdapter struct {
	name     string
	t        *testing.T
	requests []llm.Request
}

func (a *fallbackSummaryAdapter) Name() string { return a.name }

func (a *fallbackSummaryAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	_ = ctx
	a.requests = append(a.requests, req)
	switch len(a.requests) {
	case 1:
		if req.Model != "gpt-5-mini" {
			a.t.Fatalf("first summary model = %q, want gpt-5-mini", req.Model)
		}
		return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 400, "configured compaction model unavailable", nil, nil)
	case 2:
		if req.Model != "gpt-5.2" {
			a.t.Fatalf("fallback summary model = %q, want gpt-5.2", req.Model)
		}
		return llm.Response{Message: llm.Assistant("fallback summary")}, nil
	default:
		a.t.Fatalf("unexpected request %d", len(a.requests))
		return llm.Response{}, errors.New("unexpected request")
	}
}

func TestSummarizeWithLLM_DoesNotFallbackOnCancellation(t *testing.T) {
	adapter := &cancelingSummaryAdapter{name: "openai"}
	client := llm.NewClient()
	client.Register(adapter)

	profile := provider.WithCheapModel(NewOpenAIProfile("gpt-5.2"), "gpt-5-mini")
	cm := NewManager(profile, client)
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("old")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent")},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := cm.summarizeWithLLM(ctx, history, 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if len(adapter.requests) != 1 {
		t.Fatalf("requests = %d, want 1 (no active-model fallback after cancellation)", len(adapter.requests))
	}
}

type cancelingSummaryAdapter struct {
	name     string
	requests []llm.Request
}

func (a *cancelingSummaryAdapter) Name() string { return a.name }

func (a *cancelingSummaryAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	a.requests = append(a.requests, req)
	return llm.Response{}, ctx.Err()
}

func (a *cancelingSummaryAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	_ = ctx
	_ = req
	return nil, llm.ErrStreamUnsupported
}

func (a *fallbackSummaryAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	_ = ctx
	_ = req
	return nil, llm.ErrStreamUnsupported
}

func TestSummarizeWithLLM_ReplacesOldHistory(t *testing.T) {
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("Summary of work done")}
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	cm := NewManager(NewOpenAIProfile("gpt-5.2"), client)

	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("step 1")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("step 2")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent")},
	}

	ctx := context.Background()
	result, err := cm.summarizeWithLLM(ctx, history, 1)
	if err != nil {
		t.Fatalf("summarizeWithLLM: %v", err)
	}

	// First turn should contain the LLM summary.
	text := result[0].Message.Text()
	if !strings.Contains(text, "Summary of work done") {
		t.Fatalf("expected summary in first turn, got: %q", text)
	}
	// First turn should be a user message (context for the model).
	if result[0].Message.Role != llm.RoleUser {
		t.Fatalf("summary should be user role, got %s", result[0].Message.Role)
	}
}

func TestSummarizeWithLLM_PreservesRecentTurns(t *testing.T) {
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("summary")}
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	cm := NewManager(NewOpenAIProfile("gpt-5.2"), client)

	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("old")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent1")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent2")},
	}

	ctx := context.Background()
	result, err := cm.summarizeWithLLM(ctx, history, 2)
	if err != nil {
		t.Fatalf("summarizeWithLLM: %v", err)
	}

	if result[1].Message.Text() != "recent1" {
		t.Fatalf("expected recent1, got %q", result[1].Message.Text())
	}
	if result[2].Message.Text() != "recent2" {
		t.Fatalf("expected recent2, got %q", result[2].Message.Text())
	}
}

func TestSummarizeWithLLM_ErrorFallsBackGracefully(t *testing.T) {
	// Use errorAdapter for a reliable error path test.
	adapter := &errorAdapter{name: "openai"}
	client := llm.NewClient()
	client.Register(adapter)

	cm := NewManager(NewOpenAIProfile("gpt-5.2"), client)

	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("step 1")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent")},
	}

	result, err := cm.summarizeWithLLM(context.Background(), history, 1)
	if err == nil {
		t.Fatal("expected error from summarizeWithLLM when adapter fails")
	}
	if result != nil {
		t.Fatalf("expected nil result on error, got %d turns", len(result))
	}
}

// --- Phase 6: MaybeCompact orchestrator ---

// makeBigHistory creates a history where EstimateTokens returns approximately targetTokens.
func makeBigHistory(targetTokens int) []schema.Turn {
	turns := []schema.Turn{{Kind: schema.TurnUserInput, Message: llm.User("Fix the auth bug")}}
	for estimateTokens(turns) < targetTokens {
		id := fmt.Sprintf("c%d", len(turns))
		turns = append(turns,
			schema.Turn{Kind: schema.TurnAssistant, Message: assistantWithToolCall(id, "read_file", `{"file_path":"file.go"}`)},
			schema.Turn{Kind: schema.TurnTool, Message: llm.ToolResultNamed(id, "read_file", strings.Repeat("x", 400), false)},
		)
	}
	return turns
}

func TestMaybeCompact_NoCompactionBelow80Percent(t *testing.T) {
	// At 70% pressure, no compaction should fire. Compaction starts at 80% (checkpoint).
	profile := testProfile("openai", "test", 1000)
	cm := NewManager(profile, nil)
	cm.PreserveRecentTurns = 2

	// Create history filling ~70% of 1000 tokens = 700 tokens via tool results.
	history := makeBigHistory(700)
	history = append(history,
		schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant("recent1")},
		schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant("recent2")},
	)

	var evs []events.SessionEvent
	emitFn := func(kind events.EventKind, data events.EventData) {
		evs = append(evs, events.SessionEvent{Kind: kind, Data: data})
	}

	cm.MaybeCompact(context.Background(), &history, 0, emitFn)

	for _, e := range evs {
		if e.Kind == events.EventContextCompaction {
			t.Fatalf("no compaction should fire at 70%% pressure, got event: %+v", e.Data)
		}
	}
}

func TestMaybeCompact_BelowThreshold_NoAction(t *testing.T) {
	cm := NewManager(NewOpenAIProfile("gpt-5.2"), nil)

	// Small history, well below any threshold.
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("ok")},
	}

	var evs []events.SessionEvent
	emitFn := func(kind events.EventKind, data events.EventData) {
		evs = append(evs, events.SessionEvent{Kind: kind, Data: data})
	}

	cm.MaybeCompact(context.Background(), &history, 100, emitFn)
	// No compaction events should have been emitted.
	for _, e := range evs {
		if e.Kind == events.EventContextCompaction {
			t.Fatalf("unexpected CONTEXT_COMPACTION event below threshold")
		}
	}
	// History should be unchanged.
	if len(history) != 2 {
		t.Fatalf("expected 2 turns, got %d", len(history))
	}
}

func TestMaybeCompact_CheckpointThreshold(t *testing.T) {
	profile := testProfile("openai", "test", 500)
	cm := NewManager(profile, nil)
	cm.PreserveRecentTurns = 2

	// Each assistant turn ~400 chars = 100 tokens. Need 85% of 500 = 425 tokens.
	history := []schema.Turn{{Kind: schema.TurnUserInput, Message: llm.User("Fix the auth bug")}}
	for estimateTokens(history) < 425 {
		history = append(history,
			schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant(strings.Repeat("analysis ", 50))},
		)
	}
	history = append(history,
		schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant("recent1")},
		schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant("recent2")},
	)

	var evs []events.SessionEvent
	emitFn := func(kind events.EventKind, data events.EventData) {
		evs = append(evs, events.SessionEvent{Kind: kind, Data: data})
	}

	cm.MaybeCompact(context.Background(), &history, 0, emitFn)

	// At 85%, checkpoint should trigger (threshold is 80%).
	foundCheckpoint := false
	for _, e := range evs {
		if d, ok := e.Data.(events.ContextCompactionData); ok && d.Layer == "checkpoint" {
			foundCheckpoint = true
		}
	}
	if !foundCheckpoint {
		t.Fatalf("expected checkpoint compaction event; got events: %+v", evs)
	}

	// History should have been replaced with checkpoint + recent.
	if len(history) > 5 {
		t.Fatalf("expected compacted history, got %d turns", len(history))
	}
}

func TestMaybeCompact_EmitsEvents(t *testing.T) {
	profile := testProfile("openai", "test", 500)
	cm := NewManager(profile, nil)
	cm.PreserveRecentTurns = 2

	// Fill ~85% = 425 tokens (above 80% checkpoint threshold).
	history := makeBigHistory(425)
	history = append(history,
		schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant("recent1")},
		schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant("recent2")},
	)

	var evs []events.SessionEvent
	emitFn := func(kind events.EventKind, data events.EventData) {
		evs = append(evs, events.SessionEvent{Kind: kind, Data: data})
	}

	cm.MaybeCompact(context.Background(), &history, 0, emitFn)

	// Should have at least one compaction event.
	compactionCount := 0
	for _, e := range evs {
		if d, ok := e.Data.(events.ContextCompactionData); ok {
			compactionCount++
			// Each event should carry a layer and token counts.
			if d.Layer == "" {
				t.Fatalf("compaction event missing 'layer': %+v", e.Data)
			}
			if d.EstTokensBefore == 0 {
				t.Fatalf("compaction event missing 'est_tokens_before': %+v", e.Data)
			}
		}
	}
	if compactionCount == 0 {
		t.Fatalf("expected compaction events")
	}
}

func TestMaybeCompact_RespectsSysPromptSize(t *testing.T) {
	profile := testProfile("openai", "test", 1000)
	cm := NewManager(profile, nil)
	cm.PreserveRecentTurns = 2

	// Small history, but giant system prompt.
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: assistantWithToolCall("c1", "read_file", `{"file_path":"a.go"}`)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c1", "read_file", strings.Repeat("x", 400), false)},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent1")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent2")},
	}

	var evs []events.SessionEvent
	emitFn := func(kind events.EventKind, data events.EventData) {
		evs = append(evs, events.SessionEvent{Kind: kind, Data: data})
	}

	// sys prompt is 2800 chars ≈ 700 tokens + history ~100 tokens = 800/1000 = 80%
	cm.MaybeCompact(context.Background(), &history, 2800, emitFn)

	// With sys prompt, we're at ~80%, should trigger checkpoint.
	foundCheckpoint := false
	for _, e := range evs {
		if d, ok := e.Data.(events.ContextCompactionData); ok && d.Layer == "checkpoint" {
			foundCheckpoint = true
		}
	}
	if !foundCheckpoint {
		t.Fatalf("expected checkpoint compaction event when sys prompt pushes over threshold; got events: %+v", evs)
	}
}

// --- Phase 8: System prompt subagent guidance ---
// (Tests in profile_test.go)

// --- Review fix tests ---

// H1: checkpoint and summarizeWithLLM must not produce invalid message ordering.
// If preserveRecent falls mid-pair (e.g., starts on a TurnTool), the cutoff must
// be adjusted backward to include the preceding assistant turn.
func TestCheckpoint_AdjustsCutoffToAvoidOrphanedToolTurn(t *testing.T) {
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("fix the bug")},
		{Kind: schema.TurnAssistant, Message: assistantWithToolCall("c1", "read_file", `{"file_path":"a.go"}`)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c1", "read_file", "content", false)},
		{Kind: schema.TurnAssistant, Message: assistantWithToolCall("c2", "edit_file", `{"file_path":"b.go"}`)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c2", "edit_file", "OK", false)},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("done")},
	}

	// preserveRecent=3 → cutoff=3, preserved turns start at index 3 (TurnAssistant) — OK.
	// preserveRecent=2 → cutoff=4, preserved turns start at index 4 (TurnTool) — BAD.
	result := checkpoint(history, 2, nil, "communicate")

	// First preserved turn after checkpoint must NOT be a TurnTool.
	if len(result) < 2 {
		t.Fatalf("expected at least 2 turns, got %d", len(result))
	}
	if result[0].Kind != schema.TurnCheckpoint {
		t.Fatalf("first turn should be checkpoint (TurnCheckpoint), got %s", result[0].Kind)
	}
	if result[1].Kind == schema.TurnTool {
		t.Fatalf("second turn must not be TurnTool — invalid message ordering for LLM APIs")
	}
}

func TestSummarizeWithLLM_AdjustsCutoffToAvoidOrphanedToolTurn(t *testing.T) {
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("summary")}
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	cm := NewManager(NewOpenAIProfile("gpt-5.2"), client)

	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: assistantWithToolCall("c1", "read_file", `{"file_path":"a.go"}`)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c1", "read_file", "content", false)},
		{Kind: schema.TurnAssistant, Message: assistantWithToolCall("c2", "shell", `{"command":"go test"}`)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c2", "shell", "ok\nexit_code=0\n", false)},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("done")},
	}

	// preserveRecent=2 → cutoff=4, preserved starts at TurnTool — BAD.
	result, err := cm.summarizeWithLLM(context.Background(), history, 2)
	if err != nil {
		t.Fatalf("summarizeWithLLM: %v", err)
	}

	if len(result) < 2 {
		t.Fatalf("expected at least 2 turns, got %d", len(result))
	}
	if result[1].Kind == schema.TurnTool {
		t.Fatalf("second turn must not be TurnTool — invalid message ordering for LLM APIs")
	}
}

// H2: After L1 masks shell results, L3 checkpoint's parseExitCode must still
// extract exit codes from the masked format [shell: "cmd" → exit N].
func TestParseExitCode_MaskedFormat(t *testing.T) {
	// After L1 masking, shell results look like: [shell: "go test" → exit 0]
	masked := `[shell: "go test" → exit 0]`
	got := parseExitCode(masked)
	if got != "0" {
		t.Fatalf("parseExitCode on masked format: got %q, want %q", got, "0")
	}

	masked2 := `[shell: "go test" → exit 1]`
	got2 := parseExitCode(masked2)
	if got2 != "1" {
		t.Fatalf("parseExitCode on masked format: got %q, want %q", got2, "1")
	}
}

// H2: Checkpoint should match shell tool results by ToolCallID and respect cutoff.
func TestCheckpoint_ShellResultMatchesByToolCallID(t *testing.T) {
	// Assistant calls read_file + shell in same turn. Tool results follow.
	// Checkpoint should pair shell command with the shell result, not the read_file result.
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
					ID: "c1", Name: "read_file", Arguments: json.RawMessage(`{"file_path":"a.go"}`),
				}},
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
					ID: "c2", Name: "shell", Arguments: json.RawMessage(`{"command":"go test"}`),
				}},
			},
		}},
		// read_file result comes first
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c1", "read_file", "package main\n", false)},
		// shell result comes second
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c2", "shell", "PASS\nexit_code=0 duration_ms=100 timed_out=false\n", false)},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("done")},
	}

	result := checkpoint(history, 1, nil, "communicate")
	text := result[0].Message.Text()

	// The checkpoint should show exit 0, not "?" from the read_file result.
	if !strings.Contains(text, "exit 0") {
		t.Fatalf("checkpoint should contain 'exit 0' for shell result, got:\n%s", text)
	}
	if strings.Contains(text, "exit ?") {
		t.Fatalf("checkpoint should NOT contain 'exit ?' — shell result not matched correctly:\n%s", text)
	}
}

// H4: summarizeWithLLM should truncate the prompt to avoid exceeding the cheap model's context.
func TestSummarizeWithLLM_TruncatesPromptForCheapModel(t *testing.T) {
	var receivedPromptLen int
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				for _, m := range req.Messages {
					receivedPromptLen += len(m.Text())
				}
				return llm.Response{Message: llm.Assistant("summary")}
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	cm := NewManager(NewOpenAIProfile("gpt-5.2"), client)

	// Build history with enormous user messages — many times larger than any cheap model can handle.
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User(strings.Repeat("x", 100_000))},
		{Kind: schema.TurnAssistant, Message: llm.Assistant(strings.Repeat("y", 100_000))},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent")},
	}

	_, err := cm.summarizeWithLLM(context.Background(), history, 1)
	if err != nil {
		t.Fatalf("summarizeWithLLM: %v", err)
	}

	// The prompt sent to the cheap model should be bounded, not 200k+ chars.
	// The history serializer caps at ~80k chars, plus the instruction prefix.
	if receivedPromptLen > 95_000 {
		t.Fatalf("prompt sent to cheap model is too large: %d chars (should be truncated)", receivedPromptLen)
	}
}

func TestSummarizeWithLLM_RequestsInterleavedConversationTimeline(t *testing.T) {
	var prompt string
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				var promptSb1365 strings.Builder
				for _, m := range req.Messages {
					promptSb1365.WriteString(m.Text())
				}
				prompt += promptSb1365.String()
				return llm.Response{Message: llm.Assistant("summary")}
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	cm := NewManager(NewOpenAIProfile("gpt-5.2"), client)

	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("first request")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("first reply")},
		{Kind: schema.TurnUserInput, Message: llm.User("second request")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent")},
	}

	if _, err := cm.summarizeWithLLM(context.Background(), history, 1); err != nil {
		t.Fatalf("summarizeWithLLM: %v", err)
	}
	if !strings.Contains(prompt, "## Conversation Timeline") || !strings.Contains(prompt, "interleaved") {
		t.Fatalf("summary prompt should request an interleaved conversation timeline:\n%s", prompt)
	}
	if strings.Contains(prompt, "## User Messages") || strings.Contains(prompt, "## Agent Responses") {
		t.Fatalf("summary prompt should not request bundled user/agent sections:\n%s", prompt)
	}
}

// H5: summarizeWithLLM error path must be reliably testable.
func TestSummarizeWithLLM_AdapterError_ReturnsError(t *testing.T) {
	adapter := &errorAdapter{name: "openai"}
	client := llm.NewClient()
	client.Register(adapter)
	cm := NewManager(NewOpenAIProfile("gpt-5.2"), client)

	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("step 1")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent")},
	}

	result, err := cm.summarizeWithLLM(context.Background(), history, 1)
	if err == nil {
		t.Fatal("expected error from summarizeWithLLM when adapter fails")
	}
	if result != nil {
		t.Fatalf("expected nil result on error, got %d turns", len(result))
	}
}

// M1: Repeated checkpoint should still find the real original prompt, not the checkpoint message.
func TestCheckpoint_RepeatedCheckpoint_PreservesOriginalPrompt(t *testing.T) {
	// Simulate first checkpoint output (legacy format with "Original task:").
	firstCheckpoint := schema.Turn{
		Kind:    schema.TurnUserInput,
		Message: llm.User("[CONTEXT CHECKPOINT]\nOriginal task: Fix the auth bug\nFiles modified: auth.go\n[END CHECKPOINT]\n"),
	}
	history := []schema.Turn{
		firstCheckpoint,
		{Kind: schema.TurnAssistant, Message: assistantWithToolCall("c3", "read_file", `{"file_path":"auth.go"}`)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c3", "read_file", "content", false)},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("done")},
	}

	result := checkpoint(history, 1, nil, "communicate")
	text := result[0].Message.Text()

	// The original prompt text should be preserved in the User messages section,
	// extracted from the legacy "Original task:" line.
	if !strings.Contains(text, "Fix the auth bug") {
		t.Fatalf("repeated checkpoint should preserve user messages from prior checkpoint:\n%s", text)
	}
}

// Verify user messages round-trip through Markdown across repeated compactions,
// including messages with newlines and special characters.
func TestCheckpoint_UserMessages_MarkdownRoundTrip(t *testing.T) {
	// First compaction: two user messages, one with embedded newline.
	fencedMessage := "include this fence:\n```\nhello\n```"
	h1 := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("hi! ls the cwd please")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("done")},
		{Kind: schema.TurnUserInput, Message: llm.User("now fix the bug\nwith newlines")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("ok")},
		{Kind: schema.TurnUserInput, Message: llm.User(fencedMessage)},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent")},
	}
	r1 := checkpoint(h1, 1, nil, "communicate")
	text1 := r1[0].Message.Text()

	if !strings.Contains(text1, "## Conversation") {
		t.Fatalf("first checkpoint missing markdown conversation section:\n%s", text1)
	}
	if strings.Contains(text1, "<conversation>") || strings.Contains(text1, "<user_messages>") || strings.Contains(text1, "<agent_responses>") {
		t.Fatalf("first checkpoint should not use XML/JSON conversation tags:\n%s", text1)
	}
	if !strings.Contains(text1, "hi! ls the cwd please") {
		t.Fatalf("first checkpoint missing first user message:\n%s", text1)
	}
	if !strings.Contains(text1, "now fix the bug\nwith newlines") {
		t.Fatalf("first checkpoint missing second user message with newline:\n%s", text1)
	}
	conversation1 := extractCheckpointConversation(text1)
	if len(conversation1) != 3 || conversation1[2].Text != fencedMessage {
		t.Fatalf("first checkpoint did not round-trip fenced markdown message: %+v", conversation1)
	}

	// Second compaction: feed checkpoint back in + new user message.
	h2 := []schema.Turn{
		r1[0], // the checkpoint turn
		{Kind: schema.TurnUserInput, Message: llm.User("also add tests")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent2")},
	}
	r2 := checkpoint(h2, 1, nil, "communicate")
	text2 := r2[0].Message.Text()

	// All four user messages should survive.
	if !strings.Contains(text2, "hi! ls the cwd please") {
		t.Fatalf("second checkpoint lost first user message:\n%s", text2)
	}
	if !strings.Contains(text2, "now fix the bug") {
		t.Fatalf("second checkpoint lost second user message:\n%s", text2)
	}
	if !strings.Contains(text2, "also add tests") {
		t.Fatalf("second checkpoint lost fourth user message:\n%s", text2)
	}
	conversation2 := extractCheckpointConversation(text2)
	var foundFence bool
	for _, entry := range conversation2 {
		foundFence = foundFence || entry.Text == fencedMessage
	}
	if !foundFence {
		t.Fatalf("second checkpoint lost fenced markdown message: %+v", conversation2)
	}
}

func TestCheckpoint_ConversationInterleavesUserMessagesAndAgentResponses(t *testing.T) {
	reply1 := communicateCall("c1", "first reply")
	reply2 := communicateCall("c2", "second reply")
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("first request")},
		{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &reply1}}}},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c1", "communicate", `{"delivered":true}`, false)},
		{Kind: schema.TurnUserInput, Message: llm.User("second request")},
		{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &reply2}}}},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c2", "communicate", `{"delivered":true}`, false)},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent")},
	}

	result := checkpoint(history, 1, nil, "communicate")
	text := result[0].Message.Text()

	if strings.Contains(text, "<conversation>") || strings.Contains(text, "<user_messages>") || strings.Contains(text, "<agent_responses>") {
		t.Fatalf("checkpoint should use markdown, not XML/JSON tags:\n%s", text)
	}
	if !strings.Contains(text, "## Conversation") || !strings.Contains(text, "### User") || !strings.Contains(text, "### Agent") {
		t.Fatalf("checkpoint should contain markdown conversation headings:\n%s", text)
	}
	conversation := extractCheckpointConversation(text)
	want := []checkpointConversationEntry{
		{Role: "user", Text: "first request"},
		{Role: "agent", Text: "first reply"},
		{Role: "user", Text: "second request"},
		{Role: "agent", Text: "second reply"},
	}
	if len(conversation) != len(want) {
		t.Fatalf("conversation=%+v, want %+v\ncheckpoint:\n%s", conversation, want, text)
	}
	for i := range want {
		if conversation[i] != want[i] {
			t.Fatalf("conversation[%d]=%+v, want %+v\nall=%+v", i, conversation[i], want[i], conversation)
		}
	}
}

func TestCheckpoint_ConversationReadsLegacyBundledCompaction(t *testing.T) {
	oldCheckpoint := `[CONTEXT CHECKPOINT]
<user_messages>["first request","second request"]</user_messages>
<agent_responses>["first reply","second reply"]</agent_responses>
[END CHECKPOINT]`
	history := []schema.Turn{
		{Kind: schema.TurnCheckpoint, Message: llm.User(oldCheckpoint)},
		{Kind: schema.TurnUserInput, Message: llm.User("third request")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent")},
	}

	result := checkpoint(history, 1, nil, "communicate")
	if text := result[0].Message.Text(); strings.Contains(text, "<user_messages>") || strings.Contains(text, "<agent_responses>") {
		t.Fatalf("legacy checkpoint should migrate to markdown conversation:\n%s", text)
	}
	conversation := extractCheckpointConversation(result[0].Message.Text())
	want := []checkpointConversationEntry{
		{Role: "user", Text: "first request"},
		{Role: "user", Text: "second request"},
		{Role: "agent", Text: "first reply"},
		{Role: "agent", Text: "second reply"},
		{Role: "user", Text: "third request"},
	}
	if len(conversation) != len(want) {
		t.Fatalf("conversation=%+v, want %+v", conversation, want)
	}
	for i := range want {
		if conversation[i] != want[i] {
			t.Fatalf("conversation[%d]=%+v, want %+v\nall=%+v", i, conversation[i], want[i], conversation)
		}
	}
}

// M2: Checkpoint tool counts must be deterministic (sorted).
func TestCheckpoint_ToolCountsAreDeterministic(t *testing.T) {
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: assistantWithToolCall("c1", "read_file", `{"file_path":"a.go"}`)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c1", "read_file", "content", false)},
		{Kind: schema.TurnAssistant, Message: assistantWithToolCall("c2", "shell", `{"command":"go test"}`)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c2", "shell", "ok\nexit_code=0\n", false)},
		{Kind: schema.TurnAssistant, Message: assistantWithToolCall("c3", "edit_file", `{"file_path":"a.go"}`)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c3", "edit_file", "OK", false)},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("done")},
	}

	// Run checkpoint 20 times and verify output is identical each time.
	var first string
	for i := 0; i < 20; i++ {
		cp := make([]schema.Turn, len(history))
		copy(cp, history)
		result := checkpoint(cp, 1, nil, "communicate")
		text := result[0].Message.Text()
		if i == 0 {
			first = text
		} else if text != first {
			t.Fatalf("checkpoint output is non-deterministic!\nfirst: %q\nthis:  %q", first, text)
		}
	}
}

// M5: Checkpoint should include files from apply_patch.
func TestCheckpoint_TracksFilesFromApplyPatch(t *testing.T) {
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: assistantWithToolCall("c1", "apply_patch", `{"patch":"*** Begin Patch\n*** Update File: test.go\n*** End Patch"}`)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c1", "apply_patch", "OK", false)},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("done")},
	}

	result := checkpoint(history, 1, nil, "communicate")
	text := result[0].Message.Text()
	if !strings.Contains(text, "test.go") {
		t.Fatalf("checkpoint should include test.go from apply_patch:\n%s", text)
	}
}

func TestCheckpoint_IncludesWebSearchCount(t *testing.T) {
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("search for docs")},
		{Kind: schema.TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentWebSearch, WebSearch: &llm.WebSearchData{Query: "Go docs"}},
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "c1", Name: "read_file", Arguments: json.RawMessage(`{"file_path":"a.go"}`)}},
				{Kind: llm.ContentText, Text: "Found the docs."},
			},
		}},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c1", "read_file", "file contents", false)},
		// Preserved recent turns:
		{Kind: schema.TurnUserInput, Message: llm.User("next question")},
		{Kind: schema.TurnAssistant, Message: llm.Message{
			Role:    llm.RoleAssistant,
			Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "answer"}},
		}},
	}

	result := checkpoint(history, 2, nil, "communicate") // preserve last 2 turns

	if len(result) < 1 {
		t.Fatalf("checkpoint returned empty")
	}
	cpText := result[0].Message.Text()
	if !strings.Contains(cpText, "web_search") {
		t.Fatalf("checkpoint missing web_search count: %s", cpText)
	}
	if !strings.Contains(cpText, "1 read_file") {
		t.Fatalf("checkpoint missing read_file count: %s", cpText)
	}
}

// Token-based pressure: Manager should use actual InputTokens from API
// responses for pressure calculation instead of relying solely on char/4.
func TestContextManager_UsesLastInputTokensForPressure(t *testing.T) {
	profile := testProfile("openai", "test", 1000)
	cm := NewManager(profile, nil)

	// Record that the last API response reported 550 input tokens for a 10-turn history.
	cm.RecordInputTokens(550, 10)

	// Add 2 new turns since the measurement (~20 chars = ~5 tokens).
	history := make([]schema.Turn, 12)
	for i := 0; i < 10; i++ {
		history[i] = schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant("x")}
	}
	history[10] = schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant(strings.Repeat("y", 20))}
	history[11] = schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant(strings.Repeat("z", 20))}

	// Pressure should be based on: 550 (known) + ~10 (new turns) = ~560 tokens
	// Not char/4 of entire history which would be much less.
	pressure := cm.estimatePressure(history, 0)

	// With 1000 token window, pressure should be ~0.56 (not the ~0.03 from char/4).
	if pressure < 0.5 {
		t.Fatalf("pressure should use lastInputTokens, got %.2f (expected ~0.56)", pressure)
	}
}

func TestContextManager_EstimateUsageReportsRemainingWindow(t *testing.T) {
	profile := testProfile("openai", "test", 1000)
	cm := NewManager(profile, nil)
	cm.RecordInputTokens(550, 10)

	history := make([]schema.Turn, 11)
	history[10] = schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant(strings.Repeat("y", 40))}

	got := cm.EstimateUsage(history, 0)
	if got.Used != 560 || got.Window != 1000 || got.Remaining != 440 {
		t.Fatalf("EstimateUsage = %+v, want used=560 window=1000 remaining=440", got)
	}
}

func TestContextManager_FallsBackToCharHeuristicWithoutMeasurement(t *testing.T) {
	profile := testProfile("openai", "test", 1000)
	cm := NewManager(profile, nil)

	// No RecordInputTokens called — should fall back to char/4.
	history := []schema.Turn{
		{Kind: schema.TurnAssistant, Message: llm.Assistant(strings.Repeat("x", 400))},
	}

	pressure := cm.estimatePressure(history, 0)

	// 400 chars / 4 = 100 tokens, 100/1000 = 0.10
	if pressure < 0.09 || pressure > 0.11 {
		t.Fatalf("expected pressure ~0.10 from char/4 fallback, got %.2f", pressure)
	}
}

func TestContextManager_ResetsAfterCompaction(t *testing.T) {
	profile := testProfile("openai", "test", 1000)
	cm := NewManager(profile, nil)
	cm.PreserveRecentTurns = 2

	// Record high token count.
	cm.RecordInputTokens(700, 5)

	// After compaction modifies history, the measurement should reset.
	// Build a history that's big enough to trigger observation masking.
	history := makeBigHistory(650)
	history = append(history,
		schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant("recent1")},
		schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant("recent2")},
	)

	var evs []events.SessionEvent
	emitFn := func(kind events.EventKind, data events.EventData) {
		evs = append(evs, events.SessionEvent{Kind: kind, Data: data})
	}

	cm.MaybeCompact(context.Background(), &history, 0, emitFn)

	// After compaction, lastInputTokens should be reset.
	cm.mu.Lock()
	lit := cm.lastInputTokens
	cm.mu.Unlock()
	if lit != 0 {
		t.Fatalf("lastInputTokens should be reset after compaction, got %d", lit)
	}
}

// --- Fix: safeCutoff robustness (H1 + H2) ---

func TestSafeCutoff_NoAdjustmentNeeded(t *testing.T) {
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("answer")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent")},
	}
	got := safeCutoff(history, 2)
	if got != 2 {
		t.Fatalf("safeCutoff = %d, want 2 (no adjustment needed)", got)
	}
}

func TestSafeCutoff_WalksToZero_ReturnsNegative(t *testing.T) {
	// All turns after index 0 are TurnTool — walking back from any cutoff
	// should reach 0, which is not a safe position.
	history := []schema.Turn{
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c1", "read_file", "content", false)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c2", "read_file", "content", false)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c3", "read_file", "content", false)},
	}
	got := safeCutoff(history, 2)
	if got != -1 {
		t.Fatalf("safeCutoff = %d, want -1 (no safe position)", got)
	}
}

func TestSafeCutoff_SkipsSteering(t *testing.T) {
	// TurnSteering at the cutoff position should be walked back, just like TurnTool.
	// Otherwise, preserved turns would start with a steering message that becomes
	// a consecutive user-role message after the checkpoint's user-role message.
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("answer")},
		{Kind: schema.TurnSteering, Message: llm.User("you should do X")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent")},
	}
	// preserveRecent=1 → cutoff = len(history)-1 = 3.
	// history[3] is TurnAssistant (OK), so normally no adjustment.
	// But if cutoff = len(history)-2 = 2, history[2] is TurnSteering → walk to 1.
	got := safeCutoff(history, 2)
	// cutoff=2 → TurnSteering → walk back to 1 → TurnAssistant → stop.
	if got != 1 {
		t.Fatalf("safeCutoff = %d, want 1 (should skip TurnSteering)", got)
	}
}

func TestCheckpoint_SafeCutoffNegative_ReturnsUnchanged(t *testing.T) {
	// When safeCutoff returns -1, checkpoint should return history unchanged.
	// Use preserveRecent=3 with 4 turns so cutoff=1, and history[1] is TurnTool
	// which walks back to 0 → return -1.
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c1", "read_file", "content", false)},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("answer")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent")},
	}
	// preserveRecent=3 → cutoff=1 → TurnTool → walk to 0 → return -1
	result := checkpoint(history, 3, nil, "communicate")
	if len(result) != len(history) {
		t.Fatalf("expected unchanged history (len %d), got len %d", len(history), len(result))
	}
}

func TestSummarizeWithLLM_SafeCutoffNegative_ReturnsUnchanged(t *testing.T) {
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				t.Fatal("LLM should not be called when safeCutoff returns -1")
				return llm.Response{}
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	cm := NewManager(NewOpenAIProfile("gpt-5.2"), client)

	// Same scenario as checkpoint test: cutoff walks to 0 → return -1.
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c1", "read_file", "content", false)},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("answer")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent")},
	}
	result, err := cm.summarizeWithLLM(context.Background(), history, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != len(history) {
		t.Fatalf("expected unchanged history (len %d), got len %d", len(history), len(result))
	}
}

// --- Fix: Short result masking (M2) ---

func TestMaskObservations_SkipsShortResults(t *testing.T) {
	// For short tool results like "OK", the summary "[edit_file: auth.go → OK]"
	// is longer than the original content. Masking should be skipped to avoid
	// increasing pressure.
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: assistantWithToolCall("c1", "edit_file", `{"file_path":"auth.go"}`)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c1", "edit_file", "OK", false)},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("done")},
	}

	maskObservations(history, 0, "communicate")

	// "OK" (2 chars) should not be replaced with "[edit_file: auth.go → OK]" (25 chars).
	got := toolResultContent(history[2])
	if got != "OK" {
		t.Fatalf("short result should not be masked, got: %q", got)
	}
}

// --- Fix: Checkpoint prompt extraction (M1) ---

func TestCheckpoint_OriginalPromptNotOverriddenByFollowup(t *testing.T) {
	// After a previous checkpoint, history starts with the checkpoint message
	// (which embeds "Original task: Fix the auth bug"). A follow-up user message
	// "Also update the tests" appears in the preserved recent turns.
	// The second checkpoint should preserve "Fix the auth bug" from the first
	// checkpoint AND include the follow-up in user messages.
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("[CONTEXT CHECKPOINT]\nOriginal task: Fix the auth bug\nFiles modified: auth.go\n[END CHECKPOINT]\n")},
		{Kind: schema.TurnAssistant, Message: assistantWithToolCall("c1", "read_file", `{"file_path":"auth.go"}`)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c1", "read_file", "content", false)},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("I've analyzed the code")},
		// Follow-up user message in preserved region:
		{Kind: schema.TurnUserInput, Message: llm.User("Also update the tests")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("Will do")},
	}

	// preserveRecent=2 → cutoff=4 → history[:4] is compacted.
	result := checkpoint(history, 2, nil, "communicate")
	text := result[0].Message.Text()

	// The original prompt from the prior checkpoint should be preserved as a user message.
	if !strings.Contains(text, "Fix the auth bug") {
		t.Fatalf("checkpoint should preserve user messages from prior checkpoint:\n%s", text)
	}
}

// --- Fix: Pressure cascade (C1) ---

// TestMaybeCompact_SummarizeThreshold verifies summarize fires through the orchestrator.
func TestMaybeCompact_SummarizeThreshold(t *testing.T) {
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("Summary: tests were run and passed")}
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	// Small window. Checkpoint replaces old history with a checkpoint
	// message, but the preserved recent turns are large enough to keep
	// pressure above 90% after checkpoint, forcing summarize.
	profile := testProfile("openai", "test", 50)
	cm := NewManager(profile, client)
	cm.PreserveRecentTurns = 1

	// After checkpoint, result = [checkpoint_msg, recent_turn].
	// The recent turn alone needs to be ~90% of 50 tokens = 45 tokens = ~180 chars.
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User(strings.Repeat("task ", 50))},
		{Kind: schema.TurnAssistant, Message: llm.Assistant(strings.Repeat("work ", 30))},
		{Kind: schema.TurnAssistant, Message: llm.Assistant(strings.Repeat("recent content ", 15))},
	}

	var layers []string
	emitFn := func(kind events.EventKind, data events.EventData) {
		if kind == events.EventContextCompaction {
			if cd, ok := data.(events.ContextCompactionData); ok {
				layers = append(layers, cd.Layer)
			}
		}
	}

	cm.MaybeCompact(context.Background(), &history, 0, emitFn)

	foundSummarize := false
	for _, l := range layers {
		if l == "summarize" {
			foundSummarize = true
		}
	}
	if !foundSummarize {
		t.Fatalf("expected summarize layer; got %v", layers)
	}

	// First turn should be the LLM summary.
	text := history[0].Message.Text()
	if !strings.Contains(text, "[CONTEXT SUMMARY]") {
		t.Fatalf("expected summary in first turn, got: %q", text)
	}
}

// --- helpers ---

// errorAdapter always returns an error from Complete.
type errorAdapter struct {
	name string
}

func (a *errorAdapter) Name() string { return a.name }
func (a *errorAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("simulated LLM error")
}
func (a *errorAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func assistantWithToolCall(id, name, argsJSON string) llm.Message {
	return llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.ContentPart{
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
				ID:        id,
				Name:      name,
				Arguments: json.RawMessage(argsJSON),
			}},
		},
	}
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// --- Pressure (public) tests ---

func TestPressure_ReturnsEstimate(t *testing.T) {
	profile := testProfile("openai", "test", 1000)
	cm := NewManager(profile, nil)

	history := []schema.Turn{
		{Kind: schema.TurnAssistant, Message: llm.Assistant(strings.Repeat("x", 400))},
	}

	p := cm.Pressure(history, 0)
	// 400 chars / 4 = 100 tokens, 100/1000 = 0.10
	if p < 0.09 || p > 0.11 {
		t.Fatalf("Pressure() = %.2f, want ~0.10", p)
	}
}

func TestPressure_ZeroContextWindow(t *testing.T) {
	profile := &provider.Profile{} // zero value reports ContextWindowSize() == 0
	cm := NewManager(profile, nil)

	p := cm.Pressure(nil, 0)
	if p != 0 {
		t.Fatalf("Pressure() = %.2f, want 0 for zero context window", p)
	}
}

// --- SetProfile tests ---

func TestContextManager_SetProfile_UpdatesContextWindow(t *testing.T) {
	// Start with a 200K profile, switch to 1M. Pressure should reflect new window.
	smallProfile := testProfile("anthropic", "claude-opus-4-6", 200_000)
	cm := NewManager(smallProfile, nil)

	// Record 100K tokens.
	cm.RecordInputTokens(100_000, 5)
	history := make([]schema.Turn, 5)
	for i := range history {
		history[i] = schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant("x")}
	}

	// With 200K window: pressure ≈ 100K/200K = 0.50
	p1 := cm.estimatePressure(history, 0)
	if p1 < 0.45 || p1 > 0.55 {
		t.Fatalf("pressure before SetProfile = %.2f, expected ~0.50", p1)
	}

	// Switch to 1M profile.
	bigProfile := testProfile("anthropic", "claude-opus-4-6[1m]", 1_000_000)
	cm.SetProfile(bigProfile)

	// With 1M window: pressure ≈ 100K/1M = 0.10
	p2 := cm.estimatePressure(history, 0)
	if p2 < 0.08 || p2 > 0.12 {
		t.Fatalf("pressure after SetProfile = %.2f, expected ~0.10", p2)
	}
}

// --- ForceCompact tests ---

func TestForceCompact_RunsAllLayers(t *testing.T) {
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY] forced")}
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	profile := testProfile("openai", "test", 100_000)
	cm := NewManager(profile, client)
	cm.PreserveRecentTurns = 2

	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("first question")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("working on it")),
		schema.NewTurn(schema.TurnUserInput, llm.User("second question")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent1")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent2")),
	}

	var layers []string
	emitFn := func(kind events.EventKind, data events.EventData) {
		if kind == events.EventContextCompaction {
			if d, ok := data.(events.ContextCompactionData); ok {
				layers = append(layers, d.Layer)
			}
		}
	}

	cm.ForceCompact(context.Background(), &history, emitFn)

	// Both layers should fire: checkpoint then summarize.
	if len(layers) != 2 {
		t.Fatalf("expected 2 layers to fire, got %d: %v", len(layers), layers)
	}
	expected := []string{"checkpoint", "summarize"}
	for i, want := range expected {
		if layers[i] != want {
			t.Errorf("layer[%d] = %q, want %q (all: %v)", i, layers[i], want, layers)
		}
	}
}

func TestForceCompact_FiresOnCompactionTurn_Checkpoint(t *testing.T) {
	// ForceCompact should fire OnCompactionTurn for TurnCheckpoint (L3).
	profile := testProfile("openai", "test", 100_000)
	cm := NewManager(profile, nil) // no LLM client → L4 skipped
	cm.PreserveRecentTurns = 2

	var callbackTurns []schema.TurnKind
	cm.OnCompactionTurn = func(t schema.Turn) {
		callbackTurns = append(callbackTurns, t.Kind)
	}

	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("fix the bug")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("I'll fix it")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("analysis done")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent1")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent2")),
	}

	emitFn := func(kind events.EventKind, data events.EventData) {}
	cm.ForceCompact(context.Background(), &history, emitFn)

	// L3 creates a checkpoint turn. OnCompactionTurn should have been called.
	found := false
	for _, k := range callbackTurns {
		if k == schema.TurnCheckpoint {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected OnCompactionTurn to be called with TurnCheckpoint; got %v", callbackTurns)
	}
}

func TestForceCompact_FiresOnCompactionTurn_Summary(t *testing.T) {
	// ForceCompact should fire OnCompactionTurn for TurnSummary (L4).
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY] forced summary")}
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	profile := testProfile("openai", "test", 100_000)
	cm := NewManager(profile, client)
	cm.PreserveRecentTurns = 2

	var callbackTurns []schema.TurnKind
	cm.OnCompactionTurn = func(t schema.Turn) {
		callbackTurns = append(callbackTurns, t.Kind)
	}

	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("fix the bug")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("I'll fix it")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("analysis done")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent1")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent2")),
	}

	emitFn := func(kind events.EventKind, data events.EventData) {}
	cm.ForceCompact(context.Background(), &history, emitFn)

	// Both L3 (checkpoint) and L4 (summary) should fire callbacks.
	foundCheckpoint, foundSummary := false, false
	for _, k := range callbackTurns {
		if k == schema.TurnCheckpoint {
			foundCheckpoint = true
		}
		if k == schema.TurnSummary {
			foundSummary = true
		}
	}
	if !foundCheckpoint {
		t.Fatalf("expected OnCompactionTurn for TurnCheckpoint; got %v", callbackTurns)
	}
	if !foundSummary {
		t.Fatalf("expected OnCompactionTurn for TurnSummary; got %v", callbackTurns)
	}
}

func TestForceCompact_BelowThreshold(t *testing.T) {
	// Even with tiny history well below auto-compact thresholds,
	// ForceCompact should still run the layers.
	profile := testProfile("openai", "test", 1_000_000)
	cm := NewManager(profile, nil) // no LLM client → summarize skipped
	cm.PreserveRecentTurns = 2

	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("hi")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("working on it")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent1")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent2")),
	}

	var layers []string
	emitFn := func(kind events.EventKind, data events.EventData) {
		if kind == events.EventContextCompaction {
			if d, ok := data.(events.ContextCompactionData); ok {
				layers = append(layers, d.Layer)
			}
		}
	}

	cm.ForceCompact(context.Background(), &history, emitFn)

	// Checkpoint should fire. Summarize skipped (no client).
	if len(layers) != 1 || layers[0] != "checkpoint" {
		t.Fatalf("expected [checkpoint], got %v", layers)
	}
}

// --- Working notes in checkpoint ---

func TestCheckpoint_ExtractsWorkingNotes(t *testing.T) {
	// Assistant turns with text > 50 chars should be captured as working notes.
	longAnalysis := "After analyzing the codebase, I found that the auth module uses JWT tokens with RS256 signing"
	shortText := "ok"
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("fix the auth bug")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant(longAnalysis)},
		{Kind: schema.TurnAssistant, Message: llm.Assistant(shortText)},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent")},
	}

	result := checkpoint(history, 1, nil, "communicate")
	text := result[0].Message.Text()

	// Should contain a Markdown working notes section with the long analysis.
	if !strings.Contains(text, "## Working Notes") {
		t.Fatalf("checkpoint should contain markdown working notes:\n%s", text)
	}
	if strings.Contains(text, "<working_notes>") {
		t.Fatalf("checkpoint should not use XML/JSON working notes:\n%s", text)
	}
	if !strings.Contains(text, longAnalysis) {
		t.Fatalf("checkpoint should contain the long assistant analysis:\n%s", text)
	}
	// Short text should NOT appear in working notes.
	for _, note := range extractCheckpointWorkingNotes(text) {
		if note == shortText {
			t.Fatalf("working notes should not contain short assistant text %q:\n%v", shortText, note)
		}
	}
}

func TestCheckpoint_WorkingNotes_CappedAt500Chars(t *testing.T) {
	// A very long assistant text should be truncated to 500 chars in working notes.
	longText := strings.Repeat("analysis ", 100) // 900 chars
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant(longText)},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent")},
	}

	result := checkpoint(history, 1, nil, "communicate")
	text := result[0].Message.Text()

	notes := extractCheckpointWorkingNotes(text)
	if len(notes) == 0 {
		t.Fatalf("expected working notes in checkpoint:\n%s", text)
	}
	for _, note := range notes {
		if len(note) > 503 { // 500 + "..." suffix
			t.Fatalf("working note should be capped at ~500 chars, got %d", len(note))
		}
	}
}

func TestCheckpoint_WorkingNotes_SurviveCrossCompaction(t *testing.T) {
	// Working notes from a previous checkpoint should be preserved in the next one.
	firstCheckpoint := `[CONTEXT CHECKPOINT]
Files modified: auth.go

<user_messages>["fix the auth bug"]</user_messages>

<working_notes>["JWT tokens use RS256 signing and the key rotation is broken"]</working_notes>
[END CHECKPOINT]`

	history := []schema.Turn{
		{Kind: schema.TurnCheckpoint, Message: llm.User(firstCheckpoint)},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("I found the root cause in key_manager.go — the rotation interval is set to 0")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent")},
	}

	result := checkpoint(history, 1, nil, "communicate")
	text := result[0].Message.Text()

	// Both the old note and the new analysis should be present.
	if !strings.Contains(text, "JWT tokens use RS256") {
		t.Fatalf("old working note should survive cross-compaction:\n%s", text)
	}
	if !strings.Contains(text, "root cause in key_manager.go") {
		t.Fatalf("new working note should be included:\n%s", text)
	}
}

// --- Transcript-tool pointer in checkpoint ---

func TestCheckpoint_TranscriptPointer_WithSessionID(t *testing.T) {
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("do the work")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("done")},
	}
	meta := &CompactionMeta{SessionID: "01ABC"}
	result := checkpoint(history, 1, meta, "communicate")
	text := result[0].Message.Text()

	if !strings.Contains(text, "01ABC") {
		t.Errorf("checkpoint missing session id: %q", text)
	}
	if !strings.Contains(text, "read_session_transcript") {
		t.Errorf("checkpoint missing read_session_transcript tool reference: %q", text)
	}
	if !strings.Contains(text, "find_session_transcripts") {
		t.Errorf("checkpoint missing find_session_transcripts tool reference: %q", text)
	}
	if strings.Contains(text, "read_file") {
		t.Errorf("checkpoint should not reference read_file: %q", text)
	}
}

func TestCheckpoint_TranscriptPointer_EmptySessionID(t *testing.T) {
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("do the work")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("done")},
	}
	meta := &CompactionMeta{SessionID: ""}
	result := checkpoint(history, 1, meta, "communicate")
	text := result[0].Message.Text()

	if strings.Contains(text, "read_session_transcript") {
		t.Errorf("non-persistent checkpoint should not reference read_session_transcript: %q", text)
	}
	if strings.Contains(text, "read_file") {
		t.Errorf("non-persistent checkpoint should not reference read_file: %q", text)
	}
	if strings.Contains(text, "Full transcript:") {
		t.Errorf("non-persistent checkpoint should not have transcript pointer line: %q", text)
	}
}

func TestCheckpoint_WorkingNotes_ShedOldestFirst(t *testing.T) {
	// When over budget, oldest notes should be shed before user messages.
	// Build a history with many long assistant notes to exceed the 60k budget.
	// Each note ≈ 503 chars (capped at 500+...), need >120 to exceed 60k.
	var history []schema.Turn
	history = append(history, schema.Turn{Kind: schema.TurnUserInput, Message: llm.User("important task description")})
	for i := 0; i < 150; i++ {
		history = append(history, schema.Turn{
			Kind:    schema.TurnAssistant,
			Message: llm.Assistant(fmt.Sprintf("Note %03d: %s", i, strings.Repeat("detailed analysis ", 40))),
		})
	}
	history = append(history, schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant("recent")})

	result := checkpoint(history, 1, nil, "communicate")
	text := result[0].Message.Text()

	// User message should survive.
	if !strings.Contains(text, "important task description") {
		t.Fatalf("user message should survive budget shedding:\n%s", text)
	}

	// Working notes should exist but some should be shed (not all 50 can fit).
	notes := extractCheckpointWorkingNotes(text)
	if len(notes) == 0 {
		t.Fatalf("expected working notes in checkpoint:\n%s", text)
	}
	if len(notes) >= 150 {
		t.Fatalf("expected some notes to be shed, but all %d survived", len(notes))
	}
	// Latest notes should be preserved over oldest.
	lastNote := notes[len(notes)-1]
	if !strings.Contains(lastNote, "Note 149:") {
		t.Fatalf("latest note should be preserved, got: %q", lastNote)
	}
}
