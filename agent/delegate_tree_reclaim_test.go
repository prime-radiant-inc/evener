package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/llm"
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
	runtime := &Session{id: "child-" + id}
	seedDelegateReclaimRuntimeSession(t, c, id, parentID, endedAt, acknowledged, closed, runtime)
	return runtime
}

// seedDelegateReclaimRuntimeSession is seedDelegateReclaimRuntime with a
// caller-built resident runtime, for a test whose subject is what closing that
// runtime touches.
func seedDelegateReclaimRuntimeSession(t *testing.T, c *delegateTreeController, id, parentID string, endedAt time.Time, acknowledged, closed bool, runtime *Session) {
	t.Helper()
	originalNow := c.now
	c.now = func() time.Time { return endedAt }
	t.Cleanup(func() { c.now = originalNow })
	seedDelegateControllerRunning(t, c, id, parentID)
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

// TestDelegateIdleRelease_ReleasesWholeSubtreeLeafFirst pins the depth-2
// contract of the idle release: a terminal delegate whose own child delegate
// is also terminal must release BOTH resident runtimes. The release enumerates
// the subtree itself because the parent's retirement teardown deliberately
// never drains its own children (session_lifecycle's !retirement guards), so a
// release touching only the delegate's own session would strand the
// grandchild's process-local resources — stdio MCP servers included — for the
// life of the daemon. That was the reviews' worst finding against a naive
// option B, and both assertions here fail against it: the claim entries would
// be the delegate alone, and the grandchild's teardown pass would still be
// unspent.
//
// Teardown itself is asserted through the single-pass contract: a runtime
// already released under releaseRetirement answers a second
// releaseRuntime(releaseRetirement) with errRetirementTeardownSpent.
//
// The finalize tail's scheduling hook is disabled through the testOnly seam so
// this test drives releaseIdleRuntimeAfterFinalize directly and observes the
// two-member claim deterministically instead of racing a grace timer; the
// scheduled path end to end is TestIntg_DelegateIdleReleasesStdioMCPServer.
func TestDelegateIdleRelease_ReleasesWholeSubtreeLeafFirst(t *testing.T) {
	workspace := t.TempDir()
	adapter := &fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{
		// The parent delegate's turn: spawn its own child delegate.
		func(llm.Request) llm.Response {
			return toolCallResponse(llm.ToolCallData{
				ID:        "call_spawn_child",
				Name:      "delegate",
				Arguments: json.RawMessage(`{"prompt":"grandchild sentinel task"}`),
				Type:      "function",
			})
		},
		// The grandchild's turn: finish.
		func(llm.Request) llm.Response { return finalResponse("grandchild done") },
		// The parent's follow-up turn, after the grandchild's tool result: finish.
		func(llm.Request) llm.Response { return finalResponse("parent done") },
	}}
	client := llm.NewClient()
	client.Register(adapter)
	profile := withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2"))
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(workspace), SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 2,
		NoProjectPrompts: true,
		ForceRealIO:      true,
		testOnly: testConfig{
			skipGitSnapshot:            true,
			minimalSystemPrompt:        true,
			sandboxProber:              bwrapCapableProber(workspace),
			disableDelegateIdleRelease: true,
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)

	one := 1
	parentRes := sess.createDelegate(context.Background(), delegateArgs{
		Task:                "spawn one child delegate, then finish",
		DelegationAllowance: &one,
	})
	if parentRes.Err != nil {
		t.Fatalf("createDelegate: %v (status=%s reason=%s)", parentRes.Err, parentRes.Status, parentRes.Reason)
	}
	sub := sess.subagents.get(parentRes.ChildSessionID)
	if sub == nil {
		t.Fatalf("parent delegate missing from root manager: %+v", parentRes)
	}
	sub.mu.Lock()
	done := sub.done
	sub.mu.Unlock()
	select {
	case <-done:
	case <-time.After(10 * time.Second): // TRIPWIRE: fixture rendezvous normally takes milliseconds; this only bounds a deadlock.
		t.Fatal("parent delegate runner did not finish")
	}

	tree := sess.delegateController
	tree.mu.Lock()
	var grandchildID, grandchildChildID string
	children := 0
	for id, agg := range tree.durable {
		if agg.Descriptor.ParentDelegateID == parentRes.DelegateID {
			children++
			grandchildID, grandchildChildID = id, agg.Descriptor.ChildSessionID
		}
	}
	parentLive := tree.live[parentRes.DelegateID]
	grandchildLive := tree.live[grandchildID]
	tree.mu.Unlock()
	if children != 1 {
		t.Fatalf("parent delegate has %d child delegates, want exactly 1 (fixture script derailed)", children)
	}
	if parentLive == nil || parentLive.runtime == nil || grandchildLive == nil || grandchildLive.runtime == nil {
		t.Fatal("expected both subtree runtimes resident before the release")
	}
	parentSess, grandchildSess := parentLive.runtime, grandchildLive.runtime
	if got := parentSess.subagents.get(grandchildChildID); got == nil {
		t.Fatal("grandchild record missing from the parent runtime's manager before the release")
	}

	// The claim refuses until every member is terminal-idle; poll rather than
	// assume the finalize tail has fully settled.
	claimDeadline := time.Now().Add(15 * time.Second)
	var claim *delegateRuntimeReclamationClaim
	for {
		claim, err = tree.ClaimIdleRuntimeRelease(parentRes.DelegateID)
		if err != nil {
			t.Fatalf("ClaimIdleRuntimeRelease: %v", err)
		}
		if claim != nil {
			break
		}
		if time.Now().After(claimDeadline) {
			t.Fatalf("terminal subtree of %s never became claimable", parentRes.DelegateID)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got, want := reclamationDelegateIDs(claim), []string{grandchildID, parentRes.DelegateID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("claim entries = %v, want leaf-first %v", got, want)
	}
	if err := tree.AbortRuntimeReclamation(claim); err != nil {
		t.Fatalf("AbortRuntimeReclamation: %v", err)
	}

	// The production entrypoint: the parent runtime's own finalize-tail call.
	if !parentSess.releaseIdleRuntimeAfterFinalize() {
		t.Fatal("releaseIdleRuntimeAfterFinalize refused the terminal two-member subtree")
	}

	if got := sess.subagents.get(parentRes.ChildSessionID); got != nil {
		t.Fatal("released parent's record still hooked into the root manager")
	}
	if got := parentSess.subagents.get(grandchildChildID); got != nil {
		t.Fatal("released grandchild's record still hooked into the parent runtime's manager")
	}
	tree.mu.Lock()
	parentLive = tree.live[parentRes.DelegateID]
	grandchildLive = tree.live[grandchildID]
	aggregates := []*delegatestore.Aggregate{tree.durable[parentRes.DelegateID], tree.durable[grandchildID]}
	tree.mu.Unlock()
	if parentLive == nil || parentLive.runtime != nil || grandchildLive == nil || grandchildLive.runtime != nil {
		t.Fatalf("live runtime pointers after release: parent=%v grandchild=%v", parentLive.runtime, grandchildLive.runtime)
	}
	for _, agg := range aggregates {
		if agg == nil || agg.Phase != delegatestore.PhaseIdle || agg.LatestOutcome == nil {
			t.Fatalf("released delegate lost durable identity for cold restore: %+v", agg)
		}
	}
	// Both teardown passes spent: the release tore down each member itself,
	// leaf-first — which the parent's retirement teardown alone never would.
	if err := parentSess.releaseRuntime(context.Background(), closeOptions{}, releaseRetirement); !errors.Is(err, errRetirementTeardownSpent) {
		t.Fatalf("parent runtime teardown pass after release: err = %v, want errRetirementTeardownSpent", err)
	}
	if err := grandchildSess.releaseRuntime(context.Background(), closeOptions{}, releaseRetirement); !errors.Is(err, errRetirementTeardownSpent) {
		t.Fatalf("grandchild runtime teardown pass after release: err = %v, want errRetirementTeardownSpent (a depth-2 release must tear the descendant down itself)", err)
	}
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
