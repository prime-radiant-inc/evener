package agent

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
)

// notificationWakeLog records what the session's notification queue held at
// each wake. One completion must produce ONE wake: the model is interrupted
// once and reads everything that completion has to say in that single turn.
type notificationWakeLog struct {
	mu    sync.Mutex
	wakes [][]jobNotification
}

func (l *notificationWakeLog) observe(s *Session) {
	s.pendingJobNotifsMu.Lock()
	queued := append([]jobNotification(nil), s.pendingJobNotifs...)
	s.pendingJobNotifsMu.Unlock()
	l.mu.Lock()
	l.wakes = append(l.wakes, queued)
	l.mu.Unlock()
}

func (l *notificationWakeLog) snapshot() [][]jobNotification {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([][]jobNotification(nil), l.wakes...)
}

// TestWatchedJobCompletionWakesTheSessionOnce pins the coalescing rule: when a
// watched job ends, its watch settlement and its terminal status are two facts
// about ONE event, and the owner hears them in one notification turn.
//
// Before this, the watch settlement was enqueued (and woke the session) well
// before the terminal notice was built, so an idle session took a turn for the
// settlement and a second turn moments later for the completion it had just
// been told about in different words.
func TestWatchedJobCompletionWakesTheSessionOnce(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)

	// The job waits on a file rather than a clock, so the watch is provably
	// installed while the job is still running — no sleep to race with.
	gate := filepath.Join(t.TempDir(), "release")
	jobID := startBackgroundShellJob(t, s, "while [ ! -f "+gate+" ]; do sleep 0.02; done; printf 'released\\n'")

	// output_match that the job never prints: the watch is still live and
	// unfired when the job ends, so it settles with an end notice.
	watchRes, err := jobWatchTool(s, map[string]any{
		"operation":    "create",
		"source":       jobID,
		"output_match": "this-never-appears-in-the-output",
	}, jobToolResultDefaultMaxChar)
	if err != nil {
		t.Fatalf("job_watch create: %v", err)
	}
	_ = watchRes

	log := &notificationWakeLog{}
	watcher := newJobNotifyWatcher(s, func() { log.observe(s) })

	if err := os.WriteFile(gate, []byte("go\n"), 0o600); err != nil {
		t.Fatalf("release the job: %v", err)
	}
	waitForShellDone(t, s.jobManager, jobID)

	// Both notices must be queued before the assertion; the terminal notice is
	// the last thing armFinalizedJob enqueues. armFinalizedJob queues them
	// under one notification hold (see holdJobNotificationWake) so the
	// session's own notify wake -- fired once the hold releases -- tells us
	// when to look, rather than polling on a timer.
	watcher.await(t, "watch settlement and terminal notice queued", func() bool {
		s.pendingJobNotifsMu.Lock()
		defer s.pendingJobNotifsMu.Unlock()
		var watch, terminal bool
		for _, n := range s.pendingJobNotifs {
			if n.Status == jobNotificationEventWatch {
				watch = true
			}
			if n.JobID == jobID && n.Status != jobNotificationEventWatch {
				terminal = true
			}
		}
		return watch && terminal
	})

	wakes := log.snapshot()
	if len(wakes) != 1 {
		t.Fatalf("notification wakes = %d, want 1 (one completion is one notification turn); wake contents: %s",
			len(wakes), describeWakes(wakes))
	}
	var sawWatch, sawTerminal bool
	for _, n := range wakes[0] {
		if n.Status == jobNotificationEventWatch {
			sawWatch = true
		}
		if n.JobID == jobID && n.Status != jobNotificationEventWatch {
			sawTerminal = true
		}
	}
	if !sawWatch || !sawTerminal {
		t.Fatalf("the single wake carried watch=%t terminal=%t, want both: %s",
			sawWatch, sawTerminal, describeWakes(wakes))
	}

	// Coalescing changes turns, not the durable ledger: the watch's own records
	// and the job's terminal notification state are untouched by it.
	rec := loadShellRecord(t, s.jobManager, jobID)
	if rec.NotifyState != jobstore.NotifyPending {
		t.Fatalf("terminal_notification_state = %q, want %q (still awaiting its turn)", rec.NotifyState, jobstore.NotifyPending)
	}
}

func TestWatchBudgetClearCoalescesWithMatchedEvent(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	jobID := startBackgroundShellJob(t, s, "sleep 300")
	t.Cleanup(func() {
		_, _ = s.jobManager.stop(jobID)
		waitForShellDone(t, s.jobManager, jobID)
	})

	// Public job_watch rejects a concrete job.notification watch because a
	// completion notification is already automatic. This test exercises the
	// retained internal coalescing mechanism directly.
	if _, err := s.jobManager.configureWatch(watchArgs{Target: jobID, Events: []string{"job.notification"}}); err != nil {
		t.Fatalf("configure watch: %v", err)
	}
	s.jobManager.mu.Lock()
	var watched *watchConfig
	for _, cfg := range s.jobManager.watches {
		if cfg != nil && cfg.target == jobID {
			watched = cfg
			break
		}
	}
	if watched == nil {
		s.jobManager.mu.Unlock()
		t.Fatalf("watch for %s not installed", jobID)
	}
	watched.deliveries = watchDeliveryBudget - 1
	s.jobManager.mu.Unlock()

	log := &notificationWakeLog{}
	watcher := newJobNotifyWatcher(s, func() { log.observe(s) })
	onSessionEventKD(s.jobManager, events.EventJobFinished, events.JobFinishedData{
		JobID: jobID, JobType: "shell", Status: "completed",
	})
	watcher.await(t, "matched event and budget-clear notices queued", func() bool {
		s.pendingJobNotifsMu.Lock()
		defer s.pendingJobNotifsMu.Unlock()
		return len(s.pendingJobNotifs) >= 2
	})

	wakes := log.snapshot()
	if len(wakes) != 1 {
		t.Fatalf("notification wakes = %d, want 1 (matched event and budget clear are one event); wake contents: %s",
			len(wakes), describeWakes(wakes))
	}
	var matched, cleared bool
	for _, n := range wakes[0] {
		if n.JobID == jobID && n.Status != jobNotificationEventWatch {
			matched = true
		}
		if n.Status == jobNotificationEventWatch && strings.Contains(n.Reason, "watch cleared") {
			cleared = true
		}
	}
	if !matched || !cleared {
		t.Fatalf("single wake carried matched=%t cleared=%t, want both: notices=%+v", matched, cleared, wakes[0])
	}
}

func TestReceiverWatchBudgetClearCoalescesWithMatchedEvent(t *testing.T) {
	t.Parallel()
	source := newTestJM(t)
	receiver := newTestSession(t)
	rec, _ := source.createShell(createShellOpts{Command: "sleep 300"})
	if _, err := source.configureWatch(watchArgs{
		Target:            rec.JobID,
		Events:            []string{"job.notification"},
		ReceiverSessionID: receiver.ID(),
		ReceiverNotify:    receiver.enqueueJobNotificationAndNotify,
		ReceiverHoldWake:  receiver.holdJobNotificationWake,
	}); err != nil {
		t.Fatalf("configure receiver watch: %v", err)
	}
	source.mu.Lock()
	var watched *watchConfig
	for _, cfg := range source.watches {
		if cfg != nil && cfg.target == rec.JobID {
			watched = cfg
			break
		}
	}
	source.mu.Unlock()
	if watched == nil {
		t.Fatalf("receiver watch for %s not installed", rec.JobID)
	}
	source.mu.Lock()
	watched.deliveries = watchDeliveryBudget - 1
	source.mu.Unlock()

	log := &notificationWakeLog{}
	watcher := newJobNotifyWatcher(receiver, func() { log.observe(receiver) })
	onSessionEventKD(source, events.EventJobFinished, events.JobFinishedData{
		JobID: rec.JobID, JobType: "shell", Status: "completed",
	})
	watcher.await(t, "receiver matched event and budget-clear notices queued", func() bool {
		receiver.pendingJobNotifsMu.Lock()
		defer receiver.pendingJobNotifsMu.Unlock()
		return len(receiver.pendingJobNotifs) >= 2
	})

	wakes := log.snapshot()
	if len(wakes) != 1 {
		t.Fatalf("receiver notification wakes = %d, want 1; wake contents: %s",
			len(wakes), describeWakes(wakes))
	}
	var matched, cleared bool
	for _, n := range wakes[0] {
		if n.JobID == rec.JobID && n.Status != jobNotificationEventWatch {
			matched = true
		}
		if n.Status == jobNotificationEventWatch && strings.Contains(n.Reason, "watch cleared") {
			cleared = true
		}
	}
	if !matched || !cleared {
		t.Fatalf("single receiver wake carried matched=%t cleared=%t, want both: notices=%+v", matched, cleared, wakes[0])
	}
}

func describeWakes(wakes [][]jobNotification) string {
	var out strings.Builder
	for i, w := range wakes {
		out.WriteString("\n  wake ")
		out.WriteString(strconv.Itoa(i))
		out.WriteString(":")
		for _, n := range w {
			out.WriteString(" {job=")
			out.WriteString(n.JobID)
			out.WriteString(" status=")
			out.WriteString(n.Status)
			out.WriteString("}")
		}
	}
	return out.String()
}
