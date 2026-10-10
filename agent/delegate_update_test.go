package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
