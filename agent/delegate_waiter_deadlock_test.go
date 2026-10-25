package agent

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

func TestAcceptDelegateDeliveryPlanWhileProcessing(t *testing.T) {
	t.Run("claimed inline waiter resolves immediately", func(t *testing.T) {
		controller, _ := newDelegateControllerTestHarness(t, 1, 1)
		root := attachDeliveryTestReceiver(t, controller)
		root.state = SessionProcessing
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
		if ids, err := root.pendingNotificationIDs(); err != nil || len(ids) != 1 {
			t.Fatalf("canonical custody=%v err=%v", ids, err)
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
		if len(plans.updates) != 1 || len(plans.deliveries) != 0 {
			t.Fatalf("inline acceptance plans = %#v, want source acknowledgment update", plans)
		}
	})

	t.Run("ordinary delivery takes canonical custody", func(t *testing.T) {
		controller, _ := newDelegateControllerTestHarness(t, 1, 1)
		root := attachDeliveryTestReceiver(t, controller)
		root.state = SessionProcessing
		seedDelegateControllerIdle(t, controller, "dlg_target", "")
		lease, _ := startDelegateDeliveryGeneration(t, controller, "dlg_target", false)
		plan := finishDelegateDeliveryGeneration(t, controller, lease, "finished in background").deliveries[0]

		plans, deferred, err := root.acceptDelegateDeliveryPlan(plan)
		if err != nil {
			t.Fatalf("acceptDelegateDeliveryPlan: %v", err)
		}
		if deferred {
			t.Fatal("ordinary custody was deferred")
		}
		if ids, err := root.pendingNotificationIDs(); err != nil || len(ids) != 1 || ids[0] != delegateAttentionID(plan.deliveryID) {
			t.Fatalf("ordinary custody=%v err=%v", ids, err)
		}
		if len(plans.updates) != 1 || len(plans.deliveries) != 0 {
			t.Fatalf("custody plans=%#v", plans)
		}
	})
}

func TestCanceledToolResultPersistencePreservesCanonicalInlineDelivery(t *testing.T) {
	controller, _ := newDelegateControllerTestHarness(t, 1, 1)
	root := attachDeliveryTestReceiver(t, controller)
	root.state = SessionProcessing
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
	if pending != 0 {
		t.Fatalf("durable pending deliveries = %d, want source already acknowledged", pending)
	}
	replay := controller.ReplayDeliveries()
	if len(replay) != 0 {
		t.Fatalf("replay after canceled persistence = %#v, want source already settled", replay)
	}
	assertNotificationPending(t, root, []string{delegateAttentionID(plan.deliveryID)})
}

func TestCanceledAfterTakingInlineProjectionPreservesCanonicalDelivery(t *testing.T) {
	controller, _ := newDelegateControllerTestHarness(t, 1, 1)
	root := &Session{id: "root-session", stateDir: controller.stateDir, state: SessionProcessing, delegateController: controller}
	controller.rootRuntime = root
	path := transcriptPath(controller.stateDir, root.ID())
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: root.ID()})
	if err != nil {
		t.Fatalf("create root transcript: %v", err)
	}
	root.attachTranscript(writer)
	t.Cleanup(func() { _ = writer.Close() })

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

	controller.mu.Lock()
	beforeEvidence := controller.evidenceVersion
	controller.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	hookCalls := 0
	root.cfg.testOnly.delegateDeliveryCommitsTaken = func() {
		hookCalls++
		cancel()
	}
	call := llm.ToolCallData{ID: "send-call", Name: "delegate_send", Type: "function"}
	result := tool.ExecResult{ToolName: call.Name, CallID: call.ID, Output: "finished inline", FullOutput: "finished inline"}
	part := llm.ContentPart{
		Kind: llm.ContentToolResult,
		ToolResult: &llm.ToolResultData{
			ToolCallID: call.ID,
			Name:       call.Name,
			Content:    result.Output,
		},
	}
	appendErr := root.appendToolResults(ctx, []llm.ToolCallData{call}, []tool.ExecResult{result}, []llm.ContentPart{part})
	if !errors.Is(appendErr, context.Canceled) {
		t.Errorf("appendToolResults error = %v, want context canceled", appendErr)
	}
	if hookCalls != 1 {
		t.Errorf("post-take hook calls = %d, want 1", hookCalls)
	}

	controller.mu.Lock()
	evidenceDelta := controller.evidenceVersion - beforeEvidence
	admissions := len(controller.deliveries)
	pending := len(controller.durable["dlg_target"].PendingDeliveries)
	controller.mu.Unlock()
	if evidenceDelta != 0 {
		t.Errorf("controller evidence delta = %d, want projection cancellation not to mutate transport", evidenceDelta)
	}
	if admissions != 0 {
		t.Errorf("delivery admissions after cancellation = %d, want none", admissions)
	}
	if pending != 0 {
		t.Errorf("durable pending deliveries = %d, want canonical receiver custody", pending)
	}
	fold, err := readDelegateAttentionFold(path, root.ID())
	if err != nil {
		t.Fatalf("read root transcript fold: %v", err)
	}
	if callID := fold.deliveryCommits[plan.deliveryID]; callID != "" {
		t.Errorf("persisted delivery commit = %q for %q, want none", callID, plan.deliveryID)
	}
	replay := controller.ReplayDeliveries()
	if len(replay) != 0 {
		t.Errorf("replay after post-take cancellation = %#v, want source already settled", replay)
	}
	assertNotificationPending(t, root, []string{delegateAttentionID(plan.deliveryID)})
}
