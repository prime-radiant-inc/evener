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

// FailedToolCallsSnapshot returns how many of this session's tool calls have
// failed so far, and whether anyone counted — the live counterpart to the
// hub's transcript scan of a finished session (kata 12rq).
//
// The count comes from the transcript WRITER, which sees every entry as it is
// recorded and is seeded on resume from the entries already on the file. That
// makes it complete by construction for a running session, which neither
// available alternative is: re-reading the file on demand returns a floor,
// because the session is still appending to it, and counting the session's
// in-memory history sheds everything compaction summarized away. A count that
// is low but nonzero is the worst answer available here — it wears
// session-level authority while under-reporting, which is exactly the
// misreading the figure exists to prevent — so the honest fallback is silence.
//
// ok is false when there is no transcript to count (no state directory, or a
// writer that failed to open). Nil on the wire, and the client renders nothing
// rather than a fabricated "0 failed". A measured zero is a different claim and
// reports ok.
//
// Pull-callback-fed, like WorkMillisSnapshot: read on demand by the daemon's
// status projection rather than pushed on every event.
// The writer is set once during construction and read unlocked everywhere else
// in the package (see appendTurn); its own mutex guards the count.
func (s *Session) FailedToolCallsSnapshot() (int, bool) {
	return s.transcript.FailedToolCalls()
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
