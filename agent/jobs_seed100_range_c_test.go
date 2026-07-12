//go:build serffuzz

package agent

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

func seed100JobsRangeC(t *testing.T) {
	t.Helper()
	want := errors.New("range c fault")
	jm := newTestJM(t)
	freezeClock(jm)

	output, err := jm.openOutput(filepath.Join(jm.dir, "jobs", "range-c.log"), 64)
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

	assertStructured := func(name string, value, schema any, captureFailed bool, wantValue bool, wantValid *bool, wantReason string) {
		t.Helper()
		got, valid, reason := boundedStructuredResult(value, schema, captureFailed)
		if (got != nil) != wantValue || reason != wantReason {
			t.Fatalf("%s: value=%#v valid=%v reason=%q", name, got, valid, reason)
		}
		if wantValid == nil {
			if valid != nil {
				t.Fatalf("%s: valid=%v, want nil", name, *valid)
			}
		} else if valid == nil || *valid != *wantValid {
			t.Fatalf("%s: valid=%v, want %v", name, valid, *wantValid)
		}
	}
	valid, invalid := true, false
	assertStructured("capture failed", nil, nil, true, false, &invalid, structuredResultReasonSchemaCaptureFailed)
	assertStructured("schema result missing", nil, map[string]any{"type": "object"}, false, false, &invalid, structuredResultReasonSchemaResultMissing)
	assertStructured("no result requested", nil, nil, false, false, nil, "")
	assertStructured("marshal failed", make(chan int), nil, false, false, &invalid, structuredResultReasonSchemaCaptureFailed)
	assertStructured("too large", strings.Repeat("x", maxPersistedStructuredResultJSONBytes), nil, false, false, &invalid, structuredResultReasonSchemaResultTooLarge)
	assertStructured("schema invalid", 1, map[string]any{"type": "string"}, false, false, &invalid, structuredResultReasonSchemaValidationFailed)
	assertStructured("valid", map[string]any{"x": 1}, map[string]any{"type": "object"}, false, true, &valid, "")

	foreign := &jobstore.JobRecord{OwnerSessionID: "other", Status: jobstore.StatusCompleted, TerminalGen: "tg"}
	if rearm, appendEvent := rearmTerminalNotificationDecision(foreign, jm.sessionID); rearm || appendEvent {
		t.Fatalf("foreign terminal rearmed: %v, %v", rearm, appendEvent)
	}
	nonterminal := &jobstore.JobRecord{OwnerSessionID: jm.sessionID, Status: jobstore.StatusRunning, TerminalGen: "tg"}
	if rearm, appendEvent := rearmTerminalNotificationDecision(nonterminal, jm.sessionID); rearm || appendEvent {
		t.Fatalf("nonterminal rearmed: %v, %v", rearm, appendEvent)
	}
}
