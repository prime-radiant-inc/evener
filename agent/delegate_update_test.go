package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/internal/delegatestore"
	toolpkg "primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// A running delegate's update joins its own delivery queue behind whatever is
// already there. Only the head is planned, and its plan is the update's.
func TestDelegateControllerPostUpdateQueuesInDeliveryOrder(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	actor := delegateActor{lease: &delegateLease{delegateID: "dlg_target", generation: 1}}

	first, firstPlans, err := c.PostUpdate(actor, schema.CommunicateInfo{Message: "first question"}, publicationPacket("first question"), "")
	if err != nil {
		t.Fatalf("PostUpdate first: %v", err)
	}
	if !strings.HasPrefix(first, "dlg_target/update/") || len(firstPlans.deliveries) != 1 ||
		firstPlans.deliveries[0].deliveryID != first || firstPlans.deliveries[0].packet.Kind != delegatestore.PacketUpdate {
		t.Fatalf("first update %q planned %#v, want its own head plan", first, firstPlans.deliveries)
	}
	second, secondPlans, err := c.PostUpdate(actor, schema.CommunicateInfo{Message: "second question"}, publicationPacket("second question"), "")
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
	_, plans, err := c.PostUpdate(delegateActor{lease: &lease}, schema.CommunicateInfo{Message: "which table?"}, publicationPacket("which table?"), "")
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
// speak, a stopping delegate's route is closed, a settlement claimant without
// the owning open batch cannot publish, and a blank message says nothing.
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
		{"settlement without owning batch", "which table?", func(t *testing.T, c *delegateTreeController) delegateActor {
			if _, _, err := c.BeginSettlement(running); err != nil {
				t.Fatalf("BeginSettlement: %v", err)
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
			_, plans, err := c.PostUpdate(tc.actor(t, c), schema.CommunicateInfo{Message: tc.message}, publicationPacket(tc.message), "")
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
	// The direct helper's Complete(true) persists no carrier, so it cannot
	// suppress the canonical terminal notification or prove model observation.
	terminalID := delegateAttentionID(fixture.delegateID + "/delivery/1")
	var updateID string
	for _, id := range pending {
		if strings.HasPrefix(id, delegateAttentionID(fixture.delegateID+"/update/")) {
			updateID = id
		}
	}
	if len(pending) != 2 || updateID == "" || !slices.Contains(pending, terminalID) {
		t.Fatalf("root attention = %v, want one update and terminal %s", pending, terminalID)
	}
	frame := rootAttentionFold(t, root).content[updateID].Text()
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
	if got := rootAttentionFold(t, root).resolutions[updateID]; got != delegateAttentionConsumed {
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
			_ = registerCommunicateTool(reg, newToolDeps(runtime))
			ctx := context.WithValue(context.Background(), delegateRunLeaseContextKey{}, lease)
			// The refusal the controller gives this lease, which the result's
			// reason must explain.
			_, _, refusal := c.PostUpdate(delegateActor{lease: &lease}, schema.CommunicateInfo{Message: "need the schema"}, publicationPacket("need the schema"), "")
			if refusal == nil {
				t.Fatal("the controller accepted an update it should refuse")
			}
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
			if want := notDeliveredUpdate(refusal).Reason; result["reason"] != want {
				t.Fatalf("reason = %v, want %q", result["reason"], want)
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
		if isDelegateUpdateAttentionID(id) {
			updateID = id
		}
	}
	if updateID == "" || fold.resolutions[updateID] != delegateAttentionConsumed {
		t.Fatalf("update attention %q = %q, want consumed by the turn that presented it", updateID, fold.resolutions[updateID])
	}
}

// An update takes canonical custody while the parent waits in delegate_send.
// The reply carries the earlier result, update and new report in order, once
// each. Only the successful request response consumes their receiver records.
func TestDelegateSendWaitCarriesAnUpdateAheadOfTheReport(t *testing.T) {
	t.Parallel()
	adapter := newMessagingAdapter("WORKER-BRIEF")
	releaseFirst := make(chan struct{})
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
			id := deferredHeadDelegateID.FindString(deferredHeadToolResults(req.Messages))
			waitForCondition(t, 30*time.Second, "first generation canonical custody and source settlement", func() bool {
				c := root.delegateController
				c.mu.Lock()
				a := c.durable[id]
				settled := a != nil && !a.CurrentRunOpen && len(a.PendingDeliveries) == 0 && c.finalizingLocked(id) == nil
				c.mu.Unlock()
				return settled && slices.Contains(rootAttentionFold(t, root).order, delegateAttentionID(delegateDeliveryID(id, 1)))
			})
			mu.Lock()
			delegateID = id
			mu.Unlock()
			return delegateSendResponse("send", id, "Find the drain cursor table.", 60_000)
		},
		func(req llm.Request) llm.Response {
			sendReply <- lastToolResultText(req.Messages)
			nextRequest <- deferredHeadToolResults(req.Messages) + messagesText(req.Messages)
			fold := rootAttentionFold(t, root)
			for _, id := range fold.order {
				if isDelegateUpdateAttentionID(id) && fold.resolutions[id] == delegateAttentionConsumed {
					t.Error("update consumed before the successful assistant response carrying it")
				}
			}
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
	root = newMessagingRoot(t, adapter, nil)
	// TRIPWIRE: half the send's wait; a send that sits out its wait fails here.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := root.ProcessInput(ctx, "start", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	reply := <-sendReply
	first, update, report := strings.Index(reply, "GEN1_READY"), strings.Index(reply, "UPDATE_ONE"), strings.Index(reply, "GEN2_REPORT")
	if first < 0 || update < first || report < update {
		t.Fatalf("delegate_send reply = %q, want the earlier result, then the update, then the new report", reply)
	}
	request := <-nextRequest
	for _, message := range []string{"GEN1_READY", "UPDATE_ONE", "GEN2_REPORT"} {
		if n := strings.Count(request, message); n != 1 {
			t.Fatalf("the root's next request carries %s %d times, want once", message, n)
		}
	}
	if got := <-updateResult; !strings.Contains(got, `"delivery":"delivered"`) {
		t.Fatalf("the delegate's communicate result = %q, want durable receiver custody", got)
	}
	mu.Lock()
	id := delegateID
	mu.Unlock()
	if pending := delegateAggregateSnapshot(t, root.delegateController, id).PendingDeliveries; len(pending) != 0 {
		t.Fatalf("pending deliveries after the reply = %#v, want all acknowledged", pending)
	}
	// Canonical custody survives the inline carrier. Only its successful
	// assistant response proves observation and consumes the receiver records.
	if pending := rootPendingAttention(root); len(pending) != 0 {
		t.Fatalf("root attention after the reply = %v, want none", pending)
	}
	fold := rootAttentionFold(t, root)
	updates := 0
	for _, attentionID := range fold.order {
		if isDelegateUpdateAttentionID(attentionID) {
			updates++
			if !strings.Contains(fold.content[attentionID].Text(), `kind="update"`) || fold.resolutions[attentionID] != delegateAttentionConsumed || fold.deliveryCommits[strings.TrimPrefix(attentionID, "delegate:")] != "send" {
				t.Fatalf("update %s lost its kind, consumption or inline carrier: %+v", attentionID, fold)
			}
		}
	}
	if updates != 1 || len(fold.order) != 3 {
		t.Fatalf("canonical records = %v, want generations 1 and 2 plus one update", fold.order)
	}
	for _, generation := range []uint64{1, 2} {
		deliveryID := delegateDeliveryID(id, generation)
		if !slices.Contains(fold.order, delegateAttentionID(deliveryID)) || fold.deliveryCommits[deliveryID] != "send" {
			t.Fatalf("generation %d lost canonical custody or inline carrier: %+v", generation, fold)
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
	if !isDelegateUpdateAttentionID(pending[0]) || !strings.Contains(rootAttentionFold(t, root).content[pending[1]].Text(), `kind="report"`) {
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
	coordinator := waitForSoleChildIdle(t, root)
	close(coordinatorIdle)
	select {
	case text := <-presented:
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
	// The scripted adapter answers a request past its script with a plain
	// final report, so only the count shows a drain pass that ran extra
	// turns. The script's three steps are the delegate call, the end of the
	// first turn and the update's turn. The worker is still held, so its
	// report cannot yet run a turn of its own; where that later turn lands
	// depends on when the report arrives, so the count is not pinned after.
	if got := len(adapter.requestsFor("COORD-BRIEF")); got != 3 {
		t.Fatalf("the coordinator made %d model requests, want its 3 scripted steps", got)
	}
	releaseReport()
	awaitRootReport(t, root)
}

// A coordinator woken in its finalization drain by its worker's update has
// already sent its terminal communicate, and can still relay what it learns:
// its own update reaches the root ahead of its report.
func TestDelegateUpdateFromADrainTurnReachesTheRootBeforeTheReport(t *testing.T) {
	t.Parallel()
	adapter := newMessagingAdapter("COORD-BRIEF", "WORKER-BRIEF")
	coordinatorIdle, reportGate := make(chan struct{}), make(chan struct{})
	releaseReport := sync.OnceFunc(func() { close(reportGate) })
	defer releaseReport()
	relayResult := make(chan string, 1)
	adapter.script("root",
		func(llm.Request) llm.Response {
			return delegateToolResponse("coord", "COORD-BRIEF: start one worker delegate, then report and wait for it.")
		},
		func(llm.Request) llm.Response { return finalResponse("waiting on the coordinator") },
	)
	adapter.script("COORD-BRIEF",
		func(llm.Request) llm.Response {
			return delegateToolResponse("worker", "WORKER-BRIEF: ask your parent which table holds the drain cursor.")
		},
		func(llm.Request) llm.Response { return communicateResponse(true, "COORD_FINAL: waiting on the worker") },
		func(llm.Request) llm.Response {
			return updateResponse("relay", "COORD_RELAY: the worker needs the drain cursor table")
		},
		func(req llm.Request) llm.Response {
			relayResult <- lastToolResultText(req.Messages)
			releaseReport()
			return finalResponse("relayed")
		},
	)
	adapter.script("WORKER-BRIEF",
		func(llm.Request) llm.Response {
			awaitSignal(t, coordinatorIdle, "the coordinator to end its turn")
			return updateResponse("upd", "DRAIN_UPDATE: which table holds the drain cursor?")
		},
		func(llm.Request) llm.Response {
			awaitSignal(t, reportGate, "the coordinator's relay")
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
	adapter.waitForRequests(t, "COORD-BRIEF", 2)
	waitForSoleChildIdle(t, root)
	close(coordinatorIdle)
	select {
	case result := <-relayResult:
		if !strings.Contains(result, `"delivery":"delivered"`) && !strings.Contains(result, `"delivery":"queued"`) {
			t.Fatalf("the coordinator's relay result = %s, want delivered or queued", result)
		}
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	case <-time.After(30 * time.Second):
		t.Fatal("the coordinator's drain turn never relayed")
	}
	awaitRootReport(t, root)
	fold := rootAttentionFold(t, root)
	updateAt := slices.IndexFunc(fold.order, isDelegateUpdateAttentionID)
	reportAt := slices.IndexFunc(fold.order, func(id string) bool { return !isDelegateUpdateAttentionID(id) })
	if updateAt < 0 || reportAt < updateAt {
		t.Fatalf("root attention order = %v, want the coordinator's update before its report", fold.order)
	}
	frame := fold.content[fold.order[updateAt]].Text()
	if !strings.Contains(frame, `kind="update"`) || !strings.Contains(frame, "COORD_RELAY") {
		t.Fatalf("the root's first attention = %q, want the coordinator's relayed update", frame)
	}
}

// A report wakes a coordinator in its ordinary drain without waiting for
// another worker's update or completion.
func TestNotificationReportWakesDrainingParent(t *testing.T) {
	t.Parallel()
	notificationReportWakesParent(t, false)
}

// When the write that marks the update consumed fails once, the drain retries
// the marker without running a second model turn for an update the model
// already saw.
func TestDelegateUpdateConsumedMarkerFailureDoesNotRerunTheTurn(t *testing.T) {
	t.Parallel()
	drainingCoordinatorWithObservedReport(t, 1)
}

// When the retry fails too, the update stays pending for the generation's end:
// the drain neither fails the coordinator, which would stop its healthy
// workers, nor runs another model turn for an update the model already saw.
func TestDelegateUpdateConsumedMarkerRetryFailureKeepsTheCoordinatorRunning(t *testing.T) {
	t.Parallel()
	drainingCoordinatorWithObservedReport(t, 2)
}

// drainingCoordinatorWithObservedReport presents A's report before entering the
// drain, then wakes it with B's update. An already observed report must not add
// a turn. failedMarkerWrites fails that many update resolution writes.
func drainingCoordinatorWithObservedReport(t *testing.T, failedMarkerWrites int) {
	t.Helper()
	adapter := newMessagingAdapter("COORD-BRIEF", "WORKER-A", "WORKER-B")
	firstReportGate, reportDeferred := make(chan struct{}), make(chan struct{})
	updateGate, reportGate := make(chan struct{}), make(chan struct{})
	var deferredOnce sync.Once
	var root *Session
	releaseUpdate := sync.OnceFunc(func() { close(updateGate) })
	defer releaseUpdate()
	releaseReport := sync.OnceFunc(func() { close(reportGate) })
	defer releaseReport()
	presented := make(chan string, 1)
	adapter.script("root",
		func(llm.Request) llm.Response {
			return delegateToolResponse("coord", "COORD-BRIEF: start two worker delegates, then end your turn and wait for them.")
		},
		func(llm.Request) llm.Response { return finalResponse("waiting on the coordinator") },
	)
	adapter.script("COORD-BRIEF",
		func(llm.Request) llm.Response { return delegateToolResponse("a", "WORKER-A: report the cursor table.") },
		func(llm.Request) llm.Response {
			close(firstReportGate)
			awaitSignal(t, reportDeferred, "A's report to arrive before the next model boundary")
			return delegateToolResponse("b", "WORKER-B: ask your parent which table holds the drain cursor.")
		},
		func(llm.Request) llm.Response { return finalResponse("waiting on both workers") },
		func(req llm.Request) llm.Response {
			presented <- messagesText(req.Messages)
			return finalResponse("told worker b")
		},
	)
	adapter.script("WORKER-A",
		func(llm.Request) llm.Response {
			awaitSignal(t, firstReportGate, "the coordinator's second request")
			return finalResponse("SIBLING_REPORT: drain_cursor")
		},
	)
	adapter.script("WORKER-B",
		func(llm.Request) llm.Response {
			awaitSignal(t, updateGate, "the sibling's report to land")
			return updateResponse("upd", "SIBLING_UPDATE: which table holds the drain cursor?")
		},
		func(llm.Request) llm.Response {
			awaitSignal(t, reportGate, "the coordinator's turn presenting the update")
			return finalResponse("worker b done")
		},
	)
	root = newMessagingRoot(t, adapter, func(cfg *SessionConfig) {
		cfg.testOnly.delegateDeliveryClassified = func(s *Session, isDeferred bool) {
			if s != root && isDeferred {
				deferredOnce.Do(func() { close(reportDeferred) })
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
	adapter.waitForRequests(t, "COORD-BRIEF", 3)
	coordinator := waitForSoleChildIdle(t, root)
	coordinator.mu.Lock()
	path, sessionID := transcriptPath(coordinator.stateDir, coordinator.id), coordinator.id
	coordinator.mu.Unlock()
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	waitForCondition(t, 30*time.Second, "the sibling's report to be consumed by the initial turn", func() bool {
		fold, err := readDelegateAttentionFold(path, sessionID)
		return err == nil && len(fold.order) == 1 && fold.resolutions[fold.order[0]] == delegateAttentionConsumed
	})
	if text := messagesText(adapter.requestsFor("COORD-BRIEF")[2].Messages); !strings.Contains(text, "SIBLING_REPORT") {
		t.Fatalf("the initial turn's final request did not present the sibling report:\n%s", text)
	}
	// Three requests so far: two delegate calls and the end of the first turn.
	// The already observed report ran no extra turn. Worker B's own report may
	// later run a successor turn or ride the drain's end, depending on when it
	// lands, so the total after the update is not pinned.
	if got := len(adapter.requestsFor("COORD-BRIEF")); got != 3 {
		t.Fatalf("the sibling's report ran a turn: the coordinator made %d model requests, want 3", got)
	}
	var failingWrites *rootAttentionResolutionFailFS
	if failedMarkerWrites > 0 {
		failingWrites = installResolutionSyncFailuresWriter(t, coordinator, failedMarkerWrites)
	}
	releaseUpdate()
	select {
	case text := <-presented:
		if failingWrites != nil {
			// Worker B stays blocked, so no report can run a turn: the only
			// possible fifth request is a rerun for the update.
			// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
			waitForCondition(t, 30*time.Second, "the marker writes to fail", func() bool {
				return failingWrites.injectedFailures() == failedMarkerWrites
			})
			if failedMarkerWrites == 1 {
				// The retry succeeded.
				// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
				waitForCondition(t, 30*time.Second, "the update to be marked consumed", func() bool {
					fold, err := readDelegateAttentionFold(path, sessionID)
					return err == nil && len(fold.pendingIDs()) == 0
				})
			}
			if got := len(adapter.requestsFor("COORD-BRIEF")); got != 4 {
				t.Fatalf("the coordinator made %d model requests after the marker write failed, want 4", got)
			}
		}
		releaseReport()
		if !strings.Contains(text, "SIBLING_UPDATE") {
			t.Fatalf("the coordinator's first drain turn was not woken by the update:\n%s", text)
		}
		if !strings.Contains(text, "SIBLING_REPORT") {
			t.Fatalf("the update's turn does not carry the sibling's report:\n%s", text)
		}
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	case <-time.After(30 * time.Second):
		t.Fatal("the draining coordinator was never woken by the update")
	}
	awaitRootReport(t, root)
	if failingWrites != nil {
		// The coordinator's run was not failed by the bookkeeping error.
		fold := rootAttentionFold(t, root)
		for _, id := range fold.order {
			if !isDelegateUpdateAttentionID(id) && !strings.Contains(fold.content[id].Text(), `"outcome":"completed"`) {
				t.Fatalf("the coordinator's report = %q, want a completed outcome", fold.content[id].Text())
			}
		}
	}
}

// An update from another worker that reaches the coordinator during an update
// turn, in time for a later request of that turn, was shown to the model. When
// the consumed-marker write fails twice, the drain runs no further turn for
// either update: it drives from what the turn's settled requests credited, so
// a mid-turn arrival counts as shown. (One worker's second update cannot
// arrive mid-turn: it queues behind its first until that is acknowledged.)
func TestDelegateUpdateArrivingMidTurnIsNotRerunWhenTheMarkerCannotBeWritten(t *testing.T) {
	t.Parallel()
	adapter := newMessagingAdapter("COORD-BRIEF", "WORKER-A", "WORKER-B")
	coordinatorIdle, secondGate, reportGate := make(chan struct{}), make(chan struct{}), make(chan struct{})
	releaseReport := sync.OnceFunc(func() { close(reportGate) })
	defer releaseReport()
	var coordinator atomic.Pointer[Session]
	adapter.script("root",
		func(llm.Request) llm.Response {
			return delegateToolResponse("coord", "COORD-BRIEF: start two worker delegates, then end your turn and wait for them.")
		},
		func(llm.Request) llm.Response { return finalResponse("waiting on the coordinator") },
	)
	adapter.script("COORD-BRIEF",
		func(llm.Request) llm.Response {
			return delegateToolResponse("a", "WORKER-A: send an update, then finish.")
		},
		func(llm.Request) llm.Response {
			return delegateToolResponse("b", "WORKER-B: send an update, then finish.")
		},
		func(llm.Request) llm.Response { return finalResponse("waiting on both workers") },
		// Worker A's update turn: hold the tool round until worker B has
		// posted its update, which a running coordinator receives at its next
		// model boundary, so the next request presents both.
		func(llm.Request) llm.Response {
			close(secondGate)
			adapter.waitForRequests(t, "WORKER-B", 2)
			return jobListResponse("hold")
		},
		func(llm.Request) llm.Response { return finalResponse("told both workers") },
	)
	adapter.script("WORKER-A",
		func(llm.Request) llm.Response {
			awaitSignal(t, coordinatorIdle, "the coordinator to end its turn")
			return updateResponse("first", "FIRST_UPDATE: which table holds the drain cursor?")
		},
		func(llm.Request) llm.Response {
			awaitSignal(t, reportGate, "the marker writes to fail")
			return finalResponse("worker a done")
		},
	)
	adapter.script("WORKER-B",
		func(llm.Request) llm.Response {
			awaitSignal(t, secondGate, "the coordinator's update turn to start")
			return updateResponse("second", "SECOND_UPDATE: and which column?")
		},
		func(llm.Request) llm.Response {
			awaitSignal(t, reportGate, "the marker writes to fail")
			return finalResponse("worker b done")
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
	adapter.waitForRequests(t, "COORD-BRIEF", 3)
	coordinator.Store(waitForSoleChildIdle(t, root))
	failingWrites := installResolutionSyncFailuresWriter(t, coordinator.Load(), 2)
	close(coordinatorIdle)
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	waitForCondition(t, 30*time.Second, "the marker writes to fail", func() bool {
		return failingWrites.injectedFailures() == 2
	})
	// The workers are still held, so only a rerun for an update could add a
	// request beyond the five scripted steps. A rerun follows the failed retry
	// at once; a quiet second is the absence this test pins.
	// TRIPWIRE: a bounded negative wait; a passing run costs the full second.
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline) && len(adapter.requestsFor("COORD-BRIEF")) == 5; {
		time.Sleep(10 * time.Millisecond)
	}
	requests := adapter.requestsFor("COORD-BRIEF")
	if len(requests) != 5 {
		t.Fatalf("the coordinator made %d model requests, want its 5 scripted steps", len(requests))
	}
	if text := messagesText(requests[4].Messages); !strings.Contains(text, "FIRST_UPDATE") || !strings.Contains(text, "SECOND_UPDATE") {
		t.Fatalf("the update turn's later request did not present both updates:\n%s", text)
	}
	releaseReport()
	awaitRootReport(t, root)
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
			if isDelegateUpdateAttentionID(attentionID) && resolution == delegateAttentionConsumed {
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
		if !root.hasPendingNotifications() {
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
		terminalID := delegateAttentionID(delegateDeliveryID(fixture.delegateID, 1))
		var updateID string
		for _, id := range pending {
			if strings.HasPrefix(id, delegateAttentionID(fixture.delegateID+"/update/")) {
				updateID = id
			}
		}
		if len(pending) != 2 || updateID == "" || !slices.Contains(pending, terminalID) {
			t.Fatalf("root attention before restart = %v, want one update and terminal %s", pending, terminalID)
		}
		root.Close()
		restored := restoreSupervisionRoot(t, fixture, nil)
		if got := rootPendingAttention(restored); !slices.Equal(got, pending) {
			t.Fatalf("root attention after restart = %v, want %v", got, pending)
		}
		if !restored.hasPendingNotifications() {
			t.Fatal("restore did not re-arm the unread update's wake")
		}
		// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := restored.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
			t.Fatalf("root notification turn: %v", err)
		}
		requests := fixture.adapter.Requests()
		if !requestContainsText(requests[len(requests)-1], "UNREAD_UPDATE") || !requestContainsText(requests[len(requests)-1], "summary written") {
			t.Fatal("the restored root's turn did not present the update and actual terminal report")
		}
		if got := rootAttentionFold(t, restored).resolutions[updateID]; got != delegateAttentionConsumed {
			t.Fatalf("update attention after restart = %q, want consumed", got)
		}
	})
}

// Only a turn that consumes what it presents is admitted for a delegate's
// update. A parent's unleased notification drive of an idle delegate covers
// no attention, so it would present the update and leave it pending, and
// stands down; the delegate's own leased turn is admitted.
func TestDelegateUpdateAdmitsOnlyATurnThatConsumesIt(t *testing.T) {
	t.Parallel()
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("warm result") },
	}
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableDelegateUnservedRoot(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)
	updateID := delegateAttentionID(delegatestore.UpdateDeliveryID(fixture.delegateID, 1))
	if appended, err := sub.sess.appendDelegateNotificationDurably(updateID, "ADMISSION_UPDATE: which table?"); err != nil || !appended {
		t.Fatalf("append update attention = appended:%t err:%v", appended, err)
	}
	if !sub.sess.hasPendingOwnDelegateAttention() {
		t.Fatal("the delegate does not hold the pending update")
	}

	if sub.sess.acceptNotificationInput(context.Background(), "unleased-turn") {
		t.Fatal("an unleased notification drive was admitted for an update it cannot consume")
	}
	lease := delegateLease{delegateID: fixture.delegateID, generation: 1}
	leased := context.WithValue(context.Background(), delegateRunLeaseContextKey{}, lease)
	if !sub.sess.acceptNotificationInput(leased, "leased-turn") {
		t.Fatal("the delegate's own leased turn was not admitted for its update")
	}
}

// awaitRootReport waits until the root's transcript holds attention for a
// delegate's report, which is any attention that is not an update.
func awaitRootReport(t *testing.T, root *Session) {
	t.Helper()
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	waitForCondition(t, 30*time.Second, "the coordinator's report to reach the root", func() bool {
		fold, err := readDelegateAttentionFold(transcriptPath(root.stateDir, root.ID()), root.ID())
		return err == nil && slices.ContainsFunc(fold.order, func(id string) bool { return !isDelegateUpdateAttentionID(id) })
	})
}

// An update refused because the controller is retiring says the delegate is
// stopping or finishing, the same as any other closed route, and never leaks
// the controller's own error text.
func TestNotDeliveredUpdateExplainsARetiringController(t *testing.T) {
	t.Parallel()
	want := notDeliveredUpdate(errDelegateTargetBusy)
	got := notDeliveredUpdate(fmt.Errorf("post: %w", ErrRetirementUnavailable))
	if *got != *want {
		t.Fatalf("retiring controller delivery = %#v, want %#v", got, want)
	}
	if got.Status != delegateUpdateNotDelivered || strings.Contains(got.Reason, "could not be recorded") {
		t.Fatalf("retiring controller delivery = %#v, want the stopping or finishing reason", got)
	}
}

// drainingCoordinatorReport runs a coordinator that ends its turn with report
// A while a worker is still running, then lets the worker's update wake its
// finalization drain, where drainTurns script its replies. It returns the
// report frame and the reportPreview the root received, and the coordinator's
// transcript text.
//
// With undisposedJob the coordinator starts a background shell job instead of
// a worker and never disposes of it, so its drain announces the job to it.
func drainingCoordinatorReport(t *testing.T, undisposedJob bool, drainTurns ...func(llm.Request) llm.Response) (frame, preview, transcript string, coordinatorRequests int) {
	t.Helper()
	adapter := newMessagingAdapter("COORD-BRIEF", "WORKER-BRIEF")
	coordinatorIdle, reportGate := make(chan struct{}), make(chan struct{})
	releaseReport := sync.OnceFunc(func() { close(reportGate) })
	defer releaseReport()
	adapter.script("root",
		func(llm.Request) llm.Response {
			return delegateToolResponse("coord", "COORD-BRIEF: start one worker delegate, then report and wait for it.")
		},
		func(llm.Request) llm.Response { return finalResponse("waiting on the coordinator") },
	)
	startWork := func(llm.Request) llm.Response {
		return delegateToolResponse("worker", "WORKER-BRIEF: ask your parent which table holds the drain cursor.")
	}
	if undisposedJob {
		startWork = func(llm.Request) llm.Response {
			args, _ := json.Marshal(map[string]any{"command": "sleep 600", "mode": "background"})
			return toolCallResponse(llm.ToolCallData{ID: "job", Name: "shell", Arguments: args, Type: "function"})
		}
	}
	coordinatorSteps := []func(llm.Request) llm.Response{
		startWork,
		func(llm.Request) llm.Response { return communicateResponse(true, "REPORT_A: waiting on the worker") },
	}
	for _, drainTurn := range drainTurns {
		coordinatorSteps = append(coordinatorSteps, func(req llm.Request) llm.Response {
			releaseReport()
			return drainTurn(req)
		})
	}
	adapter.script("COORD-BRIEF", coordinatorSteps...)
	adapter.script("WORKER-BRIEF",
		func(llm.Request) llm.Response {
			awaitSignal(t, coordinatorIdle, "the coordinator to end its turn")
			return updateResponse("upd", "DRAIN_UPDATE: which table holds the drain cursor?")
		},
		func(llm.Request) llm.Response {
			awaitSignal(t, reportGate, "the coordinator's drain turn")
			return finalResponse("worker done")
		},
	)
	// The worker's own report can start another coordinator generation, which
	// would change the preview. Hold the first generation at its release
	// boundary, where its report is delivered, and read both views there.
	atRelease, releaseGeneration := make(chan struct{}, 1), make(chan struct{})
	release := sync.OnceFunc(func() { close(releaseGeneration) })
	defer release()
	root := newMessagingRoot(t, adapter, func(cfg *SessionConfig) {
		cfg.TurnEndsProcess = undisposedJob
		cfg.testOnly.subagentBeforeGenerationReleased = func(a *subagent, lease delegateLease) {
			a.mu.Lock()
			isCoordinator := a.stableDescriptor != nil && strings.HasPrefix(a.stableDescriptor.Task, "COORD-BRIEF")
			a.mu.Unlock()
			if isCoordinator && lease.generation == 1 {
				atRelease <- struct{}{}
				<-releaseGeneration
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
	adapter.waitForRequests(t, "COORD-BRIEF", 2)
	coordinator := waitForSoleChildIdle(t, root)
	close(coordinatorIdle)
	awaitSignal(t, atRelease, "the coordinator's first generation to reach its release boundary")
	// The preview follows the settled run, which is finished while the
	// generation is held.
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	waitForCondition(t, 30*time.Second, "the report preview to settle", func() bool {
		page, err := root.ListActivityDelegates(ctx, appwire.SessionActivityListParams{Ref: encodeRef("", root.ID())})
		if err != nil || len(page.Delegates) != 1 {
			return false
		}
		preview = page.Delegates[0].ReportPreview
		return preview != ""
	})
	// Counted while the generation is held, before a successor can start.
	coordinatorRequests = len(adapter.requestsFor("COORD-BRIEF"))
	release()
	awaitRootReport(t, root)
	fold := rootAttentionFold(t, root)
	reportAt := slices.IndexFunc(fold.order, func(id string) bool { return !isDelegateUpdateAttentionID(id) })
	if id := fold.order[reportAt]; !strings.HasSuffix(id, "/delivery/1") {
		t.Fatalf("the root's first report is %q, want the first generation's delivery", id)
	}
	frame = fold.content[fold.order[reportAt]].Text()
	transcriptRaw, err := os.ReadFile(transcriptPath(coordinator.stateDir, coordinator.ID()))
	if err != nil {
		t.Fatalf("read the coordinator's transcript: %v", err)
	}
	return frame, preview, string(transcriptRaw), coordinatorRequests
}

// bareTextDrainTurn is a drain turn whose model never calls communicate.
func bareTextDrainTurn(llm.Request) llm.Response {
	return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "told the worker"}}}}
}

// A coordinator's later turn in its drain replaces its report: the root
// receives the newer report, in its frame and in the delegate's reportPreview,
// and the earlier one stays in the coordinator's transcript.
func TestDelegateDrainTurnReportReplacesTheEarlierReport(t *testing.T) {
	t.Parallel()
	frame, preview, transcript, _ := drainingCoordinatorReport(t, false, func(llm.Request) llm.Response {
		return communicateResponse(true, "REPORT_B: told the worker to use the v2 schema")
	})
	if !strings.Contains(frame, "REPORT_B") || strings.Contains(frame, "REPORT_A") {
		t.Fatalf("the root's report frame = %q, want the drain turn's report B and not A", frame)
	}
	if preview != "REPORT_B: told the worker to use the v2 schema" {
		t.Fatalf("reportPreview = %q, want report B", preview)
	}
	if !strings.Contains(transcript, "REPORT_A") {
		t.Fatal("the coordinator's transcript lost its earlier report A")
	}
}

// A drain turn whose model never reports ends the turn, not the run: the
// earlier report stands as a completed report, not as a failure. A reply of
// bare text ends a notification turn at once, and an empty response is
// retried until it gives up.
func TestDelegateDrainTurnWithoutAReportKeepsTheEarlierReport(t *testing.T) {
	t.Parallel()
	emptyResponse := func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant}}
	}
	for name, tc := range map[string]struct {
		turn     func(llm.Request) llm.Response
		attempts int
	}{
		"bare text":      {bareTextDrainTurn, 1},
		"empty response": {emptyResponse, maxEmptyRetries + 1},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			// Scripted past the attempts, so an extra request would be a model
			// turn the drain turn should not have made.
			turns := slices.Repeat([]func(llm.Request) llm.Response{tc.turn}, tc.attempts+1)
			frame, preview, _, requests := drainingCoordinatorReport(t, false, turns...)
			if !strings.Contains(frame, "REPORT_A") || !strings.Contains(frame, `"kind":"reported"`) || !strings.Contains(frame, `"outcome":"completed"`) {
				t.Fatalf("the root's report frame = %q, want the earlier completed report A", frame)
			}
			if preview != "REPORT_A: waiting on the worker" {
				t.Fatalf("reportPreview = %q, want report A", preview)
			}
			// The delegate call, report A, and the drain turn's attempts.
			if want := 2 + tc.attempts; requests != want {
				t.Fatalf("the coordinator made %d model requests, want %d", requests, want)
			}
		})
	}
}

// A drain that announces an undisposed background job to the coordinator
// discards the announcement turns' text. The report still comes whole from the
// latest accepted report, never from the text the drain returned.
func TestDelegateDrainAnnouncementReportReplacesTheEarlierReport(t *testing.T) {
	t.Parallel()
	frame, preview, _, _ := drainingCoordinatorReport(t, true,
		func(llm.Request) llm.Response { return communicateResponse(true, "REPORT_B: left the job running") },
		func(llm.Request) llm.Response { return communicateResponse(true, "REPORT_C: detached the job") },
	)
	if !strings.Contains(frame, "REPORT_C") || strings.Contains(frame, "REPORT_A") {
		t.Fatalf("the root's report frame = %q, want the latest announcement turn's report C alone", frame)
	}
	if preview != "REPORT_C: detached the job" {
		t.Fatalf("reportPreview = %q, want report C", preview)
	}
}
