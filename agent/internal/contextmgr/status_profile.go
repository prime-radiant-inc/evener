package contextmgr

import (
	"math"

	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// EstimateStatusFor returns the estimated context usage and pressure together
// with the profile they were measured against, read in the same snapshot as
// the window and the measurement. After a fallback serves a round the manager
// holds the fallback's profile, so callers describing the window (its source,
// say) must use this profile rather than the session's primary.
func (cm *Manager) EstimateStatusFor(history []schema.Turn, sysPromptChars int) (schema.ContextMetrics, float64, *provider.Profile) {
	metrics, pressure, prof, _ := cm.EstimateStatusForHistory(history, len(history), sysPromptChars, -1)
	return metrics, pressure, prof
}

// EstimateStatusForRevision is EstimateStatusFor for a caller that can name the
// revision of the history it is describing: the session's historyRevision.
//
// Its answers are identical to EstimateStatusFor's for the same inputs. The
// revision only lets work an earlier call already did be reused, which matters
// because the server samples this once per assistant-message end and once per
// tool-call end, and projecting every turn each time cost 8.96 MB per sample at
// 10,000 turns.
//
// A negative revision means "none available" and always takes the full walk.
func (cm *Manager) EstimateStatusForRevision(history []schema.Turn, sysPromptChars, historyRevision int) (schema.ContextMetrics, float64, *provider.Profile) {
	metrics, pressure, prof, _ := cm.EstimateStatusForHistory(history, len(history), sysPromptChars, historyRevision)
	return metrics, pressure, prof
}

// EstimateStatusForHistory is the form the session uses. history may be nil when
// the caller has already asked ObserveHistory whether the manager holds this
// revision's estimate: a nil history then needs no copy at all, which is the
// point, because that copy was 8.24 MB of the 8.96 MB a sample cost at 10,000
// turns.
//
// ok=false means the manager could not answer for the whole history — the caller
// passed nil for a revision the memo does not cover. It must not treat the zero
// metrics as an answer; it should copy the history and ask again.
func (cm *Manager) EstimateStatusForHistory(history []schema.Turn, historyLen, sysPromptChars, historyRevision int) (schema.ContextMetrics, float64, *provider.Profile, bool) {
	prof, lastTokens, measuredLen, profileGen := cm.profileSnapshot()
	window, assumed := EffectiveWindow(prof)
	if window <= 0 {
		return schema.ContextMetrics{}, 0, prof, true
	}
	used, ok := cm.estimateUsedTokensMemo(prof, profileGen, lastTokens, measuredLen, history, historyLen, sysPromptChars, historyRevision)
	if !ok {
		return schema.ContextMetrics{}, 0, prof, false
	}
	compactionThreshold := 0
	if prof.CompactionThreshold() > 0 {
		th := cm.thresholdsFor(prof)
		compactionThreshold = int(math.Round(th.checkpoint * float64(window)))
	}
	return schema.ContextMetrics{
		Used:                used,
		Window:              window,
		Remaining:           max(window-used, 0),
		Assumed:             assumed,
		CompactionThreshold: compactionThreshold,
	}, pressureOf(used, window), prof, true
}

// estimateMemo remembers the estimator's additive parts over the turns it has
// already walked, so a history that only grows costs one walk of the turns
// appended since.
//
// It stores chars and fixed-price token counts rather than a finished token
// total on purpose. The estimator divides the summed chars by four ONCE over the
// whole list (llm.estimateMessagesInputTokens), so caching a per-prefix total and
// adding suffixes would round differently and drift from the full answer.
// Summing the same chars and dividing once at the end cannot drift.
//
// The key is all integers so a hit costs no allocation: reflect.DeepEqual on the
// resolved target would defeat the purpose of the cache. profileGen is the
// manager's own counter, bumped by every SetProfile, which is the only way the
// target changes.
type estimateMemo struct {
	valid bool

	// parts describes history[base:turns], where base is where the estimator's
	// own baseline started: 0 for the char/4 fallback, and the measured length
	// when an API measurement supplies the baseline.
	base  int
	turns int
	parts llm.TokenParts

	profileGen  uint64
	lastTokens  int
	measuredLen int
	sysPrompt   int
	revision    int
}

// ObserveHistory reports whether the manager already holds the estimate for the
// history historyRevision names at historyLen turns.
//
// The session asks this under its own lock. A true result means it may pass a
// nil history to EstimateStatusForHistory and skip copying — the answer is
// already materialized and exact. A false result means it must copy.
func (cm *Manager) ObserveHistory(historyRevision, historyLen int) bool {
	if historyRevision < 0 {
		return false
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()
	m := cm.estimate
	return m.valid && m.revision == historyRevision && m.turns == historyLen
}

// estimateUsedTokensMemo is estimateUsedTokensFor with the memo in front of it.
// It returns exactly what estimateUsedTokensFor would for the same history, or
// ok=false when a nil history leaves turns it has not walked.
func (cm *Manager) estimateUsedTokensMemo(prof *provider.Profile, profileGen uint64, lastTokens, measuredLen int, history []schema.Turn, historyLen int, sysPromptChars, historyRevision int) (int, bool) {
	measured := lastTokens > 0 && measuredLen <= historyLen
	// The estimator's own baseline. Parts are kept over history[base:] so the
	// prefix an API measurement already accounts for is never walked.
	base := 0
	if measured {
		base = measuredLen
	}

	cm.mu.Lock()
	memo := cm.estimate
	cm.mu.Unlock()

	reusable := memo.valid && historyRevision >= 0 && memo.revision == historyRevision &&
		memo.base == base && memo.profileGen == profileGen &&
		memo.lastTokens == lastTokens && memo.measuredLen == measuredLen &&
		memo.sysPrompt == sysPromptChars && memo.turns <= historyLen

	parts := llm.TokenParts{}
	walked := base
	if reusable {
		parts = memo.parts
		walked = memo.turns
	}
	if history != nil && len(history) > walked {
		parts = parts.Add(estimateTokenPartsFor(resolvedTargetOf(prof), history[walked:]))
		walked = len(history)
	}
	// A nil history can only answer for turns already walked. Reporting a prefix
	// as if it were the whole history is the stale-figure defect this memo must
	// not introduce.
	if history == nil && walked < historyLen {
		return 0, false
	}

	if historyRevision >= 0 {
		cm.mu.Lock()
		cm.estimate = estimateMemo{
			valid:       true,
			base:        base,
			turns:       walked,
			parts:       parts,
			profileGen:  profileGen,
			lastTokens:  lastTokens,
			measuredLen: measuredLen,
			sysPrompt:   sysPromptChars,
			revision:    historyRevision,
		}
		cm.mu.Unlock()
	}

	if measured {
		return lastTokens + parts.Total(), true
	}
	return parts.Total() + sysPromptChars/4, true
}
