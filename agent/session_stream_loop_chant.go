package agent

import (
	"fmt"
	"strings"
)

// streamContentChant detects a runaway model chanting the same short
// passage over and over with no forward progress -- the reasoning-only
// shape a raw tool-call counter is structurally blind to (cline #13041:
// "Let me" x2865, tool_use_count=0). It is gemini-cli's content-loop check,
// ported to Go: the trailing chunk of streamed text is compared against
// everything seen so far, and a trip requires both enough repeats AND that
// the text BETWEEN repeats is not itself varied -- the false-positive guard
// the corrected writeup calls load-bearing, not polish.
type streamContentChant struct {
	buf     []rune
	inFence bool
}

const (
	// chantChunkRunes is the trailing-text window compared for repetition
	// (gemini-cli's CONTENT_CHUNK_SIZE).
	chantChunkRunes = 50
	// chantThreshold is how many times the trailing chunk must recur before
	// this is even considered a candidate chant (gemini-cli's
	// CONTENT_LOOP_THRESHOLD).
	chantThreshold = 10
	// chantMaxBufRunes bounds memory and the per-delta scan cost: a
	// legitimately long response pays the same bounded per-delta check as a
	// short one, since the buffer never grows past this regardless of how
	// much total text streams through it.
	chantMaxBufRunes = 5000
	// chantMaxDistinctPeriods is the false-positive guard: among the text
	// spans BETWEEN consecutive occurrences of the repeated chunk, at most
	// this many may be distinct before the match is judged a coincidental
	// shared prefix (a numbered list, a repeated section header followed by
	// different content each time) rather than a genuine chant.
	chantMaxDistinctPeriods = chantThreshold / 2
)

func newStreamContentChant() *streamContentChant {
	return &streamContentChant{}
}

// observe feeds one delta of streamed text -- assistant text or reasoning;
// callers route both through the same tracker, since the pathology is
// content repetition regardless of channel -- and reports a trip when it
// detects chanting.
func (c *streamContentChant) observe(delta string) *loopTrip {
	if delta == "" {
		return nil
	}
	if strings.Contains(delta, "```") {
		// A fence boundary (open or close) invalidates whatever was
		// building. Content inside a fence is never chant-tracked: real
		// code legitimately repeats short, syntactically-forced substrings
		// (braces, imports, boilerplate) far more than prose ever should.
		c.buf = c.buf[:0]
		c.inFence = !c.inFence
		return nil
	}
	if c.inFence {
		return nil
	}
	c.buf = append(c.buf, []rune(delta)...)
	if len(c.buf) > chantMaxBufRunes {
		c.buf = c.buf[len(c.buf)-chantMaxBufRunes:]
	}
	return c.checkChant()
}

func (c *streamContentChant) checkChant() *loopTrip {
	n := len(c.buf)
	if n < chantChunkRunes {
		return nil
	}
	chunk := string(c.buf[n-chantChunkRunes:])
	var starts []int
	for i := 0; i+chantChunkRunes <= n; i++ {
		if string(c.buf[i:i+chantChunkRunes]) == chunk {
			starts = append(starts, i)
		}
	}
	if len(starts) < chantThreshold {
		return nil
	}
	// False-positive guard: the spans between consecutive occurrences of
	// the repeated chunk ("periods") must themselves be mostly identical,
	// not mostly unique.
	recent := starts[len(starts)-chantThreshold:]
	periods := map[string]bool{}
	for i := 1; i < len(recent); i++ {
		periods[string(c.buf[recent[i-1]:recent[i]])] = true
	}
	if len(periods) > chantMaxDistinctPeriods {
		return nil
	}
	return &loopTrip{
		Kind:   loopTripChant,
		Detail: fmt.Sprintf("the same %d characters repeated %d times with no forward progress", chantChunkRunes, len(starts)),
	}
}
