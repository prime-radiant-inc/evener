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
