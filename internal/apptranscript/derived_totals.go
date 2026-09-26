package apptranscript

import (
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// derivedTotals is what DerivedTotalsFromFile computes in one pass: the
// session's full-transcript token sum and its failed-tool-call count. A nil
// usage means the counted span carried no token data — the nil-usage semantics
// DerivedTotalsFromFile documents below.
type derivedTotals struct {
	usage          *appwire.EvenerUsage
	failedToolCall int
}

// DerivedTotalsFromFile computes BOTH derived figures — the full-transcript
// token sum of UsageTotalFromFile and the failed-tool-call count of
// FailedToolCallsFromFile — in a SINGLE scan of the transcript.
//
// It exists because the past-thread read path needs both on every read: two
// separate scans read and strictly decode the same immutable bytes twice, and
// on a tens-of-megabytes transcript that doubled cost dominated the read. One
// pass over the same narrow field-for-field subsets (usage block, tool calls
// and tool results) answers both for the price of one.
//
// Semantics are exactly the union of the two functions it replaces:
//
//   - fromEntryOrdinal is the 1-based entry ordinal at which the session's OWN
//     history begins (a fork child's SessionMeta.DivergenceTurn); entries
//     before it are the parent's verbatim prefix and contribute to neither
//     figure. The failure rule still learns tool names from EVERY assistant
//     entry, including inherited ones, because a fork child's own result can
//     answer a call the inherited prefix announced.
//   - usage is nil when the counted span carries no token data; the count is
//     still a real measurement. Errors are surfaced rather than swallowed, so
//     a caller reports "unknown" instead of a fabricated figure.
//
// The result is memoized on the same file-identity + divergence-ordinal gate
// the two individual memos use, stored alongside them so all three evict
// together. Callers that need only one figure should keep using the single
// functions; this is for read paths that provably need both.
func (c *TurnCache) DerivedTotalsFromFile(path string, maxLineBytes int, fromEntryOrdinal int) (*appwire.EvenerUsage, int, error) {
	totals, err := memoizeScan(c, path, fromEntryOrdinal,
		func(entry *turnCacheEntry) *scanMemo[derivedTotals] { return entry.derivedTotals },
		func(entry *turnCacheEntry, memo *scanMemo[derivedTotals]) { entry.derivedTotals = memo },
		func(value derivedTotals) derivedTotals {
			value.usage = cloneEvenerUsage(value.usage)
			return value
		},
		func() (derivedTotals, error) { return scanDerivedTotals(path, maxLineBytes, fromEntryOrdinal) },
	)
	if err != nil {
		return nil, 0, err
	}
	return totals.usage, totals.failedToolCall, nil
}

// scanDerivedTotals reads the transcript once, computing both figures with the
// same narrow decodes and attribution rules the two individual scans apply. It
// reuses scanSemanticTranscript so the format gate (v1 rejection,
// unknown-field strictness, header validation) is exactly the one every other
// reader in this package applies.
func scanDerivedTotals(path string, maxLineBytes int, fromEntryOrdinal int) (derivedTotals, error) {
	var totals derivedTotals
	var accumulated usageAccumulator
	failures := newFailureCounter()
	if err := narrowScan(path, maxLineBytes, 1,
		decodeNarrowEntry[derivedTotalsEntry]("derived totals"),
		func(record derivedTotalsEntry, ordinal int) error {
			counting := ordinal >= fromEntryOrdinal
			if counting {
				accumulated.add(record.Turn.Usage)
			}
			failures.observe(record.Turn.Message.Content, counting)
			return nil
		}); err != nil {
		return derivedTotals{}, err
	}
	observeIndexRead(ReadStats{derivedScans: 1})
	totals.usage = accumulated.total()
	totals.failedToolCall = failures.count
	return totals, nil
}

// derivedTotalsEntry decodes the few fields both figures need: the usage block
// for the token sum (usageOnlyEntry's field), and the tool calls/results for
// the failure count (failedToolCallEntry's shared toolScanMessage).
// scanSemanticTranscript has already validated the full record, so this
// narrow view can ignore the rest rather than paying to decode whole message
// bodies (including inline image bytes) per line.
type derivedTotalsEntry struct {
	Turn struct {
		Usage   llm.Usage       `json:"usage"`
		Message toolScanMessage `json:"message"`
	} `json:"turn"`
}
