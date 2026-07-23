package agent

import "primeradiant.com/serf/llm"

// WorkMillisSnapshot returns the session's accumulated wall-clock work time
// across all completed turns (and any dying turn accumulated by Close()
// mid-turn), in milliseconds. It does not include the in-flight turn's
// elapsed time — that is only added at the next processing boundary
// (finishProcessingAtBoundary) or on Close() mid-turn. Pull-callback-fed
// counterpart to Meta().WorkMillis, for callers that want the live metric
// without paying for a full Meta() snapshot (WS2 A7).
func (s *Session) WorkMillisSnapshot() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.workMillis
}

// ActiveTurnStartedAtMillis returns the Unix timestamp in milliseconds the
// in-flight turn began, or 0 when no turn is running. Milliseconds is the wire
// contract for appwire.SerfThread.ActiveTurnStartedAt (the web reducer reads it
// as epoch-ms; the sibling WorkMillis is likewise ms) — emitting seconds here
// mixes units with the frontend's ms `now` and clocks a ~500000h phantom span.
// Mirrors the same state-plus-turnStartedAt guard as accumulateWorkLocked (WS2 A7).
func (s *Session) ActiveTurnStartedAtMillis() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == SessionProcessing && !s.turnStartedAt.IsZero() {
		return s.turnStartedAt.UnixMilli()
	}
	return 0
}

// CumulativeUsageSnapshot returns the session's running self-only token
// totals from the context manager. Returns the zero llm.Usage when the
// context manager is not initialized, mirroring ContextPressure's nil-guard.
// Pull-callback-fed counterpart to Meta().CumulativeUsage (WS2 A7).
func (s *Session) CumulativeUsageSnapshot() llm.Usage {
	if s.contextMgr == nil {
		return llm.Usage{}
	}
	return s.contextMgr.CumulativeUsage()
}
