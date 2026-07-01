package agent

import (
	"errors"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// createShell refuses to stand up a job on a closing manager.
func TestS1Cov_createShell_ClosingRejected(t *testing.T) {
	jm := newTestJM(t)
	jm.mu.Lock()
	jm.closing = true
	jm.mu.Unlock()
	if _, err := jm.createShell(createShellOpts{Command: "x"}); !errors.Is(err, errJobManagerClosing) {
		t.Fatalf("createShell on closing manager err = %v, want errJobManagerClosing", err)
	}
}

// createShell surfaces a durable start-event append failure.
func TestS1Cov_createShell_StartAppendFailure(t *testing.T) {
	jm := newTestJM(t)
	failAppendN(jm, jobstore.EventJobStarted, 1)
	if _, err := jm.createShell(createShellOpts{Command: "x"}); err == nil {
		t.Fatal("createShell must surface the start-event append failure")
	}
}
