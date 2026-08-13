//go:build serffuzz

package agent

import (
	"reflect"
	"testing"

	"primeradiant.com/serf/agent/internal/delegatestore"
)

func FuzzDelegateRestartEquivalence(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3})
	f.Add([]byte{1, 0, 3, 2, 1})
	f.Fuzz(func(t *testing.T, program []byte) {
		if len(program) > 32 {
			program = program[:32]
		}
		uninterrupted, _ := newDelegateControllerTestHarness(t, 4, 2)
		restarting, path := newDelegateControllerTestHarness(t, 4, 2)
		seedRestartFuzzHistory(t, uninterrupted, program)
		seedRestartFuzzHistory(t, restarting, program)

		// Process-local delivery receipts do not survive a process boundary. The
		// uninterrupted comparison drops the same receipts explicitly while the
		// restarted controller proves they are absent after reopen.
		uninterrupted.mu.Lock()
		uninterrupted.deliveries = make(map[uint64]*delegateDeliveryAdmission)
		uninterrupted.evidenceVersion++
		uninterrupted.mu.Unlock()
		restarted := reopenDelegateController(t, restarting, path)

		leftEvidence, err := collectDelegateReconcileEvidence(uninterrupted.stateDir, uninterrupted.ReconcileRequirements())
		if err != nil {
			t.Fatalf("collect uninterrupted evidence: %v", err)
		}
		rightEvidence, err := collectDelegateReconcileEvidence(restarted.stateDir, restarted.ReconcileRequirements())
		if err != nil {
			t.Fatalf("collect restarted evidence: %v", err)
		}
		if _, err := uninterrupted.Reconcile(leftEvidence); err != nil {
			t.Fatalf("uninterrupted Reconcile: %v", err)
		}
		if _, err := restarted.Reconcile(rightEvidence); err != nil {
			t.Fatalf("restarted Reconcile: %v", err)
		}
		if !reflect.DeepEqual(uninterrupted.durable, restarted.durable) {
			t.Fatalf("restart state differs:\nuninterrupted=%#v\nrestarted=%#v", uninterrupted.durable, restarted.durable)
		}
		if len(restarted.live) != 0 {
			t.Fatalf("restart constructed runtime/provider state: %#v", restarted.live)
		}
		for ownerID, aggregate := range restarted.durable {
			for _, delivery := range aggregate.PendingDeliveries {
				if covered := restarted.durable[delivery.OwnerDelegateID]; covered != nil && covered.PendingStopSeq != 0 {
					t.Fatalf("covered-owner delivery survived completion: sender=%s owner=%s", ownerID, delivery.OwnerDelegateID)
				}
			}
		}
	})
}

func seedRestartFuzzHistory(t *testing.T, c *delegateTreeController, program []byte) {
	t.Helper()
	seedDelegateControllerIdle(t, c, "dlg_parent", "")
	seedDelegateControllerIdle(t, c, "dlg_child", "dlg_parent")
	for step, operation := range program {
		switch operation % 4 {
		case 0:
			aggregate := c.durable["dlg_child"]
			if aggregate.Phase == delegatestore.PhaseIdle && aggregate.PendingStopSeq == 0 {
				seedDelegateControllerDelivery(t, c, "dlg_child")
			}
		case 1:
			aggregate := c.durable["dlg_parent"]
			if aggregate.Phase != delegatestore.PhaseIdle || aggregate.PendingStopSeq != 0 {
				continue
			}
			c.mu.Lock()
			_, err := c.appendLocked(delegateControllerRunStartedEvent("dlg_parent", aggregate.Generation+1, delegatestore.TriggerOwnerInput, c.now()))
			c.mu.Unlock()
			if err != nil {
				t.Fatalf("step %d seed running parent: %v", step, err)
			}
		case 2:
			aggregate := c.durable["dlg_child"]
			if aggregate.Phase != delegatestore.PhaseIdle || aggregate.PendingStopSeq != 0 {
				continue
			}
			lease := delegateLease{delegateID: "dlg_child", generation: aggregate.Generation + 1}
			packet := delegateControllerReportedPacket("prepared")
			c.mu.Lock()
			_, err := c.appendLocked(
				delegateControllerRunStartedEvent(lease.delegateID, lease.generation, delegatestore.TriggerOwnerInput, c.now()),
				delegatestore.Event{Kind: delegatestore.EventDelegateTerminalPrepared, DelegateID: lease.delegateID, TerminalPrepared: &delegatestore.TerminalPrepared{Generation: lease.generation, Packet: packet}},
			)
			c.mu.Unlock()
			if err != nil {
				t.Fatalf("step %d seed prepared child: %v", step, err)
			}
		case 3:
			if c.stop == nil {
				_, _, _, _ = c.StopSubtree(rootDelegateActor("root-session"), "dlg_parent")
			}
		}
	}
	if c.stop == nil {
		if _, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_parent"); err != nil {
			t.Fatalf("final StopSubtree: %v", err)
		}
	}
	events, err := c.store.Load()
	if err != nil {
		t.Fatalf("Load seeded history: %v", err)
	}
	c.reconcileOrder = delegateOpenRunOrder(events, c.durable)
}
