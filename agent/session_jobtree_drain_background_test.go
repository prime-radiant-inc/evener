package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
)

// #297: a one-shot run whose model started a service holds the drain open until
// the caller's context is cancelled — the agent's own work finished in 149s and
// the process then sat for another 751s. The drain cannot tell a service from a
// long silent build (drainSubtreeIsStalled counts any running managed job as a
// live component precisely so a build is never cut), so it hands the question
// back to the model instead of guessing.

// startNeverFinishingBackgroundJob launches a real background shell that will
// outlive the test, exactly the way the registered shell tool does.
func startNeverFinishingBackgroundJob(t *testing.T, sess *Session) string {
	t.Helper()
	env := sess.currentEnv()
	se, ok := env.(execenv.StreamingExecutor)
	if !ok {
		t.Fatal("session env does not support streaming")
	}
	res := runShell(context.Background(), sess.jobManager, se, shellArgs{
		Command:    "sleep 300",
		Background: true,
		WorkingDir: env.WorkingDirectory(),
	})
	if res.JobID == "" || !res.RunningInBackground {
		t.Fatalf("shell result = %+v, want a running background job", res)
	}
	t.Cleanup(func() { _, _ = sess.jobManager.stop(res.JobID) })
	return res.JobID
}

func TestDrainNotifiesOnceAboutUndisposedBackgroundJob(t *testing.T) {
	clk := agenttest.NewFakeClock()
	sess := newSession(t, withConfig(SessionConfig{
		clock:            clk,
		NoProjectPrompts: true,
		TurnEndsProcess:  true,
	}))
	jobID := startNeverFinishingBackgroundJob(t, sess)

	notified := make(chan string, 1)
	process := func(_ context.Context, _ string, _ []ImageAttachment, kind EntryKind) (string, error) {
		if kind == EntrySteeringCarrier {
			var texts []string
			for _, m := range sess.drainSteering() {
				texts = append(texts, m.Text)
			}
			select {
			case notified <- strings.Join(texts, "\n"):
			default:
			}
		}
		return "", nil
	}

	recheck := make(chan time.Time, 4)
	kick := func(context.Context) error { return nil }

	// TRIPWIRE: a frozen fake clock drives the grace; nothing here waits on a
	// real clock. 30s only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		_, _ = sess.drainJobTreeWith(ctx, recheck, kick, process)
		close(done)
	}()

	clk.Advance(drainBackgroundGrace + time.Second)
	for range 3 {
		select {
		case recheck <- time.Time{}:
		case <-done:
		}
	}

	// Await the drain's own signal rather than a wall-clock bound: the process
	// hook publishes the carried message the moment the steering turn runs. The
	// outer ctx (cancelled by t.Cleanup) is what stops a genuine hang.
	var msg string
	for msg == "" {
		select {
		case msg = <-notified:
		case <-done:
			t.Fatal("drain returned without notifying about the undisposed background job")
		case recheck <- time.Time{}:
			clk.Advance(drainBackgroundGrace)
		}
	}
	for _, want := range []string{jobID, "detached", "job_stop"} {
		if !strings.Contains(msg, want) {
			t.Errorf("steering message does not mention %q: %q", want, msg)
		}
	}
}

// A serve session outlives the turn, so its background jobs genuinely do report
// later and docs/job-control.md's notification contract is correct. The drain
// must not nag there.
func TestDrainDoesNotNotifyWhenTurnDoesNotEndProcess(t *testing.T) {
	clk := agenttest.NewFakeClock()
	sess := newSession(t, withConfig(SessionConfig{
		clock:            clk,
		NoProjectPrompts: true,
		TurnEndsProcess:  false,
	}))
	startNeverFinishingBackgroundJob(t, sess)

	ids, only := sess.onlyBackgroundDrainJobsOutstanding()
	if !only || len(ids) == 0 {
		t.Fatalf("precondition: expected an outstanding background job, got ids=%v only=%v", ids, only)
	}

	var carrierTurns int
	process := func(_ context.Context, _ string, _ []ImageAttachment, kind EntryKind) (string, error) {
		if kind == EntrySteeringCarrier {
			carrierTurns++
		}
		return "", nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	recheck := make(chan time.Time, 4)
	go func() {
		_, _ = sess.drainJobTreeWith(ctx, recheck, func(context.Context) error { return nil }, process)
		close(done)
	}()

	clk.Advance(drainBackgroundGrace * 10)
	for range 3 {
		select {
		case recheck <- time.Time{}:
		case <-done:
		}
	}
	cancel()
	<-done

	if carrierTurns != 0 {
		t.Errorf("serve-mode drain delivered %d steering turn(s); want 0", carrierTurns)
	}
}
