package agent

import (
	"context"

	"primeradiant.com/serf/llm"
)

// StrategyHost is the narrow surface a ContextStrategy needs from its owning
// session. Strategies depend on this interface rather than on *Session, which
// breaks the context⇄session back-cycle: the strategy_*.go files no longer
// reference the concrete *Session type. *Session satisfies StrategyHost.
type StrategyHost interface {
	Emit(kind EventKind, data any)
	WithResponseSideEffects(ctx context.Context, fn func()) error
	StateDir() string
	ID() string
	Profile() ProviderProfile
	// Snapshot and Client are used by the recall tool to persist a transcript
	// and run the search sub-agent.
	Snapshot() SessionSnapshot
	Client() *llm.Client
}

// ContextStrategy defines how a session manages context pressure.
type ContextStrategy interface {
	// ManageContext is called before each LLM request. It may modify history
	// in place to reduce context pressure.
	ManageContext(ctx context.Context, history *[]Turn, sysPromptChars int, emitFn func(EventKind, any)) error

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

func (s *CompactStrategy) ManageContext(ctx context.Context, history *[]Turn, sysPromptChars int, emitFn func(EventKind, any)) error {
	s.cm.MaybeCompact(ctx, history, sysPromptChars, emitFn)
	return nil
}
