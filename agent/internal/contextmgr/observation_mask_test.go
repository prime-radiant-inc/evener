package contextmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func TestSummarizeToolResult_ReadFile(t *testing.T) {
	// Simulate a read_file result: line-numbered content (cat -n style, tab separator).
	lines := "1\tpackage main\n2\tfunc main() {}\n"
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
	got := summarizeToolResult("shell", output, true, json.RawMessage(`{"command":"go test"}`))
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

// TestSummarizeToolResult_SearchNotes summarizes real grep and glob results
// (agent/testdata/toolwire): a note the tool puts after a blank line is not a
// match, and the summary keeps it, so a compacted search still says it was cut
// short or left paths out (#4186).
func TestSummarizeToolResult_SearchNotes(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/toolwire/calls.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Items []appwire.ThreadItem `json:"items"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	args, outputs := map[string]string{}, map[string]string{}
	for _, item := range fixture.Items {
		if item.ArgumentsJSON != "" {
			args[item.CallID] = item.ArgumentsJSON
		}
		if item.Output != "" {
			outputs[item.CallID] = item.Output
		}
	}
	for _, tc := range []struct{ call, tool, want string }{
		{"call_grep_capped", "grep", `[grep: "package|func" → 1 matches; results truncated at 1; narrow the path or glob_filter, or raise max_results]`},
		{"call_glob_excluded", "glob", `[glob: "**/plan.md" → 0 files; 0 matches after excluding 1 dotfile/gitignored path(s); set include_ignored to include them]`},
	} {
		if got := summarizeToolResult(tc.tool, outputs[tc.call], false, json.RawMessage(args[tc.call])); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.call, got, tc.want)
		}
	}
}

// memory_search writes grep's output, so it summarizes like grep: entries
// counted, notes kept.
func TestSummarizeToolResult_MemorySearch(t *testing.T) {
	output := "notes.md:1:x\n\n[results truncated at 1; narrow the path or glob_filter, or raise max_results]"
	got := summarizeToolResult("memory_search", output, false, json.RawMessage(`{"pattern":"x"}`))
	want := `[memory_search: "x" → 1 matches; results truncated at 1; narrow the path or glob_filter, or raise max_results]`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// A breaker nudge the registry appended to a repeated search
// (agent/internal/tool appendIntervention) is not a note about the search, so
// the summary must not show it as one. A search that wrote no notes of its own
// is the case a positional rule (notes end at the second blank line) cannot
// tell apart, and the case the issue named (#4223). The repeated result comes
// from the registry itself, so no nudge text is copied here and the producer
// and this reader are pinned to each other.
func TestSummarizeToolResult_RepeatedSearchNudgeIsNotANote(t *testing.T) {
	for _, tc := range []struct{ name, tool, body, want string }{
		{
			name: "a repeated grep with no notes of its own",
			tool: "grep",
			body: "a.go:1:x\nb.go:2:x",
			want: `[grep: "x" → 2 matches]`,
		},
		{
			name: "a repeated grep keeps its own note",
			tool: "grep",
			body: "a.go:1:x\n\nSENTINEL-NOTE",
			want: `[grep: "x" → 1 matches; SENTINEL-NOTE]`,
		},
		{
			name: "a repeated memory_search with no notes of its own",
			tool: "memory_search",
			body: "notes.md:1:x",
			want: `[memory_search: "x" → 1 matches]`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repeated := repeatedToolOutput(t, tc.tool, tc.body)
			got := summarizeToolResult(tc.tool, repeated, false, json.RawMessage(`{"pattern":"x"}`))
			if got != tc.want {
				t.Fatalf("a repeated %s's nudge read as one of its notes: got %q, want %q", tc.tool, got, tc.want)
			}
		})
	}
}

// repeatedToolOutput runs toolName twice through a registry whose executor
// returns body, so the second result carries the registry's own repetition
// nudge for a repeated identical call. It fails when the second call drew no
// intervention, so a summarizer case cannot pass by never seeing one.
func repeatedToolOutput(t *testing.T, toolName, body string) string {
	t.Helper()
	reg := tool.NewRegistry()
	if err := reg.Register(tool.RegisteredTool{
		Definition: llm.ToolDefinition{Name: toolName},
		Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
			return body, nil
		},
		Limit: schema.ToolOutputLimit{MaxChars: 20_000, MaxLines: 200, Strategy: schema.TruncHeadCount},
	}); err != nil {
		t.Fatalf("register %s: %v", toolName, err)
	}
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	call := llm.ToolCallData{ID: "c1", Name: toolName, Arguments: json.RawMessage(`{"pattern":"x"}`)}
	first := reg.ExecuteCall(context.Background(), env, call)
	repeated := reg.ExecuteCall(context.Background(), env, call)
	if len(repeated.Output) <= len(first.Output) {
		t.Fatalf("the repeated %s call drew no intervention: first %q, second %q", toolName, first.Output, repeated.Output)
	}
	return repeated.Output
}

// A search's own notes are kept, however many blocks they span. Opaque
// sentinels stand in for the notes, so this pins that the notes cross the
// summarizer without hardcoding any of the tools' or the registry's wording.
func TestSummarizeToolResult_SearchKeepsItsNotes(t *testing.T) {
	for _, tc := range []struct{ name, tool, output, want string }{
		{
			name:   "one note the tool wrote",
			tool:   "grep",
			output: "a.go:1:x\n\nSENTINEL-NOTE",
			want:   `[grep: "x" → 1 matches; SENTINEL-NOTE]`,
		},
		{
			name:   "two blocks, the second the registry's marker",
			tool:   "glob",
			output: "a.go\n\nSENTINEL-TOOL-NOTE\n\nSENTINEL-MARKER",
			want:   `[glob: "x" → 1 files; SENTINEL-TOOL-NOTE; SENTINEL-MARKER]`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := summarizeToolResult(tc.tool, tc.output, false, json.RawMessage(`{"pattern":"x"}`))
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSummarizeToolResult_EditFile(t *testing.T) {
	got := summarizeToolResult("edit_file", "OK", false, json.RawMessage(`{"file_path":"auth.go"}`))
	want := "[edit_file: auth.go → OK]"
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

func TestSummarizeToolResult_WebFetch(t *testing.T) {
	content := "Here is some fetched content from the web page"
	got := summarizeToolResult("web_fetch", content, false, json.RawMessage(`{"url":"https://example.com"}`))
	want := fmt.Sprintf("[web_fetch: https://example.com → %d chars]", len(content))
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSummarizeToolResult_Delegate(t *testing.T) {
	got := summarizeToolResult("delegate", `{"job_id":"job_abc123"}`, false, json.RawMessage(`{"task":"do stuff"}`))
	want := "[delegate: job_abc123]"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	got = summarizeToolResult("delegate", `{"status":"running"}`, false, json.RawMessage(`{"task":"do stuff"}`))
	want = "[delegate: 20 chars]"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSummarizeToolResult_TaskList(t *testing.T) {
	got := summarizeToolResult("task_list", `[{"id":1},{"id":2},{"id":3}]`, false, json.RawMessage(`{}`))
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

// thinkingHeavyHistory is the shared input for the profile-read concurrency
// tests: a long history whose turns are replayable thinking text, so the two
// profiles' accounting differs across any window between their reads.
func thinkingHeavyHistory() []schema.Turn {
	history := make([]schema.Turn, 0, 3000)
	for range 3000 {
		history = append(history, schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "reasoning the anthropic adapter replays and the responses adapter does not"}},
			},
		}})
	}
	return history
}

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
