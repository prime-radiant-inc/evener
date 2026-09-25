package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/sandbox"
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

	var claim *delegateRuntimeReclamationClaim
	// The claim refuses until every member is terminal-idle; poll rather than
	// assume the finalize tail has fully settled. TRIPWIRE: settle normally
	// takes milliseconds; 15s only bounds a deadlock.
	waitForCondition(t, 15*time.Second, "terminal subtree of "+parentRes.DelegateID+" to become claimable", func() bool {
		claim, _, err = tree.ClaimIdleRuntimeRelease(parentRes.DelegateID)
		if err != nil {
			t.Fatalf("ClaimIdleRuntimeRelease: %v", err)
		}
		return claim != nil
	})
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
	wideChildren := 4

	workspace := t.TempDir()
	// The adapter scripts the parent's turn as ONE response carrying all
	// four delegate spawns; every later step is an identical finish. The
	// spawns all fire inside the parent's first turn, so the tree's SHAPE
	// is fixed before any follow-up call happens: the parent's post-tool
	// call and each child's first call consume the five generically
	// interchangeable finish steps in whatever global order they race, and
	// each ends its member's turn. Spawning from a finished parent is not
	// an option (its lease is spent), and spawning one-at-a-time across
	// turns lets the parent eat a child's finish step — the one-response
	// spawn wave avoids both.
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
		func(llm.Request) llm.Response { return finalResponse("member done") },
		func(llm.Request) llm.Response { return finalResponse("member done") },
		func(llm.Request) llm.Response { return finalResponse("member done") },
		func(llm.Request) llm.Response { return finalResponse("member done") },
		func(llm.Request) llm.Response { return finalResponse("member done") },
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
		// flight. TRIPWIRE: under the bounded-concurrency release the last
		// child arrives within milliseconds, so this wait costs nothing when
		// the contract holds; it only burns the timeout on a serial
		// implementation, where the max-in-flight assertion below fails
		// anyway. waitForCondition cannot run here: the probe fires on a
		// teardown goroutine, not the test's.
		deadline := time.Now().Add(3 * time.Second)
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
		NoProjectPrompts: true,
		ForceRealIO:      true,
		testOnly: testConfig{
			skipGitSnapshot:            true,
			minimalSystemPrompt:        true,
			sandboxProber:              bwrapCapableProber(workspace),
			disableDelegateIdleRelease: true,
			idleTeardownConcurrency:    &wideChildren,
			idleTeardownMemberStarted:  memberStarted,
			idleTeardownMemberSettled:  memberSettled,
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)

	four := wideChildren
	parentRes := sess.createDelegate(context.Background(), delegateArgs{
		Task:                "spawn four leaf children, then finish",
		DelegationAllowance: &four,
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
	case <-time.After(20 * time.Second): // TRIPWIRE: the scripted turn takes seconds; this only bounds a deadlock.
		t.Fatal("parent delegate runner did not finish")
	}

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
		childSub.mu.Lock()
		childDone := childSub.done
		childSub.mu.Unlock()
		select {
		case <-childDone:
		case <-time.After(20 * time.Second): // TRIPWIRE: the children's scripted turns take seconds; this only bounds a deadlock.
			t.Fatalf("child delegate runner %d did not finish", i)
		}
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

	var claim *delegateRuntimeReclamationClaim
	// The claim refuses until every member is terminal-idle; poll rather than
	// assume the finalize tail has fully settled. TRIPWIRE: settle normally
	// takes milliseconds; 15s only bounds a deadlock.
	waitForCondition(t, 15*time.Second, "terminal subtree of "+parentRes.DelegateID+" to become claimable", func() bool {
		claim, _, err = tree.ClaimIdleRuntimeRelease(parentRes.DelegateID)
		if err != nil {
			t.Fatalf("ClaimIdleRuntimeRelease: %v", err)
		}
		return claim != nil
	})
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

	probeMu.Lock()
	maxInFlight := childMaxInFlight
	parentStart, parentStarted := startedAt[parentRes.ChildSessionID]
	probeMu.Unlock()
	if !parentStarted {
		t.Fatal("the claim root's member teardown never started")
	}
	if maxInFlight != wideChildren {
		t.Fatalf("idle release settled the same-depth members serially: max concurrent child teardowns = %d, want %d", maxInFlight, wideChildren)
	}
	for _, id := range childIDs {
		probeMu.Lock()
		childSettled, settled := settledAt[childRuntimes[id].id]
		probeMu.Unlock()
		if !settled {
			t.Fatalf("child %s teardown never settled", id)
		}
		if childSettled >= parentStart {
			t.Fatalf("claim root started its teardown at seq %d before descendant %s settled at seq %d: leaf-first violated", parentStart, id, childSettled)
		}
	}

	// Every teardown body ran to completion: each member's pass is spent.
	if err := parentSess.releaseRuntime(context.Background(), closeOptions{}, releaseRetirement); !errors.Is(err, errRetirementTeardownSpent) {
		t.Fatalf("parent runtime teardown pass after release: err = %v, want errRetirementTeardownSpent", err)
	}
	for _, id := range childIDs {
		if err := childRuntimes[id].releaseRuntime(context.Background(), closeOptions{}, releaseRetirement); !errors.Is(err, errRetirementTeardownSpent) {
			t.Fatalf("child %s teardown pass after release: err = %v, want errRetirementTeardownSpent", id, err)
		}
	}
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

// TestDelegateIdleRelease_RetriesAfterPregateRefusal: a grace timer that
// fires into residue must not lose the release to a one-shot refusal — the
// refusal re-arms one more grace window, and the retry, not any caller,
// releases the runtime once the residue settles. The session clock is the
// package's fake, so the grace timers fire only when the test advances
// virtual time: the residue plant can never race the first timer.
func TestDelegateIdleRelease_RetriesAfterPregateRefusal(t *testing.T) {
	workspace := t.TempDir()
	adapter := &fakeAdapter{name: "openai"}
	client := llm.NewClient()
	client.Register(adapter)
	profile := withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2"))
	fake := agenttest.NewFakeClock()
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(workspace), SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
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
	workspace := t.TempDir()
	adapter := &fakeAdapter{name: "openai"}
	client := llm.NewClient()
	client.Register(adapter)
	profile := withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2"))
	fake := agenttest.NewFakeClock()
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(workspace), SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
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

// TestDelegateIdleRelease_ColdRestoreResumesRetainedScratch pins the scratch
// continuity of the same-process cold restore: an idle-released delegate's next
// send must resume in its ORIGINAL scratch directory, artifacts intact, not in
// a fresh mint. The retained-scratch pool is a snapshot of the root's manifest
// at init; a delegate created after init never entered it, and the release
// gives the lease back without the pool learning — so the restore path must
// converge to the live manifest (the same rows a fresh daemon adopts from)
// rather than trust the init-time pool alone. The second release/restore cycle
// additionally pins that a released runtime's stale adoption record cannot
// strand the delegate: the reacquire proves the record stale and clears it.
func TestDelegateIdleRelease_ColdRestoreResumesRetainedScratch(t *testing.T) {
	// Isolate TMPDIR (this test is not parallel): the sandboxed children mint
	// their scratch under the ambient base, and this test's root stateDir is a
	// TempDir that vanishes at cleanup — a child dir left in the SHARED base
	// would keep a pin pointing at the deleted manifest and trip every later
	// sweep that validates that base's pins. Under an isolated base the
	// cleanup removes directory and pin together.
	isolated := t.TempDir()
	t.Setenv("TMPDIR", isolated)
	_, home := sbxLane(t)
	facts := sbxBwrapFacts(home)
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("done one") },
		func(llm.Request) llm.Response { return agenttest.FinalResponse("done two") },
		func(llm.Request) llm.Response { return agenttest.FinalResponse("done three") },
	}})
	shortGrace := 100 * time.Millisecond
	s := newSession(t, withClient(client), withConfig(SessionConfig{
		StateDir:         packageFixtureTempDir(t, "scratch-continuity-*"),
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		testOnly: testConfig{
			skipGitSnapshot:          true,
			minimalSystemPrompt:      true,
			noSyncJobStore:           true,
			sandboxProber:            sandbox.FakeProber{Facts: facts},
			delegateIdleReleaseDelay: &shortGrace,
		},
	}))
	defer s.Close()
	tree := s.delegateController

	// TRIPWIRE: scripted adapters plus an in-process sandboxed child; the runs
	// and releases normally settle in well under a second each. 30s per stage
	// only bounds a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res := s.createDelegate(ctx, delegateArgs{Task: "own scratch", Sandbox: "workspace-write", DelegationAllowance: new(0)})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v (status=%s reason=%s)", res.Err, res.Status, res.Reason)
	}
	child := s.subagents.get(res.ChildSessionID)
	if child == nil {
		t.Fatalf("subagent %s not found", res.ChildSessionID)
	}
	child.mu.Lock()
	done := child.done
	child.mu.Unlock()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("delegate run did not finish: %v", ctx.Err())
	}

	// The child env owns exactly the sandbox scratch EnableSandbox minted; pin
	// its identity and leave an artifact in it that only continuity preserves.
	scratchDir := sandboxedChildScratchDir(t, child.sess)
	artifact := filepath.Join(scratchDir, "artifact.txt")
	if err := os.WriteFile(artifact, []byte("durable"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}

	restoreAndCheck := func(label string, prev *subagent) *subagent {
		t.Helper()
		// TRIPWIRE: the 100ms grace normally fires within moments of the run
		// finishing; 15s only bounds a genuine hang.
		waitForCondition(t, 15*time.Second, "idle release of "+label, func() bool {
			tree.mu.Lock()
			released := tree.live[res.DelegateID] == nil || tree.live[res.DelegateID].runtime == nil
			tree.mu.Unlock()
			return released
		})
		// TRIPWIRE: the scripted send and restore complete in well under a
		// second; 30s only bounds a genuine hang.
		sendCtx, sendCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer sendCancel()
		send := (delegateRuntime{owner: s}).send(sendCtx, res.DelegateID, "run "+label, 0).result
		if send.Err != nil {
			t.Fatalf("delegate_send after idle release (%s): %+v", label, send)
		}
		var restored *subagent
		// TRIPWIRE: the cold restore normally appears within milliseconds of
		// the send; 15s only bounds a genuine hang.
		waitForCondition(t, 15*time.Second, "cold-restored record for "+label, func() bool {
			restored = s.subagents.get(res.ChildSessionID)
			return restored != nil && restored != prev && restored.sess != nil
		})
		restored.mu.Lock()
		rdone := restored.done
		restored.mu.Unlock()
		select {
		case <-rdone:
		case <-sendCtx.Done():
			t.Fatalf("restored run (%s) did not finish: %v", label, sendCtx.Err())
		}
		if got := sandboxedChildScratchDir(t, restored.sess); filepath.Clean(got) != filepath.Clean(scratchDir) {
			t.Fatalf("restored delegate (%s) resumed in fresh scratch %q, want the retained original %q", label, got, scratchDir)
		}
		if data, err := os.ReadFile(artifact); err != nil || string(data) != "durable" {
			t.Fatalf("restored delegate (%s) lost its scratch artifact: read err = %v", label, err)
		}
		return restored
	}
	child = restoreAndCheck("first restore", child)
	_ = restoreAndCheck("second restore", child)
}

// sandboxedChildScratchDir returns the sandbox scratch directory the child
// session's own environment holds, the identity a cold restore must preserve.
func sandboxedChildScratchDir(t *testing.T, sess *Session) string {
	t.Helper()
	local, ok := sess.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatal("child env is not a LocalExecutionEnvironment")
	}
	refs, err := local.ScratchRetentionReferences()
	if err != nil {
		t.Fatalf("child scratch references: %v", err)
	}
	for _, ref := range refs {
		if ref.Kind == sandbox.ScratchKindSandbox {
			return ref.Dir
		}
	}
	t.Fatal("sandboxed child owns no sandbox scratch")
	return ""
}

// TestDelegateIdleRelease_PregateRefusesLocalRetirementResidue: the idle
// release pre-gate must refuse on the same session-local residue retirement
// refuses on — a delegate-delivery parcel the pump has not consumed, restore
// side effects still settling on the manager's children, and a manager that
// has begun closing — because a teardown through any of them would abandon
// work mid-flight. Each refusal re-arms the retry, so the settled runtime
// still releases through the timer, never through a forced teardown.
func TestDelegateIdleRelease_PregateRefusesLocalRetirementResidue(t *testing.T) {
	workspace := t.TempDir()
	adapter := &fakeAdapter{name: "openai"}
	client := llm.NewClient()
	client.Register(adapter)
	profile := withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2"))
	fake := agenttest.NewFakeClock()
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(workspace), SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
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
	runtime.pendingDelegateDeliveries = append(runtime.pendingDelegateDeliveries, delegateDeliveryPlan{})
	runtime.delegateDeliveryMu.Unlock()
	if runtime.releaseIdleRuntimeAfterFinalize() {
		t.Fatal("release succeeded with a queued delegate delivery; the pre-gate must refuse")
	}
	if !stillResident() {
		t.Fatal("refused release released the runtime anyway")
	}
	runtime.delegateDeliveryMu.Lock()
	runtime.pendingDelegateDeliveries = nil
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
		NoProjectPrompts: true,
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
	sess.drivePendingStableDelegateAttention()
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
	if err := c.AttachRuntime(parentStarted.lease, &Session{}); err != nil {
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
	if err := c.AttachRuntime(started.lease, &Session{}); err != nil {
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
