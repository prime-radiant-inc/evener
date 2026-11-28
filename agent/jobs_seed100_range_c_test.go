//go:build evenerfuzz

package agent

import (
	"errors"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/internal/jobstore"
)

func seed100JobsRangeC(t *testing.T) {
	t.Helper()
	want := errors.New("range c fault")
	jm := newTestJM(t)
	freezeClock(jm)

	output, err := jobstore.OpenOutputNoSync(filepath.Join(jm.dir, "jobs", "range-c.log"), 64)
	if err != nil {
		t.Fatal(err)
	}
	run := &runningJob{
		rec:    &jobstore.JobRecord{JobID: "range-c", Type: jobstore.JobShell, Status: jobstore.StatusRunning},
		output: output,
		done:   make(chan struct{}),
	}
	jm.running[run.rec.JobID] = run
	jm.appendEvent = func(jobstore.Event) error { return want }
	if err := jm.finishJob(run, jobstore.StatusFailed, "fault", nil); !errors.Is(err, want) {
		t.Fatalf("finish append fault = %v", err)
	}

	// A successful durable append may race with runtime removal. The persisted
	// terminal must not mutate a stale runningJob after that identity changes.
	jm.appendEvent = func(event jobstore.Event) error {
		if err := jm.store.Append(event); err != nil {
			return err
		}
		delete(jm.running, run.rec.JobID)
		return nil
	}
	if _, err := jm.writeFinishJob(run, jobstore.StatusCompleted, "stale", nil); err != nil {
		t.Fatal(err)
	}

	if err := validateStructuredResult(map[string]any{"x": 1}, map[string]any{"type": "object"}); err != nil {
		t.Fatalf("valid schema rejected: %v", err)
	}

	foreign := &jobstore.JobRecord{OwnerSessionID: "other", Status: jobstore.StatusCompleted, TerminalGen: "tg"}
	if rearm, appendEvent := rearmTerminalNotificationDecision(foreign, jm.sessionID); rearm || appendEvent {
		t.Fatalf("foreign terminal rearmed: %v, %v", rearm, appendEvent)
	}
	nonterminal := &jobstore.JobRecord{OwnerSessionID: jm.sessionID, Status: jobstore.StatusRunning, TerminalGen: "tg"}
	if rearm, appendEvent := rearmTerminalNotificationDecision(nonterminal, jm.sessionID); rearm || appendEvent {
		t.Fatalf("nonterminal rearmed: %v, %v", rearm, appendEvent)
	}
}
