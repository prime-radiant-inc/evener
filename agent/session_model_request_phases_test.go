package agent

// Tests pinning what the fold phase of prepareModelRequestWithError leaves
// for the history phase: the in-flight boundary.

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
)

func prepareRound(t *testing.T, s *Session, round int) {
	t.Helper()
	var rt events.RoundTimings
	if _, _, _, _, _, _, err := s.prepareModelRequestWithError(context.Background(), round, &rt); err != nil {
		t.Fatalf("prepareModelRequestWithError: %v", err)
	}
}

func baselineAndHistoryLen(s *Session) (baseline, historyLen int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turnHistoryBaseline, len(s.history)
}

// With no context strategy there is no fold phase, so round zero must
// establish the in-flight boundary at the end of the live history itself.
func TestPrepareModelRequest_RoundZeroWithoutStrategyBoundsInFlightAtHistoryEnd(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	s.strategy = nil
	seedNumberedSessionHistory(t, s, 5)

	prepareRound(t, s, 0)

	if baseline, n := baselineAndHistoryLen(s); baseline != n {
		t.Fatalf("turnHistoryBaseline = %d, want the live history length %d", baseline, n)
	}
}

// A later round that wins a fold translates the existing boundary past the
// removed turns instead of resetting it: a boundary at the old history end
// lands at the folded history's end.
func TestPrepareModelRequest_LaterRoundFoldShiftsBoundaryWithRemovedTurns(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	seedNumberedSessionHistory(t, s, 12)
	s.elicitNoteFn = func(context.Context, []schema.Turn) (string, error) { return "", nil }
	forcePressureAbove(t, s, 0.85)
	s.mu.Lock()
	before := len(s.history)
	s.turnHistoryBaseline = before
	s.mu.Unlock()

	prepareRound(t, s, 1)

	baseline, n := baselineAndHistoryLen(s)
	if n >= before {
		t.Fatalf("test setup: fold removed nothing (%d -> %d turns)", before, n)
	}
	if baseline != n {
		t.Fatalf("turnHistoryBaseline = %d, want %d: the boundary must follow the removed turns", baseline, n)
	}
}
