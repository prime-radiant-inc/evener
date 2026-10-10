package agent

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// savedNeedsResponseSession runs one turn that ends on needs_response in a
// session saved under a temp dir, lets before run on it, closes it, and
// returns its dir and saved meta for a restore.
func savedNeedsResponseSession(t *testing.T, before func(*Session)) (string, schema.SessionMeta) {
	t.Helper()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return endReasonResponse("which?", "needs_response") },
	}})
	dir := t.TempDir()
	live, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("test-model")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir, testOnly: immediateRestConfig()})
	if err != nil {
		t.Fatal(err)
	}
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := live.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatal(err)
	}
	before(live)
	id := live.ID()
	live.Close()
	meta, err := schema.LoadSessionMeta(dir, id)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	return dir, meta
}

func restoreNeedsResponseSession(t *testing.T, dir string, meta schema.SessionMeta, cfg RestoreSessionConfig) *Session {
	t.Helper()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	cfg.StateDir = dir
	sess, err := RestoreSessionFromMetaWithConfig(c, withTestSessionNamer(c, NewOpenAIProfile("test-model")), execenv.NewLocalExecutionEnvironment(dir), meta, cfg)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	return sess
}

// Restore rests a needs_response turn the way live does: with a job
// notification still waiting to be delivered, the session rests idle, and
// the state it announces on restore is not awaiting.
func TestRestoredNeedsResponseWithWorkPendingRestsIdle(t *testing.T) {
	t.Parallel()
	dir, meta := savedNeedsResponseSession(t, func(live *Session) {
		appendPendingJobNotificationRecord(t, live.jobManager, live.ID())
	})
	sess := restoreNeedsResponseSession(t, dir, meta, RestoreSessionConfig{})
	evs, mu, done := collectEvents(sess)
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("restored state with a notification pending = %q, want idle", got)
	}
	sess.Close()
	<-done
	mu.Lock()
	defer mu.Unlock()
	for _, ev := range *evs {
		if d, ok := ev.Data.(events.SessionStartData); ok && ev.Kind == events.EventSessionStart {
			// The wire state of an idle session with work queued is active.
			if d.State == string(SessionAwaiting) {
				t.Fatalf("restore announced %q with a notification pending", d.State)
			}
			return
		}
	}
	t.Fatal("restore announced no session start")
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
	sess := restoreNeedsResponseSession(t, dir, meta, RestoreSessionConfig{clock: fake, deferRestoreSideEffects: true})
	evs, mu, done := collectEvents(sess)
	child := newTestSessionForState(t)
	working := &subagent{id: child.ID(), sess: child, running: true}
	sess.subagents.track(working)
	sess.recomputeRestoredState(0)
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("restored state while the child works = %q, want idle", got)
	}

	working.mu.Lock()
	working.running = false
	working.mu.Unlock()
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
		t.Fatalf("filtered wake: %v", err)
	}
	requireRestsAwaitingAfterFreshQuietPeriod(t, sess, fake, evs, mu, done)
}
