package agent

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// savedNeedsResponseSession runs one turn that ends on needs_response in a
// session saved under a temp dir, lets before run on it, closes it, and
// returns its dir and saved meta for a restore.
func savedNeedsResponseSession(t *testing.T, before func(*Session)) (string, schema.SessionMeta) {
	t.Helper()
	dir, meta, _ := saveAfter(t, []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return endReasonResponse("which?", "needs_response") },
	}, func(ctx context.Context, live *Session) {
		if _, err := live.ProcessInput(ctx, "hello", nil); err != nil {
			t.Fatal(err)
		}
		before(live)
	})
	return dir, meta
}

// Restore rests a needs_response turn the way live does: with a job
// notification still waiting to be delivered, the session rests idle, and
// the state it announces on restore is not awaiting.
func TestRestoredNeedsResponseWithWorkPendingRestsIdle(t *testing.T) {
	t.Parallel()
	dir, meta := savedNeedsResponseSession(t, func(live *Session) {
		appendPendingJobNotificationRecord(t, live.jobManager, live.ID())
	})
	sess := restoreSaved(t, dir, meta, RestoreSessionConfig{})
	defer sess.Close()
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("restored state with a notification pending = %q, want idle", got)
	}
	// The wire state of an idle session with work queued is active.
	if got := queuedSessionStart(t, sess).State; got == string(SessionAwaiting) {
		t.Fatalf("restore announced %q with a notification pending", got)
	}
}

// A reply request survives a restart: a session restored onto a
// needs_response turn while a child still works rests idle, and once the
// child is done a filtered wake that finds nothing in flight waits a fresh
// quiet period, then rests awaiting and announces it.
func TestRestoredNeedsResponseHeldByWorkInFlightStillOwesItsRest(t *testing.T) {
	t.Parallel()
	dir, meta := savedNeedsResponseSession(t, func(*Session) {})
	fake := agenttest.NewFakeClock()
	// The settle recompute is deferred so a working child is in place when it
	// runs, as the root's own restore does once its side effects are back.
	sess := restoreSaved(t, dir, meta, RestoreSessionConfig{clock: fake, deferRestoreSideEffects: true})
	evs, mu, done := collectEvents(sess)
	child := newTestSessionForState(t)
	working := &subagent{id: child.ID(), sess: child, running: true}
	sess.subagents.track(working)
	sess.recomputeRestoredState(0, nil)
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("restored state while the child works = %q, want idle", got)
	}

	working.mu.Lock()
	working.running = false
	working.mu.Unlock()
	ctx := tripwireCtx(t)
	if _, err := sess.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
		t.Fatalf("filtered wake: %v", err)
	}
	requireRestsAwaitingAfterFreshQuietPeriod(t, sess, fake, evs, mu, done)
}
