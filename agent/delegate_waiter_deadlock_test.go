package agent

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

func TestAcceptDelegateDeliveryPlanWhileProcessing(t *testing.T) {
	t.Run("claimed inline waiter resolves immediately", func(t *testing.T) {
		controller, _ := newDelegateControllerTestHarness(t, 1, 1)
		root := &Session{id: "root-session", state: SessionProcessing, delegateController: controller}
		controller.rootRuntime = root
		seedDelegateControllerIdle(t, controller, "dlg_target", "")
		lease, waiter := startDelegateDeliveryGeneration(t, controller, "dlg_target", true)
		plan := finishDelegateDeliveryGeneration(t, controller, lease, "finished inline").deliveries[0]

		plans, deferred, err := root.acceptDelegateDeliveryPlan(plan)
		if err != nil {
			t.Fatalf("acceptDelegateDeliveryPlan: %v", err)
		}
		if deferred {
			t.Fatal("waiter-bearing delivery was deferred behind its own processing turn")
		}
		root.delegateDeliveryMu.Lock()
		queued := len(root.pendingDelegateDeliveries)
		root.delegateDeliveryMu.Unlock()
		if queued != 0 {
			t.Fatalf("pending deliveries = %d, want no queued inline plan", queued)
		}
		select {
		case resolution := <-waiter.resolution:
			if resolution.fallback || resolution.packet == nil || resolution.commit == nil {
				t.Fatalf("inline resolution = %#v", resolution)
			}
			if _, err := resolution.commit.Complete(false); err != nil {
				t.Fatalf("abort inline commit: %v", err)
			}
		default:
			t.Fatal("accepted inline waiter was not resolved synchronously")
		}
		if len(plans.updates) != 0 || len(plans.deliveries) != 0 {
			t.Fatalf("inline acceptance plans = %#v, want handoff only", plans)
		}
	})

	t.Run("ordinary delivery remains deferred", func(t *testing.T) {
		controller, _ := newDelegateControllerTestHarness(t, 1, 1)
		root := &Session{id: "root-session", state: SessionProcessing, delegateController: controller}
		controller.rootRuntime = root
		seedDelegateControllerIdle(t, controller, "dlg_target", "")
		lease, _ := startDelegateDeliveryGeneration(t, controller, "dlg_target", false)
		plan := finishDelegateDeliveryGeneration(t, controller, lease, "finished in background").deliveries[0]

		plans, deferred, err := root.acceptDelegateDeliveryPlan(plan)
		if err != nil {
			t.Fatalf("acceptDelegateDeliveryPlan: %v", err)
		}
		if !deferred {
			t.Fatal("ordinary delivery bypassed processing-turn deferral")
		}
		root.delegateDeliveryMu.Lock()
		defer root.delegateDeliveryMu.Unlock()
		if len(root.pendingDelegateDeliveries) != 1 || root.pendingDelegateDeliveries[0].deliveryID != plan.deliveryID {
			t.Fatalf("pending deliveries = %#v, want ordinary plan queued", root.pendingDelegateDeliveries)
		}
		if len(plans.updates) != 0 || len(plans.deliveries) != 0 {
			t.Fatalf("deferred acceptance plans = %#v", plans)
		}
	})
}

func TestCanceledToolResultPersistenceAbortsClaimedInlineDelivery(t *testing.T) {
	controller, _ := newDelegateControllerTestHarness(t, 1, 1)
	root := &Session{id: "root-session", state: SessionProcessing, delegateController: controller}
	controller.rootRuntime = root
	seedDelegateControllerIdle(t, controller, "dlg_target", "")
	lease, waiter := startDelegateDeliveryGeneration(t, controller, "dlg_target", true)
	plan := finishDelegateDeliveryGeneration(t, controller, lease, "finished inline").deliveries[0]
	if _, err := deliverDelegatePacket(plan, root); err != nil {
		t.Fatalf("deliverDelegatePacket: %v", err)
	}
	resolution := <-waiter.resolution
	if resolution.commit == nil {
		t.Fatalf("inline resolution = %#v, want a delivery commit", resolution)
	}
	t.Cleanup(func() { _, _ = resolution.commit.Complete(false) })
	root.queueDelegateDeliveryCommit("send-call", resolution.commit)

	call := llm.ToolCallData{ID: "send-call", Name: "delegate_send", Type: "function"}
	result := tool.ExecResult{ToolName: "delegate_send", CallID: call.ID, Output: "finished inline", FullOutput: "finished inline"}
	part := llm.ContentPart{
		Kind: llm.ContentToolResult,
		ToolResult: &llm.ToolResultData{
			ToolCallID: call.ID,
			Name:       call.Name,
			Content:    result.Output,
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := root.appendToolResults(ctx, []llm.ToolCallData{call}, []tool.ExecResult{result}, []llm.ContentPart{part}); !errors.Is(err, context.Canceled) {
		t.Fatalf("appendToolResults error = %v, want context canceled", err)
	}

	root.delegateDeliveryMu.Lock()
	queuedCommits := len(root.delegateDeliveryCommits[call.ID])
	root.delegateDeliveryMu.Unlock()
	if queuedCommits != 0 {
		t.Fatalf("queued commits after canceled persistence = %d, want none", queuedCommits)
	}
	controller.mu.Lock()
	admissions := len(controller.deliveries)
	pending := len(controller.durable["dlg_target"].PendingDeliveries)
	controller.mu.Unlock()
	if admissions != 0 {
		t.Fatalf("delivery admissions after canceled persistence = %d, want aborted", admissions)
	}
	if pending != 1 {
		t.Fatalf("durable pending deliveries = %d, want delivery retained for replay", pending)
	}
	replay := controller.ReplayDeliveries()
	if len(replay) != 1 || replay[0].deliveryID != plan.deliveryID || replay[0].waiter != nil {
		t.Fatalf("replay after canceled persistence = %#v, want one ordinary delivery", replay)
	}
}
