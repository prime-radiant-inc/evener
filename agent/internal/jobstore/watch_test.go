package jobstore

import (
	"regexp"
	"strings"
	"testing"
)

func TestOutputMatcherMatchesCompletedLine(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`(?i)ready`))
	got := m.Feed([]byte("starting\nserver ready\n"))
	if len(got) != 1 || got[0] != "server ready" {
		t.Fatalf("matches = %#v, want [\"server ready\"]", got)
	}
}

func TestOutputMatcherNoSilentMissAcrossChunks(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`READY`))
	if got := m.Feed([]byte("server REA")); len(got) != 0 {
		t.Fatalf("partial line must not match yet: %#v", got)
	}
	if got := m.Feed([]byte("DY now\n")); len(got) != 1 || got[0] != "server READY now" {
		t.Fatalf("split-across-chunks line must match once joined: %#v", got)
	}
}

func TestOutputMatcherDoesNotRematchOldBytes(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`ready`))
	_ = m.Feed([]byte("ready\n"))
	got := m.Feed([]byte("still going\n"))
	if len(got) != 0 {
		t.Errorf("already-consumed line must not re-match: %#v", got)
	}
}

func TestOutputMatcherFlushReturnsFinalPartial(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`done`))
	if got := m.Feed([]byte("all done")); len(got) != 0 {
		t.Fatalf("unterminated line must not match on Feed: %#v", got)
	}
	if got := m.Flush(); len(got) != 1 || got[0] != "all done" {
		t.Errorf("Flush must match the buffered final line: %#v", got)
	}
}

func TestOutputMatcherEmptyChunksAreNoop(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`partial`))
	if got := m.Feed(nil); len(got) != 0 {
		t.Fatalf("nil chunk must not match: %#v", got)
	}
	if got := m.Feed([]byte("part")); len(got) != 0 {
		t.Fatalf("partial line must not match yet: %#v", got)
	}
	if got := m.Feed([]byte{}); len(got) != 0 {
		t.Fatalf("empty chunk must not match: %#v", got)
	}
	if got := m.Feed([]byte("ial\n")); len(got) != 1 || got[0] != "partial" {
		t.Fatalf("line after empty chunk must match once completed: %#v", got)
	}
}

func TestOutputMatcherEmptyRegexpMatchesCompletedLines(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(``))
	got := m.Feed([]byte("one\n\npartial"))
	if len(got) != 2 || got[0] != "one" || got[1] != "" {
		t.Fatalf("empty regexp matches completed lines = %#v, want [\"one\", \"\"]", got)
	}
	if got := m.Flush(); len(got) != 1 || got[0] != "partial" {
		t.Fatalf("empty regexp must match flushed partial line: %#v", got)
	}
}

func TestOutputMatcherFlushRepeatedlyClearsCarry(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`done`))
	_ = m.Feed([]byte("done"))
	if got := m.Flush(); len(got) != 1 || got[0] != "done" {
		t.Fatalf("first Flush must match the buffered line: %#v", got)
	}
	if got := m.Flush(); len(got) != 0 {
		t.Fatalf("second Flush must not re-match: %#v", got)
	}
}

func TestOutputMatcherStripsCRLFBeforeMatching(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`ready$`))
	got := m.Feed([]byte("server ready\r\n"))
	if len(got) != 1 || got[0] != "server ready" {
		t.Fatalf("CRLF line matches = %#v, want [\"server ready\"]", got)
	}
}

func TestOutputMatcherBoundsOverlongCarry(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`ready`))
	if got := m.Feed([]byte(strings.Repeat("x", maxOutputMatcherLineBytes+1))); len(got) != 0 {
		t.Fatalf("overlong unterminated line must not match: %#v", got)
	}
	if len(m.carry) > maxOutputMatcherLineBytes {
		t.Fatalf("carry length = %d, want <= %d", len(m.carry), maxOutputMatcherLineBytes)
	}
	if got := m.Feed([]byte("ready\n")); len(got) != 0 {
		t.Fatalf("overlong line must be skipped through newline: %#v", got)
	}
	if got := m.Feed([]byte("server ready\n")); len(got) != 1 || got[0] != "server ready" {
		t.Fatalf("matcher did not recover after overlong line: %#v", got)
	}
}

func TestOutputMatcherFlushSkipsOverlongPartial(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`ready`))
	if got := m.Feed([]byte(strings.Repeat("x", maxOutputMatcherLineBytes+1) + "ready")); len(got) != 0 {
		t.Fatalf("overlong unterminated line must not match on Feed: %#v", got)
	}
	if got := m.Flush(); len(got) != 0 {
		t.Fatalf("overlong final partial line must not match on Flush: %#v", got)
	}
	if got := m.Feed([]byte("server ready\n")); len(got) != 1 || got[0] != "server ready" {
		t.Fatalf("matcher did not recover after overlong flush: %#v", got)
	}
}
