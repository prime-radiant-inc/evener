package agent

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/clock"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func newRetirementDelegateController(t *testing.T) (*Session, *delegateTreeController, *RetirementController) {
	t.Helper()
	root := newQueuePersistTestSession(t, t.TempDir())
	c, err := NewRetirementController(0, clock.Real())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AttachRoot(root); err != nil {
		t.Fatal(err)
	}
	if root.delegateController == nil {
		t.Fatal("root delegate controller is nil")
	}
	return root, root.delegateController, c
}

func TestRetirementColdDelegatePendingOutcomeBlocks(t *testing.T) {
	root, tree, c := newRetirementDelegateController(t)
	defer root.Close()
	seedDelegateReclaimRuntime(t, tree, "cold-pending", "", time.Unix(1, 0), false, false)
	tree.mu.Lock()
	tree.live["cold-pending"].runtime = nil
	tree.mu.Unlock()
	claim, state, err := c.TryClaim(true)
	if err != nil {
		t.Fatal(err)
	}
	if claim != nil {
		defer c.Abort(claim, "")
		t.Fatal("cold pointer concealed pending durable outcome")
	}
	if !slices.ContainsFunc(state.Blockers, func(b RetirementBlocker) bool {
		return b.Category == "delegate" && b.DelegateID == "cold-pending"
	}) {
		t.Fatalf("missing cold delegate blocker: %+v", state)
	}
}

func TestRetirementDelegateCreateClaimFirst(t *testing.T) {
	root, tree, c := newRetirementDelegateController(t)
	defer root.Close()
	claim, state, err := c.TryClaim(true)
	if err != nil || claim == nil {
		t.Fatalf("claim = %v state = %+v err = %v", claim, state, err)
	}
	defer c.Abort(claim, "")
	result := root.createDelegate(context.Background(), delegateArgs{Task: "retirement-create-sentinel"})
	if !errors.Is(result.Err, ErrRetirementUnavailable) {
		t.Errorf("create after claim error = %v, want ErrRetirementUnavailable", result.Err)
	}
	tree.mu.Lock()
	durable, reserved := len(tree.durable), len(tree.reservations)
	tree.mu.Unlock()
	if durable != 0 || reserved != 0 {
		t.Errorf("refused create left durable=%d reserved=%d, want no child identity or reservation", durable, reserved)
	}
}

func TestRetirementDelegateReservationClaimFirst(t *testing.T) {
	root, tree, c := newRetirementDelegateController(t)
	defer root.Close()
	claim, _, err := c.TryClaim(true)
	if err != nil || claim == nil {
		t.Fatalf("claim: %v %v", claim, err)
	}
	defer c.Abort(claim, "")
	reservation, err := tree.ReserveCreate(rootDelegateActor(root.ID()), delegateControllerCreatedEvent("unused", "").Created.Descriptor)
	if reservation != nil {
		defer tree.AbortStart(reservation)
	}
	if !errors.Is(err, ErrRetirementUnavailable) || reservation != nil {
		t.Fatalf("ReserveCreate after claim = %v, %v; want refusal before reservation", reservation, err)
	}
}

func TestRetirementDelegateCloseClaimFirst(t *testing.T) {
	root, tree, c := newRetirementDelegateController(t)
	defer root.Close()
	before, err := tree.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	claim, _, err := c.TryClaim(true)
	if err != nil || claim == nil {
		t.Fatalf("claim: %v %v", claim, err)
	}
	defer c.Abort(claim, "")
	if err := tree.Close(context.Background()); !errors.Is(err, ErrRetirementUnavailable) {
		t.Errorf("Close after claim = %v, want retirement refusal", err)
	}
	after, err := tree.store.Load()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Errorf("original store changed/closed: %v", err)
	}
	tree.mu.Lock()
	closing, stop := tree.closing, tree.stop
	tree.mu.Unlock()
	if closing || stop != nil {
		t.Errorf("refused Close published closing=%v stop=%v", closing, stop)
	}
}

func TestRetirementDelegateCreatedChildInheritsController(t *testing.T) {
	root, _, c := newRetirementDelegateController(t)
	defer root.Close()
	result := root.createDelegate(context.Background(), delegateArgs{Task: "inherit-controller-sentinel"})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	children := root.subagents.sessions()
	if len(children) != 1 {
		t.Fatalf("children = %d", len(children))
	}
	if got := children[0].retirementController.Load(); got != c {
		t.Fatalf("child inherited %p, want exact process controller %p", got, c)
	}
}

func TestRetirementDelegateWatchReceiptOrders(t *testing.T) {
	t.Run("claim first", func(t *testing.T) {
		root, tree, c := newRetirementDelegateController(t)
		defer root.Close()
		claim, _, err := c.TryClaim(true)
		if err != nil || claim == nil {
			t.Fatalf("claim: %v %v", claim, err)
		}
		defer c.Abort(claim, "")
		for _, acquire := range []func() (*delegateWatchReceipt, error){
			func() (*delegateWatchReceipt, error) {
				return tree.BeginWatchEnqueue("", 0, "", "receipt-sentinel", 1, false)
			},
			func() (*delegateWatchReceipt, error) {
				return tree.AcquireWatchDelivery("", 0, "", "receipt-sentinel", 1, false)
			},
		} {
			receipt, err := acquire()
			if !errors.Is(err, ErrRetirementUnavailable) || receipt != nil {
				t.Errorf("watch admitted behind claim: %+v %v", receipt, err)
			}
		}
		tree.mu.Lock()
		published := len(tree.watchEnqueues) + len(tree.watchDeliveries)
		tree.mu.Unlock()
		if published != 0 {
			t.Errorf("refusal published %d original receipts", published)
		}
	})
	t.Run("receipt first retains exact settlement authority", func(t *testing.T) {
		root, tree, c := newRetirementDelegateController(t)
		defer root.Close()
		receipt, err := tree.BeginWatchEnqueue("", 0, "", "receipt-sentinel", 7, false)
		if err != nil {
			t.Fatal(err)
		}
		claim, state, err := c.TryClaim(true)
		if err != nil || claim != nil {
			t.Fatalf("pending enqueue admitted retirement: %v %+v %v", claim, state, err)
		}
		tree.mu.Lock()
		original := tree.watchEnqueues[receipt.token]
		tree.mu.Unlock()
		if original != receipt || original.deliveryID != "receipt-sentinel" || original.updateSeq != 7 {
			t.Fatal("claim attempt erased or replaced original receipt")
		}
		delivery, err := tree.CompleteWatchEnqueue(receipt)
		if err != nil {
			t.Fatal(err)
		}
		if delivery.deliveryID != receipt.deliveryID || delivery.updateSeq != receipt.updateSeq || delivery.sourceGeneration != receipt.sourceGeneration || delivery.token == receipt.token {
			t.Fatal("settlement lost receipt identity")
		}
		claim, _, err = c.TryClaim(true)
		if err != nil || claim != nil {
			t.Fatalf("pending delivery admitted retirement: %v %v", claim, err)
		}
		if err := tree.CompleteWatchDelivery(delivery); err != nil {
			t.Fatal(err)
		}
		claim, state, err = c.TryClaim(true)
		if err != nil || claim == nil {
			t.Fatalf("settled receipt blocks retirement: %+v %v", state, err)
		}
		if err := c.Abort(claim, ""); err != nil {
			t.Fatal(err)
		}
	})
}

type retirementDelegateAdapter struct{ fakeAdapter }

func (a *retirementDelegateAdapter) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	response := communicateWithDefaultOutput("result-sentinel")
	response.Provider, response.Model = a.name, req.Model
	return response, nil
}

func retirementIdleDelegate(t *testing.T, root *Session) delegateResult {
	t.Helper()
	root.client.Register(&retirementDelegateAdapter{fakeAdapter: fakeAdapter{name: "openai"}})
	result := root.createDelegate(context.Background(), delegateArgs{Task: "idle-delegate-sentinel"})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	retirementSettleDelegate(t, root, result)
	return result
}

func retirementSettleDelegate(t *testing.T, root *Session, result delegateResult) {
	t.Helper()
	sub := root.subagents.get(result.ChildSessionID)
	if sub == nil {
		t.Fatal("created delegate missing from original manager")
	}
	sub.mu.Lock()
	done := sub.done
	sub.mu.Unlock()
	// TRIPWIRE: completion is the runner's channel, never a timing guess. The
	// bound only prevents a broken fixture from hanging the package indefinitely.
	select {
	case <-done:
	case <-time.After(10 * time.Second): // TRIPWIRE: fixture rendezvous normally takes milliseconds; this only bounds a deadlock.
		t.Fatal("delegate runner did not finish")
	}
	if _, err := root.ProcessInput(context.Background(), "settle-root-sentinel", nil); err != nil {
		t.Fatal(err)
	}
}

func TestRetirementDelegateRealIdleSource(t *testing.T) {
	root, tree, c := newRetirementDelegateController(t)
	defer root.Close()
	result := retirementIdleDelegate(t, root)
	claim, state, err := c.TryClaim(true)
	if err != nil || claim == nil {
		t.Fatalf("real idle delegate %s not eligible: %+v %v", result.DelegateID, state, err)
	}
	defer c.Abort(claim, "")
	tree.mu.Lock()
	fence := tree.retirementClaim
	tree.mu.Unlock()
	if fence != claim {
		t.Fatal("tree fence is not exact outer claim")
	}
}

func TestRetirementDelegateIdleEntrypointsClaimFirst(t *testing.T) {
	for name, enter := range map[string]func(*Session, *delegateTreeController, delegateResult) error{
		"attention open": func(_ *Session, tree *delegateTreeController, d delegateResult) error {
			_, err := tree.openDelegateAttention(d.DelegateID, "original-attention-sentinel")
			return err
		},
		"idle attach": func(root *Session, tree *delegateTreeController, d delegateResult) error {
			return tree.AttachIdleRuntime(d.DelegateID, root.subagents.get(d.ChildSessionID).sess)
		},
		"reconcile": func(_ *Session, tree *delegateTreeController, _ delegateResult) error {
			plans, err := tree.Reconcile(emptyDelegateReconcileEvidence(tree))
			if plans.retirementRelease != nil {
				defer plans.retirementRelease()
			}
			return err
		},
		"steering": func(root *Session, tree *delegateTreeController, d delegateResult) error {
			_, err := tree.BeginSteerPersistence(rootDelegateActor(root.ID()), d.DelegateID)
			return err
		},
		"attention reservation": func(root *Session, tree *delegateTreeController, d delegateResult) error {
			r, err := tree.ReserveAttention(root.subagents.get(d.ChildSessionID).sess, "attention-sentinel")
			if r != nil {
				defer tree.AbortStart(r)
			}
			return err
		},
		"send": func(root *Session, _ *delegateTreeController, d delegateResult) error {
			return (delegateRuntime{owner: root}).send(context.Background(), d.DelegateID, "send-sentinel", 0).result.Err
		},
		"cold reconstruct": func(root *Session, _ *delegateTreeController, d delegateResult) error {
			_, _, err := root.restoreColdDelegateAttentionRuntime(d.DelegateID)
			return err
		},
		"reserve start": func(root *Session, tree *delegateTreeController, d delegateResult) error {
			r, err := tree.ReserveStart(rootDelegateActor(root.ID()), d.DelegateID)
			if r != nil {
				defer tree.AbortStart(r)
			}
			return err
		},
		"stop": func(root *Session, tree *delegateTreeController, d delegateResult) error {
			_, _, _, err := tree.StopSubtree(rootDelegateActor(root.ID()), d.DelegateID)
			return err
		},
		"stop driver": func(root *Session, tree *delegateTreeController, d delegateResult) error {
			_, _, _, err := tree.StopSubtreeAndDrive(rootDelegateActor(root.ID()), d.DelegateID)
			return err
		},
		"close resumability": func(root *Session, tree *delegateTreeController, d delegateResult) error {
			_, err := tree.CloseResumability(rootDelegateActor(root.ID()), d.DelegateID, "test-stop")
			return err
		},
		"capacity reclaim": func(_ *Session, tree *delegateTreeController, _ delegateResult) error {
			claim, err := tree.ClaimRuntimeReclamation(1)
			if claim != nil {
				defer tree.AbortRuntimeReclamation(claim)
			}
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			root, tree, c := newRetirementDelegateController(t)
			defer root.Close()
			d := retirementIdleDelegate(t, root)
			before, err := tree.store.Load()
			if err != nil {
				t.Fatal(err)
			}
			claim, state, err := c.TryClaim(true)
			if err != nil || claim == nil {
				t.Fatalf("claim: %+v %v", state, err)
			}
			defer c.Abort(claim, "")
			if err := enter(root, tree, d); !errors.Is(err, ErrRetirementUnavailable) {
				t.Errorf("entry after claim = %v, want retirement refusal", err)
			}
			after, err := tree.store.Load()
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Errorf("refused entry changed original durable records: %v", err)
			}
		})
	}
}

func TestRetirementColdDelegateEvidence(t *testing.T) {
	for _, fault := range []string{"healthy", "descriptor", "wrong transcript ref", "transcript", "corrupt transcript", "recovery", "reservation", "open run", "shell work"} {
		t.Run(fault, func(t *testing.T) {
			root, tree, c := newRetirementDelegateController(t)
			defer root.Close()
			d := retirementIdleDelegate(t, root)
			// Use the production capacity reclaimer, including actual child teardown.
			if err := root.reclaimDelegateRuntimeCapacity(tree.maxRetainedTerminal); err != nil {
				t.Fatal(err)
			}
			if tree.residentDelegateRuntime(d.DelegateID) != nil || root.subagents.get(d.ChildSessionID) != nil {
				t.Fatal("production reclamation left a resident runtime")
			}
			claim, state, err := c.TryClaim(true)
			if err != nil || claim == nil {
				t.Fatalf("healthy cold baseline: %+v %v", state, err)
			}
			if err := c.Abort(claim, ""); err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "healthy":
				return
			case "descriptor", "wrong transcript ref":
				// Predicate-only corruption: not an admission-race stand-in.
				tree.mu.Lock()
				before := tree.durable[d.DelegateID].Descriptor
				tree.durable[d.DelegateID].Descriptor.TranscriptRef = ""
				if fault == "wrong transcript ref" {
					tree.durable[d.DelegateID].Descriptor.TranscriptRef = "local:" + root.ID()
				}
				tree.mu.Unlock()
				defer func() { tree.mu.Lock(); tree.durable[d.DelegateID].Descriptor = before; tree.mu.Unlock() }()
			case "transcript":
				path := filepath.Join(root.stateDir, sessionsSubdir, d.ChildSessionID+".transcript.jsonl")
				if err := os.Rename(path, path+".held"); err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := os.Rename(path+".held", path); err != nil {
						t.Error(err)
					}
				}()
			case "corrupt transcript":
				path := filepath.Join(root.stateDir, sessionsSubdir, d.ChildSessionID+".transcript.jsonl")
				original, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("invalid-json\n"), 0600); err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := os.WriteFile(path, original, 0600); err != nil {
						t.Error(err)
					}
				}()
			case "recovery":
				tree.mu.Lock()
				tree.live[d.DelegateID].recoveryRequired = true
				tree.mu.Unlock()
				defer func() { tree.mu.Lock(); tree.live[d.DelegateID].recoveryRequired = false; tree.mu.Unlock() }()
			case "reservation":
				r, err := tree.ReserveStart(rootDelegateActor(root.ID()), d.DelegateID)
				if err != nil {
					t.Fatal(err)
				}
				defer tree.AbortStart(r)
			case "open run", "shell work":
				r, err := tree.ReserveStart(rootDelegateActor(root.ID()), d.DelegateID)
				if err != nil {
					t.Fatal(err)
				}
				started, err := tree.CommitStart(r)
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if _, err := tree.FailCommittedRestart(started.lease, delegatePermanentStartFailure(errors.New("fixture cancellation"), "construction_failed")); err != nil {
						t.Error(err)
					}
				}()
				if fault == "shell work" {
					token, err := tree.BeginShellWork(started.lease)
					// admitLeaseLocked requires a ready live binding: an actually
					// cold committed run cannot start process work yet.
					if !errors.Is(err, errDelegateTargetBusy) || token != (delegateWorkToken{}) {
						t.Fatalf("cold unready shell admission: %+v %v", token, err)
					}
				}
			}
			claim, state, err = c.TryClaim(true)
			if err != nil || claim != nil || !slices.ContainsFunc(state.Blockers, func(b RetirementBlocker) bool { return b.Category == "delegate" }) {
				t.Fatalf("cold %s concealed obligation: %+v %v", fault, state, err)
			}
		})
	}
}

func TestRetirementDelegateReclaimOwnsExactRuntime(t *testing.T) {
	root, tree, c := newRetirementDelegateController(t)
	defer root.Close()
	d := retirementIdleDelegate(t, root)
	original := tree.residentDelegateRuntime(d.DelegateID)
	reclaim, err := tree.ClaimRuntimeReclamation(tree.maxRetainedTerminal)
	if err != nil || reclaim == nil {
		t.Fatalf("reclaim: %v %v", reclaim, err)
	}
	if len(reclaim.entries) != 1 || reclaim.entries[0].runtime != original {
		t.Fatal("reclamation changed exact runtime identity")
	}
	claim, state, err := c.TryClaim(true)
	if err != nil || claim != nil {
		t.Fatalf("reclaim admitted retirement: %+v %v", state, err)
	}
	if err := tree.AbortRuntimeReclamation(reclaim); err != nil {
		t.Fatal(err)
	}
	if tree.residentDelegateRuntime(d.DelegateID) != original {
		t.Fatal("abort changed resident pointer")
	}
	claim, state, err = c.TryClaim(true)
	if err != nil || claim == nil {
		t.Fatalf("settled reclaim blocked: %+v %v", state, err)
	}
	defer c.Abort(claim, "")
	_, exact, err := tree.retirementEvidence()
	if err != nil || len(exact) != 1 || exact[0] != original {
		t.Fatalf("evidence lost exact pointer: %v %v", exact, err)
	}
}

// A FIFO pauses the real cold metadata read after outer admission has closed.
// No controller phase, scheduler or claim is forged by this barrier.
func retirementPauseColdClaim(t *testing.T, c *RetirementController, path string) func() {
	t.Helper()
	if _, err := exec.LookPath("mkfifo"); err != nil {
		t.Skip("cold filesystem barrier requires mkfifo")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".original"); err != nil {
		t.Fatal(err)
	}
	restore := func() {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			t.Error(err)
		}
		if err := os.Rename(path+".original", path); err != nil {
			t.Error(err)
		}
	}
	if output, err := exec.Command("mkfifo", path).CombinedOutput(); err != nil {
		restore()
		t.Fatalf("mkfifo: %s %v", output, err)
	}
	type result struct {
		claim *RetirementClaim
		err   error
	}
	done := make(chan result, 1)
	go func() { claim, _, err := c.TryClaim(true); done <- result{claim, err} }()
	type opened struct {
		f   *os.File
		err error
	}
	writer := make(chan opened, 1)
	go func() { f, err := os.OpenFile(path, os.O_WRONLY, 0600); writer <- opened{f, err} }()
	var out opened
	// TRIPWIRE: FIFO open pairs with the actual read; timeout only bounds a
	// broken entry path, never decides which contender won the race.
	select {
	case out = <-writer:
	case <-time.After(10 * time.Second): // TRIPWIRE: fixture rendezvous normally takes milliseconds; this only bounds a deadlock.
		guard, _ := os.OpenFile(path, os.O_RDWR, 0600)
		out = <-writer
		if out.f != nil {
			out.f.Close()
		}
		if guard != nil {
			guard.Close()
		}
		restore()
		t.Fatal("cold claim did not reach metadata read")
	}
	if out.err != nil {
		restore()
		t.Fatal(out.err)
	}
	finished := false
	finish := func() {
		if finished {
			return
		}
		finished = true
		if _, err := out.f.Write(data); err != nil {
			t.Error(err)
		}
		if err := out.f.Close(); err != nil {
			t.Error(err)
		}
		select {
		case got := <-done:
			if got.claim != nil {
				c.Abort(got.claim, "")
				t.Error("populated source admitted retirement")
			}
			if got.err != nil && !errors.Is(got.err, errDelegateTargetBusy) {
				t.Error(got.err)
			}
		case <-time.After(10 * time.Second): // TRIPWIRE: fixture rendezvous normally takes milliseconds; this only bounds a deadlock.
			t.Error("cold claim did not finish")
		}
		restore()
	}
	t.Cleanup(finish)
	return finish
}

func TestRetirementDelegatePopulatedSourceRefusal(t *testing.T) {
	for _, family := range []string{"outcome", "watch"} {
		t.Run(family, func(t *testing.T) {
			root, tree, c := newRetirementDelegateController(t)
			defer root.Close()
			d := retirementIdleDelegate(t, root)
			if err := root.reclaimDelegateRuntimeCapacity(tree.maxRetainedTerminal); err != nil {
				t.Fatal(err)
			}
			var plans delegateMutationPlans
			var watch *delegateWatchReceipt
			if family == "outcome" {
				r, err := tree.ReserveStart(rootDelegateActor(root.ID()), d.DelegateID)
				if err != nil {
					t.Fatal(err)
				}
				started, err := tree.CommitStart(r)
				if err != nil {
					t.Fatal(err)
				}
				plans, err = tree.FailCommittedRestart(started.lease, delegatePermanentStartFailure(errors.New("original-outcome-sentinel"), "construction_failed"))
				if err != nil || len(plans.deliveries) != 1 {
					t.Fatalf("create pending outcome: %v %v", plans, err)
				}
			} else {
				var err error
				tree.mu.Lock()
				generation := tree.durable[d.DelegateID].Generation
				tree.mu.Unlock()
				watch, err = tree.BeginWatchEnqueue(d.DelegateID, generation, "", "original-watch-sentinel", 71, true)
				if err != nil {
					t.Fatal(err)
				}
			}
			before, err := tree.store.Load()
			if err != nil {
				t.Fatal(err)
			}
			finish := retirementPauseColdClaim(t, c, filepath.Join(root.stateDir, sessionsSubdir, d.ChildSessionID+".meta.json"))
			defer finish()
			if family == "outcome" {
				plan := plans.deliveries[0]
				token, admitted, err := tree.BeginDelivery(plan)
				if !errors.Is(err, ErrRetirementUnavailable) || admitted || token != (delegateDeliveryToken{}) {
					t.Fatalf("refused source admission: %+v %v %v", token, admitted, err)
				}
				tree.mu.Lock()
				original := tree.deliveryClaims[plan.deliveryID]
				preserved := original != nil && original.token == plan.claim && original.delegateID == d.DelegateID
				tree.mu.Unlock()
				if !preserved {
					t.Fatal("refusal consumed original outcome claim identity")
				}
			} else {
				_, err := tree.AcquireWatchDelivery(watch.sourceDelegateID, watch.sourceGeneration, watch.receiverDelegateID, watch.deliveryID, watch.updateSeq, true)
				if !errors.Is(err, ErrRetirementUnavailable) {
					t.Fatalf("watch refusal: %v", err)
				}
				tree.mu.Lock()
				preserved := tree.watchEnqueues[watch.token] == watch && len(tree.watchDeliveries) == 0
				tree.mu.Unlock()
				if !preserved {
					t.Fatal("refusal consumed original watch receipt")
				}
			}
			after, err := tree.store.Load()
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("refusal changed original durable source/generation: %v", err)
			}
			finish()
			if family == "outcome" {
				if err := root.executeDelegateMutationPlans(plans); err != nil {
					t.Fatal(err)
				}
				settled, err := tree.store.Load()
				if err != nil || !slices.ContainsFunc(settled, func(event delegatestore.Event) bool {
					return event.DelegateID == d.DelegateID && event.DeliveryAcknowledged != nil && event.DeliveryAcknowledged.DeliveryID == plans.deliveries[0].deliveryID
				}) {
					t.Fatalf("outcome settlement failed original delivery ack: %v", err)
				}
				if _, err := root.ProcessInput(context.Background(), "consume-original-outcome", nil); err != nil {
					t.Fatal(err)
				}
			} else {
				delivery, err := tree.CompleteWatchEnqueue(watch)
				if err != nil {
					t.Fatal(err)
				}
				if delivery.sourceGeneration != watch.sourceGeneration || delivery.deliveryID != watch.deliveryID || delivery.updateSeq != watch.updateSeq {
					t.Fatal("watch settlement lost original source identity")
				}
				if err := tree.CompleteWatchDelivery(delivery); err != nil {
					t.Fatal(err)
				}
			}
			claim, state, err := c.TryClaim(true)
			if err != nil || claim == nil {
				t.Fatalf("settled source blocks eligibility: %+v %v", state, err)
			}
			defer c.Abort(claim, "")
		})
	}
}

type retirementModelBarrier struct {
	retirementDelegateAdapter
	once    sync.Once
	entered chan struct{}
	resume  chan struct{}
}

func (a *retirementModelBarrier) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	a.once.Do(func() { close(a.entered); <-a.resume })
	return a.retirementDelegateAdapter.Complete(ctx, req)
}

func retirementAwait(t *testing.T, ready <-chan struct{}) {
	t.Helper()
	// TRIPWIRE: model/file rendezvous decides ordering, never elapsed time.
	select {
	case <-ready:
	case <-time.After(10 * time.Second): // TRIPWIRE: fixture rendezvous normally takes milliseconds; this only bounds a deadlock.
		t.Fatal("external dependency barrier was not reached")
	}
}

func TestRetirementDelegateCreateBeforeReservation(t *testing.T) {
	retirementCreateListingBarrier(t, true)
}

func TestRetirementDelegateCreateAfterReservationBeforeInstall(t *testing.T) {
	retirementCreateListingBarrier(t, false)
}

func retirementCreateListingBarrier(t *testing.T, beforeReservation bool) {
	t.Helper()
	root, tree, c := newRetirementDelegateController(t)
	defer root.Close()
	entered, resume := make(chan struct{}), make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(resume) })
	model := root.currentProfile().Model()
	if beforeReservation {
		// A different catalog model lists during selection; the same model takes
		// the selection fast path and first lists during child initialization.
		model = "gpt-4.1-nano"
	}
	var listing sync.Once
	adapter := &retirementDelegateAdapter{fakeAdapter: fakeAdapter{name: "openai", liveModels: func(context.Context) ([]registry.Model, error) {
		listing.Do(func() { close(entered); <-resume })
		return listedModels(model), nil
	}}}
	root.client = registryClient(t, map[string]registry.Provider{
		"openai": {Base: "openai", APIKey: "fixture-key", Models: modelRows(root.currentProfile().Model(), "gpt-4.1-nano")},
	}, adapter)
	if _, reason := resolvePluginAgentRef(root.client.Registry(), root.currentProfile(), model); reason != "" {
		t.Fatalf("barrier model is not selectable: %s", reason)
	}
	root.pluginAgents["retirement-list"] = plugin.Agent{Model: model}
	result := make(chan delegateResult, 1)
	go func() {
		result <- root.createDelegate(context.Background(), delegateArgs{Task: "before-reservation-sentinel", AgentType: "retirement-list"})
	}()
	retirementAwait(t, entered)
	tree.mu.Lock()
	unpublished := len(tree.durable) == 0 && len(tree.reservations) == 0
	committedWithoutRuntime := len(tree.durable) == 1
	for _, live := range tree.live {
		committedWithoutRuntime = committedWithoutRuntime && live.binding != nil && live.runtime == nil
	}
	tree.mu.Unlock()
	if beforeReservation && !unpublished {
		t.Fatal("model-list barrier was not before reservation")
	}
	if !beforeReservation && !committedWithoutRuntime {
		t.Fatal("model-list barrier was not after committed reservation before runtime install")
	}
	claim, state, err := c.TryClaim(true)
	if err != nil || claim != nil {
		t.Fatalf("pre-reservation create admitted retirement: %+v %v", state, err)
	}
	release.Do(func() { close(resume) })
	var d delegateResult
	select {
	case d = <-result:
	case <-time.After(10 * time.Second): // TRIPWIRE: fixture rendezvous normally takes milliseconds; this only bounds a deadlock.
		t.Fatal("create did not leave model-list barrier")
	}
	if d.Err != nil {
		t.Fatal(d.Err)
	}
	retirementSettleDelegate(t, root, d)
	tree.mu.Lock()
	count := len(tree.durable)
	original := tree.durable[d.DelegateID]
	tree.mu.Unlock()
	if count != 1 || original == nil || original.Descriptor.ChildSessionID != d.ChildSessionID {
		t.Fatal("create duplicated or replaced child identity")
	}
	claim, state, err = c.TryClaim(true)
	if err != nil || claim == nil {
		t.Fatalf("settled create blocks: %+v %v", state, err)
	}
	defer c.Abort(claim, "")
}

func TestRetirementDelegateDescendantSendAdmittedFirst(t *testing.T) {
	root, tree, c := newRetirementDelegateController(t)
	defer root.Close()
	d := retirementIdleDelegate(t, root)
	original := tree.residentDelegateRuntime(d.DelegateID)
	tree.mu.Lock()
	generation := tree.durable[d.DelegateID].Generation
	tree.mu.Unlock()
	adapter := &retirementModelBarrier{retirementDelegateAdapter: retirementDelegateAdapter{fakeAdapter: fakeAdapter{name: "openai"}}, entered: make(chan struct{}), resume: make(chan struct{})}
	var release sync.Once
	defer release.Do(func() { close(adapter.resume) })
	root.client.Register(adapter)
	result := (delegateRuntime{owner: root}).send(context.Background(), d.DelegateID, "descendant-send-sentinel", 0).result
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	retirementAwait(t, adapter.entered)
	transcriptPath := filepath.Join(root.stateDir, sessionsSubdir, d.ChildSessionID+".transcript.jsonl")
	originalInput, err := os.ReadFile(transcriptPath)
	if err != nil || bytes.Count(originalInput, []byte("descendant-send-sentinel")) != 1 {
		t.Fatalf("original send input was not persisted exactly once: %v", err)
	}
	claim, state, err := c.TryClaim(true)
	if err != nil || claim != nil {
		t.Fatalf("descendant-only send admitted retirement: %+v %v", state, err)
	}
	preservedInput, err := os.ReadFile(transcriptPath)
	if err != nil || !bytes.Equal(originalInput, preservedInput) {
		t.Fatalf("retirement attempt changed original populated send transcript: %v", err)
	}
	if tree.residentDelegateRuntime(d.DelegateID) != original || original.retirementController.Load() != c {
		t.Fatal("send replaced runtime or lost inherited controller")
	}
	tree.mu.Lock()
	current := tree.durable[d.DelegateID].Generation
	count := len(tree.durable)
	tree.mu.Unlock()
	if current != generation+1 || count != 1 {
		t.Fatalf("send generation %d (previous %d), members %d", current, generation, count)
	}
	work, err := tree.BeginShellWork(delegateLease{delegateID: d.DelegateID, generation: current})
	if err != nil {
		t.Fatal(err)
	}
	claim, state, err = c.TryClaim(true)
	if err != nil || claim != nil {
		t.Fatalf("owned shell work admitted retirement: %+v %v", state, err)
	}
	if err := tree.AbortShellWork(work); err != nil {
		t.Fatal(err)
	}
	release.Do(func() { close(adapter.resume) })
	retirementSettleDelegate(t, root, d)
	claim, state, err = c.TryClaim(true)
	if err != nil || claim == nil {
		t.Fatalf("settled descendant send blocks: %+v %v", state, err)
	}
	defer c.Abort(claim, "")
}

func TestRetirementDelegateColdReconstructionAdmittedFirst(t *testing.T) {
	root, tree, c := newRetirementDelegateController(t)
	defer root.Close()
	d := retirementIdleDelegate(t, root)
	old := tree.residentDelegateRuntime(d.DelegateID)
	if err := root.reclaimDelegateRuntimeCapacity(tree.maxRetainedTerminal); err != nil {
		t.Fatal(err)
	}
	before, err := tree.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	entered, resume := make(chan struct{}), make(chan struct{})
	var listing, release sync.Once
	defer release.Do(func() { close(resume) })
	model := root.currentProfile().Model()
	adapter := &retirementDelegateAdapter{fakeAdapter: fakeAdapter{name: "openai", liveModels: func(context.Context) ([]registry.Model, error) {
		listing.Do(func() { close(entered); <-resume })
		return listedModels(model), nil
	}}}
	root.client = registryClient(t, map[string]registry.Provider{"openai": {Base: "openai", APIKey: "fixture-key", Models: modelRows(model)}}, adapter)
	type restored struct {
		sub *subagent
		err error
	}
	done := make(chan restored, 2)
	for range 2 {
		go func() {
			_, sub, err := root.restoreColdDelegateAttentionRuntime(d.DelegateID)
			done <- restored{sub, err}
		}()
	}
	retirementAwait(t, entered)
	root.subagents.mu.Lock()
	pending := len(root.subagents.reconstructing)
	root.subagents.mu.Unlock()
	if pending != 1 || tree.residentDelegateRuntime(d.DelegateID) != nil {
		t.Fatal("cold reconstruction did not retain one original manager claim before install")
	}
	claim, state, err := c.TryClaim(true)
	if err != nil || claim != nil {
		t.Fatalf("cold reconstruction admitted retirement: %+v %v", state, err)
	}
	release.Do(func() { close(resume) })
	var exact *Session
	for range 2 {
		select {
		case got := <-done:
			if got.err != nil || got.sub == nil || got.sub.sess == nil {
				t.Fatalf("restore: %+v", got)
			}
			if exact == nil {
				exact = got.sub.sess
			} else if exact != got.sub.sess {
				t.Fatal("concurrent restore duplicated runtime")
			}
		case <-time.After(10 * time.Second): // TRIPWIRE: fixture rendezvous normally takes milliseconds; this only bounds a deadlock.
			t.Fatal("cold reconstruction did not finish")
		}
	}
	if exact == old || exact.ID() != d.ChildSessionID || exact.retirementController.Load() != c || tree.residentDelegateRuntime(d.DelegateID) != exact {
		t.Fatal("cold reconstruction lost exact saved identity or inherited controller")
	}
	after, err := tree.store.Load()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("idle reconstruction changed original durable delegate records: %v", err)
	}
	claim, state, err = c.TryClaim(true)
	if err != nil || claim == nil {
		t.Fatalf("settled reconstruction blocks: %+v %v", state, err)
	}
	defer c.Abort(claim, "")
}

func TestRetirementDelegateEvidenceLeafFirstExactPointers(t *testing.T) {
	root, tree, _ := newRetirementDelegateController(t)
	defer root.Close()
	parent := seedDelegateReclaimRuntime(t, tree, "parent", "", time.Unix(1, 0), true, false)
	child := seedDelegateReclaimRuntime(t, tree, "child", "parent", time.Unix(1, 0), true, false)
	// These permitted predicate-only seeds are not reconstruction/race fixtures.
	_, exact, err := tree.retirementEvidence()
	if err != nil || len(exact) != 2 || exact[0] != child || exact[1] != parent {
		t.Fatalf("leaf-first exact pointers = %v, err %v", exact, err)
	}
}

func TestRetirementDelegateStaleClaimCannotClearTreeFence(t *testing.T) {
	root, tree, c := newRetirementDelegateController(t)
	defer root.Close()
	retirementIdleDelegate(t, root)
	first, _, err := c.TryClaim(true)
	if err != nil || first == nil {
		t.Fatalf("first claim: %v", err)
	}
	if err := c.Abort(first, ""); err != nil {
		t.Fatal(err)
	}
	second, _, err := c.TryClaim(true)
	if err != nil || second == nil {
		t.Fatalf("second claim: %v", err)
	}
	defer c.Abort(second, "")
	tree.clearRetirementFence(first)
	if err := c.Abort(first, ""); !errors.Is(err, ErrRetirementUnavailable) {
		t.Fatalf("stale abort: %v", err)
	}
	tree.mu.Lock()
	exact := tree.retirementClaim == second
	tree.mu.Unlock()
	if !exact {
		t.Fatal("stale claim cleared current tree fence")
	}
}

func TestRetirementDelegateStopAdmittedFirst(t *testing.T) {
	root, tree, c := newRetirementDelegateController(t)
	defer root.Close()
	d := retirementIdleDelegate(t, root)
	result, cancel, plans, err := tree.StopSubtree(rootDelegateActor(root.ID()), d.DelegateID)
	if err != nil {
		t.Fatal(err)
	}
	stop := tree.stopForResult(result)
	if stop == nil {
		t.Fatal("admitted stop has no original owner")
	}
	claim, state, err := c.TryClaim(true)
	if err != nil || claim != nil {
		t.Fatalf("pending stop admitted retirement: %+v %v", state, err)
	}
	executeDelegateCancelPlan(cancel)
	if err := root.executeDelegateMutationPlans(plans); err != nil {
		t.Fatal(err)
	}
	// TRIPWIRE: stop.done/driver evidence determines completion, not the timeout.
	ctx, finish := context.WithTimeout(context.Background(), 10*time.Second)
	defer finish()
	if err := tree.drainStop(ctx, stop, root); err != nil {
		t.Fatal(err)
	}
	events, err := tree.store.Load()
	if err != nil || !slices.ContainsFunc(events, func(event delegatestore.Event) bool { return event.SubtreeStopCompleted != nil }) {
		t.Fatalf("original stop was not durably completed: %v", err)
	}
	claim, state, err = c.TryClaim(true)
	if err != nil || claim == nil {
		t.Fatalf("settled stop blocks: %+v %v", state, err)
	}
	defer c.Abort(claim, "")
}
