package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
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

// TestDelegateRuntimeReclaim_SubtreeWithANeverRunMemberIsRefused pins the
// other half of claimableRuntimeSubtreeLocked's terminal check. A member that
// was created but has never run sits in PhaseIdle, so the phase clause alone
// would accept it; only the "no outcome yet" half of the check refuses it,
// and that refusal must still bail the whole subtree, the same as a running
// child does above.
func TestDelegateRuntimeReclaim_SubtreeWithANeverRunMemberIsRefused(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 8, 4)
	c.maxRetainedTerminal = 2
	eligible := seedDelegateReclaimRuntime(t, c, "dlg_eligible", "", time.Unix(10, 0).UTC(), false, false)
	blocked := seedDelegateReclaimRuntime(t, c, "dlg_blocked", "", time.Unix(5, 0).UTC(), false, false)
	seedDelegateControllerIdle(t, c, "dlg_never_run", "dlg_blocked")

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

func seedDelegateReclaimRuntime(t testing.TB, c *delegateTreeController, id, parentID string, endedAt time.Time, acknowledged, closed bool) *Session {
	t.Helper()
	runtime := &Session{id: "child-" + id}
	seedDelegateReclaimRuntimeSession(t, c, id, parentID, endedAt, acknowledged, closed, runtime)
	return runtime
}

// seedDelegateReclaimRuntimeSession is seedDelegateReclaimRuntime with a
// caller-built resident runtime, for a test whose subject is what closing that
// runtime touches.
func seedDelegateReclaimRuntimeSession(t testing.TB, c *delegateTreeController, id, parentID string, endedAt time.Time, acknowledged, closed bool, runtime *Session) {
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
	grandchildSub := parentSess.subagents.get(grandchildChildID)
	if grandchildSub == nil {
		t.Fatal("grandchild record missing from the parent runtime's manager before the release")
	}
	// The grandchild runs on its own runner; its result reaches the parent
	// before that runner reports done, which claimSettledIdleSubtree relies on.
	grandchildSub.mu.Lock()
	grandchildDone := grandchildSub.done
	grandchildSub.mu.Unlock()
	select {
	case <-grandchildDone:
	case <-time.After(10 * time.Second): // TRIPWIRE: fixture rendezvous normally takes milliseconds; this only bounds a deadlock.
		t.Fatal("grandchild delegate runner did not finish")
	}

	claim := claimSettledIdleSubtree(t, tree, parentRes.DelegateID, parentSess, grandchildSess)
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
		t.Fatalf("live runtime pointers after release: parent=%+v grandchild=%+v", parentLive, grandchildLive)
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

// claimSettledIdleSubtree waits until delegateID's resident subtree has
// settled and returns the idle-release claim over it, still held; the caller
// inspects it and aborts it before driving the release under test.
//
// Settled is more than claimable. A child's result delivered to a finished
// parent becomes delegate attention on the parent, and the attention wake
// drives each owed attention as one more parent generation. Between two of
// those generations the parent is idle and the subtree is claimable, yet the
// next owed generation is still coming: once it reserves, the release
// correctly refuses the subtree as busy. So every member must also owe no
// delegate attention and pass the release's own pre-gates, which cover the
// delivery parcels not yet turned into attention. Those checks run BEFORE the
// claim: a held claim fences the delegate, so polling claims while attention
// is owed would push the wake into retry backoff instead of letting it run.
// Nothing can become owed after the checks pass — the callers already waited
// out every member's runner, and a finished member's deliveries reach their
// receiver before its runner reports done — so a claim that then succeeds
// covers a subtree with no generation left to run.
func claimSettledIdleSubtree(t *testing.T, tree *delegateTreeController, delegateID string, members ...*Session) *delegateRuntimeReclamationClaim {
	t.Helper()
	var claim *delegateRuntimeReclamationClaim
	// TRIPWIRE: the owed generations and the finalize tails settle in well
	// under a second; 15s only bounds a deadlock.
	waitForCondition(t, 15*time.Second, "subtree of "+delegateID+" to settle with no delegate attention owed", func() bool {
		for _, member := range members {
			owed, err := member.pendingDelegateAttentionIDs()
			if err != nil {
				t.Fatalf("pendingDelegateAttentionIDs(%s): %v", member.id, err)
			}
			if len(owed) != 0 || !member.idleReleasePregatesClear() {
				return false
			}
		}
		var err error
		claim, _, err = tree.ClaimIdleRuntimeRelease(delegateID)
		if err != nil {
			t.Fatalf("ClaimIdleRuntimeRelease: %v", err)
		}
		return claim != nil
	})
	return claim
}

// wideSubtreeReleaseProbe records what the idle release's member-teardown
// probes observed over one driven wide-subtree release: which members'
// teardowns started and settled, in what order, and how many same-depth
// children were mid-teardown at once.
type wideSubtreeReleaseProbe struct {
	parentSess    *Session
	childRuntimes map[string]*Session
	childIDs      []string
	// parentSeq is the claim root's member-teardown start sequence number.
	parentSeq   uint64
	parentStart bool
	maxInFlight int
	settledAt   map[string]uint64 // keyed by runtime session id
}

// driveWideSubtreeIdleRelease builds a depth-2 delegate tree — one parent
// delegate whose single scripted turn spawns wideChildren leaf delegates —
// waits for every member to finalize, claims the subtree, and drives the
// parent runtime's own finalize-tail release. The member-teardown probes
// hold each CHILD's teardown until all wideChildren are in flight or
// parkDeadline passes, so the concurrency the release chose is observable
// rather than racy: a wave that launches the whole depth at once reaches
// the rendezvous, while a serial or capped one burns the deadline batch
// by batch. Everything the callers assert on is captured after the
// release fully drained, so the returned state is quiescent.
func driveWideSubtreeIdleRelease(t *testing.T, wideChildren, limit int, parkDeadline time.Duration) *wideSubtreeReleaseProbe {
	t.Helper()

	// These existing hooks only record progress, never gate a runner. Keep
	// stages per runner so a sibling cannot overwrite the awaited member's tail.
	var runnerStages sync.Map
	recordStage := func(stage string) func(*subagent) {
		return func(sub *subagent) { runnerStages.Store(sub, stage) }
	}

	workspace := t.TempDir()
	// The adapter scripts the parent's turn as ONE response carrying
	// every delegate spawn; each later step is an identical finish. The
	// spawns all fire inside the parent's first turn, so the tree's SHAPE
	// is fixed before any follow-up call happens: the parent's post-tool
	// call and each child's first call consume the interchangeable finish
	// steps in whatever global order they race, and each ends its
	// member's turn. Spawning from a finished parent is not an option
	// (its lease is spent), and spawning one-at-a-time across turns lets
	// the parent eat a child's finish step — the one-response spawn wave
	// avoids both.
	steps := []func(req llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			calls := make([]llm.ToolCallData, wideChildren)
			for i := range calls {
				calls[i] = llm.ToolCallData{
					ID:        fmt.Sprintf("call_spawn_child_%d", i),
					Name:      "delegate",
					Arguments: json.RawMessage(`{"prompt":"wide child sentinel task","delegation_allowance":0}`),
					Type:      "function",
				}
			}
			return toolCallResponse(calls...)
		},
	}
	for range wideChildren + 1 {
		steps = append(steps, func(llm.Request) llm.Response { return finalResponse("member done") })
	}
	adapter := &fakeAdapter{name: "openai", steps: steps}
	client := llm.NewClient()
	client.Register(adapter)
	profile := withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2"))

	// The probe's rendezvous state, shared with the teardown goroutines the
	// release spawns. Everything it touches is mutex- or atomic-guarded.
	var probeMu sync.Mutex
	var probeSeq atomic.Uint64
	var childInFlight, childMaxInFlight int
	childSessions := make(map[string]bool)
	startedAt := make(map[string]uint64)
	settledAt := make(map[string]uint64)
	memberStarted := func(s *Session) {
		probeMu.Lock()
		seq := probeSeq.Add(1)
		startedAt[s.id] = seq
		isChild := childSessions[s.id]
		if isChild {
			childInFlight++
			if childInFlight > childMaxInFlight {
				childMaxInFlight = childInFlight
			}
		}
		probeMu.Unlock()
		if !isChild {
			return
		}
		// Rendezvous: hold this child's teardown until the whole wave is in
		// flight. TRIPWIRE: when the release launches the full depth in one
		// batch the last child arrives within milliseconds, so the wait
		// costs nothing; a capped or serial release burns parkDeadline per
		// batch instead, and the callers read the observed concurrency off
		// maxInFlight either way. waitForCondition cannot run here: the
		// probe fires on a teardown goroutine, not the test's.
		deadline := time.Now().Add(parkDeadline)
		for {
			probeMu.Lock()
			inFlight := childInFlight
			probeMu.Unlock()
			if inFlight == wideChildren || time.Now().After(deadline) {
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	memberSettled := func(s *Session) {
		probeMu.Lock()
		defer probeMu.Unlock()
		settledAt[s.id] = probeSeq.Add(1)
		if childSessions[s.id] {
			childInFlight--
		}
	}

	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(workspace), SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 5,
		ForceRealIO:      true,
		testOnly: testConfig{
			skipGitSnapshot:            true,
			minimalSystemPrompt:        true,
			sandboxProber:              bwrapCapableProber(workspace),
			disableDelegateIdleRelease: true,
			idleTeardownConcurrency:    &limit,
			idleTeardownMemberStarted:  memberStarted,
			idleTeardownMemberSettled:  memberSettled,
			subagentRunIteration: func(sub *subagent, iteration int) {
				runnerStages.Store(sub, fmt.Sprintf("run iteration %d", iteration))
			},
			subagentBeforeSettlement:          recordStage("before settlement"),
			subagentAfterFinalStatePublish:    recordStage("after final state publish"),
			subagentBeforeGenerationAnnounced: recordStage("before generation announcement"),
			subagentBeforeGenerationReleased: func(sub *subagent, _ delegateLease) {
				runnerStages.Store(sub, "before generation release")
			},
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)

	waitForRunner := func(label, delegateID string, sub *subagent) {
		t.Helper()
		sub.mu.Lock()
		done := sub.done
		sub.mu.Unlock()
		tree := sess.delegateController
		generation := "unavailable, tree.mu busy"
		if tree.mu.TryLock() {
			generation = strconv.FormatUint(tree.durable[delegateID].Generation, 10)
			tree.mu.Unlock()
		}
		select {
		case <-done:
			return
		case <-time.After(20 * time.Second): // TRIPWIRE: scripted turns take seconds; this only bounds a deadlock.
		}

		// Capture stacks before cleanup or optional state reads. A busy runtime
		// lock must not prevent the original timeout from reporting its evidence.
		var stacks strings.Builder
		if err := pprof.Lookup("goroutine").WriteTo(&stacks, 2); err != nil {
			t.Logf("goroutine dump: %v", err)
		}
		t.Logf("runner timeout goroutines:\n%s", stacks.String())
		t.Logf("awaited delegate=%s session=%s sub=%p generation=%s done=%p", delegateID, sub.id, sub, generation, done)
		runnerStages.Range(func(key, value any) bool {
			runner := key.(*subagent)
			t.Logf("runner stage session=%s sub=%p stage=%s", runner.id, runner, value)
			return true
		})
		if sub.mu.TryLock() {
			state := fmt.Sprintf("current done=%p running=%t driving=%t finalizing=%t settlementClaimed=%t status=%s err=%v", sub.done, sub.running, sub.driving, sub.finalizing, sub.settlementClaimed, sub.status, sub.err)
			sub.mu.Unlock()
			t.Log(state)
		} else {
			t.Log("runner state unavailable: sub.mu busy")
		}
		if tree.mu.TryLock() {
			var state strings.Builder
			fmt.Fprintf(&state, "current generation=%d", tree.durable[delegateID].Generation)
			if live := tree.live[delegateID]; live != nil {
				fmt.Fprintf(&state, " binding=%p finalizing=%p", live.binding, live.finalizing)
			}
			for sourceID, aggregate := range tree.durable {
				for _, pending := range aggregate.PendingDeliveries {
					receiverID := tree.rootSessionID
					if owner := tree.durable[pending.OwnerDelegateID]; owner != nil {
						receiverID = owner.Descriptor.ChildSessionID
					}
					fmt.Fprintf(&state, "\npending delivery=%s generation=%d source=%s source_session=%s owner=%q receiver_session=%s observed=unknown, transcript not read", pending.DeliveryID, pending.Generation, sourceID, aggregate.Descriptor.ChildSessionID, pending.OwnerDelegateID, receiverID)
					if receipt := tree.deliveryReceiptLocked(pending.DeliveryID); receipt != nil {
						fmt.Fprintf(&state, " receipt=%+v", *receipt)
					}
				}
			}
			tree.mu.Unlock()
			t.Log(state.String())
		} else {
			t.Log("controller state unavailable: tree.mu busy")
		}
		t.Fatalf("%s did not finish", label)
	}

	allowance := wideChildren
	parentRes := sess.createDelegate(context.Background(), delegateArgs{
		Task:                "spawn leaf children, then finish",
		DelegationAllowance: &allowance,
	})
	if parentRes.Err != nil {
		t.Fatalf("createDelegate: %v (status=%s reason=%s)", parentRes.Err, parentRes.Status, parentRes.Reason)
	}
	sub := sess.subagents.get(parentRes.ChildSessionID)
	if sub == nil {
		t.Fatalf("parent delegate missing from root manager: %+v", parentRes)
	}
	waitForRunner("parent delegate runner", parentRes.DelegateID, sub)

	// The parent's single scripted turn spawned the whole wave, so the
	// tree's shape is already fixed; the children finish on their own
	// runners. Wait for each through the parent runtime's manager.
	tree := sess.delegateController
	tree.mu.Lock()
	parentLive := tree.live[parentRes.DelegateID]
	var childIDs []string
	childSessionIDs := make([]string, 0, wideChildren)
	for id, agg := range tree.durable {
		if agg.Descriptor.ParentDelegateID == parentRes.DelegateID {
			childIDs = append(childIDs, id)
			childSessionIDs = append(childSessionIDs, agg.Descriptor.ChildSessionID)
			childSessions[agg.Descriptor.ChildSessionID] = true
		}
	}
	tree.mu.Unlock()
	if parentLive == nil || parentLive.runtime == nil {
		t.Fatal("expected the parent runtime resident before the children settle")
	}
	parentSess := parentLive.runtime
	if len(childIDs) != wideChildren {
		t.Fatalf("parent delegate has %d child delegates, want %d (fixture script derailed)", len(childIDs), wideChildren)
	}
	for i, csid := range childSessionIDs {
		childSub := parentSess.subagents.get(csid)
		if childSub == nil {
			t.Fatalf("child %s missing from the parent runtime's manager", csid)
		}
		waitForRunner(fmt.Sprintf("child delegate runner %d", i), childIDs[i], childSub)
	}

	childRuntimes := make(map[string]*Session, wideChildren)
	tree.mu.Lock()
	for _, id := range childIDs {
		if live := tree.live[id]; live != nil {
			childRuntimes[id] = live.runtime
		}
	}
	tree.mu.Unlock()
	sort.Strings(childIDs)
	for _, id := range childIDs {
		if childRuntimes[id] == nil {
			t.Fatalf("expected child %s runtime resident before the release", id)
		}
	}

	members := []*Session{parentSess}
	for _, id := range childIDs {
		members = append(members, childRuntimes[id])
	}
	claim := claimSettledIdleSubtree(t, tree, parentRes.DelegateID, members...)
	want := append([]string(nil), childIDs...)
	want = append(want, parentRes.DelegateID)
	if got := reclamationDelegateIDs(claim); !reflect.DeepEqual(got, want) {
		t.Fatalf("claim entries = %v, want leaf-first %v", got, want)
	}
	if err := tree.AbortRuntimeReclamation(claim); err != nil {
		t.Fatalf("AbortRuntimeReclamation: %v", err)
	}

	// The production entrypoint: the parent runtime's own finalize-tail call.
	if !parentSess.releaseIdleRuntimeAfterFinalize() {
		t.Fatal("releaseIdleRuntimeAfterFinalize refused the terminal wide subtree")
	}

	// The release drained before returning (every batch joined), so the
	// probe state is quiescent.
	probeMu.Lock()
	defer probeMu.Unlock()
	parentSeq, parentStart := startedAt[parentRes.ChildSessionID]
	return &wideSubtreeReleaseProbe{
		parentSess:    parentSess,
		childRuntimes: childRuntimes,
		childIDs:      childIDs,
		parentSeq:     parentSeq,
		parentStart:   parentStart,
		maxInFlight:   childMaxInFlight,
		settledAt:     settledAt,
	}
}

// assertWideSubtreeReleaseSettled pins the shared post-release contracts:
// the claim root's teardown ran, every child's teardown settled before the
// root started (leaf-first held across the wave or batch boundary), and
// every member's retirement teardown pass is spent — the release tore each
// member down itself.
func assertWideSubtreeReleaseSettled(t *testing.T, probe *wideSubtreeReleaseProbe) {
	t.Helper()
	if !probe.parentStart {
		t.Fatal("the claim root's member teardown never started")
	}
	for _, id := range probe.childIDs {
		childSettled, settled := probe.settledAt[probe.childRuntimes[id].id]
		if !settled {
			t.Fatalf("child %s teardown never settled", id)
		}
		if childSettled >= probe.parentSeq {
			t.Fatalf("claim root started its teardown at seq %d before descendant %s settled at seq %d: leaf-first violated", probe.parentSeq, id, childSettled)
		}
	}

	// Every teardown body ran to completion: each member's pass is spent.
	if err := probe.parentSess.releaseRuntime(context.Background(), closeOptions{}, releaseRetirement); !errors.Is(err, errRetirementTeardownSpent) {
		t.Fatalf("parent runtime teardown pass after release: err = %v, want errRetirementTeardownSpent", err)
	}
	for _, id := range probe.childIDs {
		if err := probe.childRuntimes[id].releaseRuntime(context.Background(), closeOptions{}, releaseRetirement); !errors.Is(err, errRetirementTeardownSpent) {
			t.Fatalf("child %s teardown pass after release: err = %v, want errRetirementTeardownSpent", id, err)
		}
	}
}

// TestDelegateIdleRelease_TeardownsSameDepthMembersConcurrently pins the
// bounded-parallel contract of the idle release's member teardown. A member
// teardown is wait-dominated — the runtime close signals processes and honors
// bounded waits, and a stdio MCP member's close can take seconds — so a
// serial release makes a wide subtree's wall clock the member count times one
// member's close. The four same-depth children here must settle CONCURRENTLY
// (the probe holds each child's teardown until all four are in flight, so the
// assertion is a rendezvous, not a scheduling race), while the claim root
// itself never starts before every descendant settled: the leaf-first
// ordering the depth-2 contract test pins, held across the wave boundary.
func TestDelegateIdleRelease_TeardownsSameDepthMembersConcurrently(t *testing.T) {
	const (
		wideChildren = 4
		limit        = wideChildren
	)
	probe := driveWideSubtreeIdleRelease(t, wideChildren, limit, 3*time.Second)
	if probe.maxInFlight != wideChildren {
		t.Fatalf("idle release settled the same-depth members serially: max concurrent child teardowns = %d, want %d", probe.maxInFlight, wideChildren)
	}
	assertWideSubtreeReleaseSettled(t, probe)
}

func TestDelegateDrainPendingColdReceiver(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, receiver, gate string
		outstanding, live    bool
	}{
		{"own", "dlg_parent", "", true, true},
		{"cold descendant", "dlg_receiver", "", true, true},
		{"cold grandchild", "dlg_receiver", "grandchild", true, true},
		{"cold grandchild, closed retained boundary", "dlg_receiver", "grandchild closed", true, true},
		{"cold grandchild stopped ancestor", "dlg_receiver", "grandchild stop", false, false},
		{"live descendant", "dlg_receiver", "resident", true, true},
		{"closed retained descendant", "dlg_receiver", "resident closed", true, true},
		{"historical receiver, source now elsewhere", "dlg_parent", "", true, true},
		{"ancestor", "", "", false, false},
		{"sibling", "dlg_sibling", "", false, false},
		{"source child, recorded receiver root", "", "source child", false, false},
		{"source child, recorded receiver sibling", "dlg_sibling", "source child", false, false},
		{"cold stopped", "dlg_receiver", "stop", false, false},
		{"cold abandoned", "dlg_receiver", "abandoned", false, false},
		{"cold grace", "dlg_receiver", "grace", true, false},
		{"live fatal", "dlg_receiver", "fatal", false, false},
		{"live stopped", "dlg_receiver", "resident stop", false, false},
		{"live abandoned", "dlg_receiver", "resident abandoned", false, false},
		{"live grace", "dlg_receiver", "resident grace", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newDelegateControllerTestHarness(t, 8, 4)
			seedDelegateControllerIdle(t, c, "dlg_parent", "")
			seedDelegateControllerIdle(t, c, "dlg_receiver", "dlg_parent")
			seedDelegateControllerIdle(t, c, "dlg_source", "")
			seedDelegateControllerIdle(t, c, "dlg_sibling", "")
			if strings.HasPrefix(tc.gate, "grandchild") {
				seedDelegateControllerIdle(t, c, "dlg_middle", "dlg_parent")
			}
			parent := &Session{id: c.durable["dlg_parent"].Descriptor.ChildSessionID, owningDelegateID: "dlg_parent", delegateController: c}
			receiver := c.durable["dlg_receiver"]
			receiver.Descriptor.OwnerSessionID = parent.id
			if tc.name == "own" || tc.gate == "source child" {
				c.durable["dlg_source"].Descriptor.ParentDelegateID = "dlg_parent"
				c.durable["dlg_source"].Descriptor.OwnerSessionID = parent.id
			}
			c.durable["dlg_source"].PendingDeliveries = []delegatestore.PendingDelivery{{DeliveryID: "pending-delivery", OwnerDelegateID: tc.receiver}}
			if !c.delegateIsAncestorLocked("dlg_parent", "dlg_receiver") || len(parent.liveDirectSubagents()) != 0 {
				t.Fatal("fixture must have a canonical descendant receiver but no resident child runtime")
			}
			if strings.Contains(tc.gate, "stop") {
				receiver.LatestOutcome = &delegatestore.Outcome{Status: delegatestore.OutcomeStopped, Reason: delegatestore.ReasonStoppedByParent}
			}
			if strings.HasPrefix(tc.gate, "grandchild") {
				middle := c.durable["dlg_middle"]
				middle.Descriptor.OwnerSessionID = parent.id
				receiver.Descriptor.ParentDelegateID = "dlg_middle"
				receiver.Descriptor.OwnerSessionID = middle.Descriptor.ChildSessionID
				if tc.gate == "grandchild stop" {
					middle.LatestOutcome, receiver.LatestOutcome = receiver.LatestOutcome, nil
				}
				if tc.gate == "grandchild closed" {
					child := &Session{id: middle.Descriptor.ChildSessionID, owningDelegateID: "dlg_middle", delegateController: c}
					c.live["dlg_middle"] = &delegateLiveState{runtime: child}
					parent.subagents = &subagentManager{subs: map[string]*subagent{child.id: {sess: child, closed: true}}}
				}
			}
			if strings.Contains(tc.gate, "abandoned") {
				parent.drainAbandonedChildren = map[string]drainAbandonedChild{receiver.Descriptor.ChildSessionID: {delegateID: "dlg_receiver", generation: receiver.Generation}}
			}
			if strings.Contains(tc.gate, "grace") {
				receiver.CurrentRunOpen = true
				parent.drainGraceChildren = map[string]drainGraceChild{receiver.Descriptor.ChildSessionID: {delegateID: "dlg_receiver", generation: receiver.Generation}}
			}
			if strings.HasPrefix(tc.gate, "resident") || tc.gate == "fatal" {
				child := &Session{id: receiver.Descriptor.ChildSessionID, owningDelegateID: "dlg_receiver", delegateController: c}
				c.live["dlg_receiver"] = &delegateLiveState{runtime: child}
				parent.subagents = &subagentManager{subs: map[string]*subagent{child.id: {sess: child, fatalRunGated: tc.gate == "fatal", closed: tc.gate == "resident closed"}}}
			}
			for _, check := range []struct {
				name string
				fn   func() (bool, error)
				want bool
			}{{"outstanding", parent.treeHasOutstandingWork, tc.outstanding}, {"live", parent.subtreeHasLiveComponent, tc.live}} {
				got, err := check.fn()
				if err != nil || got != check.want {
					t.Errorf("receiver %s = %t, err=%v, want %t", check.name, got, err, check.want)
				}
			}
			if !parent.hasPendingDelegateDeliveries() {
				t.Error("scoped drain accounting changed global transport inventory")
			}
			if tc.name == "ancestor" {
				root := &Session{id: c.rootSessionID, delegateController: c}
				if !root.hasPendingDrainDelegateDeliveries(false, nil) || !root.hasPendingDrainDelegateDeliveries(true, nil) {
					t.Error("root receiver lost its own pending delivery")
				}
			}
		})
	}
}

type reclaimLogCapture struct {
	slog.Handler
	mu      sync.Mutex
	records []slog.Record
}

func (h *reclaimLogCapture) Handle(_ context.Context, record slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, record.Clone())
	return nil
}

func TestDelegateLeafFinalizationDoesNotWaitForForeignReceiverDelivery(t *testing.T) {
	// Serial: this test replaces the process-wide default logger.
	logged := &reclaimLogCapture{Handler: slog.NewTextHandler(io.Discard, nil)}
	previous := slog.Default()
	slog.SetDefault(slog.New(logged))
	t.Cleanup(func() { slog.SetDefault(previous) })
	foreignBeforeAnnouncement := make(chan struct{})
	allowForeignAnnouncement := make(chan struct{})
	leafDrainEntered := make(chan struct{})
	foreignResourceWakeDone := make(chan struct{})
	leafResourceWakeDone := make(chan struct{})
	var announcementSeen, failRootRead atomic.Bool
	var drainEntries, rootReadFailures atomic.Int32
	var recoveredRequests atomic.Int32
	var rootID atomic.Value
	clock := agenttest.NewFakeClock()
	root := newSession(t, withoutGitSnapshot(), withClock(clock), withConfig(SessionConfig{
		StateDir: t.TempDir(), MaxSubagentDepth: 1, ForceRealIO: true,
		testOnly: testConfig{
			minimalSystemPrompt: true, disableDelegateIdleRelease: true,
			subagentBeforeGenerationAnnounced: func(sub *subagent) {
				wakeDone := leafResourceWakeDone
				if announcementSeen.CompareAndSwap(false, true) {
					wakeDone = foreignResourceWakeDone
					close(foreignBeforeAnnouncement)
					<-allowForeignAnnouncement
				}
				// done closes before the parent-resource wake. Observe that real
				// callback too, before registering the receiver's recovery callback.
				parentWake := sub.parentResourceWake
				sub.parentResourceWake = func() {
					parentWake()
					close(wakeDone)
				}
			},
			jobTreeDrainEntered: func() {
				if drainEntries.Add(1) == 2 {
					close(leafDrainEntered)
				}
			},
			delegateAttentionReadFold: func(path, sessionID string) (delegateAttentionFold, error) {
				if sessionID == rootID.Load() && failRootRead.Load() {
					rootReadFailures.Add(1)
					return delegateAttentionFold{}, errInjectedTranscriptWrite
				}
				return readDelegateAttentionFold(path, sessionID)
			},
		},
	}), withSteps(func(llm.Request) llm.Response { return finalResponse("foreign result") }, func(llm.Request) llm.Response { return finalResponse("leaf result") }, func(req llm.Request) llm.Response {
		recoveredRequests.Add(1)
		if !requestMessagesContainText(req.Messages, "foreign result") || !requestMessagesContainText(req.Messages, "leaf result") {
			t.Error("recovery request lost a delegate result")
		}
		return finalResponse("root observed both results")
	}))
	rootID.Store(root.id)
	var releaseForeign sync.Once
	unblockForeign := func() { releaseForeign.Do(func() { close(allowForeignAnnouncement) }) }
	t.Cleanup(unblockForeign)
	await := func(ch <-chan struct{}, boundary string) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(20 * time.Second): // TRIPWIRE: bounds a deadlock, not normal work.
			var stacks strings.Builder
			if err := pprof.Lookup("goroutine").WriteTo(&stacks, 2); err != nil {
				t.Logf("goroutine dump: %v", err)
			}
			t.Logf("goroutines:\n%s", stacks.String())
			t.Fatalf("%s did not finish", boundary)
		}
	}
	create := func(task string) (delegateResult, *subagent, <-chan struct{}) {
		t.Helper()
		result := root.createDelegate(context.Background(), delegateArgs{Task: task})
		if result.Err != nil {
			t.Fatalf("createDelegate: %v", result.Err)
		}
		sub := root.subagents.get(result.ChildSessionID)
		sub.mu.Lock()
		done := sub.done
		sub.mu.Unlock()
		return result, sub, done
	}
	foreign, _, foreignDone := create("foreign root receiver sentinel")
	await(foreignBeforeAnnouncement, "foreign pre-announcement")
	failRootRead.Store(true)
	unblockForeign()
	await(foreignDone, "foreign runner")
	await(foreignResourceWakeDone, "foreign post-completion resource wake")
	if rootReadFailures.Load() == 0 {
		t.Fatal("foreign delivery did not exercise the real receiver failure")
	}
	c := root.delegateController
	plans := c.ReplayDeliveries()
	if len(plans) != 1 || plans[0].delegateID != foreign.DelegateID || plans[0].ownerDelegateID != "" {
		t.Fatalf("foreign pending plans = %+v", plans)
	}
	// Retain an actual in-flight admission, so the leaf cannot accidentally
	// repair the foreign receiver's fault and erase the premise during its kick.
	token, admitted, err := c.BeginDelivery(plans[0])
	if err != nil || !admitted {
		t.Fatalf("BeginDelivery foreign: admitted=%t err=%v", admitted, err)
	}
	t.Cleanup(func() { _, _ = c.CompleteDelivery(token, false) })
	leaf, leafSub, leafDone := create("independent leaf sentinel")
	await(leafDrainEntered, "leaf drain entry")
	assertForeignStillOwed := func() {
		t.Helper()
		ids := pendingDeliveryIDs(c, foreign.DelegateID)
		fold, err := readDelegateAttentionFold(transcriptPath(root.stateDir, root.id), root.id)
		attentionID := delegateAttentionID(plans[0].deliveryID)
		_, observed := fold.observed[attentionID]
		_, accepted := fold.content[attentionID]
		if err != nil || len(ids) != 1 || ids[0] != plans[0].deliveryID || observed || accepted {
			t.Errorf("foreign custody changed: pending=%v accepted=%t observed=%t err=%v", ids, accepted, observed, err)
		}
		t.Logf("foreign delivery=%s source=%s receiver=%s pending=%v accepted=%t observed=%t", plans[0].deliveryID, foreign.DelegateID, root.id, ids, accepted, observed)
	}
	t.Cleanup(func() {
		if t.Failed() {
			assertForeignStillOwed()
		}
	})
	t.Logf("leaf=%s session=%s must finish independently of foreign delivery=%s receiver=%s", leaf.DelegateID, leaf.ChildSessionID, plans[0].deliveryID, root.id)
	await(leafDone, "independent leaf runner")
	await(leafResourceWakeDone, "leaf post-completion resource wake")
	leafSub.mu.Lock()
	status, runErr := leafSub.status, leafSub.err
	leafSub.mu.Unlock()
	if status != SubagentCompleted || runErr != nil {
		t.Fatalf("leaf outcome: status=%s err=%v", status, runErr)
	}
	assertForeignStillOwed()
	// Use the server's registered, coalesced input wake pattern. The input
	// owner starts after the retry callback finishes so both recovered results
	// share one request, without racing that callback's delivery batch.
	wakes := make(chan struct{}, 1)
	startOwner := make(chan struct{})
	ownerDone := make(chan struct{})
	turnDone := make(chan error, 1)
	ctx, cancelOwner := context.WithCancel(context.Background())
	go func() {
		defer close(ownerDone)
		select {
		case <-ctx.Done():
			return
		case <-startOwner:
		}
		select {
		case <-ctx.Done():
			return
		case <-wakes:
			_, turnErr := root.ProcessInputKind(ctx, "", nil, EntryNotification)
			turnDone <- turnErr
		}
	}()
	t.Cleanup(func() {
		cancelOwner()
		<-ownerDone
	})
	root.SetNotifyFunc(func() {
		select {
		case wakes <- struct{}{}:
		default:
		}
	})
	select {
	case <-wakes:
		t.Fatal("receiver woke before the foreign fault was released")
	default:
	}
	root.delegateDeliveryMu.Lock()
	retryArmed := root.delegateDeliveryRetry.active
	root.delegateDeliveryMu.Unlock()
	if !retryArmed {
		t.Fatal("failed delivery did not retain its automatic transport retry")
	}
	failRootRead.Store(false)
	if _, err := c.CompleteDelivery(token, false); err != nil {
		t.Fatal(err)
	}
	clock.Advance(jobNotificationRetryInitialDelay)
	clock.Drain()
	if ids := pendingDeliveryIDs(c, foreign.DelegateID); len(ids) != 0 {
		t.Fatalf("automatic retry left foreign source acknowledgment pending: %v", ids)
	}
	if len(wakes) != 1 {
		t.Fatal("automatic retry did not wake the registered receiver input owner")
	}
	close(startOwner)
	await(ownerDone, "automatically woken receiver input owner")
	if err := <-turnDone; err != nil {
		t.Fatalf("automatic notification turn: %v", err)
	}
	fold, err := readDelegateAttentionFold(transcriptPath(root.stateDir, root.id), root.id)
	attentionID := delegateAttentionID(plans[0].deliveryID)
	_, accepted := fold.content[attentionID]
	_, observed := fold.observed[attentionID]
	if err != nil || !accepted || !observed || recoveredRequests.Load() != 1 {
		t.Fatalf("recovery: accepted=%t observed=%t requests=%d err=%v", accepted, observed, recoveredRequests.Load(), err)
	}
	assertAttentionResponseReceipts(t, root, attentionID, 1)
	// Manual replay is only an idempotency check AFTER automatic observation.
	if err := root.replayDelegateDeliveries(); err != nil {
		t.Fatalf("duplicate recovery: %v", err)
	}
	acceptedCount := 0
	for _, entry := range readAttentionTranscriptEntries(t, root.TranscriptPath()) {
		if entry.Turn.AttentionID == attentionID {
			acceptedCount++
		}
	}
	if acceptedCount != 1 {
		t.Fatalf("foreign notification transcript records = %d, want 1", acceptedCount)
	}
	logged.mu.Lock()
	defer logged.mu.Unlock()
	warnings := 0
	for _, record := range logged.records {
		var sessionID string
		var warningErr error
		record.Attrs(func(attr slog.Attr) bool {
			switch attr.Key {
			case "session_id":
				sessionID = attr.Value.String()
			case "error":
				warningErr, _ = attr.Value.Any().(error)
			}
			return true
		})
		if record.Level == slog.LevelWarn && sessionID == root.id && errors.Is(warningErr, errInjectedTranscriptWrite) {
			warnings++
		}
	}
	if warnings != 1 {
		t.Fatalf("injected receiver warning records = %d, want 1", warnings)
	}
}

// TestDelegateIdleRelease_CapsConcurrentMemberTeardown pins the bound
// itself: no more than idleTeardownConcurrency same-depth members may be
// mid-teardown at once. The concurrency test above runs one batch (four
// members, limit four), which cannot tell a bounded wave from an unbounded
// one; three same-depth children under a limit of two here force the
// multi-batch path, and the probe's rendezvous makes the cap observable in
// both directions — an unbounded wave launches all three before any settles
// (max in flight 3), while the capped one parks each batch at the rendezvous
// and never holds more than two. Leaf-first still holds across the batches:
// the claim root waits for the last batch to drain.
func TestDelegateIdleRelease_CapsConcurrentMemberTeardown(t *testing.T) {
	const (
		wideChildren = 3
		limit        = 2
	)
	probe := driveWideSubtreeIdleRelease(t, wideChildren, limit, 500*time.Millisecond)
	if probe.maxInFlight > limit {
		t.Fatalf("idle release tore down %d same-depth members at once, want at most %d", probe.maxInFlight, limit)
	}
	assertWideSubtreeReleaseSettled(t, probe)
}

// TestDelegateIdleReleaseGenerationGuard pins the stale-timer contract: a
// grace timer stands down once any later generation has started or finalized,
// so an earlier generation's pending fire can never cut the newer
// generation's grace short.
func TestDelegateIdleReleaseGenerationGuard(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 4, 2)
	seedDelegateReclaimRuntime(t, c, "dlg_target", "", time.Unix(10, 0).UTC(), false, false)
	if !c.idleReleaseGenerationCurrent("dlg_target", 1) {
		t.Fatal("guard rejected the generation whose finalize armed it")
	}
	if c.idleReleaseGenerationCurrent("dlg_target", 2) {
		t.Fatal("guard accepted a superseding generation")
	}
	if c.idleReleaseGenerationCurrent("dlg_missing", 1) {
		t.Fatal("guard accepted a missing delegate")
	}
}

// TestDelegateIdleRelease_RefusesSharedTaskStoreOwner: releasing a subtree
// that owns a shared task store would strand the resolver's next cold
// restore on the owner-residency check, so the claim must refuse —
// terminally, with no retry.
func TestDelegateIdleRelease_RefusesSharedTaskStoreOwner(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 8, 4)
	seedDelegateReclaimRuntime(t, c, "dlg_owner", "", time.Unix(10, 0).UTC(), false, false)
	seedDelegateReclaimRuntime(t, c, "dlg_resolver", "", time.Unix(5, 0).UTC(), false, false)
	c.mu.Lock()
	c.durable["dlg_resolver"].Descriptor.SharedTaskStoreOwnerSessionID = c.durable["dlg_owner"].Descriptor.ChildSessionID
	c.mu.Unlock()
	claim, retryable, err := c.ClaimIdleRuntimeRelease("dlg_owner")
	if err != nil {
		t.Fatalf("ClaimIdleRuntimeRelease: %v", err)
	}
	if claim != nil {
		t.Fatal("claim admitted a subtree owning a shared task store")
	}
	if retryable {
		t.Fatal("shared-task-store refusal must be terminal, not retried")
	}
}

// TestDelegateIdleRelease_RefusesWhileManagerChildRuns: a manager child the
// durable subtree never tracked must not be abandoned mid-run by an
// opportunistic release of its owner — the pre-gate refuses while it runs,
// and once it finishes, the release settles the child with the member
// instead of leaving the record to outlive the runtime that held it.
func TestDelegateIdleRelease_RefusesWhileManagerChildRuns(t *testing.T) {
	t.Parallel()
	root, tree, _ := newRetirementDelegateController(t)
	defer root.Close()
	result := retirementIdleDelegate(t, root)
	tree.mu.Lock()
	live := tree.live[result.DelegateID]
	tree.mu.Unlock()
	if live == nil || live.runtime == nil {
		t.Fatal("fixture delegate is not resident before the release")
	}
	runtime := live.runtime

	// The manager-only child: a record no durable delegate backs, still
	// running. No production spawn creates these today — every manager
	// insertion is the stable-delegate machinery — so this pins the
	// invariant a stray one cannot violate.
	legacyDone := make(chan struct{})
	legacy := &subagent{id: "legacy-child-sentinel", sess: newSession(t), done: legacyDone}
	runtime.subagents.mu.Lock()
	runtime.subagents.subs[legacy.id] = legacy
	runtime.subagents.mu.Unlock()

	if runtime.releaseIdleRuntimeAfterFinalize() {
		t.Fatal("release succeeded while a manager child was still running")
	}
	tree.mu.Lock()
	stillResident := tree.live[result.DelegateID].runtime
	tree.mu.Unlock()
	if stillResident != runtime {
		t.Fatal("refused release changed residency")
	}

	close(legacyDone)
	if !runtime.releaseIdleRuntimeAfterFinalize() {
		t.Fatal("release refused after the manager child finished")
	}
	if err := legacy.sess.releaseRuntime(context.Background(), closeOptions{}, releaseRetirement); !errors.Is(err, errRetirementTeardownSpent) {
		t.Fatalf("manager child was not settled with the release: err = %v, want errRetirementTeardownSpent", err)
	}
	if err := runtime.releaseRuntime(context.Background(), closeOptions{}, releaseRetirement); !errors.Is(err, errRetirementTeardownSpent) {
		t.Fatalf("owner runtime teardown pass after release: err = %v, want errRetirementTeardownSpent", err)
	}
}

// TestDelegateIdleRelease_PregateRefusalLeavesRuntimeWarm: the pre-gates
// exist so a mid-release refusal can never strand an already-spent teardown
// pass, so a refusal must leave everything untouched — the runtime resident,
// the record hooked, no claim held — and the same entrypoint must succeed
// once the residue settles. The fixture disables the scheduled release, so
// the refusal here cannot re-arm a retry timer behind the test's back.
func TestDelegateIdleRelease_PregateRefusalLeavesRuntimeWarm(t *testing.T) {
	t.Parallel()
	root, tree, _ := newRetirementDelegateController(t)
	defer root.Close()
	result := retirementIdleDelegate(t, root)
	tree.mu.Lock()
	live := tree.live[result.DelegateID]
	tree.mu.Unlock()
	if live == nil || live.runtime == nil {
		t.Fatal("fixture delegate is not resident before the release")
	}
	runtime := live.runtime

	// Plant exactly the residue the first pre-gate refuses on: a queued job
	// notification no turn has accepted yet.
	runtime.pendingJobNotifsMu.Lock()
	runtime.pendingJobNotifs = append(runtime.pendingJobNotifs, jobNotification{
		Kind:   jobNotificationKindTerminal,
		JobID:  "job-residue-sentinel",
		Status: "completed",
	})
	runtime.pendingJobNotifsMu.Unlock()

	if runtime.releaseIdleRuntimeAfterFinalize() {
		t.Fatal("release succeeded with queued residue; the pre-gate must refuse")
	}
	tree.mu.Lock()
	stillResident := tree.live[result.DelegateID].runtime
	held := len(tree.reclaiming)
	tree.mu.Unlock()
	if stillResident != runtime {
		t.Fatalf("refused release changed residency: got %p, want the exact warm runtime %p", stillResident, runtime)
	}
	if held != 0 {
		t.Fatalf("refused release left %d claim(s) held", held)
	}
	if got := root.subagents.get(result.ChildSessionID); got == nil || got.sess != runtime {
		t.Fatal("refused release unhooked the delegate record")
	}

	// Once the residue settles, the same entrypoint releases the runtime.
	runtime.pendingJobNotifsMu.Lock()
	runtime.pendingJobNotifs = nil
	runtime.pendingJobNotifsMu.Unlock()
	if !runtime.releaseIdleRuntimeAfterFinalize() {
		t.Fatal("release refused after residue settled")
	}
	tree.mu.Lock()
	released := tree.live[result.DelegateID].runtime
	tree.mu.Unlock()
	if released != nil {
		t.Fatal("release after residue settled left the runtime resident")
	}
}

// idleReleaseFirstGenerationTripwire bounds this fixture's wait for the delegate
// runner's first generation to finish: sub.done, closed at the end of the
// stable-run finalize tail (subagents.go). It is a hang guard, never a speed
// assertion, and it is NOT the fixture's rendezvous — the residue plant and the
// forced refusal begin only after this wait returns. Nothing the fixture
// controls closes `done`: it follows the runner goroutine's whole first turn —
// the model turn, the durable generation settlement (this fixture sets
// ForceRealIO, so the append path fsyncs), the announcement, and the attention
// re-arm. (The child Session's construction is not in the span: createDelegate
// builds it synchronously before it launches the runner.) The inline 10s this
// replaces was sized as if `done` closed at the millisecond rendezvous its old
// comment claimed; a controlled 12s pause on one legitimate post-publication
// finalize step (testOnly.subagentAfterFinalStatePublish) makes a 10s bound fire
// at 10.03s with the runner healthy, while this bound passes. 30s matches this
// package's standing whole-operation tripwire for in-process work
// (stallHandshakeTimeout, retirementCreateBarrierTripwire,
// stalledRefreshTripwire).
const idleReleaseFirstGenerationTripwire = 30 * time.Second

// TestDelegateIdleRelease_RetriesAfterPregateRefusal: a grace timer that
// fires into residue must not lose the release to a one-shot refusal — the
// refusal re-arms one more grace window, and the retry, not any caller,
// releases the runtime once the residue settles. The session clock is the
// package's fake, so the grace timers fire only when the test advances
// virtual time: the residue plant can never race the first timer.
func TestDelegateIdleRelease_RetriesAfterPregateRefusal(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	adapter := &fakeAdapter{name: "openai"}
	client := llm.NewClient()
	client.Register(adapter)
	profile := withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2"))
	fake := agenttest.NewFakeClock()
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(workspace), SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 1,
		ForceRealIO:      true,
		clock:            fake,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			sandboxProber:       bwrapCapableProber(workspace),
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)

	res := sess.createDelegate(context.Background(), delegateArgs{Task: "idle sentinel"})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v (status=%s reason=%s)", res.Err, res.Status, res.Reason)
	}
	sub := sess.subagents.get(res.ChildSessionID)
	if sub == nil {
		t.Fatalf("delegate missing from manager: %+v", res)
	}
	sub.mu.Lock()
	done := sub.done
	sub.mu.Unlock()
	select {
	case <-done:
	case <-time.After(idleReleaseFirstGenerationTripwire):
		t.Fatal("delegate runner did not finish")
	}
	tree := sess.delegateController

	// Plant the residue, then force the refusal through the same entrypoint
	// the timer fires into: the refusal itself re-arms the retry. Virtual
	// time has not moved, so no grace timer can fire behind the plant.
	runtime := sub.sess
	runtime.pendingJobNotifsMu.Lock()
	runtime.pendingJobNotifs = append(runtime.pendingJobNotifs, jobNotification{
		Kind:   jobNotificationKindTerminal,
		JobID:  "job-residue-sentinel",
		Status: "completed",
	})
	runtime.pendingJobNotifsMu.Unlock()
	if runtime.releaseIdleRuntimeAfterFinalize() {
		t.Fatal("release succeeded with queued residue; the pre-gate must refuse")
	}
	tree.mu.Lock()
	stillResident := tree.live[res.DelegateID].runtime
	tree.mu.Unlock()
	if stillResident == nil {
		t.Fatal("refused release released the runtime anyway")
	}

	// The residue settles; the re-armed retry — not this test — must do the
	// release. The re-arm replaced the original timer, so one advance past
	// the grace fires the single outstanding retry, which releases the
	// runtime.
	runtime.pendingJobNotifsMu.Lock()
	runtime.pendingJobNotifs = nil
	runtime.pendingJobNotifsMu.Unlock()
	fake.Advance(delegateIdleReleaseDelayDefault + time.Second)
	// TRIPWIRE: the retry re-arms with the same grace, so the release
	// fires here in real time only as a goroutine handoff; 15s only bounds
	// a genuine hang.
	waitForCondition(t, 15*time.Second, "re-armed retry to release the runtime after residue settled", func() bool {
		tree.mu.Lock()
		released := tree.live[res.DelegateID] == nil || tree.live[res.DelegateID].runtime == nil
		tree.mu.Unlock()
		return released
	})
}

// TestDelegateIdleRelease_RefusalsKeepSingleGraceTimer: every transient
// refusal re-arms the grace window through the same funnel the finalize tail
// armed, and the re-arm must REPLACE the outstanding timer, never stack a
// second one — at most one grace timer per delegate is ever armed, however
// many refusals intervene. The single surviving timer still owns the
// release: once the residue settles, one advance releases the runtime
// exactly once, and the success re-arms nothing.
func TestDelegateIdleRelease_RefusalsKeepSingleGraceTimer(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	adapter := &fakeAdapter{name: "openai"}
	client := llm.NewClient()
	client.Register(adapter)
	profile := withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2"))
	fake := agenttest.NewFakeClock()
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(workspace), SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 1,
		ForceRealIO:      true,
		clock:            fake,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			sandboxProber:       bwrapCapableProber(workspace),
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)

	// Grace windows are one-shot AfterFunc callbacks. The delegate also parks a
	// quiet-watchdog ticker on this clock, and that ticker's Stop runs on a
	// context.AfterFunc goroutine nothing here can await, so a count of every
	// waiter is racy; PendingCallbacks counts only the grace windows.
	callbacksBefore := fake.PendingCallbacks()
	res := sess.createDelegate(context.Background(), delegateArgs{Task: "idle sentinel"})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v (status=%s reason=%s)", res.Err, res.Status, res.Reason)
	}
	sub := sess.subagents.get(res.ChildSessionID)
	if sub == nil {
		t.Fatalf("delegate missing from manager: %+v", res)
	}
	sub.mu.Lock()
	done := sub.done
	sub.mu.Unlock()
	select {
	case <-done:
	case <-time.After(10 * time.Second): // TRIPWIRE: fixture rendezvous normally takes milliseconds; this only bounds a deadlock.
		t.Fatal("delegate runner did not finish")
	}
	// The finalize tail arms the grace timer after the done handshake.
	// TRIPWIRE: the arm follows the handshake by a goroutine handoff; 15s only
	// bounds a genuine hang.
	waitForCondition(t, 15*time.Second, "finalize tail to arm the grace timer", func() bool {
		return fake.PendingCallbacks()-callbacksBefore == 1
	})
	tree := sess.delegateController
	runtime := sub.sess

	// Plant the residue and refuse twice through the entrypoint the timer
	// fires into: each refusal re-arms through the same funnel, so the two
	// retries must collapse onto the one outstanding grace timer.
	runtime.pendingJobNotifsMu.Lock()
	runtime.pendingJobNotifs = append(runtime.pendingJobNotifs, jobNotification{
		Kind:   jobNotificationKindTerminal,
		JobID:  "job-residue-sentinel",
		Status: "completed",
	})
	runtime.pendingJobNotifsMu.Unlock()
	for range 2 {
		if runtime.releaseIdleRuntimeAfterFinalize() {
			t.Fatal("release succeeded with queued residue; the pre-gate must refuse")
		}
	}
	if got := fake.PendingCallbacks() - callbacksBefore; got != 1 {
		t.Fatalf("two transient refusals left %d grace timers armed, want the single re-armed one", got)
	}

	// The residue settles; the one surviving timer — not any caller — must
	// release the runtime exactly once, and the success re-arms nothing.
	runtime.pendingJobNotifsMu.Lock()
	runtime.pendingJobNotifs = nil
	runtime.pendingJobNotifsMu.Unlock()
	fake.Advance(delegateIdleReleaseDelayDefault + time.Second)
	// TRIPWIRE: the surviving timer already fired during the advance, so the
	// release lands here in real time only as a goroutine handoff; 15s only
	// bounds a genuine hang.
	waitForCondition(t, 15*time.Second, "surviving grace timer to release the runtime after residue settled", func() bool {
		tree.mu.Lock()
		released := tree.live[res.DelegateID] == nil || tree.live[res.DelegateID].runtime == nil
		tree.mu.Unlock()
		return released
	})
	fake.Drain()
	if got := fake.PendingCallbacks() - callbacksBefore; got != 0 {
		t.Fatalf("successful release left %d grace timers armed, want none", got)
	}
}

// TestDelegateIdleRelease_CrossSessionArmReplacesPriorWindow pins the
// controller-side keying: a cold restore installs a new *Session for the same
// delegate, and the restored runtime's finalize must retire the window the
// previous runtime armed. Arming from two distinct sessions for one delegate
// leaves exactly one armed timer — the first session's handle stopped, not
// merely superseded at fire time — where a session-keyed map would leave both
// armed.
func TestDelegateIdleRelease_CrossSessionArmReplacesPriorWindow(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 4, 2)
	fake := agenttest.NewFakeClock()
	session := func() *Session {
		return &Session{delegateController: c, owningDelegateID: "dlg_x", clock: fake}
	}
	timersBefore := fake.BlockedCount()
	session().scheduleIdleRuntimeRelease(1)
	if got := fake.BlockedCount() - timersBefore; got != 1 {
		t.Fatalf("first arm parked %d grace timers, want 1", got)
	}
	session().scheduleIdleRuntimeRelease(2)
	if got := fake.BlockedCount() - timersBefore; got != 1 {
		t.Fatalf("cross-session arm parked %d grace timers, want the single replaced one", got)
	}
}

// TestDelegateIdleRelease_StaleArmDoesNotDisplaceNewerGeneration pins the
// generation check in the timer swap: arming a timer and installing it are two
// steps, so an older retry paused between them can resume after a newer
// generation's finalize installed its own. The stale install must be
// rejected — the stale callback would decline on the generation guard and
// leave the newer generation with no grace timer at all — and the stale timer
// is the one stopped.
func TestDelegateIdleRelease_StaleArmDoesNotDisplaceNewerGeneration(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 4, 2)
	fake := agenttest.NewFakeClock()
	s := &Session{delegateController: c, owningDelegateID: "dlg_x", clock: fake}
	timersBefore := fake.BlockedCount()
	s.scheduleIdleRuntimeRelease(2)

	// The paused older retry (generation 1) completes its arm after the newer
	// generation installed its own. Its callback is the spy: it must never
	// fire, because the swap hands the stale timer back and the caller stops
	// it instead of displacing the newer generation's window.
	staleFired := false
	stale := fake.AfterFunc(time.Hour, func() { staleFired = true })
	displaced := c.swapIdleReleaseTimer("dlg_x", stale, 1, 99, new(atomic.Bool))
	if displaced == nil {
		t.Fatal("stale arm displaced nothing; the swap must hand a timer back for stopping")
	}
	if displaced != stale {
		t.Fatal("the swap handed the newer generation's timer back for stopping; a stale arm must not displace it")
	}
	displaced.Stop()
	fake.Advance(time.Hour + time.Second)
	fake.Drain()
	if staleFired {
		t.Fatal("a stale generation's arm displaced the newer generation's grace timer; the stale callback fired an hour later")
	}
	if got := fake.BlockedCount() - timersBefore; got != 0 {
		t.Fatalf("stale-arm probe left %d grace timers parked, want none after the advance", got)
	}
}

// TestDelegateIdleRelease_FiredTimerLeavesNoHandle pins the entry retirement:
// a fired callback drops its own installed handle, so the map never
// accumulates spent timer objects — each pinning its callback closure and the
// *Session it captures — for the life of the process.
func TestDelegateIdleRelease_FiredTimerLeavesNoHandle(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 4, 2)
	fake := agenttest.NewFakeClock()
	s := &Session{delegateController: c, owningDelegateID: "dlg_x", clock: fake}
	s.scheduleIdleRuntimeRelease(1)
	fake.Advance(delegateIdleReleaseDelayDefault + time.Second)
	fake.Drain()
	c.mu.Lock()
	_, installed := c.idleReleaseTimers["dlg_x"]
	c.mu.Unlock()
	if installed {
		t.Fatal("fired grace timer left its handle installed; a spent timer must not pin its closure for the life of the process")
	}
}

// TestDelegateIdleRelease_CloseSweepsGraceTimers pins the shutdown sweep: a
// closing controller stops and drops every installed grace-timer handle, so
// the tree's teardown leaves no armed waiter and no retained closure behind.
func TestDelegateIdleRelease_CloseSweepsGraceTimers(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 4, 2)
	fake := agenttest.NewFakeClock()
	s := &Session{delegateController: c, owningDelegateID: "dlg_x", clock: fake}
	timersBefore := fake.BlockedCount()
	s.scheduleIdleRuntimeRelease(1)
	if got := fake.BlockedCount() - timersBefore; got != 1 {
		t.Fatalf("arm parked %d grace timers, want 1", got)
	}
	if err := c.closeRuntimeTree(context.Background()); err != nil {
		t.Fatalf("closeRuntimeTree: %v", err)
	}
	if got := fake.BlockedCount() - timersBefore; got != 0 {
		t.Fatalf("closing left %d grace timers parked on the clock, want the sweep to stop them", got)
	}
	c.mu.Lock()
	_, installed := c.idleReleaseTimers["dlg_x"]
	c.mu.Unlock()
	if installed {
		t.Fatal("closing left a grace-timer handle installed")
	}
}

// TestDelegateIdleRelease_OlderSameGenerationArmDoesNotDisplaceRetry pins the
// arm-sequence clause: a same-generation arm paused between creating its
// timer and installing it must not displace a newer retry of the same
// generation — if the paused timer has already fired, the displacement would
// stop the live retry and install a spent handle, leaving the delegate with no
// grace window at all.
func TestDelegateIdleRelease_OlderSameGenerationArmDoesNotDisplaceRetry(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 4, 2)
	fake := agenttest.NewFakeClock()
	timersBefore := fake.BlockedCount()
	// The newer same-generation retry installs first.
	newer := fake.AfterFunc(time.Hour, func() {})
	if displaced := c.swapIdleReleaseTimer("dlg_x", newer, 7, 2, new(atomic.Bool)); displaced != nil {
		t.Fatalf("first install displaced a timer, want none outstanding")
	}
	// The older arm, paused between arming and installing, completes last. It
	// must get its own timer back — the live retry stays installed.
	older := fake.AfterFunc(time.Hour, func() {})
	if displaced := c.swapIdleReleaseTimer("dlg_x", older, 7, 1, new(atomic.Bool)); displaced != older {
		t.Fatal("an older same-generation arm displaced the live retry; the swap must hand the stale timer back for stopping")
	}
	older.Stop()
	if got := fake.BlockedCount() - timersBefore; got != 1 {
		t.Fatalf("%d waiters parked after the stale arm stopped, want the newer retry alone", got)
	}
	c.mu.Lock()
	installed := c.idleReleaseTimers["dlg_x"]
	c.mu.Unlock()
	if installed.arm != 2 || installed.timer != newer {
		t.Fatalf("installed handle = %+v, want the newer retry (arm 2)", installed)
	}
}

// TestDelegateIdleRelease_ArmAfterCloseIsRejected pins the closing clause:
// the close's timer sweep empties the map exactly once, so an arm that
// reaches the swap after the sweep must be rejected — installing would arm a
// callback (and pin the *Session it captures) that no later close stops.
func TestDelegateIdleRelease_ArmAfterCloseIsRejected(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 4, 2)
	fake := agenttest.NewFakeClock()
	c.mu.Lock()
	c.closing = true
	c.mu.Unlock()
	timersBefore := fake.BlockedCount()
	timer := fake.AfterFunc(time.Hour, func() {})
	if displaced := c.swapIdleReleaseTimer("dlg_x", timer, 7, 1, new(atomic.Bool)); displaced != timer {
		t.Fatal("a post-close arm installed; the swap must hand the timer back for stopping")
	}
	timer.Stop()
	if got := fake.BlockedCount() - timersBefore; got != 0 {
		t.Fatalf("%d waiters remain after the post-close arm stopped", got)
	}
	c.mu.Lock()
	_, installed := c.idleReleaseTimers["dlg_x"]
	c.mu.Unlock()
	if installed {
		t.Fatal("a post-close arm installed a handle the close's sweep will never reach")
	}
}

// TestDelegateIdleRelease_FiredArmIsNeverInstalled pins the spent-arm clause:
// the immediate-dispatch path (or any real preemption longer than the delay)
// can run the whole callback before the install step, and the callback's own
// retire found no entry to drop — installing would pin the spent closure for
// the life of the process, the exact leak the retirement exists to remove.
func TestDelegateIdleRelease_FiredArmIsNeverInstalled(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 4, 2)
	fake := agenttest.NewFakeClock()
	timersBefore := fake.BlockedCount()
	spent := new(atomic.Bool)
	spent.Store(true)
	timer := fake.AfterFunc(time.Hour, func() {})
	if displaced := c.swapIdleReleaseTimer("dlg_x", timer, 7, 1, spent); displaced != timer {
		t.Fatal("an already-fired arm installed; the swap must hand the spent timer back for stopping")
	}
	timer.Stop()
	c.mu.Lock()
	_, installed := c.idleReleaseTimers["dlg_x"]
	c.mu.Unlock()
	if installed {
		t.Fatal("a spent timer was installed as the delegate's outstanding window")
	}
	if got := fake.BlockedCount() - timersBefore; got != 0 {
		t.Fatalf("%d waiters remain after the fired arm stopped", got)
	}
}

// TestDelegateIdleRelease_PregateRefusesLocalRetirementResidue: the idle
// release pre-gate must refuse on the same session-local residue retirement
// refuses on — a delegate-delivery parcel the pump has not consumed, restore
// side effects still settling on the manager's children, and a manager that
// has begun closing — because a teardown through any of them would abandon
// work mid-flight. Each refusal re-arms the retry, so the settled runtime
// still releases through the timer, never through a forced teardown.
func TestDelegateIdleRelease_PregateRefusesLocalRetirementResidue(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	adapter := &fakeAdapter{name: "openai"}
	client := llm.NewClient()
	client.Register(adapter)
	profile := withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2"))
	fake := agenttest.NewFakeClock()
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(workspace), SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 1,
		ForceRealIO:      true,
		clock:            fake,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			sandboxProber:       bwrapCapableProber(workspace),
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)

	res := sess.createDelegate(context.Background(), delegateArgs{Task: "idle sentinel"})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v (status=%s reason=%s)", res.Err, res.Status, res.Reason)
	}
	sub := sess.subagents.get(res.ChildSessionID)
	if sub == nil {
		t.Fatalf("delegate missing from manager: %+v", res)
	}
	sub.mu.Lock()
	done := sub.done
	sub.mu.Unlock()
	select {
	case <-done:
	case <-time.After(10 * time.Second): // TRIPWIRE: fixture rendezvous normally takes milliseconds; this only bounds a deadlock.
		t.Fatal("delegate runner did not finish")
	}
	tree := sess.delegateController
	runtime := sub.sess

	stillResident := func() bool {
		tree.mu.Lock()
		defer tree.mu.Unlock()
		return tree.live[res.DelegateID] != nil && tree.live[res.DelegateID].runtime != nil
	}

	// Residue 1: a queued delegate-delivery parcel the pump has not consumed.
	// Virtual time has not moved, so the finalize-armed grace timer cannot
	// fire behind the plant.
	runtime.delegateDeliveryMu.Lock()
	runtime.delegateDeliveryPumping = true
	runtime.delegateDeliveryMu.Unlock()
	if runtime.releaseIdleRuntimeAfterFinalize() {
		t.Fatal("release succeeded with an active delivery pass; the pre-gate must refuse")
	}
	if !stillResident() {
		t.Fatal("refused release released the runtime anyway")
	}
	runtime.delegateDeliveryMu.Lock()
	runtime.delegateDeliveryPumping = false
	runtime.delegateDeliveryMu.Unlock()

	// Residue 2: restore side effects still settling on this manager.
	runtime.subagents.mu.Lock()
	runtime.subagents.activeRestoreSideEffects++
	runtime.subagents.mu.Unlock()
	if runtime.releaseIdleRuntimeAfterFinalize() {
		t.Fatal("release succeeded with restore side effects settling; the pre-gate must refuse")
	}
	if !stillResident() {
		t.Fatal("refused release released the runtime anyway")
	}
	runtime.subagents.mu.Lock()
	runtime.subagents.activeRestoreSideEffects--
	runtime.subagents.mu.Unlock()

	// Residue 3: the manager has begun closing.
	runtime.subagents.mu.Lock()
	runtime.subagents.closing = true
	runtime.subagents.mu.Unlock()
	if runtime.releaseIdleRuntimeAfterFinalize() {
		t.Fatal("release succeeded with the manager closing; the pre-gate must refuse")
	}
	if !stillResident() {
		t.Fatal("refused release released the runtime anyway")
	}
	runtime.subagents.mu.Lock()
	runtime.subagents.closing = false
	runtime.subagents.mu.Unlock()

	// Every refusal re-armed a retry; one advance past the grace fires the
	// single outstanding retry the re-arms converged on, and it releases the
	// now-quiescent runtime.
	fake.Advance(delegateIdleReleaseDelayDefault + time.Second)
	// TRIPWIRE: every residue plant is cleared and the grace timers have all
	// fired, so the release only needs a goroutine handoff; 15s bounds a
	// genuine hang.
	waitForCondition(t, 15*time.Second, "re-armed retry to release the runtime after local retirement residue settled", func() bool {
		tree.mu.Lock()
		defer tree.mu.Unlock()
		return tree.live[res.DelegateID] == nil || tree.live[res.DelegateID].runtime == nil
	})
}

// TestDelegateAttentionRestore_HoldsOffIdleReleaseMidWake pins the one window
// a grace timer can reap a runtime the attention wake is about to use: a cold
// restoration installs the runtime with no generation advance, so until the
// reservation commits the runtime reads as plain terminal-idle to the release
// claims. The wake pass must hold the delegate off ClaimIdleRuntimeRelease for
// the whole restore-to-decision span — a claim landing first commits the
// reservation against a husk and the wake retry pays a second cold restore —
// and must release the hold at pass exit, declined or not, so a declined wake
// leaves the restored runtime reapable instead of pinned warm forever.
func TestDelegateAttentionRestore_HoldsOffIdleReleaseMidWake(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	adapter := &fakeAdapter{name: "openai"}
	client := llm.NewClient()
	client.Register(adapter)
	profile := withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2"))
	fake := agenttest.NewFakeClock()
	var wakeHook func(delegateID string, restored *subagent)
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(workspace), SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 1,
		ForceRealIO:      true,
		clock:            fake,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			sandboxProber:       bwrapCapableProber(workspace),
			afterDelegateAttentionRestore: func(delegateID string, restored *subagent) {
				if wakeHook != nil {
					wakeHook(delegateID, restored)
				}
			},
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)

	res := sess.createDelegate(context.Background(), delegateArgs{Task: "idle sentinel"})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v (status=%s reason=%s)", res.Err, res.Status, res.Reason)
	}
	sub := sess.subagents.get(res.ChildSessionID)
	if sub == nil {
		t.Fatalf("delegate missing from manager: %+v", res)
	}
	sub.mu.Lock()
	done := sub.done
	sub.mu.Unlock()
	select {
	case <-done:
	case <-time.After(10 * time.Second): // TRIPWIRE: fixture rendezvous normally takes milliseconds; this only bounds a deadlock.
		t.Fatal("delegate runner did not finish")
	}
	runtime := sub.sess
	if !runtime.releaseIdleRuntimeAfterFinalize() {
		t.Fatal("fixture: idle release refused; setup broken")
	}
	tree := sess.delegateController
	if _, err := tree.openDelegateAttention(res.DelegateID, "delegate:mid-wake-sentinel"); err != nil {
		t.Fatalf("open delegate attention: %v", err)
	}

	hookFired := false
	var restoredSub *subagent
	wakeHook = func(delegateID string, restored *subagent) {
		hookFired = true
		restoredSub = restored
		if delegateID != res.DelegateID {
			t.Errorf("wake pass restored %q, want %q", delegateID, res.DelegateID)
		}
		// The mid-wake claim must refuse: the wake pass owns this runtime
		// until it reserves or declines.
		claim, _, err := tree.ClaimIdleRuntimeRelease(res.DelegateID)
		if err != nil {
			t.Fatalf("claim during attention wake: %v", err)
		}
		if claim != nil {
			_ = tree.AbortRuntimeReclamation(claim)
			t.Fatalf("idle release claimed a runtime mid attention wake; the pass must hold the delegate until the wake decides")
		}
		// Decline the drive so the pass exits without a reservation; the
		// pending attention stays durable for the wake retry to re-drive.
		restored.mu.Lock()
		restored.running = true
		restored.mu.Unlock()
		// A second, overlapping wake pass selects the same delegate: holds
		// must count, so this pass's exit cannot drop the other pass's hold.
		tree.holdAttentionRestore(res.DelegateID)
	}
	sess.delegateController.recheckNotificationReceivers()
	if !hookFired {
		t.Fatal("wake pass restored nothing; fixture setup broken")
	}
	if restoredSub == nil {
		t.Fatal("wake pass reported no restored subagent")
	}
	restoredSub.mu.Lock()
	restoredSub.running = false
	restoredSub.mu.Unlock()

	// This pass exited, but the overlapping pass still holds the delegate:
	// the claim must keep refusing.
	claim, _, err := tree.ClaimIdleRuntimeRelease(res.DelegateID)
	if err != nil {
		t.Fatalf("claim while an overlapping wake pass still holds: %v", err)
	}
	if claim != nil {
		_ = tree.AbortRuntimeReclamation(claim)
		t.Fatal("a pass exit dropped an overlapping pass's hold; holds must count")
	}
	// The overlapping pass finishes: its release reopens the claim window,
	// so the restored runtime is plain terminal-idle again and the claim
	// succeeds; the abort hands residency straight back.
	tree.releaseAttentionRestoreHold(res.DelegateID)
	claim, _, err = tree.ClaimIdleRuntimeRelease(res.DelegateID)
	if err != nil {
		t.Fatalf("claim after the last overlapping pass exited: %v", err)
	}
	if claim == nil {
		t.Fatal("released holds left the delegate unclaimable; the last pass's exit must reopen the claim window")
	}
	_ = tree.AbortRuntimeReclamation(claim)
}

// TestDelegateAttentionRestoreHold_RefusesSubtreeBeforeLiveEntry: a held
// delegate with no live entry yet — a cold restore mid-install, before the
// runtime publishes — must still refuse reclamation of any subtree
// containing it, so the restored parent a chain restore is still using
// cannot be swept by a grace timer while the child install is in flight.
func TestDelegateAttentionRestoreHold_RefusesSubtreeBeforeLiveEntry(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 4, 2)
	seedDelegateControllerIdle(t, c, "dlg_parent", "")
	seedDelegateControllerIdle(t, c, "dlg_child", "dlg_parent")
	// The child's generation must run under a live parent, so interleave:
	// start the parent, finish the child, then finish the parent.
	parentReservation, err := c.ReserveStart(rootDelegateActor("root-session"), "dlg_parent")
	if err != nil {
		t.Fatalf("reserve parent start: %v", err)
	}
	parentStarted, err := c.CommitStart(parentReservation)
	if err != nil {
		t.Fatalf("commit parent start: %v", err)
	}
	if err := attachRuntimeWithHandoffForTest(c, parentStarted.lease, &Session{}); err != nil {
		t.Fatalf("attach parent runtime: %v", err)
	}
	if _, err := c.AdmitStartInput(parentStarted.lease, func() error { return nil }); err != nil {
		t.Fatalf("admit parent input: %v", err)
	}
	finishHarnessDelegateGeneration(t, c, delegateActor{lease: &parentStarted.lease}, "dlg_child")
	if _, err := c.FinishGeneration(parentStarted.lease, delegateFinish{outcome: delegatestore.OutcomeCompleted, reason: "fixture"}); err != nil {
		t.Fatalf("finish parent generation: %v", err)
	}
	// The parent is now genuinely resident terminal-idle: one finished
	// generation, a published runtime, no run binding. The harness finishes
	// leave the terminal-packet deliveries unaccepted; abort them so the
	// fixture is plain idle. The child is mid cold restore: its durable
	// generation finished terminal-idle, and its previous runtime was
	// released — the pre-install span, before the restored runtime publishes
	// to the live map.
	c.mu.Lock()
	tokens := make([]delegateDeliveryToken, 0, len(c.deliveries))
	for _, receipt := range c.deliveries {
		tokens = append(tokens, receipt.token)
	}
	c.mu.Unlock()
	for _, token := range tokens {
		if _, err := c.CompleteDelivery(token, false); err != nil {
			t.Fatalf("abort harness delivery: %v", err)
		}
	}
	c.mu.Lock()
	c.live["dlg_child"] = nil
	// The harness has no parent pump to consume the terminal-packet
	// delivery admissions its direct FinishGeneration calls create; drop
	// them to simulate the delivered state.
	c.deliveryClaims = nil
	c.mu.Unlock()
	c.holdAttentionRestore("dlg_child")
	defer c.releaseAttentionRestoreHold("dlg_child")
	claim, _, err := c.ClaimIdleRuntimeRelease("dlg_parent")
	if err != nil {
		t.Fatalf("claim parent subtree with held child: %v", err)
	}
	if claim != nil {
		_ = c.AbortRuntimeReclamation(claim)
		t.Fatal("claim swept a subtree whose member is held mid cold restore; the hold must refuse before the live-entry check")
	}
}

// TestDelegateAttentionRestoreHold_ExcludedFromReclamationCapacity pins the
// held check inside isResidentTerminalRuntimeLocked on the capacity path: a
// held delegate must be excluded from BOTH the resident count and the claim's
// candidates. If it were counted but not claimable, needed would demand one
// more slot than the claimable candidates cover, and the admission would fail
// with a capacity refusal produced solely by the held entry — spurious,
// because the one free candidate satisfies the truthful requirement exactly.
func TestDelegateAttentionRestoreHold_ExcludedFromReclamationCapacity(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 4, 2)
	c.maxRetainedTerminal = 2
	held := seedDelegateReclaimRuntime(t, c, "dlg_held", "", time.Unix(5, 0).UTC(), false, false)
	seedDelegateReclaimRuntime(t, c, "dlg_free", "", time.Unix(10, 0).UTC(), false, false)
	c.holdAttentionRestore("dlg_held")
	defer c.releaseAttentionRestoreHold("dlg_held")

	// required == maxRetainedTerminal is the boundary: the truthful resident
	// count — dlg_free alone — requires exactly the one claimable slot, so the
	// claim must succeed; counting the held delegate too would demand two and
	// refuse where one free candidate already covers the need.
	claim, err := c.ClaimRuntimeReclamation(2)
	if err != nil {
		t.Fatalf("ClaimRuntimeReclamation with a held resident: %v", err)
	}
	if claim == nil {
		t.Fatal("ClaimRuntimeReclamation declined with a held resident; the held delegate must not inflate the capacity requirement")
	}
	if got := reclamationDelegateIDs(claim); !reflect.DeepEqual(got, []string{"dlg_free"}) {
		t.Fatalf("claimed runtimes = %v, want exactly dlg_free; a held delegate is neither capacity nor a candidate", got)
	}
	c.mu.Lock()
	heldRuntime := c.live["dlg_held"].runtime
	_, heldFenced := c.reclaiming["dlg_held"]
	c.mu.Unlock()
	if heldRuntime != held {
		t.Fatalf("held delegate's runtime was claimed or cleared: got %p, want %p", heldRuntime, held)
	}
	if heldFenced {
		t.Fatal("claim fenced the held delegate's runtime; a held delegate must not be claimed")
	}
	_ = c.AbortRuntimeReclamation(claim)
}

// finishHarnessDelegateGeneration drives one harness delegate through a
// completed generation — reserve, commit, attach, admit, finish — leaving it
// durable terminal-idle with a published live runtime.
func finishHarnessDelegateGeneration(t *testing.T, c *delegateTreeController, actor delegateActor, id string) {
	t.Helper()
	reservation, err := c.ReserveStart(actor, id)
	if err != nil {
		t.Fatalf("reserve %s start: %v", id, err)
	}
	started, err := c.CommitStart(reservation)
	if err != nil {
		t.Fatalf("commit %s start: %v", id, err)
	}
	if err := attachRuntimeWithHandoffForTest(c, started.lease, &Session{}); err != nil {
		t.Fatalf("attach %s runtime: %v", id, err)
	}
	if _, err := c.AdmitStartInput(started.lease, func() error { return nil }); err != nil {
		t.Fatalf("admit %s input: %v", id, err)
	}
	if _, err := c.FinishGeneration(started.lease, delegateFinish{outcome: delegatestore.OutcomeCompleted, reason: "fixture"}); err != nil {
		t.Fatalf("finish %s generation: %v", id, err)
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
