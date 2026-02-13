package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
)

func TestSessionLogStrategy_SatisfiesInterface(t *testing.T) {
	var _ ContextStrategy = (*SessionLogStrategy)(nil)
}

func TestSessionLogStrategy_Name(t *testing.T) {
	s := &SessionLogStrategy{}
	if s.Name() != "session-log" {
		t.Errorf("expected name %q, got %q", "session-log", s.Name())
	}
}

func TestSessionLogStrategy_Tools_RegistersRecall(t *testing.T) {
	s := &SessionLogStrategy{}
	tools := s.Tools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Definition.Name != "recall" {
		t.Errorf("expected tool name %q, got %q", "recall", tools[0].Definition.Name)
	}
	if tools[0].Definition.Description == "" {
		t.Error("expected non-empty description")
	}
	// Check that the tool has a "question" parameter.
	params := tools[0].Definition.Parameters
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected properties map, got %T", params["properties"])
	}
	if _, ok := props["question"]; !ok {
		t.Error("expected 'question' parameter in tool definition")
	}
}

func TestSessionLogStrategy_ManageContext_ObservationMaskAtHighPressure(t *testing.T) {
	client := llm.NewClient()
	profile := NewOpenAIProfile("gpt-5.2")
	cm := NewContextManager(profile, client)

	// Set a low observation mask threshold so compaction triggers on our test data.
	cm.ObservationMaskThreshold = 0.05
	cm.ThinkingClearThreshold = 0.95   // high enough to not trigger
	cm.CheckpointThreshold = 0.95      // high enough to not trigger
	cm.SummarizeThreshold = 0.99       // high enough to not trigger
	cm.PreserveRecentTurns = 2

	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log.jsonl")
	sls := &SessionLogStrategy{
		cm:  cm,
		log: NewSessionLog(logPath),
	}

	// Build a history with a large tool result that will exceed 5% threshold.
	largeContent := strings.Repeat("x", 30000)

	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("read some files")},
		{Kind: TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
					ID:        "call1",
					Name:      "read_file",
					Arguments: []byte(`{"file_path": "/tmp/test.txt"}`),
				}},
			},
		}},
		{Kind: TurnTool, Message: llm.ToolResultNamed("call1", "read_file", largeContent, false)},
		{Kind: TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: "Done reading"},
		}}},
		{Kind: TurnUserInput, Message: llm.User("another task")},
		{Kind: TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: "working on it"},
		}}},
	}

	var emittedLayers []string
	emitFn := func(kind EventKind, data any) {
		if kind == EventContextCompaction {
			if cd, ok := data.(ContextCompactionData); ok {
				emittedLayers = append(emittedLayers, cd.Layer)
			}
		}
	}

	err := sls.ManageContext(context.Background(), &history, 0, 0, emitFn)
	if err != nil {
		t.Fatalf("ManageContext returned error: %v", err)
	}

	// Verify observation masking occurred.
	if len(emittedLayers) == 0 {
		t.Fatal("expected at least one compaction event")
	}
	if emittedLayers[0] != "observation_mask" {
		t.Errorf("expected first layer to be observation_mask, got %q", emittedLayers[0])
	}

	// Verify the tool result was masked.
	toolResult := history[2].Message.Content[0].ToolResult
	content, ok := toolResult.Content.(string)
	if !ok {
		t.Fatalf("expected string content, got %T", toolResult.Content)
	}
	if !strings.HasPrefix(content, "[") {
		t.Errorf("expected masked content starting with '[', got: %s", content[:50])
	}
}

func TestSessionLogStrategy_ManageContext_SessionLogCheckpointAtHighPressure(t *testing.T) {
	client := llm.NewClient()
	profile := NewOpenAIProfile("gpt-5.2")
	cm := NewContextManager(profile, client)

	// Set thresholds so checkpoint triggers but not summarize.
	cm.ObservationMaskThreshold = 0.01
	cm.ThinkingClearThreshold = 0.01
	cm.CheckpointThreshold = 0.01
	cm.SummarizeThreshold = 0.99 // don't trigger
	cm.PreserveRecentTurns = 2

	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log.jsonl")
	sessionLog := NewSessionLog(logPath)

	// Pre-populate the session log with entries.
	_ = sessionLog.Append(SessionLogEntry{
		Turn:    1,
		Action:  "shell",
		Summary: "Ran git status to check repo state",
		Outcome: "success",
	})
	_ = sessionLog.Append(SessionLogEntry{
		Turn:         2,
		Action:       "edit_file",
		Summary:      "Modified auth.go to fix login bug",
		Outcome:      "success",
		FilesTouched: []string{"auth.go"},
	})

	sls := &SessionLogStrategy{
		cm:  cm,
		log: sessionLog,
	}

	// Build history with enough content to trigger checkpoint.
	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("fix the login bug")},
		{Kind: TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentText, Text: strings.Repeat("thinking about it... ", 500)},
			},
		}},
		{Kind: TurnUserInput, Message: llm.User("keep going")},
		{Kind: TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentText, Text: "still working"},
			},
		}},
		// These are the "recent" turns that should be preserved.
		{Kind: TurnUserInput, Message: llm.User("recent question")},
		{Kind: TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentText, Text: "recent answer"},
			},
		}},
	}

	var emittedLayers []string
	emitFn := func(kind EventKind, data any) {
		if kind == EventContextCompaction {
			if cd, ok := data.(ContextCompactionData); ok {
				emittedLayers = append(emittedLayers, cd.Layer)
			}
		}
	}

	err := sls.ManageContext(context.Background(), &history, 0, 0, emitFn)
	if err != nil {
		t.Fatalf("ManageContext returned error: %v", err)
	}

	// Verify checkpoint was applied.
	foundCheckpoint := false
	for _, layer := range emittedLayers {
		if layer == "session_log_checkpoint" {
			foundCheckpoint = true
			break
		}
	}
	if !foundCheckpoint {
		t.Fatalf("expected session_log_checkpoint layer in emitted events, got: %v", emittedLayers)
	}

	// The first turn should now be the checkpoint.
	checkpointText := history[0].Message.Text()
	if !strings.Contains(checkpointText, "[CONTEXT CHECKPOINT - SESSION LOG]") {
		t.Errorf("expected checkpoint header, got: %s", checkpointText[:min(200, len(checkpointText))])
	}
	if !strings.Contains(checkpointText, "Original task: fix the login bug") {
		t.Errorf("expected original task in checkpoint, got: %s", checkpointText)
	}
	// Verify session log entries appear in the checkpoint.
	if !strings.Contains(checkpointText, "Ran git status") {
		t.Errorf("expected session log entry in checkpoint, got: %s", checkpointText)
	}
	if !strings.Contains(checkpointText, "Modified auth.go") {
		t.Errorf("expected second session log entry in checkpoint, got: %s", checkpointText)
	}

	// Last 2 turns should be preserved (recent question + recent answer).
	lastTurn := history[len(history)-1]
	if lastTurn.Message.Text() != "recent answer" {
		t.Errorf("expected last turn to be preserved recent turn, got: %s", lastTurn.Message.Text())
	}
}

func TestSessionLogStrategy_AfterAction_CallsForkSummarizeAndAppendsToLog(t *testing.T) {
	entry := SessionLogEntry{
		Action:       "shell",
		Summary:      "Ran go test and all tests passed.",
		Outcome:      "success",
		FilesTouched: []string{"main.go"},
	}
	entryJSON, _ := json.Marshal(entry)

	adapter := &stubSummarizeAdapter{
		name: "openai",
		respFn: func(req llm.Request) (llm.Response, error) {
			return llm.Response{Message: llm.Assistant(string(entryJSON))}, nil
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	profile := NewOpenAIProfile("gpt-5.2")

	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log.jsonl")

	sls := &SessionLogStrategy{
		cm:      NewContextManager(profile, client),
		log:     NewSessionLog(logPath),
		session: &Session{profile: profile},
	}

	turns := []Turn{
		{Kind: TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
					ID:        "c1",
					Name:      "shell",
					Arguments: json.RawMessage(`{"command":"go test ./..."}`),
				}},
			},
		}},
		{Kind: TurnToolResults, Message: llm.ToolResultNamed("c1", "shell", "PASS", false)},
	}

	err := sls.AfterAction(context.Background(), turns, client)
	if err != nil {
		t.Fatalf("AfterAction: %v", err)
	}

	// Verify the session log has the entry.
	entries := sls.log.Entries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(entries))
	}
	if entries[0].Action != "shell" {
		t.Errorf("expected action %q, got %q", "shell", entries[0].Action)
	}
	if entries[0].Outcome != "success" {
		t.Errorf("expected outcome %q, got %q", "success", entries[0].Outcome)
	}
	// Turn should be set to len(turns).
	if entries[0].Turn != len(turns) {
		t.Errorf("expected turn %d, got %d", len(turns), entries[0].Turn)
	}
}

func TestSessionLogStrategy_AfterAction_LLMErrorIsNonFatal(t *testing.T) {
	adapter := &stubSummarizeAdapter{
		name: "openai",
		respFn: func(req llm.Request) (llm.Response, error) {
			return llm.Response{}, fmt.Errorf("rate limited")
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	profile := NewOpenAIProfile("gpt-5.2")

	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log.jsonl")

	sls := &SessionLogStrategy{
		cm:      NewContextManager(profile, client),
		log:     NewSessionLog(logPath),
		session: &Session{profile: profile},
	}

	turns := []Turn{
		{Kind: TurnAssistant, Message: llm.Assistant("hello")},
	}

	// Should not return an error even though LLM call fails.
	err := sls.AfterAction(context.Background(), turns, client)
	if err != nil {
		t.Fatalf("AfterAction should be non-fatal on LLM error, got: %v", err)
	}

	// Log should remain empty since the summarize failed.
	if sls.log.Len() != 0 {
		t.Errorf("expected 0 log entries after failed summarize, got %d", sls.log.Len())
	}
}

func TestSessionLogStrategy_SessionLogCheckpoint_EmptyLog(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log.jsonl")

	sls := &SessionLogStrategy{
		log: NewSessionLog(logPath),
	}

	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("do something")},
		{Kind: TurnAssistant, Message: llm.Assistant("working")},
		{Kind: TurnUserInput, Message: llm.User("recent")},
		{Kind: TurnAssistant, Message: llm.Assistant("done")},
	}

	result := sls.sessionLogCheckpoint(history, 2)

	// Should still create a checkpoint even with empty log.
	if len(result) != 3 { // checkpoint + 2 preserved
		t.Fatalf("expected 3 turns, got %d", len(result))
	}

	checkpointText := result[0].Message.Text()
	if !strings.Contains(checkpointText, "[CONTEXT CHECKPOINT - SESSION LOG]") {
		t.Errorf("expected checkpoint header in: %s", checkpointText)
	}
	if !strings.Contains(checkpointText, "Original task: do something") {
		t.Errorf("expected original task in checkpoint, got: %s", checkpointText)
	}
}

func TestSessionLogStrategy_SessionLogCheckpoint_PreservesRecentTurns(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log.jsonl")

	sls := &SessionLogStrategy{
		log: NewSessionLog(logPath),
	}

	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("original task")},
		{Kind: TurnAssistant, Message: llm.Assistant("old response")},
		{Kind: TurnUserInput, Message: llm.User("keep this")},
		{Kind: TurnAssistant, Message: llm.Assistant("and this")},
	}

	result := sls.sessionLogCheckpoint(history, 2)

	// Last 2 turns should be preserved.
	if len(result) < 3 {
		t.Fatalf("expected at least 3 turns, got %d", len(result))
	}
	last := result[len(result)-1]
	if last.Message.Text() != "and this" {
		t.Errorf("expected last preserved turn text %q, got %q", "and this", last.Message.Text())
	}
	secondLast := result[len(result)-2]
	if secondLast.Message.Text() != "keep this" {
		t.Errorf("expected second-to-last turn text %q, got %q", "keep this", secondLast.Message.Text())
	}
}

func TestSessionLogStrategy_SessionLogCheckpoint_TooFewTurns(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log.jsonl")

	sls := &SessionLogStrategy{
		log: NewSessionLog(logPath),
	}

	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("only one")},
		{Kind: TurnAssistant, Message: llm.Assistant("response")},
	}

	// With preserveRecent=2 and only 2 turns, no checkpoint should happen.
	result := sls.sessionLogCheckpoint(history, 2)
	if len(result) != 2 {
		t.Fatalf("expected original 2 turns when too few for checkpoint, got %d", len(result))
	}
}
