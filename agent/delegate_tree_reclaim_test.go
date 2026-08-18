package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

func TestDelegateRuntimeReclaim_UsesPublicMaxRetainedTerminalDefault2048(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 4, 2)
	if got := c.maxRetainedTerminal; got != defaultMaxRetainedTerminal {
		t.Fatalf("controller max_retained_terminal = %d, want public default %d", got, defaultMaxRetainedTerminal)
	}
}

func TestDelegateRuntimeReclaim_ClaimsOnlyQuiescentTerminalSubtrees(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 8, 4)
	c.maxRetainedTerminal = 2
	eligible := seedDelegateReclaimRuntime(t, c, "dlg_eligible", "", time.Unix(10, 0).UTC(), false, false)
	blocked := seedDelegateReclaimRuntime(t, c, "dlg_blocked", "", time.Unix(5, 0).UTC(), false, false)
	seedDelegateControllerRunning(t, c, "dlg_running_child", "dlg_blocked")

	claim, err := c.ClaimRuntimeReclamation(1)
	if err != nil {
		t.Fatalf("ClaimRuntimeReclamation: %v", err)
	}
	if claim == nil || !reflect.DeepEqual(reclamationDelegateIDs(claim), []string{"dlg_eligible"}) {
		t.Fatalf("claimed runtimes = %#v, want only quiescent dlg_eligible", claim)
	}
	if got := claim.entries[0].runtime; got != eligible {
		t.Fatalf("claimed runtime = %p, want exact eligible runtime %p", got, eligible)
	}
	if got := c.live["dlg_blocked"].runtime; got != blocked {
		t.Fatalf("blocked subtree runtime changed during claim: got %p want %p", got, blocked)
	}
}

func TestDelegateRuntimeReclaim_ClosesPostorderAfterUnlock(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 8, 4)
	c.maxRetainedTerminal = 2
	parent := seedDelegateReclaimRuntime(t, c, "dlg_parent", "", time.Unix(10, 0).UTC(), false, false)
	child := seedDelegateReclaimRuntime(t, c, "dlg_child", "dlg_parent", time.Unix(20, 0).UTC(), false, false)
	root := &Session{delegateController: c}
	c.rootRuntime = root
	byRuntime := map[*Session]string{parent: "dlg_parent", child: "dlg_child"}
	var closed []string
	root.cfg.testOnly.delegateRuntimeReclaimClose = func(runtime *Session) {
		if !c.mu.TryLock() {
			t.Fatal("runtime close ran while the delegate controller mutex was held")
		}
		c.mu.Unlock()
		closed = append(closed, byRuntime[runtime])
	}

	if err := root.reclaimDelegateRuntimeCapacity(1); err != nil {
		t.Fatalf("reclaimDelegateRuntimeCapacity: %v", err)
	}
	if !reflect.DeepEqual(closed, []string{"dlg_child", "dlg_parent"}) {
		t.Fatalf("close order = %v, want postorder [dlg_child dlg_parent]", closed)
	}
}

func TestDelegateRuntimeReclaim_ClearsOnlyExactResidentPointers(t *testing.T) {
	t.Run("exact pointer clears", func(t *testing.T) {
		c, _ := newDelegateControllerTestHarness(t, 4, 2)
		c.maxRetainedTerminal = 1
		runtime := seedDelegateReclaimRuntime(t, c, "dlg_target", "", time.Unix(10, 0).UTC(), false, false)
		claim, err := c.ClaimRuntimeReclamation(1)
		if err != nil {
			t.Fatalf("ClaimRuntimeReclamation: %v", err)
		}
		if err := c.CompleteRuntimeReclamation(claim, map[string]*Session{"dlg_target": runtime}); err != nil {
			t.Fatalf("CompleteRuntimeReclamation: %v", err)
		}
		c.mu.Lock()
		got := c.live["dlg_target"].runtime
		c.mu.Unlock()
		if got != nil {
			t.Fatalf("completion retained exact closed runtime %p", got)
		}
	})

	t.Run("replacement pointer survives", func(t *testing.T) {
		c, _ := newDelegateControllerTestHarness(t, 4, 2)
		c.maxRetainedTerminal = 1
		oldRuntime := seedDelegateReclaimRuntime(t, c, "dlg_target", "", time.Unix(10, 0).UTC(), false, false)
		claim, err := c.ClaimRuntimeReclamation(1)
		if err != nil {
			t.Fatalf("ClaimRuntimeReclamation: %v", err)
		}
		replacement := &Session{id: "replacement"}
		c.mu.Lock()
		c.live["dlg_target"].runtime = replacement
		c.mu.Unlock()
		if err := c.CompleteRuntimeReclamation(claim, map[string]*Session{"dlg_target": oldRuntime}); err != nil {
			t.Fatalf("CompleteRuntimeReclamation: %v", err)
		}
		c.mu.Lock()
		got := c.live["dlg_target"].runtime
		c.mu.Unlock()
		if got != replacement {
			t.Fatalf("completion cleared replacement runtime: got %p want %p", got, replacement)
		}
	})
}

func TestDelegateRuntimeReclaim_PrefersClosedThenAcknowledgedThenOldestThenID(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 16, 4)
	c.maxRetainedTerminal = 5
	seedDelegateReclaimRuntime(t, c, "dlg_unacked_c", "", time.Unix(30, 0).UTC(), false, false)
	seedDelegateReclaimRuntime(t, c, "dlg_acknowledged", "", time.Unix(40, 0).UTC(), true, false)
	seedDelegateReclaimRuntime(t, c, "dlg_unacked_b", "", time.Unix(20, 0).UTC(), false, false)
	seedDelegateReclaimRuntime(t, c, "dlg_closed", "", time.Unix(50, 0).UTC(), false, true)
	seedDelegateReclaimRuntime(t, c, "dlg_unacked_a", "", time.Unix(20, 0).UTC(), false, false)

	claim, err := c.ClaimRuntimeReclamation(5)
	if err != nil {
		t.Fatalf("ClaimRuntimeReclamation: %v", err)
	}
	want := []string{"dlg_closed", "dlg_acknowledged", "dlg_unacked_a", "dlg_unacked_b", "dlg_unacked_c"}
	if got := reclamationRootIDs(claim); !reflect.DeepEqual(got, want) {
		t.Fatalf("reclamation preference = %v, want %v", got, want)
	}
}

func TestDelegateRuntimeReclaim_InsufficientCapacityFailsBeforeIDMintOrConstruction(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 8, 4)
	c.maxRetainedTerminal = 1
	seedDelegateReclaimRuntime(t, c, "dlg_blocked", "", time.Unix(5, 0).UTC(), false, false)
	seedDelegateControllerRunning(t, c, "dlg_running_child", "dlg_blocked")
	minted := 0
	c.newDelegateID = func() string {
		minted++
		return "dlg_unexpected"
	}
	root := &Session{delegateController: c}
	c.rootRuntime = root
	constructed := 0
	root.cfg.testOnly.delegateRuntimeReclaimClose = func(*Session) { constructed++ }

	if err := root.reclaimDelegateRuntimeCapacity(1); err == nil || !strings.Contains(err.Error(), "retained delegate limit reached") {
		t.Fatalf("reclamation error = %v, want retained-limit refusal", err)
	}
	if minted != 0 || constructed != 0 {
		t.Fatalf("refused admission minted %d IDs and closed/constructed %d runtimes, want zero side effects", minted, constructed)
	}
}

func TestDelegateRuntimeReclaim_CreateAndColdRestoreTriggerReclamation(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		root, _, _ := newDelegateResourceBootstrapSession(t)
		root.delegateController.maxRetainedTerminal = 1
		resident := seedDelegateReclaimRuntime(t, root.delegateController, "dlg_old", "", time.Unix(5, 0).UTC(), false, false)
		var closed []*Session
		root.cfg.testOnly.delegateRuntimeReclaimClose = func(runtime *Session) { closed = append(closed, runtime) }
		wantErr := errors.New("stop after reclamation before child construction")
		root.cfg.testOnly.subagentPrepareFault = func(point string) error {
			if point == "new_session" {
				return wantErr
			}
			return nil
		}

		result := root.createDelegate(context.Background(), delegateArgs{Task: "admission reclaims first"})
		if !errors.Is(result.Err, wantErr) {
			t.Fatalf("createDelegate error = %v, want post-reclamation construction fault", result.Err)
		}
		if !reflect.DeepEqual(closed, []*Session{resident}) {
			t.Fatalf("create reclamation closed = %v, want exact old runtime %p", closed, resident)
		}
	})

	t.Run("cold_restore", func(t *testing.T) {
		root, _, _ := newDelegateResourceBootstrapSession(t)
		root.delegateController.maxRetainedTerminal = 1
		resident := seedDelegateReclaimRuntime(t, root.delegateController, "dlg_old", "", time.Unix(5, 0).UTC(), false, false)
		root.delegateController.mu.Lock()
		target := delegateControllerCreatedEvent("dlg_restore", "")
		target.Created.Descriptor.OwnerSessionID = root.ID()
		_, err := root.delegateController.appendLocked(target)
		root.delegateController.mu.Unlock()
		if err != nil {
			t.Fatalf("seed restore target: %v", err)
		}
		reservation, err := root.delegateController.ReserveStart(rootDelegateActor(root.ID()), "dlg_restore")
		if err != nil {
			t.Fatalf("ReserveStart: %v", err)
		}
		started, err := root.delegateController.CommitStart(reservation)
		if err != nil {
			t.Fatalf("CommitStart: %v", err)
		}
		var closed []*Session
		root.cfg.testOnly.delegateRuntimeReclaimClose = func(runtime *Session) { closed = append(closed, runtime) }
		if _, _, err := (delegateRuntime{owner: root}).restoreIdle(started); err == nil {
			t.Fatal("restoreIdle unexpectedly found missing committed session metadata")
		}
		if !reflect.DeepEqual(closed, []*Session{resident}) {
			t.Fatalf("cold restore reclamation closed = %v, want exact old runtime %p", closed, resident)
		}
		_, _ = root.delegateController.FinishGeneration(started.lease, delegateFinish{outcome: delegatestore.OutcomeFailed, reason: "test_cleanup"})
	})
}

func TestDelegateRuntimeReclaim_NoTimerUnloadEventOrStableDataDeletion(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 4, 2)
	c.maxRetainedTerminal = 1
	runtime := seedDelegateReclaimRuntime(t, c, "dlg_target", "", time.Unix(10, 0).UTC(), false, false)
	before := readDelegateControllerFile(t, path)
	beforeAggregate := cloneDelegateControllerState(t, c.durable)["dlg_target"]

	for range 3 {
		if got := c.Snapshot().rows[0].id; got != "dlg_target" {
			t.Fatalf("idle observation changed stable identity to %q", got)
		}
	}
	c.mu.Lock()
	stillResident := c.live["dlg_target"].runtime
	c.mu.Unlock()
	if stillResident != runtime {
		t.Fatalf("idle observation reclaimed runtime without admission: got %p want %p", stillResident, runtime)
	}
	claim, err := c.ClaimRuntimeReclamation(1)
	if err != nil {
		t.Fatalf("ClaimRuntimeReclamation: %v", err)
	}
	if err := c.CompleteRuntimeReclamation(claim, map[string]*Session{"dlg_target": runtime}); err != nil {
		t.Fatalf("CompleteRuntimeReclamation: %v", err)
	}
	after := readDelegateControllerFile(t, path)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("process-only reclamation appended a durable event:\n before %q\n after  %q", before, after)
	}
	afterAggregate := c.durable["dlg_target"]
	if !reflect.DeepEqual(beforeAggregate, afterAggregate) {
		t.Fatalf("reclamation changed stable aggregate:\n before %#v\n after  %#v", beforeAggregate, afterAggregate)
	}
}

func seedDelegateReclaimRuntime(t *testing.T, c *delegateTreeController, id, parentID string, endedAt time.Time, acknowledged, closed bool) *Session {
	t.Helper()
	originalNow := c.now
	c.now = func() time.Time { return endedAt }
	t.Cleanup(func() { c.now = originalNow })
	seedDelegateControllerRunning(t, c, id, parentID)
	runtime := &Session{id: "child-" + id}
	c.mu.Lock()
	live := c.live[id]
	live.runtime = runtime
	live.binding.runtime = runtime
	c.mu.Unlock()
	plans, err := c.FinishGeneration(delegateLease{delegateID: id, generation: 1}, delegateFinish{
		outcome: delegatestore.OutcomeCompleted,
		reason:  "completed",
		endedAt: endedAt,
	})
	if err != nil {
		t.Fatalf("FinishGeneration(%s): %v", id, err)
	}
	for _, plan := range plans.deliveries {
		token, admitted, err := c.BeginDelivery(plan)
		if err != nil || !admitted {
			t.Fatalf("BeginDelivery(%s): admitted=%v err=%v", id, admitted, err)
		}
		if _, err := c.CompleteDelivery(token, false); err != nil {
			t.Fatalf("release delivery claim for %s: %v", id, err)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if acknowledged {
		aggregate := c.durable[id]
		if aggregate == nil || len(aggregate.PendingDeliveries) != 1 {
			t.Fatalf("seed %s pending deliveries = %#v, want one", id, aggregate)
		}
		if _, err := c.appendLocked(delegatestore.Event{
			Kind:       delegatestore.EventDelegateDeliveryAcknowledged,
			DelegateID: id,
			DeliveryAcknowledged: &delegatestore.DeliveryAcknowledged{
				DeliveryID: aggregate.PendingDeliveries[0].DeliveryID,
			},
		}); err != nil {
			t.Fatalf("acknowledge %s: %v", id, err)
		}
	}
	if closed {
		if _, err := c.appendLocked(delegatestore.Event{
			Kind:               delegatestore.EventDelegateResumabilityClosed,
			DelegateID:         id,
			ResumabilityClosed: &delegatestore.ResumabilityClosed{Reason: "test_closed"},
		}); err != nil {
			t.Fatalf("close resumability %s: %v", id, err)
		}
	}
	return runtime
}

func reclamationDelegateIDs(claim *delegateRuntimeReclamationClaim) []string {
	ids := make([]string, 0, len(claim.entries))
	for _, entry := range claim.entries {
		ids = append(ids, entry.delegateID)
	}
	return ids
}

func reclamationRootIDs(claim *delegateRuntimeReclamationClaim) []string {
	ids := make([]string, 0, len(claim.roots))
	for _, root := range claim.roots {
		ids = append(ids, root.delegateID)
	}
	return ids
}

// TestDelegateRuntimeReclaim_CarriedSteerDoesNotPinASettledSubtree pins the
// difference between the two kinds of pending steering admission.
//
// An ordinary admission means a live generation still owes work, and a subtree
// holding one is not quiescent. An admission carried across a covering stop
// means something quite different: its generation is over, and it is a parcel
// held for a successor that in the normal case never runs -- stopping a delegate
// is usually terminal, and once resumability is closed a successor is
// impossible. Treating that parcel as pending work pins the runtime, and
// because claimableRuntimeSubtreeLocked bails for the whole subtree on one
// member, it pins every sibling too. Each stopped-and-forgotten delegate would
// then burn a maxRetainedTerminal slot for the life of the process.
func TestDelegateRuntimeReclaim_CarriedSteerDoesNotPinASettledSubtree(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 8, 4)
	c.maxRetainedTerminal = 1
	settled := seedDelegateReclaimRuntime(t, c, "dlg_settled", "", time.Unix(10, 0).UTC(), false, false)

	// Exactly what CompleteSteerPersistence records on the stop-fenced path.
	c.mu.Lock()
	c.live["dlg_settled"].pendingSteers = []delegateSteeringAdmission{{
		entryID:                 "ent_carried",
		carriesAcrossGeneration: true,
	}}
	c.mu.Unlock()

	claim, err := c.ClaimRuntimeReclamation(1)
	if err != nil {
		t.Fatalf("ClaimRuntimeReclamation with a carried steer held: %v", err)
	}
	if claim == nil || !reflect.DeepEqual(reclamationDelegateIDs(claim), []string{"dlg_settled"}) {
		t.Fatalf("claimed runtimes = %#v, want dlg_settled reclaimable despite the carried admission", claim)
	}
	if got := claim.entries[0].runtime; got != settled {
		t.Fatalf("claimed runtime = %p, want the settled runtime %p", got, settled)
	}
}

// An ORDINARY pending admission still pins its subtree: that one is a live
// generation's unfinished work, and reclaiming its runtime would drop it.
func TestDelegateRuntimeReclaim_OrdinaryPendingSteerStillPinsTheSubtree(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 8, 4)
	c.maxRetainedTerminal = 1
	seedDelegateReclaimRuntime(t, c, "dlg_busy", "", time.Unix(10, 0).UTC(), false, false)

	c.mu.Lock()
	c.live["dlg_busy"].pendingSteers = []delegateSteeringAdmission{{entryID: "ent_live"}}
	c.mu.Unlock()

	if _, err := c.ClaimRuntimeReclamation(1); err == nil {
		t.Fatal("a subtree owing live steering work was reclaimed")
	}
}
