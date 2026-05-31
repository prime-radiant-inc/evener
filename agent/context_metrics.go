package agent

// ContextMetrics describes the estimated current context size.
type ContextMetrics struct {
	Used      int
	Window    int
	Remaining int
}

// ContextMetrics returns the estimated context use for this session.
func (s *Session) ContextMetrics() ContextMetrics {
	if s.contextMgr == nil {
		return ContextMetrics{}
	}
	s.mu.Lock()
	hist := append([]Turn{}, s.history...)
	s.mu.Unlock()
	return s.contextMgr.EstimateUsage(hist, 0)
}

// EstimateUsage returns the estimated used, total, and remaining context tokens.
func (cm *ContextManager) EstimateUsage(history []Turn, sysPromptChars int) ContextMetrics {
	cw := cm.currentProfile().ContextWindowSize()
	if cw <= 0 {
		return ContextMetrics{}
	}

	cm.mu.Lock()
	lastTokens := cm.lastInputTokens
	measuredLen := cm.historyLenAtMeasure
	cm.mu.Unlock()

	var used int
	if lastTokens > 0 && measuredLen <= len(history) {
		used = lastTokens + EstimateTokens(history[measuredLen:])
	} else {
		used = EstimateTokens(history) + sysPromptChars/4
	}
	remaining := cw - used
	if remaining < 0 {
		remaining = 0
	}
	return ContextMetrics{Used: used, Window: cw, Remaining: remaining}
}
