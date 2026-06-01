package agent

import (
	"context"
	"time"

	"primeradiant.com/serf/agent/events"
)

// SessionState represents the current lifecycle state of a session.
type SessionState string

const (
	// SessionIdle indicates the session is not currently processing or awaiting input.
	SessionIdle SessionState = "idle"
	// SessionProcessing indicates the session is actively processing.
	SessionProcessing SessionState = "active"
	// SessionAwaitingInput indicates the session is waiting for input.
	SessionAwaitingInput SessionState = "awaiting"
	// SessionClosed indicates the session has been closed.
	SessionClosed SessionState = "closed"
)

// State returns the current session state.
func (s *Session) State() SessionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Snapshot captures the current session state as a SessionSnapshot.
func (s *Session) Snapshot() SessionSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	return SessionSnapshot{
		ID:              s.id,
		ProfileID:       s.profile.ID(),
		Model:           s.profile.Model(),
		Config:          s.cfg,
		EnvInfo:         s.envInfo,
		History:         append([]Turn{}, s.history...),
		CreatedAt:       now,
		UpdatedAt:       now,
		TurnCount:       s.modelResponses,
		LastInputTokens: s.contextMgr.LastInputTokens(),
	}
}

// Meta returns the current session metadata without the conversation history.
func (s *Session) Meta() SessionMeta {
	originalPrompt := s.extractOriginalPrompt()

	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	parentID := s.cfg.spawn.parentSessionID
	divergence := 0
	isSubagent := s.cfg.spawn.parentSessionID != ""
	if s.forkDivergence > 0 {
		parentID = s.forkParentID
		divergence = s.forkDivergence
		isSubagent = false
	}
	return SessionMeta{
		ID:              s.id,
		ProfileID:       s.profile.ID(),
		Model:           s.profile.Model(),
		Config:          s.cfg,
		EnvInfo:         s.envInfo,
		CreatedAt:       now,
		UpdatedAt:       now,
		TurnCount:       s.modelResponses,
		LastInputTokens: s.contextMgr.LastInputTokens(),
		Name:            s.naming.value,
		NameSource:      s.naming.source,
		NameUpdatedAt:   s.naming.updated,
		OriginalPrompt:  originalPrompt,
		ParentSessionID: parentID,
		DivergenceTurn:  divergence,
		ForkLabel:       s.forkLabel,
		IsSubagent:      isSubagent,
	}
}

// ContextPressure returns the estimated context pressure as a fraction (0.0–1.0).
// Returns 0 if the context manager is not initialized.
func (s *Session) ContextPressure() float64 {
	if s.contextMgr == nil {
		return 0
	}
	s.mu.Lock()
	hist := append([]Turn{}, s.history...)
	s.mu.Unlock()
	return s.contextMgr.Pressure(hist, 0)
}

func (s *Session) closingOrClosedLocked() bool {
	return s.closing || s.state == SessionClosed
}

func (s *Session) setStateIfOpenLocked(state SessionState) {
	if s.closingOrClosedLocked() {
		return
	}
	s.state = state
}

func (s *Session) abortIfClosing(ctx context.Context) error {
	s.mu.Lock()
	closing := s.closingOrClosedLocked()
	s.mu.Unlock()
	if closing {
		return context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (s *Session) errIfClosing() error {
	s.mu.Lock()
	closing := s.closingOrClosedLocked()
	s.mu.Unlock()
	if closing {
		return context.Canceled
	}
	return nil
}

func (s *Session) abortResponseProcessing(ctx context.Context) error {
	s.mu.Lock()
	closing := s.closingOrClosedLocked()
	s.mu.Unlock()
	if closing {
		return context.Canceled
	}
	if err := ctx.Err(); err != nil {
		s.emit(events.EventError, errorDataFromError(err))
		return err
	}
	return nil
}

func (s *Session) withResponseSideEffects(ctx context.Context, fn func()) error {
	s.responseSideEffectsMu.Lock()
	defer s.responseSideEffectsMu.Unlock()
	if err := s.abortResponseProcessing(ctx); err != nil {
		return err
	}
	fn()
	return nil
}

func (s *Session) isClosingOrClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closingOrClosedLocked()
}
