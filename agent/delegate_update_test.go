package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/internal/delegatestore"
	toolpkg "primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// A running delegate's update joins its own delivery queue behind whatever is
// already there. Only the head is planned, and its plan is the update's.
func TestDelegateControllerPostUpdateQueuesInDeliveryOrder(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	actor := delegateActor{lease: &delegateLease{delegateID: "dlg_target", generation: 1}}

	first, firstPlans, err := c.PostUpdate(actor, "first question")
	if err != nil {
		t.Fatalf("PostUpdate first: %v", err)
	}
	if !strings.HasPrefix(first, "dlg_target/update/") || len(firstPlans.deliveries) != 1 ||
		firstPlans.deliveries[0].deliveryID != first || firstPlans.deliveries[0].packet.Kind != delegatestore.PacketUpdate {
		t.Fatalf("first update %q planned %#v, want its own head plan", first, firstPlans.deliveries)
	}
	second, secondPlans, err := c.PostUpdate(actor, "second question")
	if err != nil {
		t.Fatalf("PostUpdate second: %v", err)
	}
	if len(secondPlans.deliveries) != 0 {
		t.Fatalf("second update planned behind the first: %#v", secondPlans.deliveries)
	}
	pending := delegateAggregateSnapshot(t, c, "dlg_target").PendingDeliveries
	if len(pending) != 2 || pending[0].DeliveryID != first || pending[1].DeliveryID != second {
		t.Fatalf("pending deliveries = %#v, want both updates in the order posted", pending)
	}
}

// An update posted through the controller and planned at the head never takes
// the generation's inline waiter. The caller waiting in delegate_send wants
// the report, which carries the update ahead of itself.
func TestDelegateControllerPostedUpdateHeadLeavesTheInlineWaiter(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	lease, waiter := startDelegateDeliveryGeneration(t, c, "dlg_target", true)
	if err := c.AttachRuntime(lease, &Session{}); err != nil {
		t.Fatalf("AttachRuntime: %v", err)
	}
	if _, err := c.AdmitStartInput(lease, func() error { return nil }); err != nil {
		t.Fatalf("AdmitStartInput: %v", err)
	}
	_, plans, err := c.PostUpdate(delegateActor{lease: &lease}, "which table?")
	if err != nil {
		t.Fatalf("PostUpdate: %v", err)
	}
	if len(plans.deliveries) != 1 || plans.deliveries[0].waiter != nil {
		t.Fatalf("update plans = %#v, want one head plan without the waiter", plans.deliveries)
	}
	c.mu.Lock()
	kept := c.live["dlg_target"].waiters[lease.generation]
	c.mu.Unlock()
	if kept != waiter {
		t.Fatalf("generation waiter = %p, want the registered %p still waiting for the report", kept, waiter)
	}
}

// A refused update is never queued: the root has no parent, a stale run cannot
// speak, a stopping or settling delegate's route is closed, a generation whose
// terminal communicate is latched has said its last, and a blank message says
// nothing.
func TestDelegateControllerPostUpdateRefusals(t *testing.T) {
	t.Parallel()
	running := delegateLease{delegateID: "dlg_target", generation: 1}
	for _, tc := range []struct {
		name    string
		message string
		actor   func(t *testing.T, c *delegateTreeController) delegateActor
		want    error
	}{
		{"root has no parent", "which table?", func(*testing.T, *delegateTreeController) delegateActor {
			return rootDelegateActor("root-session")
		}, errDelegateNotControllable},
		{"stale run", "which table?", func(*testing.T, *delegateTreeController) delegateActor {
			return delegateActor{lease: &delegateLease{delegateID: "dlg_target", generation: 2}}
		}, errDelegateStaleLease},
		{"stopping", "which table?", func(t *testing.T, c *delegateTreeController) delegateActor {
			if _, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target"); err != nil {
				t.Fatalf("StopSubtree: %v", err)
			}
			return delegateActor{lease: &running}
		}, errDelegateTargetBusy},
		{"settling", "which table?", func(t *testing.T, c *delegateTreeController) delegateActor {
			if _, _, err := c.BeginSettlement(running); err != nil {
				t.Fatalf("BeginSettlement: %v", err)
			}
			return delegateActor{lease: &running}
		}, errDelegateTargetBusy},
		{"terminal communicate latched", "which table?", func(t *testing.T, c *delegateTreeController) delegateActor {
			c.mu.Lock()
			c.live["dlg_target"].binding.evidence = &delegateGenerationEvidence{}
			c.mu.Unlock()
			if err := c.recordTerminalSeen(running); err != nil {
				t.Fatalf("recordTerminalSeen: %v", err)
			}
			return delegateActor{lease: &running}
		}, errDelegateTargetBusy},
		{"blank message", "  \n", func(*testing.T, *delegateTreeController) delegateActor {
			return delegateActor{lease: &running}
		}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, _ := newDelegateControllerTestHarness(t, 1, 1)
			seedDelegateControllerRunning(t, c, "dlg_target", "")
			_, plans, err := c.PostUpdate(tc.actor(t, c), tc.message)
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) || len(plans.deliveries) != 0 {
				t.Fatalf("PostUpdate = plans %#v err %v, want %v", plans.deliveries, err, tc.want)
			}
			if pending := delegateAggregateSnapshot(t, c, "dlg_target").PendingDeliveries; len(pending) != 0 {
				t.Fatalf("refused update queued: %#v", pending)
			}
		})
	}
}

// A delegate's communicate(end_turn=false) is an update its parent receives.
// It stays in the delegate's own transcript, lands in an idle root's
// transcript as delegate attention with the update frame, and wakes the root.
// The root's next notification turn presents it and consumes it, and the
// delegate's tool result says it was delivered. (Reworked from the dropped
// caller-route WIP test 6f67c3f327.)
func TestDelegateUpdateWakesAnIdleRoot(t *testing.T) {
	t.Parallel()
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return communicateResponse(false, "need the heading from you") },
		func(llm.Request) llm.Response { return finalResponse("summary written") },
		func(llm.Request) llm.Response { return finalResponse("noted") },
	}
	root := restoreSupervisionRoot(t, fixture, nil)
	wakes := make(chan struct{}, 1)
	root.SetNotifyFunc(func() {
		select {
		case wakes <- struct{}{}:
		default:
		}
	})
	warmStableDelegateUnservedRoot(t, root, fixture)

	pending := rootPendingAttention(root)
	if len(pending) != 1 || !strings.HasPrefix(pending[0], delegateAttentionID(fixture.delegateID+"/update/")) {
		t.Fatalf("root attention = %v, want the delegate's update alone", pending)
	}
	frame := rootAttentionFold(t, root).content[pending[0]].Text()
	for _, attr := range []string{`delegate_id="` + fixture.delegateID + `"`, `kind="update"`, ">need the heading from you</delegate-notification>"} {
		if !strings.Contains(frame, attr) {
			t.Fatalf("update frame %q lacks %q", frame, attr)
		}
	}
	select {
	case <-wakes:
	default:
		t.Fatal("the update did not wake the root")
	}
	requests := fixture.adapter.Requests()
	if len(requests) < 2 || !strings.Contains(lastToolResultText(requests[1].Messages), `"delivery":"delivered"`) {
		t.Fatalf("the delegate's communicate result does not say delivered")
	}
	var checkpoints []string
	for _, entry := range decodeTranscriptEntries(t, afero.NewOsFs(), transcriptPath(root.stateDir, fixture.childID)) {
		if entry.Turn.Kind == schema.TurnCommunicate && entry.Turn.Communicate != nil && !entry.Turn.Communicate.EndTurn {
			checkpoints = append(checkpoints, entry.Turn.Communicate.Message)
		}
	}
	if len(checkpoints) != 1 || checkpoints[0] != "need the heading from you" {
		t.Fatalf("delegate transcript updates = %#v, want the update recorded", checkpoints)
	}

	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := root.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
		t.Fatalf("root notification turn: %v", err)
	}
	requests = fixture.adapter.Requests()
	if !requestContainsText(requests[len(requests)-1], "need the heading from you") {
		t.Fatal("the root's notification turn did not present the update")
	}
	if got := rootAttentionFold(t, root).resolutions[pending[0]]; got != delegateAttentionConsumed {
		t.Fatalf("update attention = %q, want consumed", got)
	}
}

// An update the journal refuses still succeeds as a communicate call: the
// message is already in the delegate's transcript. The result says it was not
// delivered and why, and nothing is queued for the parent.
func TestDelegateUpdateNotDeliveredStillSucceeds(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		refuse func(t *testing.T, c *delegateTreeController, lease delegateLease) delegateLease
	}{
		{"stopping delegate", func(t *testing.T, c *delegateTreeController, lease delegateLease) delegateLease {
			if _, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), lease.delegateID); err != nil {
				t.Fatalf("StopSubtree: %v", err)
			}
			return lease
		}},
		{"stale run", func(_ *testing.T, _ *delegateTreeController, lease delegateLease) delegateLease {
			return delegateLease{delegateID: lease.delegateID, generation: lease.generation + 1}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, _ := newDelegateControllerTestHarness(t, 1, 1)
			seedDelegateControllerRunning(t, c, "dlg_target", "")
			runtime := attachDelegateSteerRuntime(t, c, "dlg_target", afero.NewMemMapFs())
			runtime.profile = NewOpenAIProfile("gpt-5.2")
			runtime.delegateController = c
			runtime.owningDelegateID = "dlg_target"
			runtime.cfg.spawn.parentSessionID = "root-session"
			lease := tc.refuse(t, c, delegateLease{delegateID: "dlg_target", generation: 1})

			reg := toolpkg.NewRegistry()
			registerCommunicateTool(reg, newToolDeps(runtime))
			ctx := context.WithValue(context.Background(), delegateRunLeaseContextKey{}, lease)
			out, err := reg.Get("communicate").Exec(ctx, nil, map[string]any{"message": "need the schema", "end_turn": false})
			if err != nil {
				t.Fatalf("communicate: %v", err)
			}
			var result map[string]any
			if err := json.Unmarshal([]byte(fmt.Sprint(out)), &result); err != nil {
				t.Fatalf("decode result %v: %v", out, err)
			}
			if result["accepted"] != true || result["delivery"] != "not_delivered" {
				t.Fatalf("result = %v, want accepted and not_delivered", result)
			}
			if reason, _ := result["reason"].(string); reason == "" {
				t.Fatalf("result = %v, want a reason", result)
			}
			if pending := delegateAggregateSnapshot(t, c, "dlg_target").PendingDeliveries; len(pending) != 0 {
				t.Fatalf("refused update queued: %#v", pending)
			}
		})
	}
}

// A root mid-turn holds a delegate's update for its next model boundary: the
// request after the tool round presents it, and the turn that presented it
// consumes it, so it earns no wake of its own.
func TestDelegateUpdateReachesARunningRootAtItsNextModelBoundary(t *testing.T) {
	t.Parallel()
	adapter := newMessagingAdapter("WORKER-BRIEF")
	release, deferred := make(chan struct{}), make(chan struct{})
	var deferredOnce sync.Once
	var root *Session
	adapter.script("root",
		func(llm.Request) llm.Response {
			return delegateToolResponse("create", "WORKER-BRIEF: ask your parent which table holds the drain cursor.")
		},
		func(llm.Request) llm.Response {
			close(release)
			awaitSignal(t, deferred, "the update deferred behind the root's turn")
			return jobListResponse("list")
		},
		func(req llm.Request) llm.Response {
			if !requestContainsText(req, `kind="update"`) || !requestContainsText(req, "RUNNING_UPDATE") {
				t.Error("the root's next request does not present the update")
			}
			return finalResponse("root done")
		},
	)
	adapter.script("WORKER-BRIEF",
		func(llm.Request) llm.Response {
			awaitSignal(t, release, "the root's second request")
			return updateResponse("upd", "RUNNING_UPDATE: which table holds the drain cursor?")
		},
		func(llm.Request) llm.Response { return finalResponse("worker done") },
	)
	root = newMessagingRoot(t, adapter, func(cfg *SessionConfig) {
		cfg.testOnly.delegateDeliveryClassified = func(s *Session, isDeferred bool) {
			if s == root && isDeferred {
				deferredOnce.Do(func() { close(deferred) })
			}
		}
	})
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := root.ProcessInput(ctx, "start", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	fold := rootAttentionFold(t, root)
	var updateID string
	for _, id := range fold.order {
		if strings.Contains(id, "/update/") {
			updateID = id
		}
	}
	if updateID == "" || fold.resolutions[updateID] != delegateAttentionConsumed {
		t.Fatalf("update attention %q = %q, want consumed by the turn that presented it", updateID, fold.resolutions[updateID])
	}
}

// An update posted while the parent waits in delegate_send on that delegate
// rides the wait's reply, after the earlier result the parent was holding and
// before the new report, labelled as an update, and never arrives again as a
// notification. The delegate is told it was queued.
func TestDelegateSendWaitCarriesAnUpdateAheadOfTheReport(t *testing.T) {
	t.Parallel()
	adapter := newMessagingAdapter("WORKER-BRIEF")
	releaseFirst, firstDeferred := make(chan struct{}), make(chan struct{})
	var deferredOnce sync.Once
	sendReply, nextRequest, updateResult := make(chan string, 1), make(chan string, 1), make(chan string, 1)
	var (
		mu         sync.Mutex
		delegateID string
		root       *Session
	)
	adapter.script("root",
		func(llm.Request) llm.Response {
			return delegateToolResponse("create", "WORKER-BRIEF: answer when asked.")
		},
		func(req llm.Request) llm.Response {
			close(releaseFirst)
			awaitSignal(t, firstDeferred, "the first generation's report deferred behind the root's turn")
			id := deferredHeadDelegateID.FindString(deferredHeadToolResults(req.Messages))
			mu.Lock()
			delegateID = id
			mu.Unlock()
			return delegateSendResponse("send", id, "Find the drain cursor table.", 60_000)
		},
		func(req llm.Request) llm.Response {
			sendReply <- lastToolResultText(req.Messages)
			nextRequest <- deferredHeadToolResults(req.Messages) + messagesText(req.Messages)
			return finalResponse("root done")
		},
	)
	adapter.script("WORKER-BRIEF",
		func(llm.Request) llm.Response {
			awaitSignal(t, releaseFirst, "the root's second request")
			return finalResponse("GEN1_READY")
		},
		func(llm.Request) llm.Response {
			return updateResponse("upd", "UPDATE_ONE: which table holds the drain cursor?")
		},
		func(req llm.Request) llm.Response {
			updateResult <- lastToolResultText(req.Messages)
			return finalResponse("GEN2_REPORT")
		},
	)
	root = newMessagingRoot(t, adapter, func(cfg *SessionConfig) {
		cfg.testOnly.delegateDeliveryClassified = func(s *Session, isDeferred bool) {
			if s == root && isDeferred {
				deferredOnce.Do(func() { close(firstDeferred) })
			}
		}
	})
	// TRIPWIRE: half the send's wait; a send that sits out its wait fails here.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := root.ProcessInput(ctx, "start", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	reply := <-sendReply
	first, update, report := strings.Index(reply, "GEN1_READY"), strings.Index(reply, "UPDATE_ONE"), strings.Index(reply, "GEN2_REPORT")
	if first < 0 || update < first || report < update || !strings.Contains(reply, "· update]") {
		t.Fatalf("delegate_send reply = %q, want the held report, then the update labelled as one, then the new report", reply)
	}
	if n := strings.Count(<-nextRequest, "UPDATE_ONE"); n != 1 {
		t.Fatalf("the root's next request carries the update %d times, want once, in the reply", n)
	}
	if got := <-updateResult; !strings.Contains(got, `"delivery":"queued"`) {
		t.Fatalf("the delegate's communicate result = %q, want queued behind the held report", got)
	}
	mu.Lock()
	id := delegateID
	mu.Unlock()
	if pending := delegateAggregateSnapshot(t, root.delegateController, id).PendingDeliveries; len(pending) != 0 {
		t.Fatalf("pending deliveries after the reply = %#v, want all acknowledged", pending)
	}
	// With every delivery acknowledged nothing is left to arrive later, so the
	// update the reply carried never reaches the root as a notification too.
	if pending := rootPendingAttention(root); len(pending) != 0 {
		t.Fatalf("root attention after the reply = %v, want none", pending)
	}
	for _, attentionID := range rootAttentionFold(t, root).order {
		if strings.Contains(attentionID, "/update/") {
			t.Fatalf("the root holds update attention %q, want the update only in the reply", attentionID)
		}
	}
}

// A root awaiting its human partner's answer to ask_user holds a delegate's
// update and report: a wake runs no turn, and once the answer arrives the
// update reaches the model ahead of the report, and both are consumed.
func TestDelegateUpdateWaitsForTheAnswerToAPendingQuestion(t *testing.T) {
	t.Parallel()
	adapter := newMessagingAdapter("WORKER-BRIEF")
	release := make(chan struct{})
	adapter.script("root",
		func(llm.Request) llm.Response {
			return delegateToolResponse("create", "WORKER-BRIEF: ask your parent which database to use.")
		},
		func(llm.Request) llm.Response { return toolCallResponse(askUserCall("ask1", askUserArgsValid())) },
		func(llm.Request) llm.Response { return finalResponse("going with Postgres") },
		func(llm.Request) llm.Response { return finalResponse("noted the worker") },
	)
	adapter.script("WORKER-BRIEF",
		func(llm.Request) llm.Response {
			awaitSignal(t, release, "the root awaiting its question")
			return updateResponse("upd", "ASK_UPDATE: Postgres or SQLite?")
		},
		func(llm.Request) llm.Response { return finalResponse("ASK_REPORT") },
	)
	root := newMessagingRoot(t, adapter, nil)
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := root.ProcessInput(ctx, "pick a database", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := root.State(); got != SessionAwaiting {
		t.Fatalf("root state = %q, want awaiting its question", got)
	}
	close(release)
	pending := waitForRootAttention(t, root, 2)
	if !strings.Contains(pending[0], "/update/") || !strings.Contains(rootAttentionFold(t, root).content[pending[1]].Text(), `kind="report"`) {
		t.Fatalf("root attention = %v, want the update, then the report", pending)
	}
	before := len(adapter.requestsFor("root"))
	if _, err := root.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
		t.Fatalf("wake while awaiting: %v", err)
	}
	if got := len(adapter.requestsFor("root")); got != before {
		t.Fatalf("a wake while awaiting made %d root requests, want none", got-before)
	}
	if _, err := root.ProcessInput(ctx, "use Postgres", nil); err != nil {
		t.Fatalf("answer: %v", err)
	}
	presented := false
	for _, req := range adapter.requestsFor("root")[before:] {
		text := messagesText(req.Messages)
		u, r := strings.Index(text, "ASK_UPDATE"), strings.Index(text, "ASK_REPORT")
		if u >= 0 && r >= 0 {
			if r < u {
				t.Fatalf("the report reached the model before the update:\n%s", text)
			}
			presented = true
			break
		}
	}
	if !presented {
		t.Fatal("no request after the answer presented both the update and the report")
	}
	fold := rootAttentionFold(t, root)
	for _, id := range pending {
		if fold.resolutions[id] != delegateAttentionConsumed {
			t.Fatalf("attention %q = %q, want consumed after the answer", id, fold.resolutions[id])
		}
	}
}

// One delegate's updates reach its parent in the order sent and before its
// report, and an idle parent takes all three in one notification turn.
func TestDelegateUpdatesArriveInOrderBeforeTheReportInOneTurn(t *testing.T) {
	t.Parallel()
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("warm") },
		func(llm.Request) llm.Response { return communicateResponse(false, "FIRST_UPDATE") },
		func(llm.Request) llm.Response { return communicateResponse(false, "SECOND_UPDATE") },
		func(llm.Request) llm.Response { return finalResponse("THE_REPORT") },
		func(llm.Request) llm.Response { return finalResponse("noted all three") },
	}
	root := restoreSupervisionRoot(t, fixture, nil)
	warmStableDelegateUnservedRoot(t, root, fixture)
	if outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "go on", 0); outcome.result.Err != nil {
		t.Fatalf("send: %v", outcome.result.Err)
	}
	pending := waitForRootAttention(t, root, 3)
	fold := rootAttentionFold(t, root)
	for i, want := range []string{"FIRST_UPDATE", "SECOND_UPDATE", "THE_REPORT"} {
		if frame := fold.content[pending[i]].Text(); !strings.Contains(frame, want) {
			t.Fatalf("attention %d = %q, want %s", i, frame, want)
		}
	}
	if !strings.Contains(fold.content[pending[2]].Text(), `kind="report"`) {
		t.Fatal("the last attention item is not the report")
	}
	before := len(fixture.adapter.Requests())
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := root.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
		t.Fatalf("root notification turn: %v", err)
	}
	if got := len(fixture.adapter.Requests()) - before; got != 1 {
		t.Fatalf("the notification turn made %d requests, want one presenting all three", got)
	}
	fold = rootAttentionFold(t, root)
	for _, id := range pending {
		if fold.resolutions[id] != delegateAttentionConsumed {
			t.Fatalf("attention %q = %q, want consumed by the one turn", id, fold.resolutions[id])
		}
	}
}

// An idle parent delegate (a coordinator that ended its turn waiting on its
// worker) is woken by its worker's update: its next turn presents it. The
// coordinator's generation stays open while it drains its worker, so that
// turn runs inside the drain, after the coordinator has gone idle. The
// worker's report waits until that turn is presented, so only the update
// itself can have woken the coordinator, and that turn consumes the update, so
// no successor generation is started for it.
func TestDelegateUpdateWakesAnIdleParentDelegate(t *testing.T) {
	t.Parallel()
	adapter := newMessagingAdapter("COORD-BRIEF", "WORKER-BRIEF")
	coordinatorIdle, reportGate := make(chan struct{}), make(chan struct{})
	releaseReport := sync.OnceFunc(func() { close(reportGate) })
	defer releaseReport()
	presented := make(chan string, 1)
	adapter.script("root",
		func(llm.Request) llm.Response {
			return delegateToolResponse("coord", "COORD-BRIEF: start one worker delegate, then end your turn and wait for it.")
		},
		func(llm.Request) llm.Response { return finalResponse("waiting on the coordinator") },
	)
	adapter.script("COORD-BRIEF",
		func(llm.Request) llm.Response {
			return delegateToolResponse("worker", "WORKER-BRIEF: ask your parent which table holds the drain cursor.")
		},
		func(llm.Request) llm.Response { return finalResponse("waiting on the worker") },
		func(req llm.Request) llm.Response {
			presented <- messagesText(req.Messages)
			return finalResponse("told the worker")
		},
	)
	adapter.script("WORKER-BRIEF",
		func(llm.Request) llm.Response {
			awaitSignal(t, coordinatorIdle, "the coordinator to end its turn")
			return updateResponse("upd", "IDLE_PARENT_UPDATE: which table holds the drain cursor?")
		},
		func(llm.Request) llm.Response {
			awaitSignal(t, reportGate, "the coordinator's turn presenting the update")
			return finalResponse("worker done")
		},
	)
	root := newMessagingRoot(t, adapter, nil)
	serveSupervisionRootWakes(t, root)
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := root.ProcessInput(ctx, "start", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	// The coordinator's second request ends its turn; once it is no longer
	// processing, the worker's update finds it idle.
	adapter.waitForRequests(t, "COORD-BRIEF", 2)
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	var coordinator *Session
	waitForCondition(t, 30*time.Second, "the coordinator to go idle", func() bool {
		children := root.subagents.sessions()
		if len(children) != 1 || children[0].State() != SessionIdle {
			return false
		}
		coordinator = children[0]
		return true
	})
	close(coordinatorIdle)
	select {
	case text := <-presented:
		releaseReport()
		if !strings.Contains(text, `kind="update"`) || !strings.Contains(text, "IDLE_PARENT_UPDATE") {
			t.Fatalf("the coordinator's next turn does not present the update:\n%s", text)
		}
		if strings.Contains(text, `kind="report"`) {
			t.Fatalf("the coordinator's turn presents the worker's report, so the update did not wake it alone:\n%s", text)
		}
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	case <-time.After(30 * time.Second):
		t.Fatal("the idle coordinator was never woken by its worker's update")
	}
	if generation := updateAttentionConsumedFor(t, coordinator); generation != 0 {
		t.Fatalf("the update was consumed by attention generation %d, want the drain turn that presented it (0)", generation)
	}
}

// A coordinator that is mid-run when its worker's update arrives sees it at
// its own next model boundary, and its own generation consumes the update it
// saw, so no successor generation is started for it.
func TestDelegateUpdateReachesARunningParentDelegate(t *testing.T) {
	t.Parallel()
	adapter := newMessagingAdapter("COORD-BRIEF", "WORKER-BRIEF")
	release, coordinatorDeferred := make(chan struct{}), make(chan struct{})
	var deferredOnce sync.Once
	presented := make(chan string, 1)
	var root, coordinator *Session
	adapter.script("root",
		func(llm.Request) llm.Response {
			return delegateToolResponse("coord", "COORD-BRIEF: start one worker delegate and keep working.")
		},
		func(llm.Request) llm.Response { return finalResponse("waiting on the coordinator") },
	)
	adapter.script("COORD-BRIEF",
		func(llm.Request) llm.Response {
			return delegateToolResponse("worker", "WORKER-BRIEF: ask your parent which table holds the drain cursor.")
		},
		func(llm.Request) llm.Response {
			close(release)
			awaitSignal(t, coordinatorDeferred, "the worker's update deferred behind the coordinator's turn")
			return jobListResponse("list")
		},
		func(req llm.Request) llm.Response {
			presented <- messagesText(req.Messages)
			return finalResponse("coordinator done")
		},
	)
	adapter.script("WORKER-BRIEF",
		func(llm.Request) llm.Response {
			awaitSignal(t, release, "the coordinator's second request")
			return updateResponse("upd", "RUNNING_PARENT_UPDATE: which table holds the drain cursor?")
		},
		func(llm.Request) llm.Response { return finalResponse("worker done") },
	)
	root = newMessagingRoot(t, adapter, func(cfg *SessionConfig) {
		// Only the coordinator receives anything besides the root.
		cfg.testOnly.delegateDeliveryClassified = func(s *Session, isDeferred bool) {
			if s != root && isDeferred {
				deferredOnce.Do(func() {
					// Written before the close the coordinator's step waits
					// on, so it is visible once presented is received.
					coordinator = s
					close(coordinatorDeferred)
				})
			}
		}
	})
	serveSupervisionRootWakes(t, root)
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := root.ProcessInput(ctx, "start", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	select {
	case text := <-presented:
		if !strings.Contains(text, `kind="update"`) || !strings.Contains(text, "RUNNING_PARENT_UPDATE") {
			t.Fatalf("the coordinator's next request does not present the update:\n%s", text)
		}
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	case <-time.After(30 * time.Second):
		t.Fatal("the running coordinator never saw its worker's update")
	}
	// The worker's later report may legitimately wake the coordinator again;
	// only the update's own resolution says whether it earned a generation.
	if coordinator == nil {
		t.Fatal("the coordinator never deferred the worker's update")
	}
	if generation := updateAttentionConsumedFor(t, coordinator); generation != 0 {
		t.Fatalf("the update the coordinator already saw was consumed by attention generation %d, want its presenting generation (0)", generation)
	}
}

// updateAttentionConsumedFor waits until sess's transcript resolves the update
// attention it received as consumed, and returns the generation that consumed
// it: zero when the generation that presented it consumed it at its turn's
// end, an attention generation's number when one was started for it.
func updateAttentionConsumedFor(t *testing.T, sess *Session) uint64 {
	t.Helper()
	sess.mu.Lock()
	path, sessionID := transcriptPath(sess.stateDir, sess.id), sess.id
	sess.mu.Unlock()
	var generation uint64
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	waitForCondition(t, 30*time.Second, "the update's attention to be consumed", func() bool {
		fold, err := readDelegateAttentionFold(path, sessionID)
		if err != nil {
			return false
		}
		for attentionID, resolution := range fold.resolutions {
			if strings.Contains(attentionID, "/update/") && resolution == delegateAttentionConsumed {
				generation = fold.resumeGenerations[attentionID]
				return true
			}
		}
		return false
	})
	return generation
}

// appendColdFixtureEvents appends events to the fixture's delegate journal
// before any root opens it, the way a process that crashed left it.
func appendColdFixtureEvents(t *testing.T, fixture coldStableDelegateFixture, events ...delegatestore.Event) []delegatestore.Event {
	t.Helper()
	path := delegateResourceStorePath(fixture.stateDir, fixture.meta.ID)
	stored, err := delegatestore.ReadEvents(path)
	if err != nil {
		t.Fatalf("read delegate journal: %v", err)
	}
	state, err := delegatestore.Fold(stored)
	if err != nil {
		t.Fatalf("fold delegate journal: %v", err)
	}
	store, err := delegatestore.Open(path)
	if err != nil {
		t.Fatalf("open delegate journal: %v", err)
	}
	appended, _, err := store.AppendBatch(state, events)
	if closeErr := store.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatalf("append delegate events: %v", err)
	}
	return appended
}

// A pending update survives a restart and wakes its parent, whether it was
// still in the delegate's journal or already in the parent's transcript.
func TestDelegateUpdateSurvivesRestart(t *testing.T) {
	t.Parallel()
	// The generation that posted the update was still running at the crash.
	// Restore settles it as runtime_lost, and the parent gets the update,
	// then that report, once each.
	t.Run("posted while the run was still going", func(t *testing.T) {
		t.Parallel()
		fixture := newColdStableDelegateFixture(t, "")
		startedAt := time.Unix(1_700_000_300, 0).UTC()
		appended := appendColdFixtureEvents(t, fixture,
			delegatestore.Event{Kind: delegatestore.EventDelegateRunStarted, TS: startedAt, DelegateID: fixture.delegateID,
				RunStarted: &delegatestore.RunStarted{Generation: 1, Trigger: delegatestore.TriggerOwnerInput, StartedAt: startedAt}},
			delegatestore.Event{Kind: delegatestore.EventDelegateUpdatePosted, TS: startedAt, DelegateID: fixture.delegateID,
				UpdatePosted: &delegatestore.UpdatePosted{Generation: 1, Message: "RESTART_UPDATE: which table holds the drain cursor?"}},
		)
		updateID := delegateAttentionID(delegatestore.UpdateDeliveryID(fixture.delegateID, appended[1].Seq))
		reportID := delegateAttentionID(delegateDeliveryID(fixture.delegateID, 1))
		fixture.adapter.steps = []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response { return finalResponse("noted after the restart") },
		}
		root := restoreSupervisionRoot(t, fixture, nil)
		pending := waitForRootAttention(t, root, 2)
		if !slices.Equal(pending, []string{updateID, reportID}) {
			t.Fatalf("root attention after restore = %v, want the update then the runtime_lost report", pending)
		}
		if !root.hasPendingRootDelegateAttention() {
			t.Fatal("restore did not arm the root's wake")
		}
		// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := root.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
			t.Fatalf("root notification turn: %v", err)
		}
		requests := fixture.adapter.Requests()
		text := messagesText(requests[len(requests)-1].Messages)
		if u, r := strings.Index(text, "RESTART_UPDATE"), strings.Index(text, `kind="report"`); u < 0 || r < u || strings.Count(text, "RESTART_UPDATE") != 1 {
			t.Fatalf("the root's turn after restore = %q, want the update once, then the report", text)
		}
	})
	t.Run("delivered but not yet read", func(t *testing.T) {
		t.Parallel()
		fixture := newColdStableDelegateFixture(t, "")
		fixture.adapter.steps = []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response { return communicateResponse(false, "UNREAD_UPDATE: which table?") },
			func(llm.Request) llm.Response { return finalResponse("summary written") },
			func(llm.Request) llm.Response { return finalResponse("noted after the restart") },
		}
		root := restoreSupervisionRoot(t, fixture, nil)
		warmStableDelegateUnservedRoot(t, root, fixture)
		pending := rootPendingAttention(root)
		if len(pending) != 1 {
			t.Fatalf("root attention before restart = %v, want the update", pending)
		}
		root.Close()
		restored := restoreSupervisionRoot(t, fixture, nil)
		if got := rootPendingAttention(restored); !slices.Equal(got, pending) {
			t.Fatalf("root attention after restart = %v, want %v", got, pending)
		}
		if !restored.hasPendingRootDelegateAttention() {
			t.Fatal("restore did not re-arm the unread update's wake")
		}
		// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := restored.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
			t.Fatalf("root notification turn: %v", err)
		}
		requests := fixture.adapter.Requests()
		if !requestContainsText(requests[len(requests)-1], "UNREAD_UPDATE") {
			t.Fatal("the restored root's turn did not present the update")
		}
		if got := rootAttentionFold(t, restored).resolutions[pending[0]]; got != delegateAttentionConsumed {
			t.Fatalf("update attention after restart = %q, want consumed", got)
		}
	})
}
