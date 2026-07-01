package contextmgr

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// w3subCommunicateTurn builds an assistant turn carrying a single communicate
// tool call with an explicit end_turn flag and message, so the summarizer's
// per-turn extraction can be driven through its Agent Message / Agent Status /
// empty-message arms deterministically.
func w3subCommunicateTurn(endTurn bool, message string) schema.Turn {
	raw, _ := json.Marshal(map[string]any{"message": message, "end_turn": endTurn})
	return schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "c1", Name: "communicate", Arguments: raw, Type: "function"}},
	}})
}

// summarizeWithLLMSteered's history digest walks every turn shape. This drives
// its assistant communicate-extraction arms (end_turn message vs status vs
// skipped empty message), the oversized tool-result truncation, and the
// steering arm, then confirms a framed SUMMARY turn replaced the walked prefix.
func TestW3Sub_SummarizeSteered_DigestArms(t *testing.T) {
	longResult := strings.Repeat("z", 300) // > 200 chars -> truncation arm
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("kick off the task")),
		w3subCommunicateTurn(true, "final answer to the user"),
		w3subCommunicateTurn(false, "still working on it"),
		w3subCommunicateTurn(true, ""), // empty message -> skipped
		schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("t1", "shell", longResult, false)),
		schema.NewTurn(schema.TurnSteering, llm.User("stay focused")),
		schema.NewTurn(schema.TurnUserInput, llm.User("most recent message")),
	}

	profile := testOpenAIProfileWithContextWindow(1000)
	client := ctxmgr_scriptedClient(profile.ID(), "handoff summary body", nil)
	cm := NewManager(profile, client)

	result, err := cm.summarizeWithLLMSteered(context.Background(), history, 1, "keep the plan")
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if len(result) == 0 || result[0].Kind != schema.TurnSummary {
		t.Fatalf("expected a leading SUMMARY turn, got %+v", result)
	}
	head := result[0].Message.Text()
	if !strings.HasPrefix(head, "[CONTEXT SUMMARY]\n") || !strings.HasSuffix(head, "[END SUMMARY]") {
		t.Fatalf("summary not framed: %q", head)
	}
	// The last turn is preserved verbatim.
	if result[len(result)-1].Message.Text() != "most recent message" {
		t.Fatalf("recent turn not preserved: %q", result[len(result)-1].Message.Text())
	}
}

// The digest caps at maxHistoryChars (80k) and appends a truncation marker
// before breaking out of the walk. A history whose walked prefix overflows that
// cap drives that break arm.
func TestW3Sub_SummarizeSteered_HistoryCharCap(t *testing.T) {
	big := strings.Repeat("y", 500) // assistant text is capped at 500 chars each
	history := make([]schema.Turn, 0, 320)
	for i := 0; i < 300; i++ {
		history = append(history, schema.NewTurn(schema.TurnAssistant, llm.Assistant(big)))
	}
	history = append(history, schema.NewTurn(schema.TurnUserInput, llm.User("tail")))

	profile := testOpenAIProfileWithContextWindow(1000)
	client := ctxmgr_scriptedClient(profile.ID(), "summary", nil)
	cm := NewManager(profile, client)

	result, err := cm.summarizeWithLLMSteered(context.Background(), history, 1, "")
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if len(result) == 0 || result[0].Kind != schema.TurnSummary {
		t.Fatalf("expected a SUMMARY turn")
	}
}

// A profile with neither an active nor a configured cheap model yields no
// summarization routes, so the summarizer reports the empty-model error rather
// than attempting a call.
func TestW3Sub_SummarizeSteered_NoRoutes(t *testing.T) {
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("older")),
		schema.NewTurn(schema.TurnUserInput, llm.User("recent")),
	}
	profile := provider.NewOpenAIProfile("") // empty model -> no routes
	client := ctxmgr_scriptedClient("openai", "unused", nil)
	cm := NewManager(profile, client)

	if _, err := cm.summarizeWithLLMSteered(context.Background(), history, 1, ""); err == nil {
		t.Fatalf("expected an empty-model error")
	}
}

// formatCheckpoint sheds oldest conversation entries once working notes are
// exhausted and the variable budget floors at its 1000-char minimum. A tiny
// maxChars with more than three shell results and two oversized conversation
// entries drives the >3-shell tail slice, the budget floor, and the
// conversation-shedding loop (drop + final break).
func TestW3Sub_FormatCheckpoint_ShedAndFloorArms(t *testing.T) {
	data := checkpointData{
		lastShellResults: []string{"r1", "r2", "r3", "r4", "r5"}, // > 3 -> tail slice
		conversation: []checkpointConversationEntry{
			{Role: "user", Text: strings.Repeat("x", 2000)},
			{Role: "agent", Text: strings.Repeat("y", 2000)},
		},
	}

	cp := formatCheckpoint(data, nil, 100) // tiny budget -> floors to 1000

	if !strings.HasPrefix(cp, "[CONTEXT CHECKPOINT]\n") || !strings.HasSuffix(cp, "[END CHECKPOINT]\n") {
		t.Fatalf("checkpoint frame missing: %q", cp[:min(40, len(cp))])
	}
	// Only the three most recent shell results are rendered.
	if strings.Contains(cp, "r1\n") || strings.Contains(cp, "r2\n") {
		t.Fatalf("expected oldest shell results to be dropped:\n%s", cp)
	}
	if !strings.Contains(cp, "r5") {
		t.Fatalf("expected most recent shell result retained:\n%s", cp)
	}
	// The oldest conversation entry is shed; the newest survives.
	if strings.Contains(cp, strings.Repeat("x", 2000)) {
		t.Fatalf("oldest conversation entry should have been shed")
	}
	if !strings.Contains(cp, strings.Repeat("y", 2000)) {
		t.Fatalf("newest conversation entry should survive")
	}
}
