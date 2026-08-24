package jobstore

import (
	"regexp"
	"testing"
)

func TestCovNotifyRank(t *testing.T) {
	tests := []struct {
		state NotifyState
		want  int
	}{
		{state: NotifyPending, want: 1},
		{state: NotifyDelivered, want: 2},
		{state: NotifyConsumed, want: 2},
		{state: NotifyState(""), want: 0},
		{state: NotifyState("bogus"), want: 0},
	}
	for _, tc := range tests {
		if got := notifyRank(tc.state); got != tc.want {
			t.Errorf("notifyRank(%q) = %d, want %d", tc.state, got, tc.want)
		}
	}
}

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
