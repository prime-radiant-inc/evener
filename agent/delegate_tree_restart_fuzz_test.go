//go:build serffuzz

package agent

import (
	"path/filepath"
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
		seedDelegateShellStoreAt(t, filepath.Join(jobsDir(uninterrupted.stateDir, "child-dlg_child"), "jobs.jsonl"))
		seedDelegateShellStoreAt(t, filepath.Join(jobsDir(restarting.stateDir, "child-dlg_child"), "jobs.jsonl"))

		// Process-local delivery claims and receipts do not survive a process
		// boundary. The uninterrupted comparison drops the same evidence while
		// the restarted controller proves it is absent after reopen.
		uninterrupted.mu.Lock()
		uninterrupted.deliveries = make(map[uint64]*delegateDeliveryAdmission)
		uninterrupted.deliveryClaims = make(map[string]*delegateDeliveryClaim)
		uninterrupted.evidenceVersion++
		uninterrupted.mu.Unlock()
		restarted := reopenDelegateController(t, restarting, path)

		reconcileRestartFuzzToQuiescence(t, uninterrupted)
		reconcileRestartFuzzToQuiescence(t, restarted)
		if !reflect.DeepEqual(uninterrupted.durable, restarted.durable) {
			t.Fatalf("restart state differs:\nuninterrupted=%#v\nrestarted=%#v", uninterrupted.durable, restarted.durable)
		}
		if len(restarted.live) != 0 {
			t.Fatalf("restart constructed runtime/provider state: %#v", restarted.live)
		}
		for name, controller := range map[string]*delegateTreeController{"uninterrupted": uninterrupted, "restarted": restarted} {
			shell, err := collectShellRuntimeLossEvidence(filepath.Join(jobsDir(controller.stateDir, "child-dlg_child"), "jobs.jsonl"))
			if err != nil {
				t.Fatalf("%s collect repaired shell: %v", name, err)
			}
			if len(shell.runningJobIDs) != 0 || len(shell.pendingNotification) != 0 {
				t.Fatalf("%s shell evidence survived quiescence: %#v", name, shell)
			}
		}
		completedMembers := completedDelegateStopMembers(t, restarted)
		for senderID, aggregate := range restarted.durable {
			for _, delivery := range aggregate.PendingDeliveries {
				if _, covered := completedMembers[delivery.OwnerDelegateID]; covered {
					t.Fatalf("covered-owner delivery survived completion: sender=%s owner=%s", senderID, delivery.OwnerDelegateID)
				}
			}
		}
	})
}

func reconcileRestartFuzzToQuiescence(t *testing.T, c *delegateTreeController) {
	t.Helper()
	for cycle := 0; cycle < 32; cycle++ {
		evidence, err := collectDelegateReconcileEvidence(c.stateDir, c.ReconcileRequirements())
		if err != nil {
			t.Fatalf("cycle %d collect evidence: %v", cycle, err)
		}
		plans, err := c.Reconcile(evidence)
		if err != nil {
			t.Fatalf("cycle %d Reconcile: %v", cycle, err)
		}
		for _, plan := range plans.shellRepairs {
			if err := executeDelegateShellRepair(plan, c.now()); err != nil {
				t.Fatalf("cycle %d shell repair: %v", cycle, err)
			}
		}
		for _, plan := range plans.attention {
			if err := c.executeDelegateAttentionCleanup(plan); err != nil {
				t.Fatalf("cycle %d attention cleanup: %v", cycle, err)
			}
		}
		if c.stop == nil && len(plans.shellRepairs) == 0 && len(plans.attention) == 0 {
			return
		}
	}
	t.Fatal("reconciliation did not reach quiescence in 32 monotonic cycles")
}

func completedDelegateStopMembers(t *testing.T, c *delegateTreeController) map[string]struct{} {
	t.Helper()
	events, err := c.store.Load()
	if err != nil {
		t.Fatalf("Load completed stop events: %v", err)
	}
	targetID := ""
	for _, event := range events {
		if event.Kind == delegatestore.EventDelegateSubtreeStopCompleted && event.SubtreeStopCompleted != nil {
			targetID = event.DelegateID
		}
	}
	if targetID == "" {
		t.Fatal("restart fuzz did not complete its durable stop")
	}
	members := map[string]struct{}{targetID: {}}
	for changed := true; changed; {
		changed = false
		for id, aggregate := range c.durable {
			if aggregate == nil {
				continue
			}
			if _, included := members[id]; included {
				continue
			}
			if _, parentIncluded := members[aggregate.Descriptor.ParentDelegateID]; parentIncluded {
				members[id] = struct{}{}
				changed = true
			}
		}
	}
	return members
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
