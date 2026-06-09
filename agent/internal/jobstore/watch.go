package jobstore

import (
	"bytes"
	"regexp"
)

// OutputMatcher applies a regexp to completed lines in an output stream.
type OutputMatcher struct {
	re    *regexp.Regexp
	carry []byte
}

// NewOutputMatcher returns a matcher over re.
func NewOutputMatcher(re *regexp.Regexp) *OutputMatcher {
	return &OutputMatcher{re: re}
}

// Feed appends newly produced output and returns matching completed lines.
func (m *OutputMatcher) Feed(chunk []byte) []string {
	if len(chunk) == 0 {
		return nil
	}

	var matches []string
	if len(m.carry) != 0 {
		idx := bytes.IndexByte(chunk, '\n')
		if idx < 0 {
			m.carry = append(m.carry, chunk...)
			return nil
		}

		line := make([]byte, 0, len(m.carry)+idx)
		line = append(line, m.carry...)
		line = append(line, chunk[:idx]...)
		if m.re.Match(line) {
			matches = append(matches, string(line))
		}
		m.carry = nil
		chunk = chunk[idx+1:]
	}

	start := 0
	for {
		idx := bytes.IndexByte(chunk[start:], '\n')
		if idx < 0 {
			break
		}

		end := start + idx
		line := chunk[start:end]
		if m.re.Match(line) {
			matches = append(matches, string(line))
		}
		start = end + 1
	}

	if start < len(chunk) {
		m.carry = append(m.carry, chunk[start:]...)
	}
	return matches
}

// Flush returns a match for any buffered final partial line and clears it.
func (m *OutputMatcher) Flush() []string {
	if len(m.carry) == 0 {
		return nil
	}

	line := m.carry
	m.carry = nil
	if m.re.Match(line) {
		return []string{string(line)}
	}
	return nil
}
