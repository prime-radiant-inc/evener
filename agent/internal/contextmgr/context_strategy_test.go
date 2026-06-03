package contextmgr

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

func TestContextStrategyInterface(t *testing.T) {
	// Verify that CompactStrategy satisfies the Strategy interface.
	var _ Strategy = (*CompactStrategy)(nil)
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
	// Create a context manager with a small window so history exceeds
	// the 80% checkpoint threshold.
	client := llm.NewClient()
	profile := testProfile("openai", "test", 500)
	cm := NewManager(profile, client)
	cm.PreserveRecentTurns = 2

	cs := NewCompactStrategy(cm)

	// ~425 tokens (85% of 500) to exceed checkpoint threshold.
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("read some files")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant(strings.Repeat("analysis ", 200))},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent1")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent2")},
	}

	// Track emitted events.
	var emittedEvent events.EventKind
	emitFn := func(kind events.EventKind, data events.EventData) {
		emittedEvent = kind
	}

	// ManageContext should delegate to Manager.MaybeCompact, which should
	// emit a compaction event because pressure exceeds the threshold.
	ctx := context.Background()
	err := cs.ManageContext(ctx, &history, 0, emitFn)
	if err != nil {
		t.Fatalf("ManageContext returned error: %v", err)
	}

	// Verify that a compaction event was emitted.
	if emittedEvent != events.EventContextCompaction {
		t.Fatalf("expected EventContextCompaction to be emitted, got %v", emittedEvent)
	}

	// Verify checkpoint replaced old history.
	if history[0].Kind != schema.TurnCheckpoint {
		t.Fatalf("expected checkpoint turn, got %v", history[0].Kind)
	}
}
