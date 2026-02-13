package agent

import (
	"context"

	"primeradiant.com/serf/llm"
)

// ContextStrategy defines how a session manages context pressure.
type ContextStrategy interface {
	// ManageContext is called before each LLM request. It may modify history
	// in place to reduce context pressure.
	ManageContext(ctx context.Context, history *[]Turn, pressure float64, sysPromptChars int, emitFn func(EventKind, any)) error

	// AfterAction is called after each completed tool round.
	AfterAction(ctx context.Context, history []Turn, client *llm.Client) error

	// Tools returns additional tool definitions this strategy wants registered.
	Tools() []RegisteredTool

	// Name returns the strategy identifier for config/logging.
	Name() string
}

// CompactStrategy wraps the existing 4-layer progressive compaction.
type CompactStrategy struct {
	cm *ContextManager
}

func NewCompactStrategy(cm *ContextManager) *CompactStrategy {
	return &CompactStrategy{cm: cm}
}

func (s *CompactStrategy) Name() string { return "compact" }

func (s *CompactStrategy) Tools() []RegisteredTool { return nil }

func (s *CompactStrategy) AfterAction(ctx context.Context, history []Turn, client *llm.Client) error {
	return nil
}

func (s *CompactStrategy) ManageContext(ctx context.Context, history *[]Turn, pressure float64, sysPromptChars int, emitFn func(EventKind, any)) error {
	s.cm.MaybeCompact(ctx, history, sysPromptChars, emitFn)
	return nil
}
