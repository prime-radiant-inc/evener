package jobstore

import (
	"regexp"
	"testing"
)

func TestCovRegexp(t *testing.T) {
	re := regexp.MustCompile("hello")
	m := NewOutputMatcher(re)
	got := m.Regexp()
	if got == nil || got.String() != "hello" {
		t.Fatalf("Regexp() = %v, want pattern %q", got, "hello")
	}
	if !got.MatchString("hello world") || got.MatchString("goodbye") {
		t.Fatalf("Regexp() returned matcher with unexpected behavior: %v", got)
	}

	m2 := NewOutputMatcher(nil)
	if got := m2.Regexp(); got != nil {
		t.Fatalf("nil regexp = %v", got)
	}
}
