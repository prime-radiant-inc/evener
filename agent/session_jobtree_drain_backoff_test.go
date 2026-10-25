package agent

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/llm"
)

// Clock edges may re-evaluate resource deadlines, but never pump notification
// delivery. A real source hint must still fan out immediately.
func TestDrainResourceClockDoesNotPumpNotifications(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sess := newSession(t)
		seedWatchSendResidue(t, sess)
		wake, hint := newDrainWake()
		sess.SetNotifyFunc(hint)
		recheck := make(chan time.Time)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var kicks atomic.Int32
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = sess.drainJobTreeWith(ctx, wake, recheck, func(context.Context) error { kicks.Add(1); return nil }, stallProcess)
		}()
		synctest.Wait()
		for range 64 {
			recheck <- time.Now()
			synctest.Wait()
		}
		if got := kicks.Load(); got != 1 {
			t.Fatalf("clock-only kicks = %d, want entry only", got)
		}
		sess.notify()
		synctest.Wait()
		if got := kicks.Load(); got != 2 {
			t.Fatalf("kicks after source hint = %d, want 2", got)
		}
		cancel()
		<-done
	})
}

// A hint raised after a scan read an early signal must survive until the
// confirming pass, even if a later signal becomes quiet in that same scan.
func TestDrainWakeStraddlesQuiescence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sess := newSession(t)
		wake, hint := newDrainWake()
		sess.SetNotifyFunc(hint)
		var kicks, turns int
		process := func(context.Context, string, []ImageAttachment, EntryKind) (string, error) {
			turns++
			sess.drainJobNotifications()
			return "observed", nil
		}
		result, err := sess.drainJobTreeWith(context.Background(), wake, nil, func(context.Context) error {
			kicks++
			if kicks == 1 {
				sess.enqueueJobNotificationAndNotify(jobNotification{JobID: "straddle-731a", Status: "completed"})
			}
			return nil
		}, process)
		if err != nil || result != "observed" || turns != 1 {
			t.Fatalf("drain = %q, %v, turns %d", result, err, turns)
		}
	})
}

// Entry recovery re-materializes owned shell source obligations before trusting
// quiescence. Forwarded copies are not permission to select another receiver.
func TestDrainEntryRecoversOwnedShellPending(t *testing.T) {
	adapter := &fakeAdapter{name: "openai"}
	adapter.steps = []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			observationRequest(t, req, []string{"job_owned_entry_731a"}, nil)
			return finalResponse("owned-entry-observed-731a")
		},
	}
	sess := newSession(t, withAdapter(adapter), withConfig(SessionConfig{StateDir: t.TempDir(), TurnEndsProcess: true}), withoutGitSnapshot())
	jm := sess.jobManager
	now := jm.now()
	const id = "job_owned_entry_731a"
	const gen = "gen-owned-entry-731a"
	for _, event := range []jobstore.Event{
		{Kind: jobstore.EventJobStarted, TS: now, JobID: id, Type: jobstore.JobShell, OwnerSessionID: jm.sessionID, VisibleToSession: jm.sessionID, StartedAt: &now, Command: "true"},
		{Kind: jobstore.EventJobFinished, TS: now, JobID: id, Status: jobstore.StatusCompleted, EndedAt: &now, TerminalGen: gen},
		{Kind: jobstore.EventJobNotificationPending, TS: now, JobID: id, TerminalGen: gen},
	} {
		if err := jm.appendEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	result, err := sess.DrainJobTree(context.Background(), drainWakeForTest(sess))
	if err != nil || result != "owned-entry-observed-731a" || len(adapter.Requests()) != 1 {
		t.Fatalf("drain result=%q err=%v, provider requests=%d", result, err, len(adapter.Requests()))
	}
	recs, err := jm.store.Load()
	if err != nil || recs[id].NotifyState != jobstore.NotifyDelivered {
		t.Fatalf("source not settled: %v %v", recs[id], err)
	}
}
