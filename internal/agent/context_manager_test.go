package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/serf/internal/llm"
)

// --- Phase 1: Token tracking + estimation ---

func TestEstimateTokens_EmptyHistory(t *testing.T) {
	got := EstimateTokens(nil)
	if got != 0 {
		t.Fatalf("EstimateTokens(nil) = %d, want 0", got)
	}
	got = EstimateTokens([]Turn{})
	if got != 0 {
		t.Fatalf("EstimateTokens([]) = %d, want 0", got)
	}
}

func TestEstimateTokens_SingleUserTurn(t *testing.T) {
	text := "Hello, world! This is a test message."
	turns := []Turn{{Kind: TurnUserInput, Message: llm.User(text)}}
	got := EstimateTokens(turns)
	want := len(text) / 4
	if got != want {
		t.Fatalf("EstimateTokens = %d, want %d (len=%d)", got, want, len(text))
	}
}

func TestEstimateTokens_WithToolResults(t *testing.T) {
	content := "file contents here with lots of text"
	turns := []Turn{
		{Kind: TurnUserInput, Message: llm.User("read a file")},
		{Kind: TurnTool, Message: llm.ToolResultNamed("c1", "read_file", content, false)},
	}
	got := EstimateTokens(turns)
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
	turns := []Turn{
		{Kind: TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "let me think about this carefully"}},
				{Kind: llm.ContentText, Text: "answer"},
			},
		}},
	}
	got := EstimateTokens(turns)
	totalChars := len("let me think about this carefully") + len("answer")
	want := totalChars / 4
	if got != want {
		t.Fatalf("EstimateTokens = %d, want %d", got, want)
	}
}

func TestContextManager_AddUsage_Accumulates(t *testing.T) {
	cm := NewContextManager(NewOpenAIProfile("gpt-5.2"), nil)
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
	cm := NewContextManager(NewOpenAIProfile("gpt-5.2"), nil)

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
	got := summarizeToolResult("read_file", lines, false, json.RawMessage(`{"file_path":"auth.go"}`))
	want := "[read_file: auth.go, 2 lines]"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSummarizeToolResult_Shell_Success(t *testing.T) {
	output := "ok\nexit_code=0 duration_ms=42 timed_out=false\n"
	got := summarizeToolResult("shell", output, false, json.RawMessage(`{"command":"go test"}`))
	want := `[shell: "go test" → exit 0]`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSummarizeToolResult_Shell_Failure(t *testing.T) {
	output := "FAIL\nexit_code=1 duration_ms=42 timed_out=false\n"
	got := summarizeToolResult("shell", output, false, json.RawMessage(`{"command":"go test"}`))
	want := `[shell: "go test" → exit 1]`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSummarizeToolResult_Shell_LongCommand(t *testing.T) {
	longCmd := "this is a very long command that exceeds sixty characters in length and should be truncated"
	output := "ok\nexit_code=0 duration_ms=1 timed_out=false\n"
	got := summarizeToolResult("shell", output, false, json.RawMessage(`{"command":"`+longCmd+`"}`))
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
	got := summarizeToolResult("grep", output, false, json.RawMessage(`{"pattern":"TODO"}`))
	want := `[grep: "TODO" → 2 matches]`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSummarizeToolResult_Glob(t *testing.T) {
	output := "a.go\nb.go\nc.go\n"
	got := summarizeToolResult("glob", output, false, json.RawMessage(`{"pattern":"*.go"}`))
	want := `[glob: "*.go" → 3 files]`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSummarizeToolResult_EditFile(t *testing.T) {
	got := summarizeToolResult("edit_file", "OK", false, json.RawMessage(`{"file_path":"auth.go"}`))
	want := "[edit_file: auth.go → OK]"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSummarizeToolResult_EditFile_Error(t *testing.T) {
	got := summarizeToolResult("edit_file", "not found", true, json.RawMessage(`{"file_path":"auth.go"}`))
	want := "[edit_file: auth.go → error]"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSummarizeToolResult_WriteFile(t *testing.T) {
	got := summarizeToolResult("write_file", "OK", false, json.RawMessage(`{"file_path":"new.go"}`))
	want := "[write_file: new.go → OK]"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSummarizeToolResult_ApplyPatch(t *testing.T) {
	got := summarizeToolResult("apply_patch", "OK", false, json.RawMessage(`{"patch":"*** Begin Patch\n*** Update File: auth.go\n"}`))
	want := "[apply_patch → OK]"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSummarizeToolResult_ApplyPatch_Error(t *testing.T) {
	got := summarizeToolResult("apply_patch", "failed to apply", true, json.RawMessage(`{"patch":"bad"}`))
	want := "[apply_patch → error]"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSummarizeToolResult_WebFetch(t *testing.T) {
	content := "Here is some fetched content from the web page"
	got := summarizeToolResult("web_fetch", content, false, json.RawMessage(`{"url":"https://example.com"}`))
	want := fmt.Sprintf("[web_fetch: https://example.com → %d chars]", len(content))
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSummarizeToolResult_SpawnAgent(t *testing.T) {
	got := summarizeToolResult("spawn_agent", `{"agent_id":"abc123"}`, false, json.RawMessage(`{"task":"do stuff"}`))
	want := "[spawn_agent: abc123]"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSummarizeToolResult_TaskList(t *testing.T) {
	got := summarizeToolResult("task_list", `[{"id":1},{"id":2},{"id":3}]`, false, json.RawMessage(`{"action":"view"}`))
	want := "[task_list: view → 3 tasks]"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSummarizeToolResult_UseSkill(t *testing.T) {
	body := "This is the skill body with instructions"
	got := summarizeToolResult("use_skill", body, false, json.RawMessage(`{"skill_name":"tdd"}`))
	want := fmt.Sprintf("[use_skill: tdd → %d chars]", len(body))
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSummarizeToolResult_UnknownTool(t *testing.T) {
	content := "some output"
	got := summarizeToolResult("custom_tool", content, false, json.RawMessage(`{}`))
	want := "[custom_tool: 11 chars]"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestMaskObservations_PreservesRecentTurns(t *testing.T) {
	// 4 turns: 2 old tool results + 2 recent. With preserveRecent=2, only the first 2 should be masked.
	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("task")},
		{Kind: TurnAssistant, Message: assistantWithToolCall("c1", "read_file", `{"file_path":"a.go"}`)},
		{Kind: TurnTool, Message: llm.ToolResultNamed("c1", "read_file", "1 | line1\n2 | line2\n", false)},
		{Kind: TurnAssistant, Message: assistantWithToolCall("c2", "read_file", `{"file_path":"b.go"}`)},
		{Kind: TurnTool, Message: llm.ToolResultNamed("c2", "read_file", "1 | stuff\n", false)},
		{Kind: TurnAssistant, Message: llm.Assistant("done")},
	}

	freed := maskObservations(history, 2)

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

	// freed may be small or even negative for tiny test data; just verify masking occurred.
	_ = freed
}

func TestMaskObservations_SkipsAlreadyMasked(t *testing.T) {
	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("task")},
		{Kind: TurnTool, Message: llm.ToolResultNamed("c1", "read_file", "[read_file: a.go, 10 lines]", false)},
		{Kind: TurnAssistant, Message: llm.Assistant("done")},
	}

	freed := maskObservations(history, 0)
	got := toolResultContent(history[1])
	if got != "[read_file: a.go, 10 lines]" {
		t.Fatalf("already-masked result should be unchanged, got: %q", got)
	}
	if freed != 0 {
		t.Fatalf("expected 0 freed tokens for already-masked, got %d", freed)
	}
}

func TestMaskObservations_SkipsErrorResults(t *testing.T) {
	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("task")},
		{Kind: TurnTool, Message: llm.ToolResultNamed("c1", "shell", "command not found\nexit_code=127 duration_ms=1 timed_out=false\n", true)},
		{Kind: TurnAssistant, Message: llm.Assistant("done")},
	}

	freed := maskObservations(history, 0)
	got := toolResultContent(history[1])
	if startsWith(got, "[shell:") {
		t.Fatalf("error result should NOT be masked, got: %q", got)
	}
	if freed != 0 {
		t.Fatalf("expected 0 freed for error results, got %d", freed)
	}
}

func TestMaskObservations_PreservesCommunicate(t *testing.T) {
	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("task")},
		{Kind: TurnTool, Message: llm.ToolResultNamed("c1", "communicate", `{"delivered":true,"inbox":[]}`, false)},
		{Kind: TurnAssistant, Message: llm.Assistant("done")},
	}

	freed := maskObservations(history, 0)
	got := toolResultContent(history[1])
	if startsWith(got, "[communicate:") {
		t.Fatalf("communicate result should NOT be masked, got: %q", got)
	}
	if freed != 0 {
		t.Fatalf("expected 0 freed for communicate, got %d", freed)
	}
}

func TestMaskObservations_EmptyHistory(t *testing.T) {
	freed := maskObservations(nil, 6)
	if freed != 0 {
		t.Fatalf("expected 0 freed for nil history, got %d", freed)
	}
	freed = maskObservations([]Turn{}, 6)
	if freed != 0 {
		t.Fatalf("expected 0 freed for empty history, got %d", freed)
	}
}

func TestMaskObservations_PreservesAssistantTurns(t *testing.T) {
	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("task")},
		{Kind: TurnAssistant, Message: llm.Assistant("thinking about it")},
		{Kind: TurnTool, Message: llm.ToolResultNamed("c1", "read_file", "1 | content\n", false)},
		{Kind: TurnAssistant, Message: llm.Assistant("done")},
	}

	maskObservations(history, 0)
	// Assistant turn text should be unchanged.
	if history[1].Message.Text() != "thinking about it" {
		t.Fatalf("assistant turn text should be preserved, got: %q", history[1].Message.Text())
	}
}

// --- Phase 3: Thinking clearing ---

func TestClearThinking_RemovesOldThinkingText(t *testing.T) {
	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("task")},
		{Kind: TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "long reasoning about the problem"}},
				{Kind: llm.ContentText, Text: "my answer"},
			},
		}},
		{Kind: TurnAssistant, Message: llm.Assistant("final answer")},
	}

	freed := clearThinking(history, 1)

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

	if freed <= 0 {
		t.Fatalf("expected positive freed tokens, got %d", freed)
	}
}

func TestClearThinking_PreservesRecentThinking(t *testing.T) {
	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("task")},
		{Kind: TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "recent thinking"}},
				{Kind: llm.ContentText, Text: "answer"},
			},
		}},
	}

	freed := clearThinking(history, 2)

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
	if freed != 0 {
		t.Fatalf("expected 0 freed for recent turns, got %d", freed)
	}
}

func TestClearThinking_PreservesRedactedThinking(t *testing.T) {
	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("task")},
		{Kind: TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentRedThinking, Thinking: &llm.ThinkingData{
					Text:     "",
					Redacted: true,
				}},
				{Kind: llm.ContentText, Text: "answer"},
			},
		}},
		{Kind: TurnAssistant, Message: llm.Assistant("final")},
	}

	freed := clearThinking(history, 0)

	// Redacted thinking should remain untouched.
	for _, p := range history[1].Message.Content {
		if p.Kind == llm.ContentRedThinking && p.Thinking != nil {
			if p.Thinking.Redacted != true {
				t.Fatalf("redacted thinking should be preserved")
			}
		}
	}
	if freed != 0 {
		t.Fatalf("expected 0 freed for redacted thinking, got %d", freed)
	}
}

func TestClearThinking_NoThinkingBlocks(t *testing.T) {
	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("task")},
		{Kind: TurnAssistant, Message: llm.Assistant("answer")},
	}

	freed := clearThinking(history, 0)
	if freed != 0 {
		t.Fatalf("expected 0 freed for no thinking blocks, got %d", freed)
	}
}

func TestClearThinking_MixedContent(t *testing.T) {
	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("task")},
		{Kind: TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "thought one"}},
				{Kind: llm.ContentText, Text: "text one"},
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "c1", Name: "read_file", Arguments: json.RawMessage(`{"file_path":"a.go"}`)}},
			},
		}},
		{Kind: TurnTool, Message: llm.ToolResultNamed("c1", "read_file", "content", false)},
		{Kind: TurnAssistant, Message: llm.Assistant("done")},
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
	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("Fix the auth bug in login.go")},
		{Kind: TurnAssistant, Message: assistantWithToolCall("c1", "read_file", `{"file_path":"login.go"}`)},
		{Kind: TurnTool, Message: llm.ToolResultNamed("c1", "read_file", "1 | package main\n", false)},
		{Kind: TurnAssistant, Message: assistantWithToolCall("c2", "edit_file", `{"file_path":"login.go","old_string":"old","new_string":"new"}`)},
		{Kind: TurnTool, Message: llm.ToolResultNamed("c2", "edit_file", "OK", false)},
		{Kind: TurnAssistant, Message: llm.Assistant("done")},
	}

	result := checkpoint(history, 2)

	// Should have checkpoint message + 2 preserved recent turns.
	if len(result) != 3 {
		t.Fatalf("expected 3 turns, got %d", len(result))
	}
	// First turn should be the checkpoint.
	if result[0].Kind != TurnUserInput {
		t.Fatalf("checkpoint should be TurnUserInput, got %s", result[0].Kind)
	}
	text := result[0].Message.Text()
	if !strings.Contains(text, "[CONTEXT CHECKPOINT]") {
		t.Fatalf("checkpoint missing header: %q", text)
	}
	if !strings.Contains(text, "[END CHECKPOINT]") {
		t.Fatalf("checkpoint missing footer: %q", text)
	}
}

func TestCheckpoint_IncludesOriginalTask(t *testing.T) {
	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("Fix the auth bug in login.go")},
		{Kind: TurnAssistant, Message: llm.Assistant("on it")},
		{Kind: TurnAssistant, Message: llm.Assistant("done")},
	}

	result := checkpoint(history, 1)
	text := result[0].Message.Text()
	if !strings.Contains(text, "Fix the auth bug in login.go") {
		t.Fatalf("checkpoint missing original task: %q", text)
	}
}

func TestCheckpoint_TracksModifiedFiles(t *testing.T) {
	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("task")},
		{Kind: TurnAssistant, Message: assistantWithToolCall("c1", "edit_file", `{"file_path":"auth.go"}`)},
		{Kind: TurnTool, Message: llm.ToolResultNamed("c1", "edit_file", "OK", false)},
		{Kind: TurnAssistant, Message: assistantWithToolCall("c2", "write_file", `{"file_path":"user.go"}`)},
		{Kind: TurnTool, Message: llm.ToolResultNamed("c2", "write_file", "OK", false)},
		{Kind: TurnAssistant, Message: assistantWithToolCall("c3", "apply_patch", `{"patch":"*** Begin Patch\n*** Update File: test.go\n*** End Patch"}`)},
		{Kind: TurnTool, Message: llm.ToolResultNamed("c3", "apply_patch", "OK", false)},
		{Kind: TurnAssistant, Message: llm.Assistant("done")},
	}

	result := checkpoint(history, 1)
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
	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("task")},
		{Kind: TurnAssistant, Message: assistantWithToolCall("c1", "read_file", `{"file_path":"a.go"}`)},
		{Kind: TurnTool, Message: llm.ToolResultNamed("c1", "read_file", "content", false)},
		{Kind: TurnAssistant, Message: assistantWithToolCall("c2", "read_file", `{"file_path":"b.go"}`)},
		{Kind: TurnTool, Message: llm.ToolResultNamed("c2", "read_file", "content", false)},
		{Kind: TurnAssistant, Message: assistantWithToolCall("c3", "shell", `{"command":"go test"}`)},
		{Kind: TurnTool, Message: llm.ToolResultNamed("c3", "shell", "ok\nexit_code=0 duration_ms=1 timed_out=false\n", false)},
		{Kind: TurnAssistant, Message: llm.Assistant("done")},
	}

	result := checkpoint(history, 1)
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
	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("task")},
		{Kind: TurnAssistant, Message: llm.Assistant("old answer")},
		{Kind: TurnAssistant, Message: llm.Assistant("recent1")},
		{Kind: TurnAssistant, Message: llm.Assistant("recent2")},
	}

	result := checkpoint(history, 2)
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
	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("task")},
		{Kind: TurnAssistant, Message: llm.Assistant("answer")},
	}

	result := checkpoint(history, 6)
	if len(result) != len(history) {
		t.Fatalf("expected unchanged history length %d, got %d", len(history), len(result))
	}
}

// --- helpers ---

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
