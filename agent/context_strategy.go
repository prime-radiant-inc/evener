package agent

import (
	"context"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// strategyHost is the narrow surface a contextStrategy needs from its owning
// session. Strategies depend on this interface rather than on *Session, which
// breaks the context⇄session back-cycle: the strategy_*.go files no longer
// reference the concrete *Session type. *Session satisfies strategyHost.
type strategyHost interface {
	emit(kind events.EventKind, data events.EventData)
	withResponseSideEffects(ctx context.Context, fn func()) error
	StateDir() string
	ID() string
	Profile() *provider.Profile
	// Snapshot and Client are used by the recall tool to persist a transcript
	// and run the search sub-agent.
	Snapshot() SessionSnapshot
	Client() *llm.Client
}

// contextStrategy defines how a session manages context pressure.
type contextStrategy interface {
	// ManageContext is called before each LLM request. It may modify history
	// in place to reduce context pressure.
	ManageContext(ctx context.Context, history *[]schema.Turn, sysPromptChars int, emitFn func(events.EventKind, events.EventData)) error

	// AfterAction is called after each completed tool round.
	AfterAction(ctx context.Context, history []schema.Turn, client *llm.Client) error

	// Tools returns additional tool definitions this strategy wants registered.
	Tools() []tool.RegisteredTool

	// Name returns the strategy identifier for config/logging.
	Name() string
}

// compactStrategy wraps the existing 4-layer progressive compaction.
type compactStrategy struct {
	cm *contextManager
}

// newCompactStrategy returns a compactStrategy backed by the given contextManager.
func newCompactStrategy(cm *contextManager) *compactStrategy {
	return &compactStrategy{cm: cm}
}

// Name returns the strategy identifier "compact".
func (s *compactStrategy) Name() string { return "compact" }

// Tools returns no additional tool definitions for this strategy.
func (s *compactStrategy) Tools() []tool.RegisteredTool { return nil }

// AfterAction does nothing for this strategy and always returns nil.
func (s *compactStrategy) AfterAction(ctx context.Context, history []schema.Turn, client *llm.Client) error {
	return nil
}

// ManageContext delegates to the contextManager's MaybeCompact to reduce context
// pressure before an LLM request and returns nil.
func (s *compactStrategy) ManageContext(ctx context.Context, history *[]schema.Turn, sysPromptChars int, emitFn func(events.EventKind, events.EventData)) error {
	s.cm.MaybeCompact(ctx, history, sysPromptChars, emitFn)
	return nil
}
