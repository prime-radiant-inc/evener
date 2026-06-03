package agent

import (
	"context"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/contextmgr"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/llm"
)

// ctxHost adapts a *Session to the contextmgr.Host seam. contextmgr.Host
// requires exported methods (a Go interface with unexported methods can only be
// satisfied within its own package), but the session-side primitives behind
// them — emit, withResponseSideEffects, snapshot — are deliberately internal.
// The adapter bridges the two: it exposes the exported method set contextmgr
// needs without growing Session's public surface.
type ctxHost struct{ s *Session }

var _ contextmgr.Host = (*ctxHost)(nil)

// Emit forwards an event to the session's emitter.
func (h *ctxHost) Emit(kind events.EventKind, data events.EventData) { h.s.emit(kind, data) }

// WithResponseSideEffects runs fn under the session's response side-effect lock.
func (h *ctxHost) WithResponseSideEffects(ctx context.Context, fn func()) error {
	return h.s.withResponseSideEffects(ctx, fn)
}

// Snapshot returns a full snapshot of the session for the recall search tools.
func (h *ctxHost) Snapshot() contextmgr.Snapshot { return h.s.snapshot() }

// StateDir returns the session's configured state directory.
func (h *ctxHost) StateDir() string { return h.s.StateDir() }

// ID returns the session's identifier.
func (h *ctxHost) ID() string { return h.s.ID() }

// Profile returns the session's current provider profile.
func (h *ctxHost) Profile() *provider.Profile { return h.s.Profile() }

// Client returns the session's LLM client.
func (h *ctxHost) Client() *llm.Client { return h.s.Client() }
