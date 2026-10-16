package apptranscript

import (
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// UsageTotalFromFile sums the per-turn token usage recorded across a WHOLE
// transcript, so a session that never persisted a cumulative total in its meta
// can still report an honest full-session figure. A compaction fold copy is NOT
// counted: it re-appends an entry the file already holds, and a duplicated line
// is not a second spend.
//
// As of #4667 the transcript-index sidecar records each turn's usage, so a
// daemonless read derives this total from the index
// (transcriptindex.Index.DerivedTotals) and pays no scan. This function is the
// fallback for a reader that holds no index, and the reference the derived
// answer is tested against, so it reads the transcript's own usage blocks end
// to end.
//
// fromEntryOrdinal is the 1-based entry ordinal at which the session's OWN
// history begins, i.e. a fork child's SessionMeta.DivergenceTurn. A fork's
// child transcript opens with a verbatim copy of the parent's prefix, and those
// tokens were spent by the parent: counting them would attribute another
// session's spend to this one. Pass 0 (or 1) for a session that inherited
// nothing.
//
// Returns nil, not a zero total, when the counted span carries no token data —
// an unopened aside fork, or a transcript predating per-turn usage. Absent and
// zero are different claims, and the callers render them differently. Errors
// are surfaced rather than swallowed, so a caller reports "unknown" instead of
// a fabricated figure.
func (c *TurnCache) UsageTotalFromFile(path string, maxLineBytes int, fromEntryOrdinal int) (*appwire.EvenerUsage, error) {
	return memoizeScan(c, path, fromEntryOrdinal,
		func(entry *turnCacheEntry) *scanMemo[*appwire.EvenerUsage] { return entry.usageTotal },
		func(entry *turnCacheEntry, memo *scanMemo[*appwire.EvenerUsage]) { entry.usageTotal = memo },
		cloneEvenerUsage,
		func() (*appwire.EvenerUsage, error) { return scanUsageTotal(path, maxLineBytes, fromEntryOrdinal) },
	)
}

// scanUsageTotal reads the transcript once, decoding only each entry's usage
// block. It reuses scanSemanticTranscript so the format gate (v1 rejection,
// unknown-field strictness, header validation) is exactly the one every other
// reader in this package applies.
func scanUsageTotal(path string, maxLineBytes int, fromEntryOrdinal int) (*appwire.EvenerUsage, error) {
	var accumulated usageAccumulator
	if err := narrowScan(path, maxLineBytes, fromEntryOrdinal,
		decodeNarrowEntry[usageOnlyEntry]("usage"),
		func(record usageOnlyEntry, _ int) error {
			if record.Turn.OriginalOrdinal != nil {
				return nil // a compaction fold copy: its original already counts
			}
			accumulated.add(record.Turn.Usage)
			return nil
		}); err != nil {
		return nil, err
	}
	observeIndexRead(ReadStats{usageScans: 1})
	return accumulated.total(), nil
}

// usageAccumulator applies the token-sum rule shared by scanUsageTotal and
// scanDerivedTotals: only entries carrying real token data count, and a span
// that carried none totals to nil rather than a fabricated zero (absent and
// zero are different claims — see UsageTotalFromFile).
type usageAccumulator struct {
	sum     llm.Usage
	counted bool
}

func (a *usageAccumulator) add(usage llm.Usage) {
	if appwire.EvenerUsageFromLLM(usage) == nil {
		return
	}
	a.sum = a.sum.Add(usage)
	a.counted = true
}

func (a *usageAccumulator) total() *appwire.EvenerUsage {
	if !a.counted {
		return nil
	}
	return appwire.EvenerUsageFromLLM(a.sum)
}

// usageOnlyEntry decodes the one field the sum needs. scanSemanticTranscript has
// already validated the full record, so this narrow view can ignore the rest
// rather than paying to decode a whole turn's message content per line.
type usageOnlyEntry struct {
	Turn struct {
		Usage           llm.Usage `json:"usage"`
		OriginalOrdinal *uint64   `json:"original_ordinal"`
	} `json:"turn"`
}

// cloneEvenerUsage hands each caller its own copy, so a caller that stamps the
// result onto a wire struct cannot mutate the memo other callers share.
func cloneEvenerUsage(u *appwire.EvenerUsage) *appwire.EvenerUsage {
	if u == nil {
		return nil
	}
	copied := *u
	return &copied
}
