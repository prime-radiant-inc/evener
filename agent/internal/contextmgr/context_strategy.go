package contextmgr

import (
	"context"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// Host is the narrow surface a Strategy needs from its owning session.
// Strategies depend on this interface rather than on the concrete session type,
// which breaks the context⇄session back-cycle. Its methods are exported because
// a Go interface with unexported methods can only be satisfied from within its
// own package; package agent supplies an adapter (ctxHost) that forwards these
// to its *Session.
type Host interface {
	Emit(kind events.EventKind, data events.EventData)
	WithResponseSideEffects(ctx context.Context, fn func()) error
	StateDir() string
	ID() string
	Profile() *provider.Profile
}

// Strategy defines how a session manages context pressure.
type Strategy interface {
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

// CompactStrategy wraps the existing 4-layer progressive compaction.
type CompactStrategy struct {
	cm *Manager
}

// NewCompactStrategy returns a CompactStrategy backed by the given Manager.
func NewCompactStrategy(cm *Manager) *CompactStrategy {
	return &CompactStrategy{cm: cm}
}

// Name returns the strategy identifier "compact".
func (s *CompactStrategy) Name() string { return "compact" }

// Tools returns no additional tool definitions for this strategy.
func (s *CompactStrategy) Tools() []tool.RegisteredTool { return nil }

// AfterAction does nothing for this strategy and always returns nil.
func (s *CompactStrategy) AfterAction(ctx context.Context, history []schema.Turn, client *llm.Client) error {
	return nil
}

// ManageContext delegates to the Manager's MaybeCompact to reduce context
// pressure before an LLM request and returns nil.
func (s *CompactStrategy) ManageContext(ctx context.Context, history *[]schema.Turn, sysPromptChars int, emitFn func(events.EventKind, events.EventData)) error {
	s.cm.MaybeCompact(ctx, history, sysPromptChars, emitFn)
	return nil
}
