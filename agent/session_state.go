package agent

import (
	"context"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/schema"
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

// Meta returns the current session metadata without the conversation history.
func (s *Session) Meta() schema.SessionMeta {
	originalPrompt := s.extractOriginalPrompt()

	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	parentID := s.cfg.spawn.parentSessionID
	divergence := 0
	isSubagent := s.cfg.spawn.parentSessionID != ""
	if s.fork.divergence > 0 {
		parentID = s.fork.parentID
		divergence = s.fork.divergence
		isSubagent = false
	}
	return schema.SessionMeta{
		ID:              s.id,
		ProfileID:       s.profile.ID(),
		Model:           s.profile.Model(),
		CheapModel:      s.profile.CheapModelRefString(),
		Config:          s.cfg.toSnapshot(),
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
		ForkLabel:       s.fork.label,
		IsSubagent:      isSubagent,
		Goal:            s.goalSnapshotForMeta(),
	}
}

// goalSnapshotForMeta calls PersistSnapshot on the goal store and maps the
// resulting primitives to a *schema.GoalSnapshot. Returns nil when no goal is
// set (PersistSnapshot ok==false). Called from Meta() which holds s.mu; the
// goal store has its own independent mutex, so there is no lock-order issue.
func (s *Session) goalSnapshotForMeta() *schema.GoalSnapshot {
	obj, status, stopReason, iters, streak, madeProgress, created, updated, ok := s.getOrCreateGoalStore().PersistSnapshot()
	if !ok {
		return nil
	}
	return &schema.GoalSnapshot{
		Objective:        obj,
		Status:           status,
		Iterations:       iters,
		NoProgressStreak: streak,
		MadeProgressOnce: madeProgress,
		StopReason:       stopReason,
		CreatedAt:        created,
		UpdatedAt:        updated,
	}
}

// ContextPressure returns the estimated context pressure as a fraction (0.0–1.0).
// Returns 0 if the context manager is not initialized.
func (s *Session) ContextPressure() float64 {
	if s.contextMgr == nil {
		return 0
	}
	s.mu.Lock()
	hist := append([]schema.Turn{}, s.history...)
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

func (s *Session) finishProcessingAtBoundary(ctx context.Context, state SessionState) {
	transitioned := false
	s.mu.Lock()
	if s.state == SessionProcessing && !s.closingOrClosedLocked() {
		s.state = state
		transitioned = true
	}
	s.mu.Unlock()
	if transitioned {
		if err := s.drainPendingWatchSends(ctx); err != nil {
			s.emit(events.EventWarning, events.WarningData{Message: "watch send retry at processing boundary failed: " + err.Error()})
		}
	}
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
