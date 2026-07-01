package agent

import (
	"context"
	"strings"
	"testing"
)

// TestW2Conc_ProcessOneInputClosedSessionBails pins the entry guard: driving an
// input turn on an already-closed session returns a "session is closed" error
// without processing, so a late input racing Close does not resurrect the loop.
func TestW2Conc_ProcessOneInputClosedSessionBails(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	s.Close()

	_, _, err := s.processOneInput(context.Background(), "hello", nil, EntryUserInput, nil)
	if err == nil || !strings.Contains(err.Error(), "session is closed") {
		t.Fatalf("processOneInput on a closed session err = %v, want 'session is closed'", err)
	}
}
