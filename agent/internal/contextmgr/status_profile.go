package contextmgr

import (
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
)

// EstimateStatusFor is EstimateStatus that also returns the profile the status
// was measured against, read in the same snapshot as the window and the
// measurement. After a fallback serves a round the manager holds the
// fallback's profile, so callers describing the window (its source, say) must
// use this profile rather than the session's primary.
func (cm *Manager) EstimateStatusFor(history []schema.Turn, sysPromptChars int) (schema.ContextMetrics, float64, *provider.Profile) {
	prof, lastTokens, measuredLen := cm.profileSnapshot()
	window, assumed := effectiveWindowOf(prof)
	if window <= 0 {
		return schema.ContextMetrics{}, 0, prof
	}
	used := cm.estimateUsedTokensFor(prof, lastTokens, measuredLen, history, sysPromptChars)
	return schema.ContextMetrics{
		Used:      used,
		Window:    window,
		Remaining: max(window-used, 0),
		Assumed:   assumed,
	}, pressureOf(used, window), prof
}
