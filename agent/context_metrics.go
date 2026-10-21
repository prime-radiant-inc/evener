package agent

import "primeradiant.com/evener/agent/schema"

// ContextMetrics describes the estimated current context size, all in tokens.
// It is an alias for schema.ContextMetrics so the type that callers (the hub
// and serve.go) already consume off package agent stays identical after the
// underlying struct moved to schema.
type ContextMetrics = schema.ContextMetrics

// ContextStatus returns one snapshot's estimated context use and pressure.
//
// The history copy is shallow: the new slice shares each turn's nested values.
// That is safe under the existing invariant that published turns are replaced,
// not mutated in place (retainDelegateAttentionTurn and attention-turn removal,
// agent/session_attention.go:1563-1610; recordTurn clones SkillState before the
// append, agent/session.go:2347-2352). ContextStatus relies on that same
// invariant and does not introduce a stronger immutability guarantee.
//
// The copy stays under s.mu; the estimate runs after the unlock.
func (s *Session) ContextStatus() (ContextMetrics, float64) {
	if s.contextMgr == nil {
		return ContextMetrics{}, 0
	}
	s.mu.Lock()
	hist := append([]schema.Turn{}, s.history...)
	s.mu.Unlock()
	return s.contextMgr.EstimateStatus(hist, 0)
}

// ContextMetrics returns the estimated context use for this session. Each call
// takes one full sample.
func (s *Session) ContextMetrics() ContextMetrics {
	metrics, _ := s.ContextStatus()
	return metrics
}
