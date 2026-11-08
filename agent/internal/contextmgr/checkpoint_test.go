package contextmgr

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

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

// A write is only a completed-work fact when its paired result confirmed
// success. This asserts the extracted status before rendering.
func TestCheckpoint_WriteStatus_PairsCallsWithResults(t *testing.T) {
	t.Parallel()
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("prompt")},
		{Kind: schema.TurnAssistant, Message: assistantWithToolCall("w1", "write_file", `{"file_path":"confirmed.go"}`)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("w1", "write_file", "OK", false)},
		{Kind: schema.TurnAssistant, Message: assistantWithToolCall("w2", "edit_file", `{"file_path":"failed.go"}`)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("w2", "edit_file", "permission denied", true)},
		{Kind: schema.TurnAssistant, Message: assistantWithToolCall("w3", "apply_patch", `{"patch":"*** Begin Patch\n*** Update File: pending.go\n*** End Patch"}`)},
		// No result for w3: the attempt stays unconfirmed, never completed.
		{Kind: schema.TurnAssistant, Message: llm.Assistant("done")},
	}

	data := collectCheckpointData(history, len(history), "communicate")

	wants := map[string]checkpointWriteStatus{
		"confirmed.go": writeConfirmed,
		"failed.go":    writeFailed,
		"pending.go":   writeUnconfirmed,
	}
	for path, want := range wants {
		got, ok := data.fileWrites[path]
		if !ok || got != want {
			t.Fatalf("fileWrites[%q] = %v (present=%v), want %v", path, got, ok, want)
		}
	}
}

// A confirmed write is sticky: a later failed or unconfirmed attempt on the
// same path must not downgrade it out of "Files modified:", while a path whose
// first attempt failed is confirmed once a later attempt succeeds.
func TestCheckpoint_ConfirmedWriteNotDowngradedByLaterAttempt(t *testing.T) {
	t.Parallel()
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("prompt")},
		// confirmed.go succeeds, then a later edit of the same path fails.
		{Kind: schema.TurnAssistant, Message: assistantWithToolCall("w1", "write_file", `{"file_path":"confirmed.go"}`)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("w1", "write_file", "OK", false)},
		{Kind: schema.TurnAssistant, Message: assistantWithToolCall("w2", "edit_file", `{"file_path":"confirmed.go"}`)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("w2", "edit_file", "denied", true)},
		// retried.go fails, then a later attempt succeeds.
		{Kind: schema.TurnAssistant, Message: assistantWithToolCall("w3", "write_file", `{"file_path":"retried.go"}`)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("w3", "write_file", "denied", true)},
		{Kind: schema.TurnAssistant, Message: assistantWithToolCall("w4", "write_file", `{"file_path":"retried.go"}`)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("w4", "write_file", "OK", false)},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("done")},
	}

	result := checkpoint(history, 1, nil, "communicate")
	line := checkpointModifiedFilesLine(result[0].Message.Text())
	for _, path := range []string{"confirmed.go", "retried.go"} {
		if !strings.Contains(line, path) {
			t.Fatalf("confirmed write %q missing from Files modified: %q", path, line)
		}
	}
}

// A rejected or unanswered terminal communicate is not a completed agent
// reply; only an accepted result belongs in the extracted conversation.
func TestCheckpoint_CommunicateOnlyRecordsAcceptedReplies(t *testing.T) {
	t.Parallel()
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("prompt")},
		{Kind: schema.TurnAssistant, Message: assistantWithToolCall("c1", "communicate", `{"message":"rejected reply","end_turn":true}`)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c1", "communicate", "dispatch failed", true)},
		{Kind: schema.TurnAssistant, Message: assistantWithToolCall("c2", "communicate", `{"message":"accepted reply","end_turn":true}`)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c2", "communicate", "delivered", false)},
		{Kind: schema.TurnAssistant, Message: assistantWithToolCall("c3", "communicate", `{"message":"unanswered reply","end_turn":true}`)},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("still working")},
	}

	data := collectCheckpointData(history, len(history), "communicate")

	var agents []string
	for _, e := range data.conversation {
		if e.Role == "agent" {
			agents = append(agents, e.Text)
		}
	}
	if len(agents) != 1 || agents[0] != "accepted reply" {
		t.Fatalf("agent conversation = %v, want only the accepted reply", agents)
	}
}

// A failed or result-missing write must never render on the "Files modified:"
// line, and re-compacting a checkpoint must not upgrade it to completed work.
// This pins the rendered completion contract the audit finding faulted.
func TestCheckpoint_AttemptedWritesNeverUpgradeAcrossCompactions(t *testing.T) {
	t.Parallel()
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("prompt")},
		{Kind: schema.TurnAssistant, Message: assistantWithToolCall("w1", "write_file", `{"file_path":"good.go"}`)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("w1", "write_file", "OK", false)},
		{Kind: schema.TurnAssistant, Message: assistantWithToolCall("w2", "write_file", `{"file_path":"failed.go"}`)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("w2", "write_file", "denied", true)},
		{Kind: schema.TurnAssistant, Message: assistantWithToolCall("w3", "write_file", `{"file_path":"pending.go"}`)},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("done")},
	}

	attempted := []string{"failed.go", "pending.go"}

	first := checkpoint(history, 1, nil, "communicate")
	firstLine := checkpointModifiedFilesLine(first[0].Message.Text())
	if !strings.Contains(firstLine, "good.go") {
		t.Fatalf("first checkpoint should report the confirmed write: %q", firstLine)
	}
	for _, path := range attempted {
		if strings.Contains(firstLine, path) {
			t.Fatalf("attempted write %q reported as modified in first checkpoint: %q", path, firstLine)
		}
	}

	// Re-compacting the checkpoint must not upgrade an attempted write either.
	second := checkpoint(first, 0, nil, "communicate")
	secondLine := checkpointModifiedFilesLine(second[0].Message.Text())
	for _, path := range attempted {
		if strings.Contains(secondLine, path) {
			t.Fatalf("attempted write %q upgraded to modified after re-compaction: %q", path, secondLine)
		}
	}
}

// checkpointModifiedFilesLine returns the rendered "Files modified:" line, or
// "" when the checkpoint reports no confirmed writes.
func checkpointModifiedFilesLine(text string) string {
	for line := range strings.SplitSeq(text, "\n") {
		if rest, ok := strings.CutPrefix(line, "Files modified:"); ok {
			return rest
		}
	}
	return ""
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

// H1: checkpoint and summarizeFold must not produce invalid message ordering.
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
	reply1 := agenttest.CommunicateCall("c1", "first reply")
	reply2 := agenttest.CommunicateCall("c2", "second reply")
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
	for i := range 20 {
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

func TestCheckpoint_PreservesToolCallAcrossHookCompleted(t *testing.T) {
	const callID = "call_with_hook"
	tests := []struct {
		name           string
		hookMessages   []string
		preserveRecent int
		wantKinds      string
	}{
		{
			name:           "cutoff on tool results",
			hookMessages:   []string{"PreToolUse hook exit 0"},
			preserveRecent: 2,
			wantKinds:      "CHECKPOINT,ASSISTANT,HOOK_COMPLETED,TOOL_RESULTS,ASSISTANT",
		},
		{
			name:           "cutoff on hook marker",
			hookMessages:   []string{"PreToolUse hook exit 0"},
			preserveRecent: 3,
			wantKinds:      "CHECKPOINT,ASSISTANT,HOOK_COMPLETED,TOOL_RESULTS,ASSISTANT",
		},
		{
			name:           "cutoff on adjacent hook markers",
			hookMessages:   []string{"PreToolUse first hook exit 0", "PreToolUse second hook exit 0"},
			preserveRecent: 4,
			wantKinds:      "CHECKPOINT,ASSISTANT,HOOK_COMPLETED,HOOK_COMPLETED,TOOL_RESULTS,ASSISTANT",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			history := []schema.Turn{
				{Kind: schema.TurnUserInput, Message: llm.User("task")},
				{Kind: schema.TurnAssistant, Message: llm.Assistant("earlier answer")},
				{Kind: schema.TurnAssistant, Message: assistantWithToolCall(callID, "probe", `{}`)},
			}
			for _, message := range tc.hookMessages {
				history = append(history, schema.NewTurn(schema.TurnHookCompleted, llm.System(message)))
			}
			history = append(history,
				schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed(callID, "probe", "ok", false)),
				schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant("recent")},
			)

			result := checkpoint(history, tc.preserveRecent, nil, "communicate")

			gotKinds := make([]string, len(result))
			for i, turn := range result {
				gotKinds[i] = string(turn.Kind)
			}
			if got := strings.Join(gotKinds, ","); got != tc.wantKinds {
				t.Fatalf("checkpoint kinds = %s, want %s", got, tc.wantKinds)
			}

			foundCall := false
			foundResult := false
			for _, turn := range result {
				for _, part := range turn.Message.Content {
					if part.Kind == llm.ContentToolCall && part.ToolCall != nil && part.ToolCall.ID == callID {
						foundCall = true
					}
					if part.Kind == llm.ContentToolResult && part.ToolResult != nil && part.ToolResult.ToolCallID == callID {
						foundResult = true
					}
				}
			}
			if !foundCall {
				t.Fatalf("preserved assistant is missing tool call %q", callID)
			}
			if !foundResult {
				t.Fatalf("preserved result is missing tool call ID %q", callID)
			}
		})
	}
}

func TestCheckpoint_PreservesToolCallAcrossAttentionResolution(t *testing.T) {
	const callID = "call_with_attention_resolution"
	for _, test := range []struct {
		name           string
		preserveRecent int
	}{
		{name: "cutoff on tool results", preserveRecent: 2},
		{name: "cutoff on resolution marker", preserveRecent: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolution := schema.NewTurn(schema.TurnAttentionResolution, llm.System("private resolution marker"))
			resolution.AttentionResolution = &schema.AttentionResolutionInfo{
				AttentionID: "delegate:delivery-1",
				Disposition: "consumed",
			}
			history := []schema.Turn{
				{Kind: schema.TurnUserInput, Message: llm.User("task")},
				{Kind: schema.TurnAssistant, Message: llm.Assistant("earlier answer")},
				{Kind: schema.TurnAssistant, Message: assistantWithToolCall(callID, "probe", `{}`)},
				resolution,
				schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed(callID, "probe", "ok", false)),
				{Kind: schema.TurnAssistant, Message: llm.Assistant("recent")},
			}

			result := checkpoint(history, test.preserveRecent, nil, "communicate")
			gotKinds := make([]string, len(result))
			for i, turn := range result {
				gotKinds[i] = string(turn.Kind)
			}
			const want = "CHECKPOINT,ASSISTANT,ATTENTION_RESOLUTION,TOOL_RESULTS,ASSISTANT"
			if got := strings.Join(gotKinds, ","); got != want {
				t.Fatalf("checkpoint kinds = %s, want %s", got, want)
			}
		})
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

func TestCheckpoint_TranscriptPointer_WithSessionID(t *testing.T) {
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("do the work")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("done")},
	}
	meta := &CompactionMeta{SessionID: "01ABC", AvailableTranscriptTools: []string{"read_transcript", "find_session_transcripts"}}
	result := checkpoint(history, 1, meta, "communicate")
	text := result[0].Message.Text()

	if !strings.Contains(text, "01ABC") {
		t.Errorf("checkpoint missing session id: %q", text)
	}
	if !strings.Contains(text, "read_transcript") {
		t.Errorf("checkpoint missing read_transcript tool reference: %q", text)
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

	if strings.Contains(text, "read_transcript") {
		t.Errorf("non-persistent checkpoint should not reference read_transcript: %q", text)
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
	for i := range 150 {
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

func TestCheckpoint_ShedOrder_PreservesOriginalTask(t *testing.T) {
	// The first conversation entry is the session's original task statement.
	// Budget shedding must keep it and drop the oldest entry after it, so a
	// handoff under pressure never loses what the user asked for.
	original := "ORIGINAL TASK: " + strings.Repeat("t", 400)
	middle := "middle chatter " + strings.Repeat("m", 400)
	recent := "recent " + strings.Repeat("r", 400)
	data := checkpointData{
		conversation: []checkpointConversationEntry{
			{Role: "user", Text: original},
			{Role: "agent", Text: middle},
			{Role: "agent", Text: recent},
		},
	}

	// maxChars 100 floors the variable budget to 1000: enough for the original
	// task plus the newest entry once the middle entry is shed.
	cp := formatCheckpoint(data, nil, 100)

	if !strings.Contains(cp, original) {
		t.Fatalf("the original task (conversation[0]) must survive shedding:\n%s", cp)
	}
	if strings.Contains(cp, middle) {
		t.Fatalf("the oldest entry after the original task should be shed first:\n%s", cp)
	}
	if !strings.Contains(cp, recent) {
		t.Fatalf("the newest conversation entry should survive:\n%s", cp)
	}
}

func TestCheckpoint_ShedOrder_PinsEarliestUserEntry(t *testing.T) {
	// The pin targets the earliest user entry, not index 0: an agent-led prefix
	// must not redirect the pin onto chatter and shed the original task.
	lead := "leading agent chatter " + strings.Repeat("l", 400)
	original := "ORIGINAL TASK: " + strings.Repeat("t", 400)
	recent := "recent " + strings.Repeat("r", 400)
	data := checkpointData{
		conversation: []checkpointConversationEntry{
			{Role: "agent", Text: lead},
			{Role: "user", Text: original},
			{Role: "agent", Text: recent},
		},
	}

	cp := formatCheckpoint(data, nil, 100)

	if !strings.Contains(cp, original) {
		t.Fatalf("the earliest user entry (the original task) must survive shedding:\n%s", cp)
	}
	if strings.Contains(cp, lead) {
		t.Fatalf("the agent entry before the original task should be shed first:\n%s", cp)
	}
}

func TestCheckpoint_ShedOrder_NoUserEntryShedsOldestFirst(t *testing.T) {
	// With no user entry there is no original task to pin; shedding must keep its
	// pre-existing oldest-first order rather than protecting an arbitrary entry.
	old := "OLD AGENT " + strings.Repeat("a", 800)
	newest := "NEW AGENT " + strings.Repeat("b", 800)
	data := checkpointData{
		conversation: []checkpointConversationEntry{
			{Role: "agent", Text: old},
			{Role: "agent", Text: newest},
		},
	}

	cp := formatCheckpoint(data, nil, 100)

	if strings.Contains(cp, old) {
		t.Fatalf("with no user entry the oldest agent entry should be shed:\n%s", cp)
	}
	if !strings.Contains(cp, newest) {
		t.Fatalf("with no user entry the newest agent entry should survive:\n%s", cp)
	}
}

func TestCheckpoint_ShedOrder_MiddleTrimsAnOversizedPartnerMessage(t *testing.T) {
	// The human partner's messages are never dropped (#4281). One too large to
	// fit is trimmed in the middle, keeping its head and tail and saying where
	// the full text is, and the agent's own reply is shed first.
	original := "ORIGINAL TASK: " + strings.Repeat("t", 5000) + " TASK_TAIL"
	data := checkpointData{
		conversation: []checkpointConversationEntry{
			{Role: "user", Text: original},
			{Role: "agent", Text: "small follow-up"},
		},
	}

	cp := formatCheckpoint(data, persistentMeta, 1200)

	entries := extractCheckpointConversation(cp)
	if len(entries) != 1 || entries[0].Role != "user" {
		t.Fatalf("want only the partner's message, trimmed; got %q", entries)
	}
	got := entries[0].Text
	if !strings.HasPrefix(got, "ORIGINAL TASK: ") || !strings.HasSuffix(got, " TASK_TAIL") {
		t.Fatalf("the trimmed message lost its head or tail:\n%s", got)
	}
	if !isMiddleTrimOf(got, original) {
		t.Fatalf("want the message's head and tail around the trimmed-message note:\n%s", got)
	}
}

// isMiddleTrimOf reports whether got is original with its middle replaced by
// the trimmed-message note for the characters removed.
func isMiddleTrimOf(got, original string) bool {
	// The note's length depends only on how many digits the trimmed count has,
	// which fixes how much of each end was kept.
	for digits := 1; digits <= 9; digits++ {
		noteLen := len(trimmedMessageNote(0)) - 1 + digits
		keep := (len(got) - noteLen) / 2
		if keep <= 0 || 2*keep >= len(original) {
			continue
		}
		if got == original[:keep]+trimmedMessageNote(len(original)-2*keep)+original[len(original)-keep:] {
			return true
		}
	}
	return false
}

func TestCheckpoint_ShedOrder_ShedsNotesToFitBudget(t *testing.T) {
	// Working notes carry the lowest priority: when the conversation and notes
	// together exceed the budget, notes are shed before the original task, which
	// survives intact.
	original := "ORIGINAL TASK: " + strings.Repeat("t", 300)
	data := checkpointData{
		conversation: []checkpointConversationEntry{{Role: "user", Text: original}},
		workingNotes: []string{
			"NOTE-A " + strings.Repeat("a", 500),
			"NOTE-B " + strings.Repeat("b", 500),
			"NOTE-C " + strings.Repeat("c", 500),
		},
	}

	const maxChars = 1200
	cp := formatCheckpoint(data, nil, maxChars)

	if len(cp) > maxChars {
		t.Fatalf("checkpoint length %d exceeds maxChars %d", len(cp), maxChars)
	}
	if !strings.Contains(cp, original) {
		t.Fatalf("the original task should survive intact:\n%s", cp)
	}
	if !strings.Contains(cp, "NOTE-C") {
		t.Fatalf("the newest working note should survive:\n%s", cp)
	}
	if strings.Contains(cp, "NOTE-A") {
		t.Fatalf("the oldest working note should be shed first:\n%s", cp)
	}
}

func TestCheckpoint_ShedOrder_SkipsWhitespaceUserEntry(t *testing.T) {
	// A whitespace-only user turn must not become the pinned original task and
	// redirect shedding onto the real one.
	original := "ORIGINAL TASK: " + strings.Repeat("t", 400)
	chatter := "chatter " + strings.Repeat("c", 400)
	recent := "recent " + strings.Repeat("r", 400)
	data := checkpointData{
		conversation: []checkpointConversationEntry{
			{Role: "user", Text: "   "},
			{Role: "user", Text: original},
			{Role: "agent", Text: chatter},
			{Role: "agent", Text: recent},
		},
	}

	cp := formatCheckpoint(data, nil, 100)

	if !strings.Contains(cp, original) {
		t.Fatalf("the real original task must survive a leading whitespace entry:\n%s", cp)
	}
	if strings.Contains(cp, chatter) {
		t.Fatalf("the oldest entry after the original task should be shed:\n%s", cp)
	}
}

func TestCheckpoint_ShedOrder_DropsOversizedWorkingNote(t *testing.T) {
	// A lone working note larger than the budget is shed like any other note
	// rather than allowed to blow the size cap: notes have the lowest priority.
	data := checkpointData{
		conversation: []checkpointConversationEntry{{Role: "user", Text: "task"}},
		workingNotes: []string{"NOTE " + strings.Repeat("n", 3000)},
	}

	const maxChars = 1200
	cp := formatCheckpoint(data, nil, maxChars)

	if len(cp) > maxChars {
		t.Fatalf("checkpoint length %d exceeds maxChars %d", len(cp), maxChars)
	}
	if !strings.Contains(cp, "task") {
		t.Fatalf("the conversation should survive:\n%s", cp)
	}
	if strings.Contains(cp, "NOTE ") {
		t.Fatalf("the oversized lone note should be shed:\n%s", cp)
	}
}
