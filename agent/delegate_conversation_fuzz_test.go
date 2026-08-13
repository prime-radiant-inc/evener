//go:build serffuzz

package agent

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/serf/agent/internal/delegatestore"
	"primeradiant.com/serf/agent/transcript"
)

func FuzzDelegateConversationTransitions(f *testing.F) {
	f.Add([]byte{0, 2, 3, 4, 6, 8, 0, 2, 5, 6, 8})
	f.Add([]byte{1, 2, 3, 7, 9, 10, 6, 11})
	f.Add([]byte{0, 2, 3, 3, 4, 6, 8, 8})
	f.Fuzz(func(t *testing.T, program []byte) {
		if len(program) > 64 {
			program = program[:64]
		}
		c, _ := newDelegateControllerTestHarness(t, 2, 1)
		seedDelegateControllerIdle(t, c, "dlg_fuzz", "")
		fs := afero.NewMemMapFs()
		writer, err := transcript.NewWriterWithFS(fs, "/delegate.jsonl", transcript.Header{SessionID: "child-dlg_fuzz"})
		if err != nil {
			t.Fatalf("NewWriterWithFS: %v", err)
		}
		t.Cleanup(func() { _ = writer.Close() })
		runtime := &Session{}
		runtime.attachTranscript(writer)
		receiver := newFakeDelegateDeliveryReceiver()
		acceptedSteers := make(map[string]bool)
		boundSteers := make(map[string]bool)
		var deliveryPlans []delegateDeliveryPlan
		stopRequested := false

		for step, operation := range program {
			switch operation % 12 {
			case 0:
				if c.durable["dlg_fuzz"].Phase != delegatestore.PhaseIdle || c.durable["dlg_fuzz"].PendingStopSeq != 0 {
					break
				}
				reservation, err := c.ReserveStart(rootDelegateActor("root-session"), "dlg_fuzz")
				if err != nil {
					break
				}
				if operation&0x80 != 0 {
					_, _ = c.RegisterInlineWaiter(reservation)
				}
				started, err := c.CommitStart(reservation)
				if err != nil {
					break
				}
				if c.AttachRuntime(started.lease, runtime) == nil {
					_, _ = c.AdmitStartInput(started.lease, func() error { return nil })
				}

			case 1:
				lease, ok := firstDelegateControllerBinding(c, true)
				if !ok {
					break
				}
				before := delegateConversationHistoryIDs(runtime)
				if _, err := c.Steer(context.Background(), rootDelegateActor("root-session"), lease.delegateID, fmt.Sprintf("steer-%d-%d", step, operation)); err == nil {
					after := delegateConversationHistoryIDs(runtime)
					if len(after) != len(before)+1 {
						t.Fatalf("accepted steer history grew from %d to %d", len(before), len(after))
					}
					acceptedSteers[after[len(after)-1]] = true
				}

			case 2:
				lease, ok := firstDelegateControllerBinding(c, true)
				if !ok {
					break
				}
				pending := delegateConversationPendingSteers(c, lease.delegateID)
				if _, err := c.BeginModelRequest(lease); err == nil {
					present := delegateConversationHistoryIDSet(runtime)
					for _, id := range pending {
						if !present[id] {
							continue
						}
						if boundSteers[id] {
							t.Fatalf("steer %s bound twice", id)
						}
						boundSteers[id] = true
					}
				}

			case 3:
				if lease, ok := firstDelegateControllerBinding(c, true); ok {
					_, _, _ = c.BeginSettlement(lease, nil)
				}

			case 4:
				if lease, ok := firstDelegateControllerBinding(c, true); ok {
					packet := delegateControllerReportedPacket(fmt.Sprintf("report-%d", step))
					_, _, _ = c.BeginSettlement(lease, &packet)
				}

			case 5, 6:
				if lease, ok := firstDelegateControllerBinding(c, false); ok {
					finish := delegateFinish{outcome: delegatestore.OutcomeFailed, reason: "fuzz_finish"}
					if operation%12 == 5 {
						finish.outcome = delegatestore.OutcomeCompleted
					}
					if plans, err := c.FinishGeneration(lease, finish); err == nil {
						deliveryPlans = append(deliveryPlans, plans.deliveries...)
					}
				}

			case 7:
				stale := delegateLease{delegateID: "dlg_fuzz", generation: c.durable["dlg_fuzz"].Generation + 1}
				before := cloneDelegateControllerState(t, c.durable)
				plans, err := c.FinishGeneration(stale, delegateFinish{outcome: delegatestore.OutcomeFailed, reason: "stale"})
				if err != nil || len(plans.updates) != 0 || !reflect.DeepEqual(before, c.durable) {
					t.Fatalf("stale finish mutated state: err=%v plans=%#v", err, plans)
				}

			case 8:
				if len(deliveryPlans) == 0 {
					deliveryPlans = append(deliveryPlans, c.ReplayDeliveries()...)
				}
				if len(deliveryPlans) == 0 {
					break
				}
				plan := deliveryPlans[0]
				deliveryPlans = deliveryPlans[1:]
				if plan.waiter == nil {
					plans, _ := deliverDelegatePacket(plan, receiver)
					deliveryPlans = append(deliveryPlans, plans.deliveries...)
					break
				}
				_, _ = deliverDelegatePacket(plan, nil)
				resolution := <-plan.waiter.resolution
				if resolution.commit != nil {
					plans, _ := resolution.commit.Complete(operation&1 == 0)
					deliveryPlans = append(deliveryPlans, plans.deliveries...)
				}

			case 9:
				if waiter := delegateConversationFirstWaiter(c); waiter != nil {
					ctx, cancel := context.WithCancel(context.Background())
					cancel()
					if resolution := c.waitForDelegateInline(ctx, waiter); !resolution.fallback {
						t.Fatalf("unclaimed waiter timeout = %#v", resolution)
					}
				}

			case 10:
				if !stopRequested {
					appendDelegateControllerStopRequest(t, c, "dlg_fuzz")
					stopRequested = true
				}

			case 11:
				if lease, ok := firstDelegateControllerBinding(c, false); ok {
					_ = c.BeginTool(lease)
				}
			}

			assertDelegateConversationInvariants(t, c, runtime, acceptedSteers, stopRequested)
		}
	})
}

func assertDelegateConversationInvariants(t *testing.T, c *delegateTreeController, runtime *Session, acceptedSteers map[string]bool, stopRequested bool) {
	t.Helper()
	historyIDs := delegateConversationHistoryIDSet(runtime)
	for id := range acceptedSteers {
		if !historyIDs[id] {
			t.Fatalf("accepted steer %s disappeared from the conversation", id)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	aggregate := c.durable["dlg_fuzz"]
	if aggregate.Phase == delegatestore.PhaseSettling && aggregate.PreparedTerminal == nil {
		t.Fatal("durable settling has no prepared terminal")
	}
	if aggregate.Phase != delegatestore.PhaseSettling && aggregate.Phase != delegatestore.PhaseStopping && aggregate.PreparedTerminal != nil {
		t.Fatalf("phase %s retained prepared terminal", aggregate.Phase)
	}
	if stopRequested && aggregate.PendingStopSeq != 0 && aggregate.Phase != delegatestore.PhaseStopping && aggregate.Phase != delegatestore.PhaseClosed {
		t.Fatalf("stop precedence regressed to phase %s", aggregate.Phase)
	}
	for _, delivery := range aggregate.PendingDeliveries {
		if want := delegateDeliveryID(aggregate.DelegateID, delivery.Generation); delivery.DeliveryID != want {
			t.Fatalf("delivery ID %s, want %s", delivery.DeliveryID, want)
		}
	}
	if live := c.live["dlg_fuzz"]; live != nil {
		for generation, waiter := range live.waiters {
			if waiter == nil || waiter.generation != generation {
				t.Fatalf("waiter key %d does not match %#v", generation, waiter)
			}
		}
	}
	if aggregate.LatestOutcome != nil && string(aggregate.LatestOutcome.Status) == string(delegatestore.DispositionCompletedNoAction) {
		t.Fatalf("private disposition leaked as public outcome: %#v", aggregate.LatestOutcome)
	}
}

func delegateConversationHistoryIDs(runtime *Session) []string {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	ids := make([]string, 0, len(runtime.history))
	for _, turn := range runtime.history {
		if turn.StableTurnID != "" {
			ids = append(ids, turn.StableTurnID)
		}
	}
	return ids
}

func delegateConversationHistoryIDSet(runtime *Session) map[string]bool {
	ids := delegateConversationHistoryIDs(runtime)
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}

func delegateConversationPendingSteers(c *delegateTreeController, delegateID string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	live := c.live[delegateID]
	if live == nil {
		return nil
	}
	ids := make([]string, 0, len(live.pendingSteers))
	for _, pending := range live.pendingSteers {
		ids = append(ids, pending.entryID)
	}
	return ids
}

func delegateConversationFirstWaiter(c *delegateTreeController) *delegateInlineWaiter {
	c.mu.Lock()
	defer c.mu.Unlock()
	live := c.live["dlg_fuzz"]
	if live == nil {
		return nil
	}
	for _, waiter := range live.waiters {
		return waiter
	}
	return nil
}
