package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// The final firing and the budget notice are separate obligations even when
// neither can reach its receiver until after the source manager is reopened.
func TestWatchBudgetEndPreservesFiringThroughReceiverReopen(t *testing.T) {
	for _, send := range []bool{false, true} {
		t.Run(fmt.Sprintf("send=%t", send), func(t *testing.T) {
			adapter := newMessagingAdapter()
			s := newMessagingRoot(t, adapter, func(cfg *SessionConfig) { cfg.clock = agenttest.NewFakeClock() })
			jm := s.jobManager
			args := watchArgs{Target: runtimeMessageAliasCaller, Events: []string{"communicate"}, Note: "budget-custody-a914"}
			if send {
				args.Send = &watchSendArgs{To: runtimeMessageAliasCaller, Message: "budget-firing-b826"}
			}
			res, err := jm.configureWatch(args)
			if err != nil {
				t.Fatal(err)
			}
			jm.mu.Lock()
			_, cfg, ok := jm.watchConfigByIDLocked(res.WatchID)
			jm.mu.Unlock()
			if !ok {
				t.Fatal("missing registration")
			}
			var teardownAttempts atomic.Int32
			appendEvents := jm.appendEvents
			jm.appendEvents = func(evs []jobstore.Event) error {
				for _, ev := range evs {
					if ev.Kind == jobstore.EventWatchCleared && ev.WatchID == res.WatchID {
						teardownAttempts.Add(1)
					}
				}
				return appendEvents(evs)
			}
			if err := s.attachedTranscript().Close(); err != nil {
				t.Fatal(err)
			}
			for range watchDeliveryBudget + 1 {
				jm.onSessionEvent(events.SessionEvent{SessionID: s.ID(), Kind: events.EventCommunicate, Data: events.CommunicateData{Message: "budget-event-c932"}})
			}
			produced := producerSourceEvents(t, jm, jobstore.EventWatchSendPending)
			if len(produced) != watchDeliveryBudget+1 {
				t.Fatalf("source firings plus notice=%d, want %d", len(produced), watchDeliveryBudget+1)
			}
			firing, notice := *produced[watchDeliveryBudget-1].WatchSend, *produced[watchDeliveryBudget].WatchSend
			if firing.DeliveryID == "" || notice.DeliveryID == "" || firing.DeliveryID == notice.DeliveryID || firing.Key == notice.Key {
				t.Fatalf("firing and notice lost separate immutable identities: firing=%+v notice=%+v", firing, notice)
			}
			if firing.TriggerReason != "event: COMMUNICATE" || firing.TriggerIdentity != args.Target || firing.CallbackFrame == send || notice.TriggerReason != "budget_exhausted" || notice.EndNotice || !notice.CallbackFrame {
				t.Fatalf("incorrect condition/budget callback classification: firing=%+v notice=%+v", firing, notice)
			}
			noticeKey := notice.Key
			noticeKey.ResolvedWatchedIdentity = firing.Key.ResolvedWatchedIdentity
			if firing.Key.WatchID != cfg.watchID || firing.Key.WatchGeneration != cfg.generation || firing.Key.WatchTarget != args.Target || firing.Key.VisibleSessionID != s.ID() || firing.Key.ResolvedSendTo != runtimeMessageAliasCaller || firing.Key.ResolvedWatchedIdentity != runtimeMessageAliasCaller || notice.Key.ResolvedWatchedIdentity != "" || noticeKey != firing.Key || firing.ReceiverSessionID != s.ID() || notice.ReceiverSessionID != s.ID() || notice.CoalescedCount != 0 {
				t.Fatal("budget notice changed the watch/receiver binding or coalesced with a condition")
			}
			jm.mu.Lock()
			fires, callbacks, pending := cfg.conditionFires, cfg.deliveries, len(cfg.pending)
			jm.mu.Unlock()
			if fires != watchDeliveryBudget || (send && callbacks != 0) || (!send && callbacks != watchDeliveryBudget) || jm.watchCount() != 0 || pending != 2 || teardownAttempts.Load() != 1 {
				t.Fatalf("after budget: fires=%d deliveries=%d live=%d pending=%d teardown attempts=%d", fires, callbacks, jm.watchCount(), pending, teardownAttempts.Load())
			}
			for _, state := range []jobstore.WatchSendState{firing, notice} {
				requireWatchSettlement(t, jm, state, 0)
			}
			if producerRecordCount(t, s) != 0 || len(adapter.requestsFor("root")) != 0 {
				t.Fatal("unavailable receiver accepted a frame or executed the provider")
			}
			repairWatchReceiver(t, s)
			restored := reopenWatchSource(t, s, jm)
			restored.dispatchProducedWatchSends()
			restored.dispatchProducedWatchSends()
			ids := []string{watchSourceID(firing), watchSourceID(notice)}
			expected := map[string]string{
				ids[0]: watchSourcePayload(firing),
				ids[1]: watchSourcePayload(notice),
			}
			for _, state := range []jobstore.WatchSendState{firing, notice} {
				requireWatchSettlement(t, restored, state, 1)
			}
			if producerRecordCount(t, s) != 2 || restored.hasPendingWatchSends() || len(adapter.requestsFor("root")) != 0 {
				t.Fatal("reopen did not transfer exactly two frames without executing the provider")
			}
			requireWatchDeliveryReceipts(t, s, expected, nil)
			if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
				t.Fatal(err)
			}
			requests := adapter.requestsFor("root")
			if len(requests) != 1 {
				t.Fatalf("provider requests=%d, want 1", len(requests))
			}
			requireWatchProviderPayloads(t, requests[0], expected)
			if !requestMessagesContainText(requests[0].Messages, args.Note) || (send && !requestMessagesContainText(requests[0].Messages, args.Send.Message)) {
				t.Fatal("provider request lost caller-supplied note or send payload")
			}
			observations := map[string]int{ids[0]: 1, ids[1]: 1}
			requireWatchDeliveryReceipts(t, s, expected, observations)
			pendingIDs, err := s.pendingNotificationIDs()
			if err != nil || len(pendingIDs) != 0 {
				t.Fatalf("after response pending IDs=%v error=%v", pendingIDs, err)
			}
			restored.dispatchProducedWatchSends()
			if producerRecordCount(t, s) != 2 || len(adapter.requestsFor("root")) != 1 {
				t.Fatal("post-observation pump duplicated delivery or provider execution")
			}
			// Re-arm the same source through the public configuration path. A new
			// opaque note/send distinguishes the new episode from retained history.
			freshArgs := watchArgs{Target: args.Target, Events: []string{"communicate"}, Note: "rearm-custody-d741"}
			if send {
				freshArgs.Send = &watchSendArgs{To: runtimeMessageAliasCaller, Message: "rearm-firing-e852"}
			}
			freshResult, err := restored.configureWatch(freshArgs)
			if err != nil {
				t.Fatal(err)
			}
			restored.mu.Lock()
			_, fresh, freshOK := restored.watchConfigByIDLocked(freshResult.WatchID)
			_, _, oldLive := restored.watchConfigByIDLocked(res.WatchID)
			freshOK = freshOK && fresh.watchID != cfg.watchID && fresh.generation != cfg.generation && fresh.generation != "" && fresh.target == args.Target && fresh.conditionFires == 0 && fresh.deliveries == 0 && fresh.eventCount == 0 && fresh.nextUpdateSeq == 0 && !fresh.budgetTripped && len(fresh.pending) == 0
			restored.mu.Unlock()
			if !freshOK || oldLive || restored.watchCount() != 1 {
				t.Fatal("re-arm reused old identity, generation, counters or budget latch")
			}
			if err := s.attachedTranscript().Close(); err != nil {
				t.Fatal(err)
			}
			restored.onSessionEvent(events.SessionEvent{SessionID: s.ID(), Kind: events.EventCommunicate, Data: events.CommunicateData{Message: "rearm-event-f963"}})
			freshRows := producerSourceEvents(t, restored, jobstore.EventWatchSendPending)
			if len(freshRows) != len(produced)+1 || freshRows[len(freshRows)-1].WatchSend == nil {
				t.Fatalf("first re-armed condition produced %d source rows, want %d", len(freshRows), len(produced)+1)
			}
			freshState := *freshRows[len(freshRows)-1].WatchSend
			if freshState.DeliveryID == "" || freshState.DeliveryID == firing.DeliveryID || freshState.DeliveryID == notice.DeliveryID || freshState.UpdateSeq != 1 || freshState.Key.WatchID != fresh.watchID || freshState.Key.WatchGeneration != fresh.generation || freshState.Key.WatchTarget != args.Target || freshState.Key.ResolvedWatchedIdentity != args.Target || freshState.Key.VisibleSessionID != s.ID() || freshState.Key.ResolvedSendTo != runtimeMessageAliasCaller || freshState.ReceiverSessionID != s.ID() || freshState.TriggerIdentity != args.Target || freshState.TriggerReason != "event: COMMUNICATE" || freshState.CallbackFrame == send || freshState.EndNotice || freshState.CoalescedCount != 0 {
				t.Fatalf("first re-armed condition lost fresh source/receiver identity: %+v", freshState)
			}
			restored.mu.Lock()
			freshFires, freshDeliveries, freshPending := fresh.conditionFires, fresh.deliveries, len(fresh.pending)
			restored.mu.Unlock()
			if freshFires != 1 || (!send && freshDeliveries != 1) || (send && freshDeliveries != 0) || freshPending != 1 || restored.watchCount() != 1 {
				t.Fatalf("first re-arm fires=%d deliveries=%d pending=%d live=%d", freshFires, freshDeliveries, freshPending, restored.watchCount())
			}
			if !strings.Contains(freshState.Frame, freshArgs.Note) || (send && (freshState.Message != freshArgs.Send.Message || !strings.Contains(freshState.Frame, freshArgs.Send.Message))) {
				t.Fatal("first re-armed source lost new opaque payload")
			}
			requireWatchSettlement(t, restored, freshState, 0)
			requireWatchDeliveryReceipts(t, s, expected, observations)
			if producerRecordCount(t, s) != 2 || len(adapter.requestsFor("root")) != 1 {
				t.Fatal("closed receiver accepted or observed fresh debt")
			}
			// Reopen the real source while the real receiver is still unavailable.
			// Restoring a live config clears it as runtime_lost, but its already
			// produced first firing must retain the original configuration binding.
			recovered := reopenWatchSource(t, s, restored)
			recovered.dispatchProducedWatchSends()
			requireWatchSettlement(t, recovered, freshState, 0)
			if !recovered.hasPendingWatchSends() || producerRecordCount(t, s) != 2 || len(adapter.requestsFor("root")) != 1 {
				t.Fatal("source reopen fabricated custody before receiver repair")
			}
			repairWatchReceiver(t, s)
			recovered.dispatchProducedWatchSends()
			freshID := watchSourceID(freshState)
			expected[freshID] = watchSourcePayload(freshState)
			requireWatchDeliveryReceipts(t, s, expected, observations)
			custodyEvents := loadJobStoreEvents(t, recovered)
			for _, state := range []jobstore.WatchSendState{firing, notice, freshState} {
				for kind, want := range map[jobstore.EventKind]int{jobstore.EventWatchSendDelivered: 1, jobstore.EventWatchSendDropped: 0, jobstore.EventWatchSendEvicted: 0} {
					if got := countExactWatchSendEvents(custodyEvents, kind, state); got != want {
						t.Fatalf("delivery=%s update=%d kind=%s records=%d want=%d", state.DeliveryID, state.UpdateSeq, kind, got, want)
					}
				}
			}
			if recovered.hasPendingWatchSends() || producerRecordCount(t, s) != 3 || len(adapter.requestsFor("root")) != 1 {
				t.Fatal("fresh source recovery did not transfer exactly one new ID without observation")
			}
			if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
				t.Fatal(err)
			}
			requests = adapter.requestsFor("root")
			if len(requests) != 2 {
				t.Fatalf("provider requests=%d, want 2 after re-arm recovery", len(requests))
			}
			if !requestMessagesContainText(requests[1].Messages, freshArgs.Note) || (send && !requestMessagesContainText(requests[1].Messages, freshArgs.Send.Message)) {
				t.Fatal("re-arm provider request lost caller-supplied note or send payload")
			}
			requireWatchProviderPayloads(t, requests[1], expected)
			observations[ids[0]], observations[ids[1]], observations[freshID] = 2, 2, 1
			requireWatchDeliveryReceipts(t, s, expected, observations)
			before := len(loadJobStoreEvents(t, recovered))
			recovered.dispatchProducedWatchSends()
			if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
				t.Fatal(err)
			}
			requireWatchDeliveryReceipts(t, s, expected, observations)
			postPumpEvents := loadJobStoreEvents(t, recovered)
			for _, state := range []jobstore.WatchSendState{firing, notice, freshState} {
				for kind, want := range map[jobstore.EventKind]int{jobstore.EventWatchSendDelivered: 1, jobstore.EventWatchSendDropped: 0, jobstore.EventWatchSendEvicted: 0} {
					if got := countExactWatchSendEvents(postPumpEvents, kind, state); got != want {
						t.Fatalf("delivery=%s update=%d kind=%s records=%d want=%d", state.DeliveryID, state.UpdateSeq, kind, got, want)
					}
				}
			}
			if len(postPumpEvents) != before || producerRecordCount(t, s) != 3 || len(adapter.requestsFor("root")) != 2 || recovered.hasPendingWatchSends() {
				t.Fatal("post-rearm pump duplicated source records, custody or provider execution")
			}
			t.Logf("old firing=%s notice=%s fresh=%s: recovered exact payloads and IDs, custody/settlement/consumption once each, provider requests=2", firing.DeliveryID, notice.DeliveryID, freshState.DeliveryID)
		})
	}
}

func TestWatchBudgetRetryDoesNotProduceCondition51(t *testing.T) {
	for _, source := range []string{"event", "output", "attach"} {
		for _, send := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/send=%t", source, send), func(t *testing.T) {
				jm := newTestJM(t)
				jm.clock = agenttest.NewFakeClock()
				var mu sync.Mutex
				fail := true
				var results []error
				injected := errors.New("budget teardown unavailable")
				appendEvents := jm.appendEvents
				jm.appendEvents = func(evs []jobstore.Event) error {
					mu.Lock()
					defer mu.Unlock()
					for _, ev := range evs {
						if ev.Kind == jobstore.EventWatchCleared {
							if fail {
								results = append(results, injected)
								return injected
							}
							results = append(results, nil)
						}
					}
					return appendEvents(evs)
				}
				args := watchArgs{Target: runtimeMessageAliasCaller, Events: []string{"communicate"}}
				if source != "event" {
					rec, err := jm.createShell(createShellOpts{Command: "budget-retry"})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() {
						if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "", nil); err != nil {
							t.Error(err)
						}
					})
					args.Target, args.Events, args.OutputMatch = rec.JobID, nil, "hit"
				}
				if send {
					args.Send = &watchSendArgs{To: runtimeMessageAliasCaller, Message: "budget-retry-frame"}
				}
				res, err := jm.configureWatch(args)
				if err != nil {
					t.Fatal(err)
				}
				jm.mu.Lock()
				_, cfg, ok := jm.watchConfigByIDLocked(res.WatchID)
				jm.mu.Unlock()
				if !ok {
					t.Fatal("missing watch")
				}
				var offset int64
				fire := func() {
					switch source {
					case "event":
						onSessionEventKD(jm, events.EventCommunicate, nil)
					case "output":
						offset += 4
						jm.feedJobOutput(args.Target, []byte("hit\n"), offset)
					case "attach":
						jm.fireAttachScan(cfg, args.Target, []byte("hit\n"))
					}
				}
				for range watchDeliveryBudget {
					fire()
				}
				mu.Lock()
				if len(results) != 1 || !errors.Is(results[0], injected) {
					t.Errorf("first teardown results=%v", results)
				}
				fail = false
				mu.Unlock()
				before := producerSourceEvents(t, jm, jobstore.EventWatchSendPending)
				if len(before) != watchDeliveryBudget || jm.watchCount() != 1 {
					t.Fatalf("before retry produced=%d live=%d", len(before), jm.watchCount())
				}
				fire()
				mu.Lock()
				if len(results) != 2 || results[1] != nil {
					t.Errorf("teardown results=%v, want failure then success", results)
				}
				mu.Unlock()
				after := producerSourceEvents(t, jm, jobstore.EventWatchSendPending)
				jm.mu.Lock()
				fires, deliveries := cfg.conditionFires, cfg.deliveries
				jm.mu.Unlock()
				if fires != watchDeliveryBudget || (!send && deliveries != watchDeliveryBudget) || jm.watchCount() != 0 || len(after) != watchDeliveryBudget+1 || after[len(after)-1].WatchSend.TriggerReason != "budget_exhausted" {
					t.Fatalf("retry fires=%d deliveries=%d live=%d source rows=%d", fires, deliveries, jm.watchCount(), len(after))
				}
				requireWatchSettlement(t, jm, *before[len(before)-1].WatchSend, 0)
			})
		}
	}
}

// A budget-notice append failure must not hide its already durable final firing.
// The existing source retry, not another event or user input, owns the failed ID.
func TestWatchCallbackBatchPartialNoticeFailure(t *testing.T) {
	t.Parallel()
	clock := agenttest.NewFakeClock()
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{func(llm.Request) llm.Response { return finalResponse("partial-batch-response-648") }}}
	s := newSession(t, withAdapter(adapter), withConfig(SessionConfig{StateDir: t.TempDir(), clock: clock}))
	jm := s.jobManager
	res, err := jm.configureWatch(watchArgs{Target: runtimeMessageAliasCaller, Events: []string{"communicate"}, Note: "partial-batch-729"})
	if err != nil {
		t.Fatal(err)
	}
	jm.mu.Lock()
	_, cfg, ok := jm.watchConfigByIDLocked(res.WatchID)
	if ok {
		cfg.deliveries, cfg.conditionFires = watchDeliveryBudget-1, watchDeliveryBudget-1
	}
	jm.mu.Unlock()
	if !ok {
		t.Fatal("watch missing")
	}
	var fail atomic.Bool
	fail.Store(true)
	injected := errors.New("notice append unavailable")
	appendEvent := jm.appendEvent
	var failed jobstore.WatchSendState
	var failures atomic.Int32
	jm.appendEvent = func(ev jobstore.Event) error {
		if ev.Kind == jobstore.EventWatchSendPending && ev.WatchSend.TriggerReason == "budget_exhausted" && fail.Load() {
			failed = *ev.WatchSend
			failures.Add(1)
			return injected
		}
		return appendEvent(ev)
	}
	jm.mu.Lock()
	notification := jm.watchNotificationFromWatch(cfg, "", "event: COMMUNICATE", nil)
	jm.mu.Unlock()
	// Exercise this callback batch alone, without a second producer's tail pump.
	jm.enqueueWatchNotifications([]jobNotification{notification})
	source := producerSourceEvents(t, jm, jobstore.EventWatchSendPending)
	if len(source) != 1 || source[0].WatchSend.TriggerReason != "event: COMMUNICATE" || failures.Load() == 0 {
		t.Fatalf("partial scenario did not fire: rows=%d failures=%d", len(source), failures.Load())
	}
	firing := *source[0].WatchSend
	firingID := fmt.Sprintf("watch:%s:%d", firing.DeliveryID, firing.UpdateSeq)
	budgetID := fmt.Sprintf("watch:%s:%d", failed.DeliveryID, failed.UpdateSeq)
	if failed.DeliveryID == "" || failed.DeliveryID == firing.DeliveryID || failed.Key == firing.Key {
		t.Fatal("failed budget notice lost its separate identity")
	}
	requireWatchSettlement(t, jm, firing, 1)
	ids, err := s.pendingNotificationIDs()
	if err != nil || !equalNotificationIDs(ids, []string{firingID}) {
		t.Fatalf("healthy source did not progress: ids=%v error=%v", ids, err)
	}
	requireCallbackBatchReceipts(t, s, []string{firingID}, 0)
	jm.mu.Lock()
	retry, pending, fires := jm.watchTransportRetry.active, len(cfg.pending), cfg.conditionFires
	jm.mu.Unlock()
	if !retry || pending != 1 || fires != watchDeliveryBudget || jm.watchCount() != 0 {
		t.Fatalf("failed debt not retained: retry=%t pending=%d fires=%d live=%d", retry, pending, fires, jm.watchCount())
	}
	fail.Store(false)
	clock.Advance(jobNotificationRetryInitialDelay)
	clock.Drain()
	source = producerSourceEvents(t, jm, jobstore.EventWatchSendPending)
	if len(source) != 2 || source[1].WatchSend.DeliveryID != failed.DeliveryID || source[1].WatchSend.UpdateSeq != failed.UpdateSeq || source[1].WatchSend.Key != failed.Key {
		t.Fatalf("timed retry changed failed source identity: %+v", source)
	}
	ids, err = s.pendingNotificationIDs()
	if err != nil || !equalNotificationIDs(ids, []string{firingID, budgetID}) {
		t.Fatalf("retry custody=%v error=%v", ids, err)
	}
	for _, state := range []jobstore.WatchSendState{firing, failed} {
		requireWatchSettlement(t, jm, state, 1)
		requireCallbackBatchSource(t, s, state)
	}
	requireCallbackBatchReceipts(t, s, ids, 0)
	if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	requests := adapter.Requests()
	if len(requests) != 1 {
		t.Fatalf("provider requests=%d want1", len(requests))
	}
	requireCallbackBatchRequest(t, s, requests[0], ids)
	requireCallbackBatchReceipts(t, s, ids, 1)
	jm.dispatchProducedWatchSends()
	if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	requireCallbackBatchReceipts(t, s, ids, 1)
	if len(adapter.Requests()) != 1 || jm.hasPendingWatchSends() {
		t.Fatal("retry duplicated accepted ID or left source debt")
	}
	t.Logf("partial notice failures=%d healthy=%s recovered same ID=%s, accepted once and observed once", failures.Load(), firingID, budgetID)
}

// Synthetic canonical events have per-ID custody/replay obligations, not a
// global one-raw-handoff promise outside automatic completion's owner hold.
func TestWatchCanonicalStandaloneBudgetCustody(t *testing.T) {
	t.Parallel()
	s, adapter := newTerminalFlushSession(t, true)
	adapter.steps = []func(llm.Request) llm.Response{func(llm.Request) llm.Response { return finalResponse("standalone-response-193") }}
	res, err := s.jobManager.configureWatch(watchArgs{Target: runtimeMessageAliasCaller, Events: []string{"communicate"}})
	if err != nil {
		t.Fatal(err)
	}
	s.jobManager.mu.Lock()
	_, cfg, _ := s.jobManager.watchConfigByIDLocked(res.WatchID)
	cfg.deliveries, cfg.conditionFires = watchDeliveryBudget-1, watchDeliveryBudget-1
	s.jobManager.mu.Unlock()
	onSessionEventKD(s.jobManager, events.EventCommunicate, events.CommunicateData{Message: "standalone-event-583"})
	source := producerSourceEvents(t, s.jobManager, jobstore.EventWatchSendPending)
	if len(source) != 2 {
		t.Fatalf("source rows=%d want2", len(source))
	}
	ids := make([]string, 0, 2)
	for i, event := range source {
		state := *event.WatchSend
		if state.Key.WatchID != res.WatchID || (i == 0 && state.TriggerReason != "event: COMMUNICATE") || (i == 1 && state.TriggerReason != "budget_exhausted") {
			t.Fatalf("unexpected source fact: %+v", state)
		}
		ids = append(ids, fmt.Sprintf("watch:%s:%d", state.DeliveryID, state.UpdateSeq))
		requireWatchSettlement(t, s.jobManager, state, 1)
		requireCallbackBatchSource(t, s, state)
	}
	if source[0].WatchSend.DeliveryID == source[1].WatchSend.DeliveryID || source[0].WatchSend.Key == source[1].WatchSend.Key {
		t.Fatal("budget overwrote final firing")
	}
	requireCallbackBatchReceipts(t, s, ids, 0)
	s.jobManager.dispatchProducedWatchSends()
	requireCallbackBatchReceipts(t, s, ids, 0)
	if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	requests := adapter.Requests()
	if len(requests) != 1 {
		t.Fatalf("provider requests=%d want1 when drain begins after both custody transfers", len(requests))
	}
	requireCallbackBatchRequest(t, s, requests[0], ids)
	requireCallbackBatchReceipts(t, s, ids, 1)
	s.jobManager.dispatchProducedWatchSends()
	if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	requireCallbackBatchReceipts(t, s, ids, 1)
	if len(adapter.Requests()) != 1 {
		t.Fatal("replay duplicated provider execution")
	}
}

// Source fields, not the production canonical-ID helper, define the expected IDs.
func watchSourceID(state jobstore.WatchSendState) string {
	return fmt.Sprintf("watch:%s:%d", state.DeliveryID, state.UpdateSeq)
}

func watchSourcePayload(state jobstore.WatchSendState) string {
	return strings.ReplaceAll(state.Frame, "delivery_id: "+state.DeliveryID+"\n", "")
}

// Check exact membership as well as per-ID custody, observation and consumption.
// The provider request is checked separately, so a receipt cannot invent presentation.
func requireWatchDeliveryReceipts(t *testing.T, s *Session, expected map[string]string, observations map[string]int) {
	t.Helper()
	accepted, observed, consumed := map[string]int{}, map[string]int{}, map[string]int{}
	for _, entry := range notificationRawEntries(t, s.TranscriptPath()) {
		if id := entry.Turn.AttentionID; id != "" {
			frame, ok := expected[id]
			if !ok || frame == "" || entry.Turn.Message.Text() != frame {
				t.Fatalf("unexpected or changed canonical payload for %s", id)
			}
			accepted[id]++
		}
		for _, id := range entry.Turn.ObservedNotificationIDs {
			if _, ok := expected[id]; !ok {
				t.Fatalf("unexpected observed ID %s", id)
			}
			observed[id]++
		}
		if r := entry.Turn.AttentionResolution; r != nil && r.Disposition == string(delegateAttentionConsumed) {
			if _, ok := expected[r.AttentionID]; !ok {
				t.Fatalf("unexpected consumed ID %s", r.AttentionID)
			}
			consumed[r.AttentionID]++
		}
	}
	for id := range expected {
		wantConsumed := 0
		if observations[id] > 0 {
			wantConsumed = 1
		}
		if accepted[id] != 1 || observed[id] != observations[id] || consumed[id] != wantConsumed {
			t.Fatalf("watch %s accepted=%d observed=%d consumed=%d, want 1/%d/%d", id, accepted[id], observed[id], consumed[id], observations[id], wantConsumed)
		}
		assertAttentionResponseReceipts(t, s, id, observations[id])
	}
}

func requireWatchProviderPayloads(t *testing.T, request llm.Request, expected map[string]string) {
	t.Helper()
	for id, frame := range expected {
		matches := 0
		for _, message := range request.Messages {
			if message.Role == llm.RoleUser && frame != "" && strings.Contains(message.Text(), frame) {
				matches++
			}
		}
		if matches != 1 {
			t.Fatalf("canonical payload %s appears in %d provider messages, want one", id, matches)
		}
	}
}

func reopenWatchSource(t *testing.T, s *Session, old *jobManager) *jobManager {
	t.Helper()
	if err := old.closeStoreOnly(); err != nil {
		t.Fatal(err)
	}
	restored, err := newJobManager(s.stateDir, s.ID(), s.enqueueJobNotificationAndNotify)
	if err != nil {
		t.Fatal(err)
	}
	restored.clock = s.clock
	s.jobManager, restored.retirementOwner = restored, s
	restored.delegateController = s.delegateController
	return restored
}

func repairWatchReceiver(t *testing.T, s *Session) {
	t.Helper()
	writer, _, err := transcript.OpenWriterForSession(transcriptPath(s.stateDir, s.ID()), s.ID())
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.transcript, s.transcriptReady = writer, true
	s.mu.Unlock()
}
