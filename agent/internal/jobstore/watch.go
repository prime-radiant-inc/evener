package jobstore

import (
	"bytes"
	"regexp"
)

const maxOutputMatcherLineBytes = 4096

// OutputMatcher applies a regexp to completed lines in an output stream.
type OutputMatcher struct {
	re       *regexp.Regexp
	carry    []byte
	overlong bool
	// scanOffset marks the stream position already covered by an attach-time
	// scan: FeedAt contributes no matches from bytes below it.
	scanOffset int64
	// feedOffset counts every byte handed to Feed, supplying the stream end
	// offset for callers that feed sequentially without tracking offsets.
	feedOffset int64
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
	if len(chunk) == 0 || endOffset <= m.scanOffset {
		return nil
	}
	if start := endOffset - int64(len(chunk)); start < m.scanOffset {
		chunk = chunk[m.scanOffset-start:]
	}

	var matches []string
	for len(chunk) > 0 {
		idx := bytes.IndexByte(chunk, '\n')
		if idx < 0 {
			m.appendPartialLine(chunk)
			break
		}

		m.appendPartialLine(chunk[:idx])
		if !m.overlong {
			line := completedLine(m.carry)
			if m.re.Match(line) {
				matches = append(matches, string(line))
			}
		}
		m.carry = nil
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
	if len(m.carry) == 0 || m.overlong {
		m.carry = nil
		m.overlong = false
		return nil
	}

	line := m.carry
	m.carry = nil
	if len(line) > maxOutputMatcherLineBytes {
		return nil
	}
	if m.re.Match(line) {
		return []string{string(line)}
	}
	return nil
}

func (m *OutputMatcher) appendPartialLine(part []byte) {
	m.carry, m.overlong = appendLineFragment(m.carry, m.overlong, part)
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
