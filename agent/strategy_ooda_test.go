package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
)

func TestOODAStrategy_SatisfiesInterface(t *testing.T) {
	var _ ContextStrategy = (*OODAStrategy)(nil)
}

func TestOODAStrategy_Name(t *testing.T) {
	s := &OODAStrategy{}
	if s.Name() != "ooda" {
		t.Errorf("expected name %q, got %q", "ooda", s.Name())
	}
}

func TestOODAStrategy_Tools_InheritsFromSessionLogStrategy(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log.jsonl")
	sls := &SessionLogStrategy{
		log: NewSessionLog(logPath),
	}
	ooda := &OODAStrategy{
		SessionLogStrategy: sls,
	}

	tools := ooda.Tools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Definition.Name != "recall" {
		t.Errorf("expected tool name %q, got %q", "recall", tools[0].Definition.Name)
	}
}

func TestOODAStrategy_ManageContext_NoOrientMessageWhenLogEmpty(t *testing.T) {
	client := llm.NewClient()
	profile := NewOpenAIProfile("gpt-5.2")
	cm := NewContextManager(profile, client)

	// Set thresholds high so no compaction occurs.
	cm.ObservationMaskThreshold = 0.99
	cm.ThinkingClearThreshold = 0.99
	cm.CheckpointThreshold = 0.99
	cm.SummarizeThreshold = 0.99
	cm.PreserveRecentTurns = 2

	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log.jsonl")
	sessionLog := NewSessionLog(logPath)

	ooda := &OODAStrategy{
		SessionLogStrategy: &SessionLogStrategy{
			cm:  cm,
			log: sessionLog,
		},
	}

	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("hello")},
		{Kind: TurnAssistant, Message: llm.Assistant("world")},
	}

	err := ooda.ManageContext(context.Background(), &history, 0, 0, func(EventKind, any) {})
	if err != nil {
		t.Fatalf("ManageContext returned error: %v", err)
	}

	// Should not add an orient message when log is empty.
	if len(history) != 2 {
		t.Errorf("expected 2 turns (no orient message), got %d", len(history))
	}
}

func TestOODAStrategy_ManageContext_InjectsOrientMessageWhenLogHasEntries(t *testing.T) {
	client := llm.NewClient()
	profile := NewOpenAIProfile("gpt-5.2")
	cm := NewContextManager(profile, client)

	// Set thresholds high so no compaction occurs.
	cm.ObservationMaskThreshold = 0.99
	cm.ThinkingClearThreshold = 0.99
	cm.CheckpointThreshold = 0.99
	cm.SummarizeThreshold = 0.99
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

	ooda := &OODAStrategy{
		SessionLogStrategy: &SessionLogStrategy{
			cm:  cm,
			log: sessionLog,
		},
	}

	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("hello")},
		{Kind: TurnAssistant, Message: llm.Assistant("world")},
	}

	err := ooda.ManageContext(context.Background(), &history, 0, 0, func(EventKind, any) {})
	if err != nil {
		t.Fatalf("ManageContext returned error: %v", err)
	}

	// Should add an orient message at the end.
	if len(history) != 3 {
		t.Fatalf("expected 3 turns (including orient message), got %d", len(history))
	}

	// Last turn should be the orient message.
	orientTurn := history[len(history)-1]
	if orientTurn.Kind != TurnSteering {
		t.Errorf("expected last turn to be TurnSteering, got %v", orientTurn.Kind)
	}

	orientText := orientTurn.Message.Text()
	if !strings.Contains(orientText, "[SESSION ORIENTATION]") {
		t.Errorf("expected orient message header, got: %s", orientText)
	}
	if !strings.Contains(orientText, "Ran git status") {
		t.Errorf("expected session log entry in orient message, got: %s", orientText)
	}
	if !strings.Contains(orientText, "Modified auth.go") {
		t.Errorf("expected second session log entry in orient message, got: %s", orientText)
	}
	if !strings.Contains(orientText, "[END ORIENTATION]") {
		t.Errorf("expected orient message footer, got: %s", orientText)
	}
}

func TestOODAStrategy_ManageContext_TruncatesVeryLargeLog(t *testing.T) {
	client := llm.NewClient()
	profile := NewOpenAIProfile("gpt-5.2")
	cm := NewContextManager(profile, client)

	// Set thresholds high so no compaction occurs.
	cm.ObservationMaskThreshold = 0.99
	cm.ThinkingClearThreshold = 0.99
	cm.CheckpointThreshold = 0.99
	cm.SummarizeThreshold = 0.99
	cm.PreserveRecentTurns = 2

	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log.jsonl")
	sessionLog := NewSessionLog(logPath)

	// Create a large log entry that exceeds 80k chars.
	largeSummary := strings.Repeat("x", 85000)
	_ = sessionLog.Append(SessionLogEntry{
		Turn:    1,
		Action:  "shell",
		Summary: largeSummary,
		Outcome: "success",
	})

	ooda := &OODAStrategy{
		SessionLogStrategy: &SessionLogStrategy{
			cm:  cm,
			log: sessionLog,
		},
	}

	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("hello")},
		{Kind: TurnAssistant, Message: llm.Assistant("world")},
	}

	err := ooda.ManageContext(context.Background(), &history, 0, 0, func(EventKind, any) {})
	if err != nil {
		t.Fatalf("ManageContext returned error: %v", err)
	}

	// Should add an orient message.
	if len(history) != 3 {
		t.Fatalf("expected 3 turns, got %d", len(history))
	}

	orientText := history[len(history)-1].Message.Text()
	if !strings.Contains(orientText, "[session log truncated, use recall tool for details]") {
		t.Errorf("expected truncation notice in orient message, got: %s", orientText[:min(500, len(orientText))])
	}
}

func TestOODAStrategy_ManageContext_AppliesCompactionLayers(t *testing.T) {
	client := llm.NewClient()
	profile := NewOpenAIProfile("gpt-5.2")
	cm := NewContextManager(profile, client)

	// Set low thresholds to trigger compaction.
	cm.ObservationMaskThreshold = 0.05
	cm.ThinkingClearThreshold = 0.95
	cm.CheckpointThreshold = 0.95
	cm.SummarizeThreshold = 0.99
	cm.PreserveRecentTurns = 2

	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log.jsonl")
	sessionLog := NewSessionLog(logPath)

	_ = sessionLog.Append(SessionLogEntry{
		Turn:    1,
		Action:  "shell",
		Summary: "Did something",
		Outcome: "success",
	})

	ooda := &OODAStrategy{
		SessionLogStrategy: &SessionLogStrategy{
			cm:  cm,
			log: sessionLog,
		},
	}

	// Build a history with large content to trigger compaction.
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

	err := ooda.ManageContext(context.Background(), &history, 0, 0, emitFn)
	if err != nil {
		t.Fatalf("ManageContext returned error: %v", err)
	}

	// Should have applied compaction (inherited from SessionLogStrategy).
	if len(emittedLayers) == 0 {
		t.Fatal("expected at least one compaction event")
	}
	if emittedLayers[0] != "observation_mask" {
		t.Errorf("expected first layer to be observation_mask, got %q", emittedLayers[0])
	}

	// Should also have injected orient message at the end.
	lastTurn := history[len(history)-1]
	if lastTurn.Kind != TurnSteering {
		t.Errorf("expected last turn to be TurnSteering, got %v", lastTurn.Kind)
	}
	if !strings.Contains(lastTurn.Message.Text(), "[SESSION ORIENTATION]") {
		t.Errorf("expected orient message at end after compaction")
	}
}

func TestOODAStrategy_AfterAction_InheritsFromSessionLogStrategy(t *testing.T) {
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

	ooda := &OODAStrategy{
		SessionLogStrategy: &SessionLogStrategy{
			cm:      NewContextManager(profile, client),
			log:     NewSessionLog(logPath),
			session: &Session{profile: profile},
		},
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

	err := ooda.AfterAction(context.Background(), turns, client)
	if err != nil {
		t.Fatalf("AfterAction: %v", err)
	}

	// Verify the session log has the entry (inherited behavior).
	entries := ooda.log.Entries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(entries))
	}
	if entries[0].Action != "shell" {
		t.Errorf("expected action %q, got %q", "shell", entries[0].Action)
	}
}
