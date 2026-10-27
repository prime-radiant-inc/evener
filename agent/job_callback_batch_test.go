package agent

import (
	"fmt"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func equalNotificationIDs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// Check accepted rows and assistant receipts independently, including the
// lifetime-local history used when StateDir is empty.
func requireCallbackBatchReceipts(t *testing.T, s *Session, ids []string, receipts int) {
	t.Helper()
	s.mu.Lock()
	history := append([]schema.Turn(nil), s.history...)
	s.mu.Unlock()
	requireCallbackBatchTurnCounts(t, history, ids, receipts)
	if s.stateDir != "" {
		var turns []schema.Turn
		for _, entry := range notificationRawEntries(t, s.TranscriptPath()) {
			turns = append(turns, entry.Turn)
		}
		requireCallbackBatchTurnCounts(t, turns, ids, receipts)
	}
}

func requireCallbackBatchRequest(t *testing.T, s *Session, request llm.Request, ids []string) {
	t.Helper()
	s.attentionMu.Lock()
	fold, err := s.delegateAttentionFoldLocked()
	fold = fold.clone()
	s.attentionMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		matches := 0
		for _, message := range request.Messages {
			if message.Role == llm.RoleUser && strings.Contains(message.Text(), fold.content[id].Text()) {
				matches++
			}
		}
		if fold.content[id].Text() == "" || matches != 1 {
			t.Fatalf("provider request contains source-backed ID %s payload %d times, want1", id, matches)
		}
	}
}

func requireLegacyBudgetBatch(t *testing.T, jm *jobManager, cfg *watchConfig, notices []jobNotification) {
	t.Helper()
	produced := producerSourceEvents(t, jm, jobstore.EventWatchSendPending)
	if len(produced) != 2 {
		t.Fatalf("source callback rows=%d want2", len(produced))
	}
	firing, budget := *produced[0].WatchSend, *produced[1].WatchSend
	if !strings.HasPrefix(firing.TriggerReason, "event: ") || budget.TriggerReason != "budget_exhausted" || firing.Key == budget.Key || firing.DeliveryID == budget.DeliveryID {
		t.Fatalf("source lost distinct final firing/budget identities: firing=%+v budget=%+v", firing, budget)
	}
	if len(notices) != 2 || notices[0].callbackConfig != cfg || notices[1].callbackConfig != cfg || notices[0].callbackTrigger != firing.TriggerReason || notices[1].callbackTrigger != budget.TriggerReason {
		t.Fatalf("legacy batch is not source firing then machine budget notice: %+v", notices)
	}
	for i, state := range []jobstore.WatchSendState{firing, budget} {
		frame := (&Session{}).formatJobNotificationReminder([]deliverableJobNotification{{notification: notices[i]}})
		if state.Frame != frame {
			t.Fatalf("legacy payload %d differs from independently journaled source", i)
		}
	}
	jm.mu.Lock()
	fires, deliveries := cfg.conditionFires, cfg.deliveries
	jm.mu.Unlock()
	if fires != watchDeliveryBudget || deliveries != watchDeliveryBudget || jm.watchCount() != 0 {
		t.Fatalf("fires=%d deliveries=%d live=%d", fires, deliveries, jm.watchCount())
	}
	cleared := producerSourceEvents(t, jm, jobstore.EventWatchCleared)
	if len(cleared) != 1 {
		t.Fatalf("teardown rows=%d want1", len(cleared))
	}
}

func requireCallbackBatchSource(t *testing.T, s *Session, state jobstore.WatchSendState) {
	t.Helper()
	id := fmt.Sprintf("watch:%s:%d", state.DeliveryID, state.UpdateSeq)
	s.attentionMu.Lock()
	fold, err := s.delegateAttentionFoldLocked()
	fold = fold.clone()
	s.attentionMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if state.Frame == "" || fold.content[id].Text() != state.Frame {
		t.Fatalf("canonical payload %s differs from independent source frame", id)
	}
}

func requireCallbackBatchTurnCounts(t *testing.T, turns []schema.Turn, ids []string, receipts int) {
	t.Helper()
	for _, id := range ids {
		accepted, observed := 0, 0
		for _, turn := range turns {
			if turn.AttentionID == id {
				accepted++
			}
			for _, seen := range turn.ObservedNotificationIDs {
				if seen == id {
					observed++
					if turn.Kind != schema.TurnAssistant {
						t.Fatalf("ID %s observed outside a successful assistant response", id)
					}
				}
			}
		}
		if accepted != 1 || observed != receipts {
			t.Fatalf("ID %s accepted=%d observed=%d want1,%d", id, accepted, observed, receipts)
		}
	}
}
