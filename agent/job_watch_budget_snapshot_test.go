package agent

import (
	"errors"
	"fmt"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/jobstore"
)

func TestWatchBudgetRejectsPrebuiltConditionSnapshot(t *testing.T) {
	t.Parallel()
	for _, send := range []bool{false, true} {
		t.Run(fmt.Sprintf("send=%t", send), func(t *testing.T) {
			jm := newTestJM(t)
			jm.clock = agenttest.NewFakeClock()
			args := watchArgs{Target: runtimeMessageAliasCaller, Events: []string{"communicate"}, Note: "prebuilt-budget-sentinel"}
			if send {
				args.Send = &watchSendArgs{To: runtimeMessageAliasCaller, Message: "prebuilt-send-sentinel"}
			}
			result, err := jm.configureWatch(args)
			if err != nil {
				t.Fatal(err)
			}
			for range watchDeliveryBudget - 1 {
				onSessionEventKD(jm, events.EventCommunicate, nil)
			}
			jm.mu.Lock()
			_, cfg, ok := jm.watchConfigByIDLocked(result.WatchID)
			if !ok || cfg.conditionFires != watchDeliveryBudget-1 {
				jm.mu.Unlock()
				t.Fatal("real firings did not reach 49")
			}
			event := events.SessionEvent{SessionID: jm.sessionID, Kind: events.EventCommunicate, Data: events.CommunicateData{Message: "prebuilt-event-sentinel"}}
			decision := evaluateWatchEvent(cfg.eventSnapshot(isActiveWatchTargetLocked(jm, cfg.target)), event)
			if !decision.matched || decision.send != send {
				jm.mu.Unlock()
				t.Fatal("snapshots were not real eligible condition firings")
			}
			// Both calls use the producer's snapshot constructor while count is 49.
			// Persistence and teardown below are the real source operations, not planner checks.
			var a, b watchSendDelivery
			if send {
				a = jm.watchSendSnapshot(cfg, decision.watchedIdentity, "event: COMMUNICATE", event)
				b = jm.watchSendSnapshot(cfg, decision.watchedIdentity, "event: COMMUNICATE", event)
			} else {
				n := jm.watchNotificationFromWatch(cfg, decision.notifyJobID, "event: COMMUNICATE", event.Provenance)
				callbackRoot := events.SessionEvent{SessionID: jm.sessionID, Provenance: n.Provenance}
				a = jm.watchSendSnapshot(cfg, cfg.target, "event: COMMUNICATE", callbackRoot).withSelfInfluence(jm.classifySelfInfluenceLocked(cfg, n.Provenance))
				b = jm.watchSendSnapshot(cfg, cfg.target, "event: COMMUNICATE", callbackRoot).withSelfInfluence(jm.classifySelfInfluenceLocked(cfg, n.Provenance))
				a.callback, b.callback = true, true
				a.production, b.production = n.production, n.production
				jm.rememberDetachedPendingLocked(cfg)
				a.allowAfterTerminalExpiry, b.allowAfterTerminalExpiry = true, true
				a.frame = (&Session{}).formatJobNotificationReminder([]deliverableJobNotification{{notification: n}})
				b.frame = a.frame
			}
			jm.mu.Unlock()
			a = jm.snapshotWatchSendFrame(a)
			b = jm.snapshotWatchSendFrame(b)
			if a.deliveryID == "" || a.deliveryID == b.deliveryID || a.updateSeq >= b.updateSeq {
				t.Fatal("producer snapshots did not allocate distinct increasing identities")
			}
			injected := errors.New("prebuilt budget clear fault")
			fail := true
			attempts := 0
			appendEvents := jm.appendEvents
			jm.appendEvents = func(evs []jobstore.Event) error {
				for _, ev := range evs {
					if ev.Kind == jobstore.EventWatchCleared && ev.WatchID == result.WatchID {
						attempts++
						if fail {
							return injected
						}
					}
				}
				return appendEvents(evs)
			}
			firing, _, persisted, err := jm.recordWatchSend(a)
			if err != nil || !persisted {
				t.Fatalf("50th source persist=%t err=%v", persisted, err)
			}
			jm.releaseCallbackBudgetOwner(cfg, a.deliveryID)
			jm.mu.Lock()
			count, latched := cfg.conditionFires, cfg.budgetTripped
			jm.mu.Unlock()
			if attempts != 1 || count != watchDeliveryBudget || latched || jm.watchCount() != 1 {
				t.Fatalf("clear rollback not reached: attempts=%d fires=%d latched=%t live=%d", attempts, count, latched, jm.watchCount())
			}
			before := producerSourceEvents(t, jm, jobstore.EventWatchSendPending)
			if len(before) != watchDeliveryBudget || before[len(before)-1].WatchSend.DeliveryID != a.deliveryID {
				t.Fatal("50th firing identity did not reach real source journal")
			}
			_, _, admitted, err := jm.recordWatchSend(b)
			jm.releaseCallbackBudgetOwner(cfg, b.deliveryID)
			if err != nil {
				t.Fatal(err)
			}
			after := producerSourceEvents(t, jm, jobstore.EventWatchSendPending)
			t.Logf("two snapshots built at 49, A persisted as 50, clear append failed once and latch rolled back, B admitted=%t source rows=%d", admitted, len(after))
			if admitted || len(after) != watchDeliveryBudget {
				t.Fatalf("prebuilt snapshot admitted condition 51: admitted=%t source rows=%d", admitted, len(after))
			}
			folded, err := jm.store.LoadWatchSends()
			if err != nil {
				t.Fatal(err)
			}
			pending := folded.Pending[firing.Key]
			if pending == nil || pending.DeliveryID != a.deliveryID || pending.UpdateSeq != a.updateSeq {
				t.Fatalf("rejected snapshot replaced retained 50th firing: %+v", pending)
			}
			fail = false
			jm.autoClearWatchOverBudget(cfg)
			final := producerSourceEvents(t, jm, jobstore.EventWatchSendPending)
			if jm.watchCount() != 0 || attempts != 2 || len(final) != watchDeliveryBudget+1 {
				t.Fatalf("successful teardown retry live=%d attempts=%d rows=%d", jm.watchCount(), attempts, len(final))
			}
			notice := final[len(final)-1].WatchSend
			if notice.TriggerReason != "budget_exhausted" || notice.DeliveryID == a.deliveryID || notice.Key == firing.Key {
				t.Fatalf("notice lost its distinct source obligation: %+v", notice)
			}
			jm.mu.Lock()
			count, deliveries := cfg.conditionFires, cfg.deliveries
			jm.mu.Unlock()
			if count != watchDeliveryBudget || (!send && deliveries != watchDeliveryBudget) {
				t.Fatalf("budget counters changed after rejection: fires=%d callbacks=%d", count, deliveries)
			}
			jm.autoClearWatchOverBudget(cfg)
			jm.dispatchProducedWatchSends()
			if len(producerSourceEvents(t, jm, jobstore.EventWatchSendPending)) != watchDeliveryBudget+1 {
				t.Fatal("recovery duplicated firing or budget notice")
			}
			requireWatchSettlement(t, jm, firing, 0)
		})
	}
}
