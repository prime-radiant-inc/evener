package jobstore

import (
	"bytes"
	"regexp"

	"primeradiant.com/serf/agent/provenance"
)

const maxOutputMatcherLineBytes = 4096

// OutputMatcher applies a regexp to completed lines in an output stream.
type OutputMatcher struct {
	re       *regexp.Regexp
	carry    []byte
	overlong bool
	// carryProvenance is the causal provenance of the buffered partial line.
	// It is reported with the match when a later chunk completes that line.
	carryProvenance *provenance.Causal
	// scanOffset marks the stream position already covered by an attach-time
	// scan: FeedAt contributes no matches from bytes below it.
	scanOffset int64
	// feedOffset counts every byte handed to Feed, supplying the stream end
	// offset for callers that feed sequentially without tracking offsets.
	feedOffset int64
}

// OutputMatch is a matched output line plus the causal provenance of the line
// fragments that produced it.
type OutputMatch struct {
	Line       string
	Provenance *provenance.Causal
}

// NewOutputMatcher returns a matcher over re.
func NewOutputMatcher(re *regexp.Regexp) *OutputMatcher {
	return &OutputMatcher{re: re}
}

// Regexp returns the compiled pattern this matcher applies, so callers that
// already hold a matcher can reuse its regexp instead of recompiling.
func (m *OutputMatcher) Regexp() *regexp.Regexp { return m.re }

// SetScanOffset marks bytes at stream offsets below off as covered by an
// attach-time scan. FeedAt discards them, so output seen by both the scan
// and the stream cannot fire twice.
func (m *OutputMatcher) SetScanOffset(off int64) {
	m.scanOffset = off
}

// SeedCarry primes the partial-line carry with the retained tail after the
// last newline of an attach-time scan, so a token straddling the scan
// boundary still matches once the rest of its line arrives.
func (m *OutputMatcher) SeedCarry(tail []byte) {
	m.carry = nil
	m.carryProvenance = nil
	m.overlong = false
	m.appendPartialLine(tail)
}

// Feed appends newly produced output and returns matching completed lines.
// It assumes the stream is fed sequentially from its first byte: an internal
// byte counter supplies the end offset FeedAt needs, so bytes an attach-time
// scan already covered are skipped even when their chunk arrives after
// SetScanOffset.
func (m *OutputMatcher) Feed(chunk []byte) []string {
	m.feedOffset += int64(len(chunk))
	return m.FeedAt(chunk, m.feedOffset)
}

// FeedAt appends newly produced output whose final byte sits just before
// stream offset endOffset and returns matching completed lines. Chunks
// wholly below the scan offset are discarded; a straddling chunk contributes
// only its suffix at or beyond the scan offset.
func (m *OutputMatcher) FeedAt(chunk []byte, endOffset int64) []string {
	return outputMatchLines(m.FeedAtWithProvenance(chunk, endOffset, nil))
}

// FeedAtWithProvenance is FeedAt plus causal provenance for the supplied chunk.
// If a line straddles chunks, its reported provenance is the union of every
// chunk that contributed to the carried line.
func (m *OutputMatcher) FeedAtWithProvenance(chunk []byte, endOffset int64, p *provenance.Causal) []OutputMatch {
	if len(chunk) == 0 || endOffset <= m.scanOffset {
		return nil
	}
	if start := endOffset - int64(len(chunk)); start < m.scanOffset {
		chunk = chunk[m.scanOffset-start:]
	}

	var matches []OutputMatch
	for len(chunk) > 0 {
		idx := bytes.IndexByte(chunk, '\n')
		if idx < 0 {
			m.appendPartialLineWithProvenance(chunk, p)
			break
		}

		m.appendPartialLineWithProvenance(chunk[:idx], p)
		lineProvenance := provenance.Clone(m.carryProvenance)
		if !m.overlong {
			line := completedLine(m.carry)
			if m.re.Match(line) {
				matches = append(matches, OutputMatch{Line: string(line), Provenance: lineProvenance})
			}
		}
		m.carry = nil
		m.carryProvenance = nil
		m.overlong = false
		chunk = chunk[idx+1:]
	}
	return matches
}

// ScanRetained applies the matcher's regexp to the COMPLETE lines in data
// (everything up to and including the final newline; any unterminated tail is
// ignored, as it belongs to the carry) and returns the LAST matching line. It is
// a level check used at attach: it does not touch the carry, scanOffset, or
// feedOffset, so it composes with a subsequent FeedAt. Lines longer than
// maxOutputMatcherLineBytes are skipped exactly as the stream path skips them, so
// attach and stream agree on what counts as a valid line.
func (m *OutputMatcher) ScanRetained(data []byte) (last string, matched bool) {
	var (
		line     []byte
		overlong bool
	)
	for len(data) > 0 {
		idx := bytes.IndexByte(data, '\n')
		if idx < 0 {
			// Unterminated tail: belongs to the carry, not this scan.
			break
		}
		line, overlong = appendLineFragment(line, overlong, data[:idx])
		if !overlong {
			completed := completedLine(line)
			if m.re.Match(completed) {
				last = string(completed)
				matched = true
			}
		}
		line = line[:0]
		overlong = false
		data = data[idx+1:]
	}
	return last, matched
}

// Flush returns a match for any buffered final partial line and clears it.
func (m *OutputMatcher) Flush() []string {
	return outputMatchLines(m.FlushWithProvenance(nil))
}

// FlushWithProvenance returns a match for any buffered final partial line,
// unioned with p, and clears it.
func (m *OutputMatcher) FlushWithProvenance(p *provenance.Causal) []OutputMatch {
	if len(m.carry) == 0 || m.overlong {
		m.carry = nil
		m.carryProvenance = nil
		m.overlong = false
		return nil
	}

	line := m.carry
	lineProvenance := provenance.Union(m.carryProvenance, p)
	m.carry = nil
	m.carryProvenance = nil
	if len(line) > maxOutputMatcherLineBytes {
		return nil
	}
	if m.re.Match(line) {
		return []OutputMatch{{Line: string(line), Provenance: lineProvenance}}
	}
	return nil
}

func (m *OutputMatcher) appendPartialLine(part []byte) {
	m.carry, m.overlong = appendLineFragment(m.carry, m.overlong, part)
}

func (m *OutputMatcher) appendPartialLineWithProvenance(part []byte, p *provenance.Causal) {
	m.appendPartialLine(part)
	if p != nil {
		m.carryProvenance = provenance.Union(m.carryProvenance, p)
	}
}

func outputMatchLines(matches []OutputMatch) []string {
	if len(matches) == 0 {
		return nil
	}
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		out = append(out, match.Line)
	}
	return out
}

// appendLineFragment appends a within-line fragment to carry, enforcing the
// overlong-line policy: once a line exceeds maxOutputMatcherLineBytes the carry
// is dropped and overlong latches until the line ends. It is the single home for
// the matcher's line-length policy, shared by the streaming carry
// (appendPartialLine) and the attach-time level scan (ScanRetained).
func appendLineFragment(carry []byte, overlong bool, part []byte) ([]byte, bool) {
	if len(part) == 0 || overlong {
		return carry, overlong
	}

	limit := maxOutputMatcherLineBytes
	if part[len(part)-1] == '\r' {
		limit++
	}
	if len(carry)+len(part) > limit {
		return nil, true
	}
	return append(carry, part...), false
}

func completedLine(line []byte) []byte {
	if len(line) > 0 && line[len(line)-1] == '\r' {
		return line[:len(line)-1]
	}
	return line
}
