package agent

import (
	"context"
	"testing"

	"primeradiant.com/serf/llm"
)

func TestContextStrategyInterface(t *testing.T) {
	// Verify that CompactStrategy satisfies the ContextStrategy interface.
	var _ ContextStrategy = (*CompactStrategy)(nil)
}

func TestCompactStrategyName(t *testing.T) {
	cs := &CompactStrategy{}
	if cs.Name() != "compact" {
		t.Errorf("expected name %q, got %q", "compact", cs.Name())
	}
}

func TestCompactStrategyTools(t *testing.T) {
	cs := &CompactStrategy{}
	if len(cs.Tools()) != 0 {
		t.Errorf("compact strategy should register no tools, got %d", len(cs.Tools()))
	}
}

func TestCompactStrategyManageContext_Delegation(t *testing.T) {
	// Create a mock client and context manager.
	client := llm.NewClient()
	profile := NewOpenAIProfile("gpt-5.2")
	cm := NewContextManager(profile, client)

	// Set a low observation mask threshold to ensure compaction triggers.
	// OpenAI gpt-5.2 has 128k context window, so at 5% we need ~6.4k tokens.
	cm.ObservationMaskThreshold = 0.05
	cm.PreserveRecentTurns = 2

	cs := NewCompactStrategy(cm)

	// Build a history that will exceed the 5% threshold.
	// We need ~6.4k tokens = ~25.6k chars total.
	// Create tool result with 30k chars to ensure we exceed the threshold.
	largeContent := make([]byte, 30000)
	for i := range largeContent {
		largeContent[i] = 'x'
	}

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
		{Kind: TurnTool, Message: llm.ToolResultNamed("call1", "read_file", string(largeContent), false)},
		{Kind: TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: "Done reading"},
		}}},
		{Kind: TurnUserInput, Message: llm.User("another task")},
		{Kind: TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: "working on it"},
		}}},
	}

	// Track emitted events.
	var emittedEvent EventKind
	emitFn := func(kind EventKind, data any) {
		emittedEvent = kind
	}

	// ManageContext should delegate to ContextManager.MaybeCompact, which should
	// emit a compaction event because pressure exceeds the threshold.
	ctx := context.Background()
	err := cs.ManageContext(ctx, &history, 0.0, 0, emitFn)
	if err != nil {
		t.Fatalf("ManageContext returned error: %v", err)
	}

	// Verify that a compaction event was emitted.
	if emittedEvent != EventContextCompaction {
		t.Fatalf("expected EventContextCompaction to be emitted, got %v", emittedEvent)
	}

	// Verify that the tool result was masked (observation masking is the first layer).
	// The content should now start with "[" indicating it was masked.
	// The old turn (index 2) should be masked; recent turns are preserved.
	toolResult := history[2].Message.Content[0].ToolResult
	if content, ok := toolResult.Content.(string); !ok || content[0] != '[' {
		t.Fatalf("expected tool result to be masked, got content: %v", toolResult.Content)
	}
}
