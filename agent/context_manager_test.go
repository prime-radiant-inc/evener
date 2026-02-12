package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/serf/llm"
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

func TestSummarizeToolResult_SpawnAgent(t *testing.T) {
	got := summarizeToolResult("spawn_agent", `{"agent_id":"abc123"}`, json.RawMessage(`{"task":"do stuff"}`))
	want := "[spawn_agent: abc123]"
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
	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("task")},
		{Kind: TurnAssistant, Message: assistantWithToolCall("c1", "read_file", `{"file_path":"a.go"}`)},
		{Kind: TurnTool, Message: llm.ToolResultNamed("c1", "read_file", bigContent, false)},
		{Kind: TurnAssistant, Message: assistantWithToolCall("c2", "read_file", `{"file_path":"b.go"}`)},
		{Kind: TurnTool, Message: llm.ToolResultNamed("c2", "read_file", bigContent, false)},
		{Kind: TurnAssistant, Message: llm.Assistant("done")},
	}

	maskObservations(history, 2)

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
	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("task")},
		{Kind: TurnTool, Message: llm.ToolResultNamed("c1", "read_file", "[read_file: a.go, 10 lines]", false)},
		{Kind: TurnAssistant, Message: llm.Assistant("done")},
	}

	maskObservations(history, 0)
	got := toolResultContent(history[1])
	if got != "[read_file: a.go, 10 lines]" {
		t.Fatalf("already-masked result should be unchanged, got: %q", got)
	}
}

func TestMaskObservations_SkipsErrorResults(t *testing.T) {
	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("task")},
		{Kind: TurnTool, Message: llm.ToolResultNamed("c1", "shell", "command not found\nexit_code=127 duration_ms=1 timed_out=false\n", true)},
		{Kind: TurnAssistant, Message: llm.Assistant("done")},
	}

	maskObservations(history, 0)
	got := toolResultContent(history[1])
	if startsWith(got, "[shell:") {
		t.Fatalf("error result should NOT be masked, got: %q", got)
	}
}

func TestMaskObservations_PreservesCommunicate(t *testing.T) {
	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("task")},
		{Kind: TurnTool, Message: llm.ToolResultNamed("c1", "communicate", `{"delivered":true,"inbox":[]}`, false)},
		{Kind: TurnAssistant, Message: llm.Assistant("done")},
	}

	maskObservations(history, 0)
	got := toolResultContent(history[1])
	if startsWith(got, "[communicate:") {
		t.Fatalf("communicate result should NOT be masked, got: %q", got)
	}
}

func TestMaskObservations_EmptyHistory(t *testing.T) {
	maskObservations(nil, 6)
	maskObservations([]Turn{}, 6)
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
	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("task")},
		{Kind: TurnAssistant, Message: llm.Assistant("answer")},
	}

	clearThinking(history, 0)
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

	// Should have checkpoint message + preserved turns.
	// safeCutoff may back up the cutoff to avoid orphaned TurnTool, so we
	// may get more than 2 preserved turns.
	if len(result) < 3 {
		t.Fatalf("expected at least 3 turns, got %d", len(result))
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

// --- Phase 5: LLM summarization ---

func TestSummarizeWithLLM_CallsCheapModel(t *testing.T) {
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				// Verify the model is the cheap model.
				if req.Model != "gpt-4.1-nano" {
					t.Errorf("expected cheap model gpt-4.1-nano, got %q", req.Model)
				}
				// Verify the prompt asks for summarization.
				found := false
				for _, m := range req.Messages {
					if strings.Contains(m.Text(), "Summarize") || strings.Contains(m.Text(), "summarize") {
						found = true
					}
				}
				if !found {
					t.Error("expected summarization prompt")
				}
				return llm.Response{Message: llm.Assistant("Summary: fixed auth bug")}
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	cm := NewContextManager(NewOpenAIProfile("gpt-5.2"), client)

	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("Fix the auth bug")},
		{Kind: TurnAssistant, Message: llm.Assistant("I'll fix it")},
		{Kind: TurnAssistant, Message: llm.Assistant("recent1")},
		{Kind: TurnAssistant, Message: llm.Assistant("recent2")},
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

	cm := NewContextManager(NewOpenAIProfile("gpt-5.2"), client)

	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("task")},
		{Kind: TurnAssistant, Message: llm.Assistant("step 1")},
		{Kind: TurnAssistant, Message: llm.Assistant("step 2")},
		{Kind: TurnAssistant, Message: llm.Assistant("recent")},
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

	cm := NewContextManager(NewOpenAIProfile("gpt-5.2"), client)

	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("task")},
		{Kind: TurnAssistant, Message: llm.Assistant("old")},
		{Kind: TurnAssistant, Message: llm.Assistant("recent1")},
		{Kind: TurnAssistant, Message: llm.Assistant("recent2")},
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

	cm := NewContextManager(NewOpenAIProfile("gpt-5.2"), client)

	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("task")},
		{Kind: TurnAssistant, Message: llm.Assistant("step 1")},
		{Kind: TurnAssistant, Message: llm.Assistant("recent")},
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
func makeBigHistory(targetTokens int) []Turn {
	turns := []Turn{{Kind: TurnUserInput, Message: llm.User("Fix the auth bug")}}
	for EstimateTokens(turns) < targetTokens {
		id := fmt.Sprintf("c%d", len(turns))
		turns = append(turns,
			Turn{Kind: TurnAssistant, Message: assistantWithToolCall(id, "read_file", `{"file_path":"file.go"}`)},
			Turn{Kind: TurnTool, Message: llm.ToolResultNamed(id, "read_file", strings.Repeat("x", 400), false)},
		)
	}
	return turns
}

func TestMaybeCompact_BelowThreshold_NoAction(t *testing.T) {
	cm := NewContextManager(NewOpenAIProfile("gpt-5.2"), nil)

	// Small history, well below any threshold.
	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("task")},
		{Kind: TurnAssistant, Message: llm.Assistant("ok")},
	}

	var events []SessionEvent
	emitFn := func(kind EventKind, data any) {
		events = append(events, SessionEvent{Kind: kind, Data: data})
	}

	cm.MaybeCompact(context.Background(), &history, 100, emitFn)
	// No compaction events should have been emitted.
	for _, e := range events {
		if e.Kind == EventContextCompaction {
			t.Fatalf("unexpected CONTEXT_COMPACTION event below threshold")
		}
	}
	// History should be unchanged.
	if len(history) != 2 {
		t.Fatalf("expected 2 turns, got %d", len(history))
	}
}

func TestMaybeCompact_ObservationMaskThreshold(t *testing.T) {
	// Use a tiny context window to make it easy to hit thresholds.
	profile := &baseProfile{id: "openai", model: "test", contextWindow: 1000}
	cm := NewContextManager(profile, nil)
	cm.PreserveRecentTurns = 2

	// Create history that fills ~65% of 1000 tokens = 650 tokens.
	history := makeBigHistory(650)
	// Add 2 recent turns that won't be touched.
	history = append(history,
		Turn{Kind: TurnAssistant, Message: llm.Assistant("recent1")},
		Turn{Kind: TurnAssistant, Message: llm.Assistant("recent2")},
	)

	var events []SessionEvent
	emitFn := func(kind EventKind, data any) {
		events = append(events, SessionEvent{Kind: kind, Data: data})
	}

	cm.MaybeCompact(context.Background(), &history, 0, emitFn)

	// Should have emitted at least an observation_mask event.
	foundMask := false
	for _, e := range events {
		if e.Kind == EventContextCompaction {
			if layer, ok := e.DataMap()["layer"].(string); ok && layer == "observation_mask" {
				foundMask = true
			}
		}
	}
	if !foundMask {
		t.Fatalf("expected observation_mask compaction event; got events: %+v", events)
	}
}

func TestMaybeCompact_CheckpointThreshold(t *testing.T) {
	profile := &baseProfile{id: "openai", model: "test", contextWindow: 500}
	cm := NewContextManager(profile, nil)
	cm.PreserveRecentTurns = 2

	// Use assistant text (not tool results) so observation masking can't reduce pressure.
	// Each assistant turn ~400 chars = 100 tokens. Need 85% of 500 = 425 tokens.
	history := []Turn{{Kind: TurnUserInput, Message: llm.User("Fix the auth bug")}}
	for EstimateTokens(history) < 425 {
		history = append(history,
			Turn{Kind: TurnAssistant, Message: llm.Assistant(strings.Repeat("analysis ", 50))},
		)
	}
	history = append(history,
		Turn{Kind: TurnAssistant, Message: llm.Assistant("recent1")},
		Turn{Kind: TurnAssistant, Message: llm.Assistant("recent2")},
	)

	var events []SessionEvent
	emitFn := func(kind EventKind, data any) {
		events = append(events, SessionEvent{Kind: kind, Data: data})
	}

	cm.MaybeCompact(context.Background(), &history, 0, emitFn)

	// At 85%, observation mask and thinking clear won't help (no tool results or thinking).
	// So checkpoint should trigger.
	foundCheckpoint := false
	for _, e := range events {
		if e.Kind == EventContextCompaction {
			if layer, ok := e.DataMap()["layer"].(string); ok && layer == "checkpoint" {
				foundCheckpoint = true
			}
		}
	}
	if !foundCheckpoint {
		t.Fatalf("expected checkpoint compaction event; got events: %+v", events)
	}

	// History should have been replaced with checkpoint + recent.
	if len(history) > 5 {
		t.Fatalf("expected compacted history, got %d turns", len(history))
	}
}

func TestMaybeCompact_EmitsEvents(t *testing.T) {
	profile := &baseProfile{id: "openai", model: "test", contextWindow: 500}
	cm := NewContextManager(profile, nil)
	cm.PreserveRecentTurns = 2

	// Fill ~75% = 375 tokens.
	history := makeBigHistory(375)
	history = append(history,
		Turn{Kind: TurnAssistant, Message: llm.Assistant("recent1")},
		Turn{Kind: TurnAssistant, Message: llm.Assistant("recent2")},
	)

	var events []SessionEvent
	emitFn := func(kind EventKind, data any) {
		events = append(events, SessionEvent{Kind: kind, Data: data})
	}

	cm.MaybeCompact(context.Background(), &history, 0, emitFn)

	// Should have at least one compaction event.
	compactionCount := 0
	for _, e := range events {
		if e.Kind == EventContextCompaction {
			compactionCount++
			// Each event should have layer and token counts.
			if _, ok := e.DataMap()["layer"]; !ok {
				t.Fatalf("compaction event missing 'layer': %+v", e.Data)
			}
			if _, ok := e.DataMap()["est_tokens_before"]; !ok {
				t.Fatalf("compaction event missing 'est_tokens_before': %+v", e.Data)
			}
		}
	}
	if compactionCount == 0 {
		t.Fatalf("expected compaction events")
	}
}

func TestMaybeCompact_RespectsSysPromptSize(t *testing.T) {
	profile := &baseProfile{id: "openai", model: "test", contextWindow: 1000}
	cm := NewContextManager(profile, nil)
	cm.PreserveRecentTurns = 2

	// Small history, but giant system prompt.
	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("task")},
		{Kind: TurnAssistant, Message: assistantWithToolCall("c1", "read_file", `{"file_path":"a.go"}`)},
		{Kind: TurnTool, Message: llm.ToolResultNamed("c1", "read_file", strings.Repeat("x", 400), false)},
		{Kind: TurnAssistant, Message: llm.Assistant("recent1")},
		{Kind: TurnAssistant, Message: llm.Assistant("recent2")},
	}

	var events []SessionEvent
	emitFn := func(kind EventKind, data any) {
		events = append(events, SessionEvent{Kind: kind, Data: data})
	}

	// sys prompt is 2400 chars ≈ 600 tokens + history ~100 tokens = 700/1000 = 70%
	cm.MaybeCompact(context.Background(), &history, 2400, emitFn)

	// With sys prompt, we're at ~70%, should trigger observation masking.
	foundMask := false
	for _, e := range events {
		if e.Kind == EventContextCompaction {
			if layer, ok := e.DataMap()["layer"].(string); ok && layer == "observation_mask" {
				foundMask = true
			}
		}
	}
	if !foundMask {
		t.Fatalf("expected observation_mask compaction event when sys prompt pushes over threshold")
	}
}

// --- Phase 7: Wire into Session ---

func TestSession_ContextManager_Created(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("ok")} },
		},
	})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	if sess.contextMgr == nil {
		t.Fatal("expected contextMgr to be created")
	}
}

func TestSession_ContextManager_AccumulatesUsage(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	reasoningTokens := 10
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{
					Message: llm.Assistant("ok"),
					Usage: llm.Usage{
						InputTokens:     100,
						OutputTokens:    50,
						TotalTokens:     150,
						ReasoningTokens: &reasoningTokens,
					},
				}
			},
		},
	})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx := context.Background()
	_, err = sess.ProcessInput(ctx, "hi")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	usage := sess.contextMgr.CumulativeUsage()
	if usage.InputTokens != 100 {
		t.Fatalf("InputTokens = %d, want 100", usage.InputTokens)
	}
	if usage.OutputTokens != 50 {
		t.Fatalf("OutputTokens = %d, want 50", usage.OutputTokens)
	}
}

func TestSession_ContextManager_CompactsWhenNeeded(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	// Create an adapter that returns a tool call first, then "ok".
	callCount := 0
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// First round: return a read_file tool call with a big result.
			func(req llm.Request) llm.Response {
				callCount++
				return llm.Response{
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
								ID:        "c1",
								Name:      "read_file",
								Arguments: json.RawMessage(`{"file_path":"big.txt"}`),
							}},
						},
					},
				}
			},
			// Second round: just return text.
			func(req llm.Request) llm.Response {
				callCount++
				return llm.Response{Message: llm.Assistant("done")}
			},
		},
	})

	// Write a big file that will fill a small context window.
	bigContent := strings.Repeat("line of content\n", 200)
	env := NewLocalExecutionEnvironment(dir)
	if _, err := env.WriteFile("big.txt", bigContent); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Use a very small context window profile.
	profile := &baseProfile{
		id:            "openai",
		model:         "gpt-5.2",
		contextWindow: 500,
		basePrompt:    "You are a test agent.",
		toolDefs: []llm.ToolDefinition{
			defReadFile(),
			defCommunicate(),
		},
	}

	sess, err := NewSession(c, profile, env, SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Verify context manager is configured with the small window.
	if sess.contextMgr.profile.ContextWindowSize() != 500 {
		t.Fatalf("expected context window 500, got %d", sess.contextMgr.profile.ContextWindowSize())
	}

	ctx := context.Background()
	_, err = sess.ProcessInput(ctx, "read the file")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	if callCount != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", callCount)
	}
}

func TestSession_ContextManager_EmitsEvents(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	// Return a tool call that produces big output, then "done".
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
								ID:        "c1",
								Name:      "read_file",
								Arguments: json.RawMessage(`{"file_path":"big.txt"}`),
							}},
						},
					},
				}
			},
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("done")}
			},
		},
	})

	bigContent := strings.Repeat("line of content\n", 300)
	env := NewLocalExecutionEnvironment(dir)
	if _, err := env.WriteFile("big.txt", bigContent); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	profile := &baseProfile{
		id:            "openai",
		model:         "gpt-5.2",
		contextWindow: 500, // Tiny window to force compaction.
		basePrompt:    "Agent.",
		toolDefs: []llm.ToolDefinition{
			defReadFile(),
			defCommunicate(),
		},
	}

	sess, err := NewSession(c, profile, env, SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	var events []SessionEvent
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range sess.Events() {
			events = append(events, ev)
		}
	}()

	ctx := context.Background()
	_, err = sess.ProcessInput(ctx, "read the file")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()
	<-done

	foundCompaction := false
	for _, e := range events {
		if e.Kind == EventContextCompaction {
			foundCompaction = true
		}
	}
	if !foundCompaction {
		t.Fatal("expected CONTEXT_COMPACTION event when using small context window")
	}
}

// --- Phase 8: System prompt subagent guidance ---
// (Tests in profile_test.go)

// --- Review fix tests ---

// H1: checkpoint and summarizeWithLLM must not produce invalid message ordering.
// If preserveRecent falls mid-pair (e.g., starts on a TurnTool), the cutoff must
// be adjusted backward to include the preceding assistant turn.
func TestCheckpoint_AdjustsCutoffToAvoidOrphanedToolTurn(t *testing.T) {
	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("fix the bug")},
		{Kind: TurnAssistant, Message: assistantWithToolCall("c1", "read_file", `{"file_path":"a.go"}`)},
		{Kind: TurnTool, Message: llm.ToolResultNamed("c1", "read_file", "content", false)},
		{Kind: TurnAssistant, Message: assistantWithToolCall("c2", "edit_file", `{"file_path":"b.go"}`)},
		{Kind: TurnTool, Message: llm.ToolResultNamed("c2", "edit_file", "OK", false)},
		{Kind: TurnAssistant, Message: llm.Assistant("done")},
	}

	// preserveRecent=3 → cutoff=3, preserved turns start at index 3 (TurnAssistant) — OK.
	// preserveRecent=2 → cutoff=4, preserved turns start at index 4 (TurnTool) — BAD.
	result := checkpoint(history, 2)

	// First preserved turn after checkpoint must NOT be a TurnTool.
	if len(result) < 2 {
		t.Fatalf("expected at least 2 turns, got %d", len(result))
	}
	if result[0].Kind != TurnUserInput {
		t.Fatalf("first turn should be checkpoint (TurnUserInput), got %s", result[0].Kind)
	}
	if result[1].Kind == TurnTool {
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
	cm := NewContextManager(NewOpenAIProfile("gpt-5.2"), client)

	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("task")},
		{Kind: TurnAssistant, Message: assistantWithToolCall("c1", "read_file", `{"file_path":"a.go"}`)},
		{Kind: TurnTool, Message: llm.ToolResultNamed("c1", "read_file", "content", false)},
		{Kind: TurnAssistant, Message: assistantWithToolCall("c2", "shell", `{"command":"go test"}`)},
		{Kind: TurnTool, Message: llm.ToolResultNamed("c2", "shell", "ok\nexit_code=0\n", false)},
		{Kind: TurnAssistant, Message: llm.Assistant("done")},
	}

	// preserveRecent=2 → cutoff=4, preserved starts at TurnTool — BAD.
	result, err := cm.summarizeWithLLM(context.Background(), history, 2)
	if err != nil {
		t.Fatalf("summarizeWithLLM: %v", err)
	}

	if len(result) < 2 {
		t.Fatalf("expected at least 2 turns, got %d", len(result))
	}
	if result[1].Kind == TurnTool {
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
	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("task")},
		{Kind: TurnAssistant, Message: llm.Message{
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
		{Kind: TurnTool, Message: llm.ToolResultNamed("c1", "read_file", "package main\n", false)},
		// shell result comes second
		{Kind: TurnTool, Message: llm.ToolResultNamed("c2", "shell", "PASS\nexit_code=0 duration_ms=100 timed_out=false\n", false)},
		{Kind: TurnAssistant, Message: llm.Assistant("done")},
	}

	result := checkpoint(history, 1)
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
	cm := NewContextManager(NewOpenAIProfile("gpt-5.2"), client)

	// Build history with enormous user messages — many times larger than any cheap model can handle.
	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User(strings.Repeat("x", 100_000))},
		{Kind: TurnAssistant, Message: llm.Assistant(strings.Repeat("y", 100_000))},
		{Kind: TurnAssistant, Message: llm.Assistant("recent")},
	}

	_, err := cm.summarizeWithLLM(context.Background(), history, 1)
	if err != nil {
		t.Fatalf("summarizeWithLLM: %v", err)
	}

	// The prompt sent to the cheap model should be bounded, not 200k+ chars.
	// A reasonable max is ~50k chars (~12.5k tokens) for a cheap model.
	if receivedPromptLen > 60_000 {
		t.Fatalf("prompt sent to cheap model is too large: %d chars (should be truncated)", receivedPromptLen)
	}
}

// H5: summarizeWithLLM error path must be reliably testable.
func TestSummarizeWithLLM_AdapterError_ReturnsError(t *testing.T) {
	adapter := &errorAdapter{name: "openai"}
	client := llm.NewClient()
	client.Register(adapter)
	cm := NewContextManager(NewOpenAIProfile("gpt-5.2"), client)

	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("task")},
		{Kind: TurnAssistant, Message: llm.Assistant("step 1")},
		{Kind: TurnAssistant, Message: llm.Assistant("recent")},
	}

	result, err := cm.summarizeWithLLM(context.Background(), history, 1)
	if err == nil {
		t.Fatal("expected error from summarizeWithLLM when adapter fails")
	}
	if result != nil {
		t.Fatalf("expected nil result on error, got %d turns", len(result))
	}
}

// M1: Repeated checkpoint should still find the real original task, not the checkpoint message.
func TestCheckpoint_RepeatedCheckpoint_PreservesOriginalTask(t *testing.T) {
	// Simulate first checkpoint output.
	firstCheckpoint := Turn{
		Kind:    TurnUserInput,
		Message: llm.User("[CONTEXT CHECKPOINT]\nOriginal task: Fix the auth bug\nFiles modified: auth.go\n[END CHECKPOINT]\n"),
	}
	history := []Turn{
		firstCheckpoint,
		{Kind: TurnAssistant, Message: assistantWithToolCall("c3", "read_file", `{"file_path":"auth.go"}`)},
		{Kind: TurnTool, Message: llm.ToolResultNamed("c3", "read_file", "content", false)},
		{Kind: TurnAssistant, Message: llm.Assistant("done")},
	}

	result := checkpoint(history, 1)
	text := result[0].Message.Text()

	// The original task should be "Fix the auth bug", NOT the checkpoint message itself.
	if strings.Contains(text, "Original task: [CONTEXT CHECKPOINT]") {
		t.Fatalf("repeated checkpoint should extract real original task, not checkpoint text:\n%s", text)
	}
	if !strings.Contains(text, "Fix the auth bug") {
		t.Fatalf("repeated checkpoint should preserve real original task:\n%s", text)
	}
}

// M2: Checkpoint tool counts must be deterministic (sorted).
func TestCheckpoint_ToolCountsAreDeterministic(t *testing.T) {
	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("task")},
		{Kind: TurnAssistant, Message: assistantWithToolCall("c1", "read_file", `{"file_path":"a.go"}`)},
		{Kind: TurnTool, Message: llm.ToolResultNamed("c1", "read_file", "content", false)},
		{Kind: TurnAssistant, Message: assistantWithToolCall("c2", "shell", `{"command":"go test"}`)},
		{Kind: TurnTool, Message: llm.ToolResultNamed("c2", "shell", "ok\nexit_code=0\n", false)},
		{Kind: TurnAssistant, Message: assistantWithToolCall("c3", "edit_file", `{"file_path":"a.go"}`)},
		{Kind: TurnTool, Message: llm.ToolResultNamed("c3", "edit_file", "OK", false)},
		{Kind: TurnAssistant, Message: llm.Assistant("done")},
	}

	// Run checkpoint 20 times and verify output is identical each time.
	var first string
	for i := 0; i < 20; i++ {
		cp := make([]Turn, len(history))
		copy(cp, history)
		result := checkpoint(cp, 1)
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
	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("task")},
		{Kind: TurnAssistant, Message: assistantWithToolCall("c1", "apply_patch", `{"patch":"*** Begin Patch\n*** Update File: test.go\n*** End Patch"}`)},
		{Kind: TurnTool, Message: llm.ToolResultNamed("c1", "apply_patch", "OK", false)},
		{Kind: TurnAssistant, Message: llm.Assistant("done")},
	}

	result := checkpoint(history, 1)
	text := result[0].Message.Text()
	if !strings.Contains(text, "test.go") {
		t.Fatalf("checkpoint should include test.go from apply_patch:\n%s", text)
	}
}

func TestCheckpoint_IncludesWebSearchCount(t *testing.T) {
	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("search for docs")},
		{Kind: TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentWebSearch, WebSearch: &llm.WebSearchData{Query: "Go docs"}},
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "c1", Name: "read_file", Arguments: json.RawMessage(`{"file_path":"a.go"}`)}},
				{Kind: llm.ContentText, Text: "Found the docs."},
			},
		}},
		{Kind: TurnTool, Message: llm.ToolResultNamed("c1", "read_file", "file contents", false)},
		// Preserved recent turns:
		{Kind: TurnUserInput, Message: llm.User("next question")},
		{Kind: TurnAssistant, Message: llm.Message{
			Role:    llm.RoleAssistant,
			Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "answer"}},
		}},
	}

	result := checkpoint(history, 2) // preserve last 2 turns

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

// Token-based pressure: ContextManager should use actual InputTokens from API
// responses for pressure calculation instead of relying solely on char/4.
func TestContextManager_UsesLastInputTokensForPressure(t *testing.T) {
	profile := &baseProfile{id: "openai", model: "test", contextWindow: 1000}
	cm := NewContextManager(profile, nil)

	// Record that the last API response reported 550 input tokens for a 10-turn history.
	cm.RecordInputTokens(550, 10)

	// Add 2 new turns since the measurement (~20 chars = ~5 tokens).
	history := make([]Turn, 12)
	for i := 0; i < 10; i++ {
		history[i] = Turn{Kind: TurnAssistant, Message: llm.Assistant("x")}
	}
	history[10] = Turn{Kind: TurnAssistant, Message: llm.Assistant(strings.Repeat("y", 20))}
	history[11] = Turn{Kind: TurnAssistant, Message: llm.Assistant(strings.Repeat("z", 20))}

	// Pressure should be based on: 550 (known) + ~10 (new turns) = ~560 tokens
	// Not char/4 of entire history which would be much less.
	pressure := cm.estimatePressure(history, 0)

	// With 1000 token window, pressure should be ~0.56 (not the ~0.03 from char/4).
	if pressure < 0.5 {
		t.Fatalf("pressure should use lastInputTokens, got %.2f (expected ~0.56)", pressure)
	}
}

func TestContextManager_FallsBackToCharHeuristicWithoutMeasurement(t *testing.T) {
	profile := &baseProfile{id: "openai", model: "test", contextWindow: 1000}
	cm := NewContextManager(profile, nil)

	// No RecordInputTokens called — should fall back to char/4.
	history := []Turn{
		{Kind: TurnAssistant, Message: llm.Assistant(strings.Repeat("x", 400))},
	}

	pressure := cm.estimatePressure(history, 0)

	// 400 chars / 4 = 100 tokens, 100/1000 = 0.10
	if pressure < 0.09 || pressure > 0.11 {
		t.Fatalf("expected pressure ~0.10 from char/4 fallback, got %.2f", pressure)
	}
}

func TestContextManager_ResetsAfterCompaction(t *testing.T) {
	profile := &baseProfile{id: "openai", model: "test", contextWindow: 1000}
	cm := NewContextManager(profile, nil)
	cm.PreserveRecentTurns = 2

	// Record high token count.
	cm.RecordInputTokens(700, 5)

	// After compaction modifies history, the measurement should reset.
	// Build a history that's big enough to trigger observation masking.
	history := makeBigHistory(650)
	history = append(history,
		Turn{Kind: TurnAssistant, Message: llm.Assistant("recent1")},
		Turn{Kind: TurnAssistant, Message: llm.Assistant("recent2")},
	)

	var events []SessionEvent
	emitFn := func(kind EventKind, data any) {
		events = append(events, SessionEvent{Kind: kind, Data: data})
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
	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("task")},
		{Kind: TurnAssistant, Message: llm.Assistant("answer")},
		{Kind: TurnAssistant, Message: llm.Assistant("recent")},
	}
	got := safeCutoff(history, 2)
	if got != 2 {
		t.Fatalf("safeCutoff = %d, want 2 (no adjustment needed)", got)
	}
}

func TestSafeCutoff_WalksToZero_ReturnsNegative(t *testing.T) {
	// All turns after index 0 are TurnTool — walking back from any cutoff
	// should reach 0, which is not a safe position.
	history := []Turn{
		{Kind: TurnTool, Message: llm.ToolResultNamed("c1", "read_file", "content", false)},
		{Kind: TurnTool, Message: llm.ToolResultNamed("c2", "read_file", "content", false)},
		{Kind: TurnTool, Message: llm.ToolResultNamed("c3", "read_file", "content", false)},
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
	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("task")},
		{Kind: TurnAssistant, Message: llm.Assistant("answer")},
		{Kind: TurnSteering, Message: llm.User("you should do X")},
		{Kind: TurnAssistant, Message: llm.Assistant("recent")},
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
	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("task")},
		{Kind: TurnTool, Message: llm.ToolResultNamed("c1", "read_file", "content", false)},
		{Kind: TurnAssistant, Message: llm.Assistant("answer")},
		{Kind: TurnAssistant, Message: llm.Assistant("recent")},
	}
	// preserveRecent=3 → cutoff=1 → TurnTool → walk to 0 → return -1
	result := checkpoint(history, 3)
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
	cm := NewContextManager(NewOpenAIProfile("gpt-5.2"), client)

	// Same scenario as checkpoint test: cutoff walks to 0 → return -1.
	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("task")},
		{Kind: TurnTool, Message: llm.ToolResultNamed("c1", "read_file", "content", false)},
		{Kind: TurnAssistant, Message: llm.Assistant("answer")},
		{Kind: TurnAssistant, Message: llm.Assistant("recent")},
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
	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("task")},
		{Kind: TurnAssistant, Message: assistantWithToolCall("c1", "edit_file", `{"file_path":"auth.go"}`)},
		{Kind: TurnTool, Message: llm.ToolResultNamed("c1", "edit_file", "OK", false)},
		{Kind: TurnAssistant, Message: llm.Assistant("done")},
	}

	maskObservations(history, 0)

	// "OK" (2 chars) should not be replaced with "[edit_file: auth.go → OK]" (25 chars).
	got := toolResultContent(history[2])
	if got != "OK" {
		t.Fatalf("short result should not be masked, got: %q", got)
	}
}

// --- Fix: Checkpoint task extraction (M1) ---

func TestCheckpoint_OriginalTaskNotOverriddenByFollowup(t *testing.T) {
	// After a previous checkpoint, history starts with the checkpoint message
	// (which embeds "Original task: Fix the auth bug"). A follow-up user message
	// "Also update the tests" appears in the preserved recent turns.
	// The second checkpoint should extract "Fix the auth bug" from the first
	// checkpoint, NOT replace it with the follow-up.
	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("[CONTEXT CHECKPOINT]\nOriginal task: Fix the auth bug\nFiles modified: auth.go\n[END CHECKPOINT]\n")},
		{Kind: TurnAssistant, Message: assistantWithToolCall("c1", "read_file", `{"file_path":"auth.go"}`)},
		{Kind: TurnTool, Message: llm.ToolResultNamed("c1", "read_file", "content", false)},
		{Kind: TurnAssistant, Message: llm.Assistant("I've analyzed the code")},
		// Follow-up user message in preserved region:
		{Kind: TurnUserInput, Message: llm.User("Also update the tests")},
		{Kind: TurnAssistant, Message: llm.Assistant("Will do")},
	}

	// preserveRecent=2 → cutoff=4 → history[4] is TurnUserInput → safe, stays 4.
	// Bug: the scan visits history[4] ("Also update the tests") which is in the
	// preserved section and overrides the original task extracted from the checkpoint.
	result := checkpoint(history, 2)
	text := result[0].Message.Text()

	if strings.Contains(text, "Also update the tests") {
		t.Fatalf("checkpoint should use original task from compacted region, not follow-up:\n%s", text)
	}
	if !strings.Contains(text, "Fix the auth bug") {
		t.Fatalf("checkpoint missing original task:\n%s", text)
	}
}

// --- Fix: Pressure cascade (C1) ---

// TestMaybeCompact_L1StopsCascade verifies that when observation masking alone
// frees enough tokens, higher layers (L2/L3/L4) do not fire. This catches the
// bug where stale lastInputTokens caused all layers to cascade in a single pass.
func TestMaybeCompact_L1StopsCascade(t *testing.T) {
	profile := &baseProfile{id: "openai", model: "test", contextWindow: 1000}
	cm := NewContextManager(profile, nil)
	cm.PreserveRecentTurns = 2

	// Build history with large tool results that L1 can mask down significantly.
	// Target ~75% pressure via char/4 (750 tokens). Tool results are big so
	// masking will free a lot.
	history := makeBigHistory(750)
	history = append(history,
		Turn{Kind: TurnAssistant, Message: llm.Assistant("recent1")},
		Turn{Kind: TurnAssistant, Message: llm.Assistant("recent2")},
	)

	// Simulate a previous API call that reported 750 input tokens.
	// Bug: after L1 masks tool results in-place, the between-layer pressure()
	// call still uses lastInputTokens=750 (stale), so pressure stays at 75%
	// and L2 (thinking_clear, threshold=70%) fires unnecessarily.
	cm.RecordInputTokens(750, len(history))

	var layers []string
	emitFn := func(kind EventKind, data any) {
		if kind == EventContextCompaction {
			if cd, ok := data.(ContextCompactionData); ok {
				layers = append(layers, cd.Layer)
			}
		}
	}

	cm.MaybeCompact(context.Background(), &history, 0, emitFn)

	// L1 (observation_mask) should fire.
	if len(layers) == 0 || layers[0] != "observation_mask" {
		t.Fatalf("expected observation_mask as first layer, got %v", layers)
	}

	// After L1 masks the big tool results, char/4 pressure should drop well
	// below 70%. L2 (thinking_clear) and higher should NOT fire.
	for _, l := range layers[1:] {
		if l == "thinking_clear" || l == "checkpoint" || l == "summarize" {
			t.Fatalf("layer %q should not fire after L1 freed enough tokens; layers=%v", l, layers)
		}
	}
}

// --- Integration tests ---

// TestMaybeCompact_L1ThenL3_MaskedShellParsed verifies that when L1 masks
// shell results, L3 checkpoint still extracts correct exit codes from the
// masked "[shell: ... → exit N]" format.
func TestMaybeCompact_L1ThenL3_MaskedShellParsed(t *testing.T) {
	// Use a context window where L1 masking reduces tool output but still
	// leaves history above the 80% checkpoint threshold, so both fire.
	profile := &baseProfile{id: "openai", model: "test", contextWindow: 80}
	cm := NewContextManager(profile, nil)
	cm.PreserveRecentTurns = 2

	// Build history with shell tool calls (big output so L1 masks them)
	// plus assistant text that L1 can't touch, keeping pressure above 80%.
	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("run tests")},
		{Kind: TurnAssistant, Message: assistantWithToolCall("c1", "shell", `{"command":"go test ./..."}`)},
		{Kind: TurnTool, Message: llm.ToolResultNamed("c1", "shell", strings.Repeat("output\n", 20)+"exit_code=0 duration_ms=100 timed_out=false\n", false)},
		{Kind: TurnAssistant, Message: llm.Assistant(strings.Repeat("analysis ", 30))},
		{Kind: TurnAssistant, Message: llm.Assistant(strings.Repeat("more ", 30))},
		{Kind: TurnAssistant, Message: llm.Assistant("recent1")},
		{Kind: TurnAssistant, Message: llm.Assistant("recent2")},
	}

	var layers []string
	emitFn := func(kind EventKind, data any) {
		if kind == EventContextCompaction {
			if cd, ok := data.(ContextCompactionData); ok {
				layers = append(layers, cd.Layer)
			}
		}
	}

	cm.MaybeCompact(context.Background(), &history, 0, emitFn)

	// With this small window, L1 and L3 should both fire.
	foundL1, foundL3 := false, false
	for _, l := range layers {
		if l == "observation_mask" {
			foundL1 = true
		}
		if l == "checkpoint" {
			foundL3 = true
		}
	}
	if !foundL1 {
		t.Fatalf("expected observation_mask layer; got %v", layers)
	}
	if !foundL3 {
		t.Fatalf("expected checkpoint layer; got %v", layers)
	}

	// The checkpoint should contain "exit 0" from the shell call.
	text := history[0].Message.Text()
	if !strings.Contains(text, "exit 0") {
		t.Fatalf("checkpoint should contain 'exit 0' from masked shell result:\n%s", text)
	}
}

// TestMaybeCompact_SummarizeThreshold verifies L4 fires through the orchestrator.
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

	// Small window. L3 (checkpoint) replaces old history with a checkpoint
	// message, but the preserved recent turns are large enough to keep
	// pressure above 90% after checkpoint, forcing L4.
	profile := &baseProfile{id: "openai", model: "test", contextWindow: 50}
	cm := NewContextManager(profile, client)
	cm.PreserveRecentTurns = 1

	// History with pure text (so L1/L2 can't help much).
	// After L3 checkpoint, result = [checkpoint_msg, recent_turn].
	// The recent turn alone needs to be ~90% of 50 tokens = 45 tokens = ~180 chars.
	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User(strings.Repeat("task ", 50))},
		{Kind: TurnAssistant, Message: llm.Assistant(strings.Repeat("work ", 30))},
		{Kind: TurnAssistant, Message: llm.Assistant(strings.Repeat("recent content ", 15))},
	}

	var layers []string
	emitFn := func(kind EventKind, data any) {
		if kind == EventContextCompaction {
			if cd, ok := data.(ContextCompactionData); ok {
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
	return llm.Response{}, fmt.Errorf("simulated LLM error")
}
func (a *errorAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	return nil, fmt.Errorf("stream not implemented")
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
