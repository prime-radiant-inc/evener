package agent

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/llm"
)

// The source executor deliberately retains its acknowledgment until the real
// canonical observation and the drain's blocking select boundary. Entry alone
// cannot establish this interleaving. No resource clock or polling drives it.
func TestDelegateSourceAcknowledgmentWakesObservedDrain(t *testing.T) {
	for _, replay := range []bool{false, true} {
		t.Run(map[bool]string{false: "completion", true: "committed receiver replay"}[replay], func(t *testing.T) {
			adapter := newMessagingAdapter()
			s := newMessagingRoot(t, adapter, nil)
			c := s.delegateController
			const source = "dlg_ack_drain"
			seedDelegateControllerIdle(t, c, source, "")
			reservation, err := c.ReserveStart(rootDelegateActor(s.ID()), source)
			if err != nil {
				t.Fatal(err)
			}
			started, err := c.CommitStart(reservation)
			if err != nil {
				t.Fatal(err)
			}
			plans := finishDelegateDeliveryGeneration(t, c, started.lease, "source-debt-report")
			if len(plans.deliveries) != 1 {
				t.Fatalf("source plans = %#v", plans.deliveries)
			}
			plan := plans.deliveries[0]
			token, admitted, err := c.BeginDelivery(plan)
			if err != nil || !admitted {
				t.Fatalf("BeginDelivery = %t, %v", admitted, err)
			}
			content, err := delegateNotificationContent(plan)
			if err != nil {
				t.Fatal(err)
			}
			id := delegateAttentionID(plan.deliveryID)
			if appended, err := s.appendDelegateNotificationDurably(id, content); err != nil || !appended {
				t.Fatalf("canonical custody = %t, %v", appended, err)
			}
			adapter.script("root", func(req llm.Request) llm.Response {
				observationRequest(t, req, []string{"source-debt-report"}, nil)
				return agenttest.FinalResponse("observed once")
			})
			// Keep the admitted receipt nonretryable through normal request
			// preparation, which itself replays retryable source deliveries.
			if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
				t.Fatal(err)
			}
			requireOwnedDrainAttentionObservedOnce(t, s, id)
			if replay {
				// Only after observation, make a real failed source append retain
				// the committed receiver receipt for the post-boundary replay.
				c.commitColdDeliveryReceiver(token)
				if err := c.store.Close(); err != nil {
					t.Fatal(err)
				}
				if _, err := c.CompleteDelivery(token, true); err == nil {
					t.Fatal("closed source journal acknowledged delivery")
				}
				reopened, err := delegatestore.Open(filepath.Join(jobsDir(s.stateDir, s.ID()), "delegates.jsonl"))
				if err != nil {
					t.Fatal(err)
				}
				c.mu.Lock()
				c.store = reopened
				receipt := c.deliveryReceiptLocked(plan.deliveryID)
				retained := receipt != nil && receipt.token == token && receipt.retryable && receipt.receiverCommitted
				c.mu.Unlock()
				if !retained {
					t.Fatal("failed acknowledgment lost exact committed retryable receipt")
				}
			}
			pending := delegateAggregateSnapshot(t, c, source).PendingDeliveries
			if len(pending) != 1 || pending[0].DeliveryID != plan.deliveryID || pending[0].OwnerDelegateID != "" || !c.deliveryPending(source, plan.deliveryID) {
				t.Fatalf("exact source debt before drain = %+v", pending)
			}
			wake, hint := newDrainWake()
			s.SetNotifyFunc(hint)
			parked := make(chan struct{})
			var once sync.Once
			observe := func(session *Session) {
				if session == s && len(wake) == 0 && c.deliveryPending(source, plan.deliveryID) {
					once.Do(func() { close(parked) })
				}
			}
			observeJobTreeDrainWait.Store(&observe)
			defer observeJobTreeDrainWait.Store(nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				// The source executor in this test owns the delayed acknowledgment;
				// the injected kick must not steal that operation at drain entry.
				_, err := s.drainJobTreeWith(ctx, wake, nil, func(context.Context) error { return nil }, s.ProcessInputKind)
				done <- err
			}()
			awaitSignal(t, parked, "observed receiver drain parked on source debt")
			if pending := s.pendingDrainDelegateReceivers(nil); len(pending) != 1 || pending[0] != s.ID() {
				t.Fatalf("parked source debt receivers = %v", pending)
			}
			requireOwnedDrainAttentionObservedOnce(t, s, id)
			if replay {
				retry := c.ReplayDeliveries()
				if len(retry) != 1 || retry[0].deliveryID != plan.deliveryID || !c.deliveryReceiverCommitted(token) {
					t.Fatalf("committed receiver retry = %#v", retry)
				}
				_, err = deliverDelegatePacket(retry[0], nil)
			} else {
				_, err = c.CompleteDelivery(token, true)
			}
			if err != nil {
				t.Fatalf("source acknowledgment: %v", err)
			}
			if err := awaitInput(t, done); err != nil {
				t.Fatalf("drain after acknowledgment: %v", err)
			}
			if producerRecordCount(t, s) != 1 || len(adapter.requestsFor("root")) != 1 {
				t.Fatalf("records = %d, requests = %d, want one each", producerRecordCount(t, s), len(adapter.requestsFor("root")))
			}
			requireOwnedDrainAttentionObservedOnce(t, s, id)
			events, err := c.store.Load()
			if err != nil {
				t.Fatal(err)
			}
			acks := 0
			for _, event := range events {
				if event.DeliveryAcknowledged != nil && event.DeliveryAcknowledged.DeliveryID == plan.deliveryID {
					acks++
				}
			}
			if acks != 1 || c.deliveryPending(source, plan.deliveryID) || len(s.pendingDrainDelegateReceivers(nil)) != 0 {
				t.Fatalf("acks = %d, source debt remains = %t", acks, c.deliveryPending(source, plan.deliveryID))
			}
		})
	}
}
