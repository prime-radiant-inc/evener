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
	if len(part) == 0 || m.overlong {
		return
	}

	limit := maxOutputMatcherLineBytes
	if part[len(part)-1] == '\r' {
		limit++
	}
	if len(m.carry)+len(part) > limit {
		m.carry = nil
		m.overlong = true
		return
	}
	m.carry = append(m.carry, part...)
}

func completedLine(line []byte) []byte {
	if len(line) > 0 && line[len(line)-1] == '\r' {
		return line[:len(line)-1]
	}
	return line
}
