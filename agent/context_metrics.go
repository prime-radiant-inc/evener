package agent

import (
	"primeradiant.com/evener/agent/internal/contextmgr"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
)

// ContextMetrics describes the estimated current context size, all in tokens.
// It is an alias for schema.ContextMetrics so the type that callers (the hub
// and serve.go) already consume off package agent stays identical after the
// underlying struct moved to schema.
type ContextMetrics = schema.ContextMetrics

// ContextStatus returns one snapshot's estimated context use and pressure.
//
// The history copy is shallow: the new slice shares each turn's nested values.
// That is safe under the existing invariant that published turns are replaced,
// not mutated in place. ContextStatus relies on that same invariant and does
// not introduce a stronger immutability guarantee.
//
// The snapshot is taken under s.mu's read lock and the estimate runs after the
// unlock. The lock is a reader, not a writer, because the sample must not exclude
// the emit path's provenance read: on a read/write lock a pending writer blocks new
// readers, so an exclusive sample here stalls every concurrent emit for the whole
// sample. The context manager's own lock is taken inside this one (ObserveHistory),
// and contextmgr never calls back into Session, so the pair has no reverse edge to
// cycle on.
func (s *Session) ContextStatus() (ContextMetrics, float64) {
	if s.contextMgr == nil {
		return ContextMetrics{}, 0
	}
	s.mu.RLock()
	// Copy only the turns the manager has not walked: an append leaves the
	// revision alone, so the ordinary post-append sample copies the appended
	// tail rather than the transcript. The copy was the largest part of a
	// sample's cost at scale (8.24 MB of 8.96 MB at 10,000 turns), and its only
	// purpose is to let the estimate run unlocked.
	covered, _ := s.contextMgr.ObserveHistory(s.historyRevision, len(s.history))
	var hist []schema.Turn
	if covered < len(s.history) {
		hist = append([]schema.Turn{}, s.history[covered:]...)
	}
	histLen := len(s.history)
	revision := s.historyRevision
	s.mu.RUnlock()
	// The manager holds the profile that answered the last round (a fallback
	// replaces it), so its window, usage and source come from one snapshot.
	metrics, pressure, profile, ok := s.contextMgr.EstimateStatusForHistory(hist, histLen, 0, revision)
	if !ok {
		// The memo could not answer for the whole history. Take the copy and ask
		// again rather than report a prefix.
		s.mu.RLock()
		hist = append([]schema.Turn{}, s.history...)
		histLen = len(s.history)
		revision = s.historyRevision
		s.mu.RUnlock()
		metrics, pressure, profile, _ = s.contextMgr.EstimateStatusForHistory(hist, histLen, 0, revision)
	}
	_, metrics.WindowSource = budgetingWindow(profile)
	return metrics, pressure
}

// budgetingWindow is the window a session on profile budgets against and where
// it came from: the profile's own window, or the context manager's assumed one
// when the model's is unknown.
func budgetingWindow(profile *provider.Profile) (window int, source string) {
	if profile == nil {
		return 0, ""
	}
	window, _ = contextmgr.EffectiveWindow(profile)
	return window, profile.ContextWindowSource()
}

// contextMetrics returns the estimated context use for this session. Each call
// takes one full sample.
func (s *Session) contextMetrics() ContextMetrics {
	metrics, _ := s.ContextStatus()
	return metrics
}
