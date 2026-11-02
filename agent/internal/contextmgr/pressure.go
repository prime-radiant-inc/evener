package contextmgr

import (
	"slices"

	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// RecordInputTokens stores the exact input token count from an API response,
// along with the history length at that point. This enables accurate pressure
// calculation for subsequent turns without relying on the char/4 heuristic.
func (cm *Manager) RecordInputTokens(tokens int, historyLen int) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.lastInputTokens = tokens
	cm.historyLenAtMeasure = historyLen
}

// RecordInputTokensFor records an API measurement only when the manager still
// holds the profile that produced it. A SetProfile that lands while a request is
// in flight already invalidated the measurement that request belonged to, so
// writing it afterwards would attribute one model's count to another -- and the
// estimator reads the target, so the count would be wrong under the new rules too.
func (cm *Manager) RecordInputTokensFor(profile *provider.Profile, tokens int, historyLen int) bool {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if !sameProfileTarget(cm.profile, profile) {
		return false
	}
	cm.lastInputTokens = tokens
	cm.historyLenAtMeasure = historyLen
	return true
}

// SetProfile replaces the provider profile so that ContextWindowSize() and
// other profile-derived values stay current after a model change. An exact
// token count belongs only to the instance, model, and protocol that produced
// it, so changing any part of that target invalidates the measurement.
func (cm *Manager) SetProfile(profile *provider.Profile) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if !sameProfileTarget(cm.profile, profile) {
		cm.lastInputTokens = 0
		cm.historyLenAtMeasure = 0
		// The unknown-window disclosure names the model it assumed for, so
		// it belongs to the outgoing target: a different model — still
		// unknown — discloses its own assumption on its first fold.
		cm.assumedWindowDisclosed = false
	}
	cm.profile = profile
}

func sameProfileTarget(a, b *provider.Profile) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.ID() != b.ID() || a.Model() != b.Model() || a.Protocol() != b.Protocol() {
		return false
	}
	// The estimator reads more of the row than its identity (see
	// EstimatorTargetsEquivalent), so every field it reads has to match before a
	// measurement may survive: the count was taken under the old rules.
	return sameEstimatorTarget(a.Resolved(), b.Resolved())
}

// sameEstimatorTarget compares the resolved fields the token estimator reads. The
// rules belong to the estimator, so the comparison lives with them
// (llm.EstimatorTargetsEquivalent) and this only names what it is for here.
func sameEstimatorTarget(a, b registry.Resolved) bool {
	return llm.EstimatorTargetsEquivalent(a, b)
}

// currentProfile returns the active profile under cm.mu so reads do not race
// SetProfile (called from Session.SetModel). The profile pointer is swapped
// atomically; a caller uses the returned value for the duration of one operation.
func (cm *Manager) currentProfile() *provider.Profile {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.profile
}

// LastInputTokens returns the most recently recorded input token count.
func (cm *Manager) LastInputTokens() int {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.lastInputTokens
}

// Pressure returns the current context pressure as a fraction of the window.
// It may exceed 1.0 when the estimated usage is larger than the window.
func (cm *Manager) Pressure(history []schema.Turn, sysPromptChars int) float64 {
	return cm.estimatePressure(history, sysPromptChars)
}

// estimatePressure calculates what fraction of the context window is in use.
// Uses actual API-reported token counts when available, falling back to char/4.
// One profile snapshot covers the window, the measurement, and the estimate, so
// a model switch (SetProfile) cannot pair one model's window with another's
// tokens.
func (cm *Manager) estimatePressure(history []schema.Turn, sysPromptChars int) float64 {
	prof, lastTokens, measuredLen := cm.profileSnapshot()
	return cm.pressureFromSnapshot(prof, lastTokens, measuredLen, history, sysPromptChars)
}

// pressureWithProfile reads pressure and the profile it was measured against from
// ONE snapshot, so a compaction layer's decision and that layer's before/after
// diagnostics describe one model even if SetProfile lands in between.
func (cm *Manager) pressureWithProfile(history *[]schema.Turn, sysPromptChars int) (float64, *provider.Profile) {
	prof, lastTokens, measuredLen := cm.profileSnapshot()
	return cm.pressureFromSnapshot(prof, lastTokens, measuredLen, *history, sysPromptChars), prof
}

// pressureFromSnapshot is estimatePressure's core for callers that already hold a
// (profile, measurement) snapshot. The compaction phases read pressure and their
// before/after diagnostics from one snapshot apiece, so a concurrent SetProfile
// cannot decide a layer by one model and describe it by another.
func (cm *Manager) pressureFromSnapshot(prof *provider.Profile, lastTokens, measuredLen int, history []schema.Turn, sysPromptChars int) float64 {
	cw, _ := effectiveWindowOf(prof)
	if cw <= 0 {
		return 0
	}
	used := cm.estimateUsedTokensFor(prof, lastTokens, measuredLen, history, sysPromptChars)
	return pressureOf(used, cw)
}

// contextWindowOf returns the profile's context window, or 0 when the manager
// has no profile. A Manager built without one (NewManager(nil, ...), as the
// session-log strategy tests do) has no window to read, and the readers below
// report "unknown" for that rather than crashing.
func contextWindowOf(prof *provider.Profile) int {
	if prof == nil {
		return 0
	}
	return prof.ContextWindowSize()
}

// effectiveWindowOf is the window the budget sites read: the profile's real
// window when it has one, and AssumedContextWindow when the window is
// unknown. A nil profile — a manager without a model, as test harnesses
// build — has nothing to assume against and stays (0, false).
func effectiveWindowOf(prof *provider.Profile) (window int, assumed bool) {
	if cw := contextWindowOf(prof); cw > 0 {
		return cw, false
	}
	if prof == nil {
		return 0, false
	}
	return AssumedContextWindow, true
}

// AssumedContextWindow is the window the manager budgets against when the
// model's real one is unknown — no user row declares it, the catalog has no
// row, and no live listing supplied one (issue #4564). A zero window used to
// mean "no limit": compaction and the usage warning went dead, and every
// round resent the full accumulated history. Unknown instead assumes a
// window small enough that compaction errs toward folding too early. 128K is
// the floor of current frontier text models' advertised windows, so the
// assumption undershoots only sub-128K models, whose oversized requests fail
// at the API either way; the disclosure names the remedy — declare
// context_window on the provider's model row.
const AssumedContextWindow = 128_000

// UnknownWindowInputTripwire is the exact input-token measurement at which an
// unknown-window session folds regardless of the window math (issue #4564): a
// few hundred K. The tripwire is independent of the assumed window and of the
// configured thresholds, so growth stays bounded even if the assumption or
// the thresholds are wrong. A known window governs on its own, so the
// tripwire never fires for one.
const UnknownWindowInputTripwire = 200_000

// resolvedTargetOf returns the profile's resolved registry row, or the zero row
// when there is no profile: the estimator then falls back to the names the turns
// carry, exactly as the targetless estimator did before the profile-aware
// accounting.
func resolvedTargetOf(prof *provider.Profile) registry.Resolved {
	if prof == nil {
		return registry.Resolved{}
	}
	return prof.Resolved()
}

// profileSnapshot reads the active profile together with the API measurement
// that belongs to it, in one critical section. SetProfile swaps the profile and
// clears the measurement in one section of the same lock, so reading them in
// separate sections could pair one model's window with another model's cleared
// measurement.
func (cm *Manager) profileSnapshot() (*provider.Profile, int, int) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.profile, cm.lastInputTokens, cm.historyLenAtMeasure
}

// estimateUsedTokens is the pure token-accounting core shared by estimatePressure
// and EstimateUsage: it returns the estimated input tokens for a history given the
// last API-reported token count and the history length at which it was measured.
// When a measurement is available and still applies (lastTokens > 0 and the
// measured length is within the current history), it uses the measurement as a
// baseline and estimates only the turns appended since; otherwise it falls back to
// the char/4 heuristic over the whole history plus the system prompt. It takes a
// fresh profile snapshot for callers that hold none.
func (cm *Manager) estimateUsedTokens(lastTokens, measuredLen int, history []schema.Turn, sysPromptChars int) int {
	return cm.estimateUsedTokensFor(cm.currentProfile(), lastTokens, measuredLen, history, sysPromptChars)
}

// estimateUsedTokensFor is the snapshot-taking core: the caller passes the same
// profile it read the window from, so a model switch mid-computation cannot
// combine one model's window with another model's accounting.
func (cm *Manager) estimateUsedTokensFor(prof *provider.Profile, lastTokens, measuredLen int, history []schema.Turn, sysPromptChars int) int {
	if lastTokens > 0 && measuredLen <= len(history) {
		// Use the known token count as baseline, then estimate only new turns.
		return lastTokens + cm.estimateTokensFor(prof, history[measuredLen:])
	}
	// Fall back to char/4 for everything.
	return cm.estimateTokensFor(prof, history) + sysPromptChars/4
}

// EstimatePressure returns the estimated fraction of context window in use.
func (cm *Manager) EstimatePressure(history []schema.Turn, sysPromptChars int) float64 {
	return cm.estimatePressure(history, sysPromptChars)
}

// EstimateStatus returns the estimated context usage and pressure from one
// (profile, measurement) snapshot and one pass over history.
func (cm *Manager) EstimateStatus(history []schema.Turn, sysPromptChars int) (schema.ContextMetrics, float64) {
	prof, lastTokens, measuredLen := cm.profileSnapshot()
	window, assumed := effectiveWindowOf(prof)
	if window <= 0 {
		return schema.ContextMetrics{}, 0
	}
	used := cm.estimateUsedTokensFor(prof, lastTokens, measuredLen, history, sysPromptChars)
	return schema.ContextMetrics{
		Used:      used,
		Window:    window,
		Remaining: max(window-used, 0),
		Assumed:   assumed,
	}, pressureOf(used, window)
}

// pressureOf reports used as a fraction of window. A zero or negative window
// has no fraction to report. The result may exceed 1.0 when used exceeds
// window.
func pressureOf(used, window int) float64 {
	if window <= 0 {
		return 0
	}
	return float64(used) / float64(window)
}

// EstimateUsage returns the estimated used, total, and remaining context
// tokens, with Assumed marking a Window that is the unknown-window assumption
// rather than a size anything vouches for. It delegates to EstimateStatus, so
// the window, the measurement, and the estimate come from one snapshot.
func (cm *Manager) EstimateUsage(history []schema.Turn, sysPromptChars int) schema.ContextMetrics {
	metrics, _ := cm.EstimateStatus(history, sysPromptChars)
	return metrics
}

// ApplyThresholdScale scales all compaction thresholds on cm by the given
// factor. A factor of 0 or 1 leaves defaults unchanged.
//
// Thresholds are clamped to a minimum of 0.20 so that aggressive scaling
// doesn't collapse all layers into a narrow band.
func ApplyThresholdScale(cm *Manager, scale float64) {
	if scale > 0 && scale != 1.0 {
		clamp := func(v float64) float64 {
			if v < 0.20 {
				return 0.20
			}
			return v
		}
		cm.ObservationMaskThreshold = clamp(cm.ObservationMaskThreshold * scale)
		cm.ThinkingClearThreshold = clamp(cm.ThinkingClearThreshold * scale)
		cm.WarnThreshold = clamp(cm.WarnThreshold * scale)
		cm.CheckpointThreshold = clamp(cm.CheckpointThreshold * scale)
		cm.SummarizeThreshold = clamp(cm.SummarizeThreshold * scale)
	}
}

// estimateTokens estimates token count for turns using the char/4 heuristic.
// The manager's profile knows the target's protocol and reasoning capabilities,
// so thinking text is billed exactly for the adapters that replay it: history
// accounting that kept it at zero let compaction defer past the window an
// oversized request would cross. It takes a fresh profile snapshot for callers
// that hold none.
func (cm *Manager) estimateTokens(turns []schema.Turn) int {
	return cm.estimateTokensFor(cm.currentProfile(), turns)
}

// estimateTokensFor is the snapshot-taking estimator: the caller passes the
// same profile whose window its reading used.
func (cm *Manager) estimateTokensFor(prof *provider.Profile, turns []schema.Turn) int {
	messages := make([]llm.Message, 0, len(turns))
	for _, t := range turns {
		if t.Kind == schema.TurnAttentionResolution {
			continue
		}
		messages = append(messages, t.Message)
	}
	return llm.EstimateMessagesInputTokensForResolved(resolvedTargetOf(prof), messages).Tokens
}

func attentionTransparentTurnCount(history []schema.Turn) int {
	count := 0
	for _, turn := range history {
		if turn.Kind != schema.TurnAttentionResolution {
			count++
		}
	}
	return count
}

func attentionTransparentHistory(history []schema.Turn) []schema.Turn {
	if attentionTransparentTurnCount(history) == len(history) {
		return history
	}
	visible := make([]schema.Turn, 0, len(history))
	for _, turn := range history {
		if turn.Kind != schema.TurnAttentionResolution {
			visible = append(visible, turn)
		}
	}
	return visible
}

func attentionTransparentRecentCutoff(history []schema.Turn, preserveRecent int) int {
	if preserveRecent <= 0 {
		return len(history)
	}
	seen := 0
	for i, turn := range slices.Backward(history) {
		if turn.Kind == schema.TurnAttentionResolution {
			continue
		}
		seen++
		if seen == preserveRecent {
			return i
		}
	}
	return 0
}
