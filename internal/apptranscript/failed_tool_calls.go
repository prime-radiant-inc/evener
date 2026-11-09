package apptranscript

import (
	"primeradiant.com/evener/agent/transcript"
)

// FailedToolCallsFromFile counts how many of a session's tool calls failed,
// over the WHOLE transcript. A compaction fold copy is not counted: it
// re-appends a result the file already holds, and it projects no row and wears
// no failure glyph, so counting it would disagree with the glyphs this rule
// promises to match.
//
// It exists because a client cannot answer the question. thread/read windows
// turns (turnLimit), so a count over the items a client holds covers whatever
// fraction of the session happens to be loaded — measured at ~47% on a long
// real session. For failures specifically an undercount is worse than no
// answer: "0 failed", computed from a partial window, states in the session's
// own chrome that nothing went wrong, which is exactly the misreading this
// count exists to prevent.
//
// WHAT COUNTS is the same thing the transcript marks with a --danger glyph, so
// the figure and the glyphs can never disagree:
//
//   - a tool result carrying an error (ToolResultData.IsError — the wire's
//     ThreadItem.Error, and the "failed" status SettledToolStatus stamps);
//
// A SHELL call that ran and exited nonzero is NOT one of them (Jesse,
// 2026-10-16): the renderer shows it as a small inline mark, never a failure
// glyph, so the count must not include it either.
//
// communicate results are excluded: ProjectTurn drops them, so they render no
// row and wear no glyph. A result whose own record omits its name is resolved
// from the call that announced it, the same way ProjectTurn resolves it.
//
// fromEntryOrdinal is the 1-based entry ordinal at which the session's OWN
// history begins — a fork child's SessionMeta.DivergenceTurn. A child
// transcript opens with a verbatim copy of the parent's prefix, and those
// failures were the parent's; charging them to the child is the same
// attribution bug the token sum's identical bound exists to prevent. Pass 0
// (or 1) for a session that inherited nothing.
//
// A count of zero is a real measurement, not an absence: the transcript records
// every tool result and none of them failed. Only an unreadable transcript (a
// legacy format_version 1 file, a missing one) is unknown, and that arrives as
// an error rather than a fabricated zero.
func (c *TurnCache) FailedToolCallsFromFile(path string, maxLineBytes int, fromEntryOrdinal int) (int, error) {
	return memoizeScan(c, path, fromEntryOrdinal,
		func(entry *turnCacheEntry) *scanMemo[int] { return entry.failedToolCalls },
		func(entry *turnCacheEntry, memo *scanMemo[int]) { entry.failedToolCalls = memo },
		nil,
		func() (int, error) { return scanFailedToolCalls(path, maxLineBytes, fromEntryOrdinal) },
	)
}

// scanFailedToolCalls reads the transcript once, decoding only the tool calls
// and tool results. It reuses scanSemanticTranscript so the format gate (v1
// rejection, unknown-field strictness, header validation) is exactly the one
// every other reader in this package applies.
func scanFailedToolCalls(path string, maxLineBytes int, fromEntryOrdinal int) (int, error) {
	failures := transcript.NewFailureCounter(fromEntryOrdinal)
	if err := narrowScan(path, maxLineBytes, 1,
		decodeNarrowEntry[failedToolCallEntry]("tool calls"),
		func(record failedToolCallEntry, _ int) error {
			failures.ObserveToolParts(record.Turn.OriginalOrdinal != nil, record.Turn.Message.Content)
			return nil
		}); err != nil {
		return 0, err
	}
	observeIndexRead(ReadStats{failureScans: 1})
	return failures.Count(), nil
}

// failedToolCallEntry decodes the few fields the count needs.
// scanSemanticTranscript has already validated the full record, so this narrow
// view can ignore the rest rather than paying to decode whole message bodies
// (including inline image bytes) per line.
type failedToolCallEntry struct {
	Turn struct {
		// No turn kind: the content parts are the discriminator this needs.
		// Tool calls and tool results only ever appear on the assistant and
		// tool-result kinds, so matching on the part itself is both narrower
		// and immune to a new turn kind that carries them.
		Message         toolScanMessage `json:"message"`
		OriginalOrdinal *uint64         `json:"original_ordinal"`
	} `json:"turn"`
}

// toolScanMessage is the narrow message decode the failure rule needs: only
// the content parts that carry tool calls and tool results. It is shared by
// failedToolCallEntry and derivedTotalsEntry so the two scans decode the same
// field-for-field subset of schema.Turn and cannot drift apart.
type toolScanMessage struct {
	Content []transcript.ToolPart `json:"content"`
}
