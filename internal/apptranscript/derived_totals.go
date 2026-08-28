package apptranscript

import (
	"encoding/json"
	"fmt"
	"os"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// derivedTotals is what DerivedTotalsFromFile computes in one pass: the
// session's full-transcript token sum and failed-tool-call count, plus whether
// the counted span carried any usage at all (absent and zero are different
// claims — see UsageTotalFromFile).
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
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, fmt.Errorf("stat transcript: %w", err)
	}
	identity := derivedTotalsKey{
		size:           info.Size(),
		modUnixNano:    info.ModTime().UnixNano(),
		fileIdentity:   fileIdentity(info),
		changeIdentity: fileChangeIdentity(info),
		fromOrdinal:    fromEntryOrdinal,
	}

	c.mu.Lock()
	if entry, ok := c.entries[path]; ok && entry.derivedTotals != nil && entry.derivedTotals.key == identity {
		totals := entry.derivedTotals.totals
		c.touch(path)
		c.mu.Unlock()
		return cloneEvenerUsage(totals.usage), totals.failedToolCall, nil
	}
	c.mu.Unlock()

	totals, err := scanDerivedTotals(path, maxLineBytes, fromEntryOrdinal)
	if err != nil {
		return nil, 0, err
	}

	c.mu.Lock()
	entry := c.entries[path]
	entry.derivedTotals = &derivedTotalsMemo{key: identity, totals: totals}
	c.entries[path] = entry
	c.touch(path)
	c.evictLocked()
	c.mu.Unlock()
	return cloneEvenerUsage(totals.usage), totals.failedToolCall, nil
}

// scanDerivedTotals reads the transcript once, computing both figures with the
// same narrow decodes and attribution rules the two individual scans apply. It
// reuses scanSemanticTranscript so the format gate (v1 rejection,
// unknown-field strictness, header validation) is exactly the one every other
// reader in this package applies.
func scanDerivedTotals(path string, maxLineBytes int, fromEntryOrdinal int) (derivedTotals, error) {
	var totals derivedTotals
	var accumulated llm.Usage
	counted := false
	ordinal := 0
	// toolNames resolves a result whose own record omits its name, mirroring
	// ProjectTurn's map of the same name. It is filled from EVERY assistant
	// entry, including ones before the divergence cut: a fork child's own
	// result can answer a call the inherited prefix announced.
	toolNames := map[string]string{}
	if _, err := scanSemanticTranscript(path, maxLineBytes, func(raw json.RawMessage) error {
		ordinal++
		var record derivedTotalsEntry
		if err := json.Unmarshal(raw, &record); err != nil {
			// Unreachable for any line scanSemanticTranscript admits: it has
			// already strictly decoded the whole entry into transcript.Entry,
			// of which this is a field-for-field subset. A failure here means
			// this struct has drifted from schema.Turn, and skipping the
			// record would silently undercount — reporting a wrong figure is
			// worse than reporting none, so surface it.
			return fmt.Errorf("decode transcript entry derived totals: %w", err)
		}
		counting := ordinal >= fromEntryOrdinal
		if counting && appwire.EvenerUsageFromLLM(record.Turn.Usage) != nil {
			accumulated = accumulated.Add(record.Turn.Usage)
			counted = true
		}
		for _, part := range record.Turn.Message.Content {
			switch {
			case part.ToolCall != nil && part.Kind == llm.ContentToolCall:
				toolNames[part.ToolCall.ID] = part.ToolCall.Name
			case part.ToolResult != nil && part.Kind == llm.ContentToolResult:
				if counting && failedToolResult(part.ToolResult, toolNames) {
					totals.failedToolCall++
				}
			}
		}
		return nil
	}); err != nil {
		return derivedTotals{}, err
	}
	observeIndexRead(ReadStats{derivedScans: 1})
	if !counted {
		return totals, nil
	}
	totals.usage = appwire.EvenerUsageFromLLM(accumulated)
	return totals, nil
}

// derivedTotalsEntry decodes the few fields both figures need: the usage block
// for the token sum, and the tool calls/results for the failure count.
// scanSemanticTranscript has already validated the full record, so this
// narrow view can ignore the rest rather than paying to decode whole message
// bodies (including inline image bytes) per line.
type derivedTotalsEntry struct {
	Turn struct {
		Usage   llm.Usage `json:"usage"`
		Message struct {
			Content []struct {
				Kind     llm.ContentKind `json:"kind"`
				ToolCall *struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"tool_call"`
				ToolResult *failedToolCallResult `json:"tool_result"`
			} `json:"content"`
		} `json:"message"`
	} `json:"turn"`
}

// derivedTotalsKey is the file identity a memoized combined scan is valid for.
// It mirrors usageTotalKey and failedToolCallsKey exactly (object identity,
// size, mtime as nanos, platform change time, divergence ordinal) so the
// combined memo can never outlive the two it consolidates.
type derivedTotalsKey struct {
	size           int64
	modUnixNano    int64
	fileIdentity   string
	changeIdentity string
	fromOrdinal    int
}

type derivedTotalsMemo struct {
	key    derivedTotalsKey
	totals derivedTotals
}
