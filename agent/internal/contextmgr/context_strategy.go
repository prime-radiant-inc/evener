package contextmgr

import (
	"context"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
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

	// AfterAction is called after each completed tool round. A non-nil error
	// reports an auxiliary failure the session surfaces as a deduplicated
	// warning, not a turn failure; a cadence-gated strategy re-reports its
	// recorded failure (auxFailure) from a skipped turn so the session does
	// not read the skip as recovery.
	AfterAction(ctx context.Context, history []schema.Turn, client *llm.Client) error

	// Tools returns additional tool definitions this strategy wants registered.
	Tools() []tool.RegisteredTool

	// Name returns the strategy identifier for config/logging.
	Name() string
}

// auxFailure records an opt-in memory strategy's most recent auxiliary
// summarization failure. A strategy returns a failure to its caller (which
// surfaces it as a deduplicated warning) and re-reports the recorded failure
// from a call that does no work -- a turn its own cadence guard skipped an
// attempt on -- so a persistent auxiliary outage stays one warning instead of
// re-warning when the next eligible action retries. A successful auxiliary
// call clears it.
type auxFailure struct {
	err error
}

func (a *auxFailure) record(err error) error {
	a.err = err
	return err
}

func (a *auxFailure) clear() { a.err = nil }

func (a *auxFailure) stale() error { return a.err }

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
