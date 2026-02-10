package agent

import (
	"encoding/json"
	"fmt"
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

func toolResultContent(t Turn) string {
	for _, p := range t.Message.Content {
		if p.Kind == llm.ContentToolResult && p.ToolResult != nil {
			if s, ok := p.ToolResult.Content.(string); ok {
				return s
			}
		}
	}
	return ""
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
