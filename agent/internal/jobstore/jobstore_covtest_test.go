package jobstore

import (
	"regexp"
	"testing"
)

// TestCovNotifyRank covers notifyRank (fold.go lines 269-278).
func TestCovNotifyRank(t *testing.T) {
	if got := notifyRank(NotifyPending); got != 1 {
		t.Fatalf("NotifyPending = %d, want 1", got)
	}
	if got := notifyRank(NotifyDelivered); got != 2 {
		t.Fatalf("NotifyDelivered = %d, want 2", got)
	}
	if got := notifyRank(NotifyConsumed); got != 2 {
		t.Fatalf("NotifyConsumed = %d, want 2", got)
	}
	// Default / zero value → 0.
	if got := notifyRank(NotifyState("")); got != 0 {
		t.Fatalf("empty = %d, want 0", got)
	}
	if got := notifyRank(NotifyState("bogus")); got != 0 {
		t.Fatalf("bogus = %d, want 0", got)
	}
}

// TestCovRegexp covers Regexp (watch.go line 95).
func TestCovRegexp(t *testing.T) {
	re := regexp.MustCompile("hello")
	m := NewOutputMatcher(re)
	got := m.Regexp()
	if got == nil || !got.MatchString("hello world") {
		t.Fatalf("Regexp = %v", got)
	}

	// Nil regexp.
	m2 := NewOutputMatcher(nil)
	if got := m2.Regexp(); got != nil {
		t.Fatalf("nil regexp = %v", got)
	}
}
