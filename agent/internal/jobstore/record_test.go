package jobstore

import (
	"strings"
	"testing"
)

func TestStatusIsTerminal(t *testing.T) {
	terminal := []Status{StatusCompleted, StatusFailed, StatusCancelled, StatusStopped}
	for _, s := range terminal {
		if !s.IsTerminal() {
			t.Errorf("Status %q should be terminal", s)
		}
	}
	if StatusRunning.IsTerminal() {
		t.Errorf("Status %q should not be terminal", StatusRunning)
	}
}

func TestNewJobIDFormatAndUniqueness(t *testing.T) {
	a := NewJobID()
	b := NewJobID()
	if !strings.HasPrefix(a, "job_") {
		t.Errorf("job id %q should start with job_", a)
	}
	if a == b {
		t.Errorf("two job ids should differ: %q == %q", a, b)
	}
	// "job_" + 26-char ULID
	if len(a) != len("job_")+26 {
		t.Errorf("job id %q has unexpected length %d", a, len(a))
	}
}
