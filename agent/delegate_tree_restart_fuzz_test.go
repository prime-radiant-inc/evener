//go:build evenerfuzz

package agent

import (
	"path/filepath"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
)

func FuzzDelegateRestartEquivalence(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3})
	f.Add([]byte{1, 0, 3, 2, 1})
	f.Add([]byte{2, 3})    // terminal preparation, then Stop before settlement
	f.Add([]byte{0, 2, 3}) // unaccepted delivery survives the prepared-terminal cut
	f.Add([]byte{3, 2, 0}) // Stop before completion rejects later admission
	f.Fuzz(func(t *testing.T, program []byte) {
		if len(program) > 32 {
			program = program[:32]
		}
		for _, historical := range []bool{false, true} {
			name := "preserving"
			if historical {
				name = "historical"
			}
			t.Run(name, func(t *testing.T) {
				uninterrupted, _ := newDelegateControllerTestHarness(t, 4, 2)
				restarting, path := newDelegateControllerTestHarness(t, 4, 2)
				seedRestartFuzzHistory(t, uninterrupted, program, historical)
				seedRestartFuzzHistory(t, restarting, program, historical)
				pendingBefore := make(map[string][]delegatestore.PendingDelivery)
				for id, aggregate := range restarting.durable {
					pendingBefore[id] = append([]delegatestore.PendingDelivery(nil), aggregate.PendingDeliveries...)
				}
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
					if len(shell.runningJobIDs) != 0 {
						t.Fatalf("%s shell evidence survived quiescence: %#v", name, shell)
					}
					path := filepath.Join(jobsDir(controller.stateDir, "child-dlg_child"), "jobs.jsonl")
					events, err := jobstore.ReadEvents(path)
					if err != nil {
						t.Fatal(err)
					}
					consumed, generation := 0, ""
					for _, event := range events {
						if event.Kind == jobstore.EventJobFinished && event.JobID == "job-shell" {
							generation = event.TerminalGen
						}
						if event.Kind == jobstore.EventJobNotificationConsumed {
							consumed++
						}
					}
					if generation == "" {
						t.Fatalf("%s missing repaired job-shell terminal generation", name)
					}
					if historical {
						if len(shell.pendingNotification) != 0 || consumed != 1 {
							t.Fatalf("%s historical discard changed: shell=%+v consumed=%d", name, shell, consumed)
						}
					} else if !reflect.DeepEqual(shell.pendingNotification, []shellNotificationIdentity{{jobID: "job-shell", terminalGeneration: generation}}) || consumed != 0 {
						t.Fatalf("%s preserving Stop lost exact shell custody: shell=%+v generation=%s consumed=%d", name, shell, generation, consumed)
					}
				}
				completedMembers := completedDelegateStopMembers(t, restarted)
				for senderID, aggregate := range restarted.durable {
					for _, delivery := range aggregate.PendingDeliveries {
						if _, covered := completedMembers[delivery.OwnerDelegateID]; historical && covered {
							t.Fatalf("covered-owner delivery survived completion: sender=%s owner=%s", senderID, delivery.OwnerDelegateID)
						}
					}
				}
				if !historical {
					for senderID, deliveries := range pendingBefore {
						for _, before := range deliveries {
							if _, covered := completedMembers[before.OwnerDelegateID]; !covered {
								continue
							}
							owner := restarted.durable[before.OwnerDelegateID]
							if owner == nil || !owner.Resumable {
								continue
							}
							found := false
							for _, after := range restarted.durable[senderID].PendingDeliveries {
								if reflect.DeepEqual(before, after) {
									found = true
								}
							}
							if !found {
								t.Fatalf("preserving Stop lost pre-reconcile delivery: sender=%s delivery=%+v", senderID, before)
							}
						}
					}
				}
			})
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

func seedRestartFuzzHistory(t *testing.T, c *delegateTreeController, program []byte, historical bool) {
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
				if historical {
					appendDelegateControllerStopRequest(t, c, "dlg_parent")
					events, err := c.store.Load()
					if err != nil {
						t.Fatal(err)
					}
					if err := c.restorePendingStop(events); err != nil {
						t.Fatal(err)
					}
				} else if _, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_parent"); err != nil {
					t.Fatalf("step %d StopSubtree: %v", step, err)
				}
			}
		}
	}
	if c.stop == nil {
		if historical {
			appendDelegateControllerStopRequest(t, c, "dlg_parent")
		} else if _, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_parent"); err != nil {
			t.Fatalf("final StopSubtree: %v", err)
		}
	}
	events, err := c.store.Load()
	if err != nil {
		t.Fatalf("Load seeded history: %v", err)
	}
	if c.stop == nil {
		if err := c.restorePendingStop(events); err != nil {
			t.Fatal(err)
		}
	}
	c.reconcileOrder = delegateOpenRunOrder(events, c.durable)
}
