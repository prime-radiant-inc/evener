package agent

import (
	"errors"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/internal/jobstore"
)

// TestWriteFinishJobRetriesTheRetainedTerminalEvent pins High 3.
//
// A finish whose append is refused leaves the session still running: the
// terminal record never became durable, so a completion notification armed
// against it would fire for an event the journal does not hold. The caller
// therefore retries the finish.
//
// A retry that builds a NEW event mints a new terminal generation. The fold
// keeps the first finish it meets, and the notification was armed for the
// first generation — so the retry's record is invisible to the notification
// machinery and the session silently skips its completion. The retry has to
// carry the event the refused attempt built.
func TestWriteFinishJobRetriesTheRetainedTerminalEvent(t *testing.T) {
	jm := newTestJM(t)
	t.Cleanup(func() { _ = jm.store.Close() })

	output, err := jobstore.OpenOutputNoSync(filepath.Join(jm.dir, "jobs", "retried-finish.log"), 64)
	if err != nil {
		t.Fatalf("open output: %v", err)
	}
	t.Cleanup(func() { _ = output.Close() })
	run := &runningJob{
		rec:    &jobstore.JobRecord{JobID: "job_retried_finish", Type: jobstore.JobShell, Status: jobstore.StatusRunning},
		output: output,
		done:   make(chan struct{}),
	}
	jm.running[run.rec.JobID] = run
	t.Cleanup(func() { delete(jm.running, run.rec.JobID) })

	refused := errors.New("journal name is not reachable")
	jm.appendEvent = func(jobstore.Event) error { return refused }
	if _, err := jm.writeFinishJob(run, jobstore.StatusCompleted, "done", nil); !errors.Is(err, refused) {
		t.Fatalf("finish whose append was refused = %v, want the append's error: the retry below is only a retry if this attempt failed", err)
	}
	if run.retainedFinish == nil {
		t.Fatal("the refused finish retained no event, so the retry has nothing to reuse and must mint a new generation")
	}
	firstGeneration := run.retainedFinish.TerminalGen

	var persisted []jobstore.Event
	jm.appendEvent = func(event jobstore.Event) error {
		persisted = append(persisted, event)
		return nil
	}
	terminal, err := jm.writeFinishJob(run, jobstore.StatusCompleted, "done", nil)
	if err != nil {
		t.Fatalf("retried finish: %v", err)
	}
	if len(persisted) != 1 {
		t.Fatalf("the retry appended %d events, want the single finish it was asked to persist", len(persisted))
	}
	if persisted[0].TerminalGen != firstGeneration {
		t.Fatalf("the retry persisted terminal generation %q, want the %q the refused attempt built: the completion notification was armed for the first and never matches the second", persisted[0].TerminalGen, firstGeneration)
	}
	if terminal == nil || terminal.generation != firstGeneration {
		t.Fatalf("the retried finish completed with terminal generation %v, want the %q the refused attempt built", terminal, firstGeneration)
	}
	if run.retainedFinish != nil {
		t.Fatal("the durable retry left the retained event in place, so a later finish would reuse a generation that is already spent")
	}
}
