package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func newTerminalFlushSession(t *testing.T, persistent bool) (*Session, *fakeAdapter) {
	t.Helper()
	adapter := &fakeAdapter{name: "openai"}
	cfg := SessionConfig{}
	if persistent {
		cfg.StateDir = t.TempDir()
	}
	return newSession(t, withAdapter(adapter), withConfig(cfg)), adapter
}

// The callback only hands off. The producer barrier is outside it, allowing a
// separate worker to prove a split request before shell canonical acceptance.
func TestTerminalFlushHandoff(t *testing.T) {
	// Serial: diagnostic capture replaces the default logger through session cleanup.
	for _, tc := range []struct {
		name              string
		self, end, budget bool
		watches           int
	}{
		{"self-event", true, false, false, 1},
		{"end-notice", false, true, false, 1},
		{"both", true, true, false, 2},
		{"budget-at-completion", true, false, true, 2},
	} {
		for _, persistent := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/persistent=%t", tc.name, persistent), func(t *testing.T) {
				captureReceiverErrorLogs(t, errors.New("unexpected healthy diagnostic"))
				s, a := newTerminalFlushSession(t, persistent)
				jm := s.jobManager
				gate := filepath.Join(t.TempDir(), "release")
				jobID := startBackgroundShellJob(t, s, "while [ ! -f "+gate+" ]; do sleep 0.02; done; printf 'released\\n'")
				if tc.self {
					if _, err := jobWatchTool(s, map[string]any{"operation": "create", "source": "self", "events": []any{"job.notification"}, "note": "terminal-custody-917"}, jobToolResultDefaultMaxChar); err != nil {
						t.Fatal(err)
					}
				}
				if tc.end {
					if _, err := jobWatchTool(s, map[string]any{"operation": "create", "source": jobID, "output_match": "combined-unmatched-128"}, jobToolResultDefaultMaxChar); err != nil {
						t.Fatal(err)
					}
				}
				if tc.budget {
					jm.mu.Lock()
					for _, cfg := range jm.watches {
						cfg.deliveries, cfg.conditionFires = watchDeliveryBudget-1, watchDeliveryBudget-1
					}
					jm.mu.Unlock()
				}
				var beforeResponse [][]schema.Turn
				var beforeSource []string
				for range 4 {
					a.steps = append(a.steps, func(llm.Request) llm.Response {
						s.mu.Lock()
						beforeResponse = append(beforeResponse, append([]schema.Turn(nil), s.history...))
						s.mu.Unlock()
						records, err := jm.store.Load()
						if err == nil && records[jobID] != nil {
							beforeSource = append(beforeSource, string(records[jobID].NotifyState))
						}
						return finalResponse("terminal-response-327")
					})
				}
				wake := make(chan struct{}, 1)
				var wakes atomic.Int32
				var wakeMu sync.Mutex
				var handoffs [][]string
				var handoffErrors []error
				s.SetNotifyFunc(func() {
					ids, err := s.pendingNotificationIDs()
					wakeMu.Lock()
					handoffs = append(handoffs, ids)
					if err != nil {
						handoffErrors = append(handoffErrors, err)
					}
					wakeMu.Unlock()
					wakes.Add(1)
					select {
					case wake <- struct{}{}:
					default:
					}
				})
				hookEntered, releaseHook := make(chan struct{}), make(chan struct{})
				var releaseOnce sync.Once
				release := func() { releaseOnce.Do(func() { close(releaseHook) }) }
				jm.testOnlyAfterNotifyPendingAppend = func(id string) {
					if id == jobID {
						close(hookEntered)
						<-releaseHook
					}
				}
				workerDone := make(chan error, 1)
				ctx, cancel := context.WithCancel(context.Background())
				go func() {
					select {
					case <-wake:
						_, err := s.ProcessInputKind(ctx, "", nil, EntryNotification)
						workerDone <- err
					case <-ctx.Done():
						workerDone <- ctx.Err()
					}
				}()
				workerJoined := false
				defer func() {
					release()
					cancel()
					if !workerJoined {
						select {
						case <-workerDone:
						case <-time.After(5 * time.Second):
							t.Error("worker did not join")
						}
					}
					waitForShellDone(t, jm, jobID)
				}()
				if err := os.WriteFile(gate, []byte("go"), 0o600); err != nil {
					t.Fatal(err)
				}
				select {
				case <-hookEntered:
				case <-time.After(5 * time.Second):
					t.Fatal("producer hook did not fire")
				}
				early := wakes.Load()
				t.Logf("PRODUCER_BARRIER_REACHED wakes=%d, no shell canonical custody yet", early)
				if early != 0 {
					select {
					case err := <-workerDone:
						workerJoined = true
						if err != nil {
							t.Fatal(err)
						}
					case <-time.After(5 * time.Second):
						t.Fatal("eager worker did not finish before terminal publication")
					}
					t.Logf("EARLY_PROVIDER_DONE requests=%d", len(a.Requests()))
					t.Error("same completion published a wake before terminal custody")
				}
				release()
				waitForShellDone(t, jm, jobID)
				if !workerJoined {
					select {
					case err := <-workerDone:
						workerJoined = true
						if err != nil {
							t.Fatal(err)
						}
					case <-time.After(5 * time.Second):
						t.Fatal("no handoff worker after publication")
					}
				}
				for range 2 {
					if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
						t.Fatal(err)
					}
				}
				requests := a.Requests()
				s.attentionMu.Lock()
				fold, err := s.delegateAttentionFoldLocked()
				fold = fold.clone()
				s.attentionMu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
				rec := loadShellRecord(t, jm, jobID)
				shellID := "shell:" + jobID + ":" + rec.TerminalGen
				if len(fold.order) != tc.watches+1 || fold.order[tc.watches] != shellID {
					t.Fatalf("canonical order=%v, want source watches then exact shell %s", fold.order, shellID)
				}
				wakeMu.Lock()
				if len(handoffErrors) != 0 || len(handoffs) != 1 || !equalNotificationIDs(handoffs[0], fold.order) {
					t.Errorf("handoff IDs=%v errors=%v, want one ordered batch %v", handoffs, handoffErrors, fold.order)
				}
				wakeMu.Unlock()
				for _, id := range fold.order {
					if _, ok := fold.observed[id]; !ok {
						t.Errorf("no successful observation for %s", id)
					}
					if fold.resolutions[id] != delegateAttentionConsumed {
						t.Errorf("not consumed: %s", id)
					}
					if len(requests) > 0 {
						matches := 0
						for _, m := range requests[0].Messages {
							if m.Role == llm.RoleUser && strings.Contains(m.Text(), fold.content[id].Text()) {
								matches++
							}
						}
						if matches != 1 {
							t.Errorf("first request covers canonical ID %s in %d user messages, want1", id, matches)
						}
					}
				}
				// Memory-only receivers still have the temporary producer journal. Use
				// it for independent watch IDs, never as a receiver disk-receipt claim.
				independentSource := producerSourceEvents(t, jm, jobstore.EventWatchSendPending)
				if len(independentSource) != tc.watches {
					t.Fatalf("independent source count=%d want%d", len(independentSource), tc.watches)
				}
				if len(beforeResponse) != 1 || len(beforeSource) != 1 || beforeSource[0] != string(jobstore.NotifyDelivered) {
					t.Errorf("pre-response requests=%d source states=%v, want one delivered source", len(beforeResponse), beforeSource)
				}
				if len(beforeResponse) > 0 {
					requireCallbackBatchTurnCounts(t, beforeResponse[0], fold.order, 0)
				}
				requireCallbackBatchReceipts(t, s, fold.order, 1)
				if tc.budget {
					firing, budget := *independentSource[0].WatchSend, *independentSource[1].WatchSend
					if firing.TriggerReason != "event: JOB_FINISHED" || budget.TriggerReason != "budget_exhausted" || firing.Key == budget.Key || firing.DeliveryID == budget.DeliveryID {
						t.Fatalf("completion lost distinct firing/budget facts: firing=%+v budget=%+v", firing, budget)
					}
					if jm.watchCount() != 0 {
						t.Fatal("budget watch remains live")
					}
					for _, event := range independentSource {
						requireWatchSettlement(t, jm, *event.WatchSend, 1)
					}
				}
				for i, event := range independentSource {
					state := event.WatchSend
					id := fmt.Sprintf("watch:%s:%d", state.DeliveryID, state.UpdateSeq)
					if fold.order[i] != id || fold.content[id].Text() != state.Frame {
						t.Fatal("independent source identity or full frame changed")
					}
				}
				if persistent {
					counts := map[string]int{}
					for _, e := range notificationRawEntries(t, s.TranscriptPath()) {
						for _, id := range e.Turn.ObservedNotificationIDs {
							counts[id]++
						}
					}
					for _, id := range fold.order {
						if counts[id] != 1 {
							t.Errorf("raw assistant receipts %s=%d want1", id, counts[id])
						}
					}
				}
				if ids, err := s.pendingNotificationIDs(); err != nil || len(ids) != 0 {
					t.Errorf("pending=%v err=%v", ids, err)
				}
				t.Logf("SUMMARY handoff_wakes=%d provider_requests=%d exact_canonical_order=%v", wakes.Load(), len(requests), fold.order)
				if wakes.Load() != 1 {
					t.Errorf("handoff wakes=%d want1", wakes.Load())
				}
				if len(requests) != 1 {
					t.Errorf("provider requests=%d want1", len(requests))
				}
			})
		}
	}
}
