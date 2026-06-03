package agent

import "primeradiant.com/serf/agent/schema"

// ContextMetrics describes the estimated current context size, all in tokens.
// It is an alias for schema.ContextMetrics so the type that callers (the hub
// and serve.go) already consume off package agent stays identical after the
// underlying struct moved to schema.
type ContextMetrics = schema.ContextMetrics

// ContextMetrics returns the estimated context use for this session.
func (s *Session) ContextMetrics() ContextMetrics {
	if s.contextMgr == nil {
		return ContextMetrics{}
	}
	s.mu.Lock()
	hist := append([]schema.Turn{}, s.history...)
	s.mu.Unlock()
	return s.contextMgr.EstimateUsage(hist, 0)
}
