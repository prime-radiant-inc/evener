package apptranscript

import (
	"encoding/json"
	"fmt"
	"os"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// UsageTotalFromFile sums the per-turn token usage recorded across a WHOLE
// transcript, so a session that never persisted a cumulative total in its meta
// can still report an honest full-session figure.
//
// This is deliberately NOT derived from the turn index. Every other reader here
// is windowed — LatestFromFile and PageFromFile project a bounded range and
// nothing else — and a session total summed over a window is a partial figure
// wearing a full-session label. The index records offsets and visibility, not
// tokens, so answering "how many tokens did this session spend" means reading
// the transcript's own usage blocks end to end. Bumping turnIndexVersion to
// carry per-record usage would invalidate every sidecar on disk to save a scan
// that this file memoizes anyway.
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
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat transcript: %w", err)
	}
	identity := scanMemoIdentity(info, fromEntryOrdinal)

	c.mu.Lock()
	if entry, ok := c.entries[path]; ok && entry.usageTotal != nil && entry.usageTotal.key == identity {
		total := entry.usageTotal.total
		c.touch(path)
		c.mu.Unlock()
		return cloneEvenerUsage(total), nil
	}
	c.mu.Unlock()

	total, err := scanUsageTotal(path, maxLineBytes, fromEntryOrdinal)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	entry := c.entries[path]
	entry.usageTotal = &usageTotalMemo{key: identity, total: total}
	c.entries[path] = entry
	c.touch(path)
	c.evictLocked()
	c.mu.Unlock()
	return cloneEvenerUsage(total), nil
}

// scanUsageTotal reads the transcript once, decoding only each entry's usage
// block. It reuses scanSemanticTranscript so the format gate (v1 rejection,
// unknown-field strictness, header validation) is exactly the one every other
// reader in this package applies.
func scanUsageTotal(path string, maxLineBytes int, fromEntryOrdinal int) (*appwire.EvenerUsage, error) {
	var accumulated llm.Usage
	counted := false
	ordinal := 0
	if _, err := scanSemanticTranscript(path, maxLineBytes, func(raw json.RawMessage) error {
		ordinal++
		if ordinal < fromEntryOrdinal {
			return nil
		}
		var record usageOnlyEntry
		if err := json.Unmarshal(raw, &record); err != nil {
			// Unreachable for any line scanSemanticTranscript admits: it has
			// already strictly decoded the whole entry into transcript.Entry,
			// of which this is a field-for-field subset. A failure here means
			// usageOnlyEntry has drifted from schema.Turn, and skipping the
			// record would silently undercount — reporting a wrong total is
			// worse than reporting none, so surface it.
			return fmt.Errorf("decode transcript entry usage: %w", err)
		}
		if appwire.EvenerUsageFromLLM(record.Turn.Usage) == nil {
			return nil
		}
		accumulated = accumulated.Add(record.Turn.Usage)
		counted = true
		return nil
	}); err != nil {
		return nil, err
	}
	observeIndexRead(ReadStats{usageScans: 1})
	if !counted {
		return nil, nil
	}
	return appwire.EvenerUsageFromLLM(accumulated), nil
}

// usageOnlyEntry decodes the one field the sum needs. scanSemanticTranscript has
// already validated the full record, so this narrow view can ignore the rest
// rather than paying to decode a whole turn's message content per line.
type usageOnlyEntry struct {
	Turn struct {
		Usage llm.Usage `json:"usage"`
	} `json:"turn"`
}

type usageTotalMemo struct {
	key   scanMemoKey
	total *appwire.EvenerUsage
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
