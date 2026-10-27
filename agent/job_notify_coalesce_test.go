package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/llm"
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

// A real Session uses canonical custody, not the legacy notification queue.
// Source Delivered means custody, and only the scripted response observes it.
func TestWatchedJobCompletionWakesTheSessionOnce(t *testing.T) {
	t.Parallel()
	for _, persistent := range []bool{true, false} {
		t.Run(fmt.Sprintf("persistent=%t", persistent), func(t *testing.T) {
			s, adapter := newTerminalFlushSession(t, persistent)
			adapter.steps = []func(llm.Request) llm.Response{func(llm.Request) llm.Response { return finalResponse("completion-response-429") }}
			gate := filepath.Join(t.TempDir(), "release")
			jobID := startBackgroundShellJob(t, s, "while [ ! -f "+gate+" ]; do sleep 0.02; done; printf 'released\\n'")
			if _, err := jobWatchTool(s, map[string]any{"operation": "create", "source": jobID, "output_match": "unmatched-completion-517"}, jobToolResultDefaultMaxChar); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var wakes [][]string
			watcher := newJobNotifyWatcher(s, func() {
				ids, err := s.pendingNotificationIDs()
				if err != nil {
					t.Error(err)
				}
				mu.Lock()
				wakes = append(wakes, ids)
				mu.Unlock()
			})
			if err := os.WriteFile(gate, []byte("go"), 0o600); err != nil {
				t.Fatal(err)
			}
			waitForShellDone(t, s.jobManager, jobID)
			watcher.await(t, "canonical watch and shell custody", func() bool {
				ids, err := s.pendingNotificationIDs()
				return err == nil && len(ids) == 2
			})
			source := producerSourceEvents(t, s.jobManager, jobstore.EventWatchSendPending)
			if len(source) != 1 {
				t.Fatalf("source watch frames=%d want1", len(source))
			}
			state := *source[0].WatchSend
			ids := []string{fmt.Sprintf("watch:%s:%d", state.DeliveryID, state.UpdateSeq), requireCanonicalShellNotification(t, s, jobID, 0)}
			requireCallbackBatchSource(t, s, state)
			mu.Lock()
			snapshots := append([][]string(nil), wakes...)
			mu.Unlock()
			if len(snapshots) != 1 || !equalNotificationIDs(snapshots[0], ids) {
				t.Fatalf("canonical handoffs=%v want one ordered batch %v", snapshots, ids)
			}
			requireCallbackBatchReceipts(t, s, ids, 0)
			requireWatchSettlement(t, s.jobManager, state, 1)
			if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
				t.Fatal(err)
			}
			requests := adapter.Requests()
			if len(requests) != 1 {
				t.Fatalf("provider requests=%d want1", len(requests))
			}
			requireCallbackBatchRequest(t, s, requests[0], ids)
			requireCallbackBatchReceipts(t, s, ids, 1)
			requireCanonicalShellNotification(t, s, jobID, 1)
			if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
				t.Fatal(err)
			}
			s.jobManager.dispatchProducedWatchSends()
			if len(adapter.Requests()) != 1 {
				t.Fatal("replay executed another provider request")
			}
			requireCallbackBatchReceipts(t, s, ids, 1)
		})
	}
}

func TestWatchBudgetClearCoalescesWithMatchedEvent(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	// Construct the retained local legacy rail directly. Never remove canonical
	// owner/controller wiring from a real Session to force a legacy fixture.
	source := newTestJM(t)
	source.enqueue = s.enqueueJobNotificationAndNotify
	source.holdWake = s.holdJobNotificationWake
	rec, err := source.createShell(createShellOpts{Command: "legacy-local-931"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.configureWatch(watchArgs{Target: rec.JobID, Events: []string{"job.notification"}}); err != nil {
		t.Fatal(err)
	}
	source.mu.Lock()
	var watched *watchConfig
	for _, cfg := range source.watches {
		if cfg.target == rec.JobID {
			watched = cfg
			break
		}
	}
	if watched == nil {
		source.mu.Unlock()
		t.Fatal("watch missing")
	}
	watched.deliveries, watched.conditionFires = watchDeliveryBudget-1, watchDeliveryBudget-1
	source.mu.Unlock()
	jobID := rec.JobID
	log := &notificationWakeLog{}
	watcher := newJobNotifyWatcher(s, func() { log.observe(s) })
	onSessionEventKD(source, events.EventJobFinished, events.JobFinishedData{
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
	requireLegacyBudgetBatch(t, source, watched, wakes[0])
	var matched, cleared bool
	for _, n := range wakes[0] {
		if n.JobID == jobID && n.Status != jobNotificationEventWatch {
			matched = true
		}
		if n.Status == jobNotificationEventWatch && n.callbackTrigger == watchBudgetEndTrigger {
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
	// One condition fire short of the budget, which is what the breaker latches on.
	watched.deliveries = watchDeliveryBudget - 1
	watched.conditionFires = watchDeliveryBudget - 1
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
	requireLegacyBudgetBatch(t, source, watched, wakes[0])
	var matched, cleared bool
	for _, n := range wakes[0] {
		if n.JobID == rec.JobID && n.Status != jobNotificationEventWatch {
			matched = true
		}
		if n.Status == jobNotificationEventWatch && n.callbackTrigger == watchBudgetEndTrigger {
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
