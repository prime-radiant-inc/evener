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
// The copy is taken under s.mu and the estimate runs after the unlock.
func (s *Session) ContextStatus() (ContextMetrics, float64) {
	if s.contextMgr == nil {
		return ContextMetrics{}, 0
	}
	s.mu.Lock()
	hist := append([]schema.Turn{}, s.history...)
	s.mu.Unlock()
	// The manager holds the profile that answered the last round (a fallback
	// replaces it), so its window, usage and source come from one snapshot.
	metrics, pressure, profile := s.contextMgr.EstimateStatusFor(hist, 0)
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
