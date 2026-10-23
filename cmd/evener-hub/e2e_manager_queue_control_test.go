package hub

import (
	"context"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/test/e2e/fakellm"
)

// Runs inside the real SSH acceptance fixture with the worker held at the model
// boundary. Queue changes cannot be mistaken for normal next-turn consumption.
func acceptanceRemoteQueueControls(ctx context.Context, t *testing.T, provider *fakellm.Server, human *appwire.Client, manager, worker *fakellm.Call, localRef, target, instance, opening, task string) (*fakellm.Call, *fakellm.Call, []string) {
	t.Helper()
	var mutationIDs []string
	for _, action := range []string{"drain_queue", "promote_queued"} {
		queuedText := "ACCEPT-REMOTE-DRAIN-QUEUED"
		if action == "promote_queued" {
			queuedText = "ACCEPT-REMOTE-PROMOTE"
		}
		queued, err := clientRequest[appwire.TurnQueueResponse](ctx, human, appwire.MethodTurnQueue, appwire.TurnQueueParams{Ref: localRef, ExpectedInstanceID: instance, ClientMutationID: newMutationID(t), Input: []appwire.InputItem{{Type: "text", Text: queuedText}}})
		if err != nil || len(queued.Receipt.QueueEntryIDs) != 1 {
			t.Fatalf("seed remote queue: %+v %v", queued, err)
		}
		queue := awaitQueueDepth(ctx, t, human, localRef, 1)
		mutationID := newMutationID(t)
		mutationIDs = append(mutationIDs, mutationID)
		arguments := map[string]any{"ref": target, "expectedInstanceId": instance, "clientMutationId": mutationID}
		if action == "drain_queue" {
			arguments["expectedQueueRevision"] = queue.Revision
			arguments["input"] = []appwire.InputItem{{Type: "text", Text: "ACCEPT-REMOTE-DRAIN-INPUT"}}
		} else {
			arguments["index"] = 0
			arguments["expectedEntryId"] = queue.IDs[0]
		}
		manager.RespondToolCall("session_control", map[string]any{"action": action, "arguments": arguments})
		manager, err = provider.Next(ctx.Done())
		if err != nil || !manager.Contains(opening) {
			t.Fatalf("queue control driver: %v %v", manager, err)
		}
		passed := t.Run(action, func(t *testing.T) {
			var result appwire.SessionOperationResponse
			if !managerToolResult(manager, &result) || result.Control == nil {
				t.Fatalf("remote %s failed: refusal=%s", action, managerToolRefusal(manager))
			}
			var receipt appwire.MutationReceipt
			if action == "drain_queue" && result.Control.DrainQueue != nil {
				receipt = result.Control.DrainQueue.Receipt
			} else if action == "promote_queued" && result.Control.PromoteQueued != nil {
				receipt = result.Control.PromoteQueued.Receipt
			}
			if receipt.Disposition != appwire.MutationDispositionApplied {
				t.Fatalf("remote %s receipt: %+v", action, receipt)
			}
			after := awaitQueueDepth(ctx, t, human, localRef, 0)
			if after.Revision <= queue.Revision {
				t.Fatalf("queue revision did not advance: before=%+v after=%+v", queue, after)
			}
		})
		if !passed {
			// Leave the next case an independent queue even on a red run.
			if _, err := clientRequest[appwire.TurnCancelQueuedResponse](ctx, human, appwire.MethodTurnCancelQueued, appwire.TurnCancelQueuedParams{Ref: localRef, ExpectedInstanceID: instance, ClientMutationID: newMutationID(t), ExpectedEntryID: queue.IDs[0]}); err != nil {
				t.Fatal(err)
			}
		}
	}
	worker.RespondToolCall("task_list", map[string]any{})
	var err error
	worker, err = provider.Next(ctx.Done())
	if err != nil || !worker.Contains(task) {
		t.Fatalf("remote worker continuation: %v %v", worker, err)
	}
	for _, marker := range []string{"ACCEPT-REMOTE-DRAIN-QUEUED", "ACCEPT-REMOTE-DRAIN-INPUT", "ACCEPT-REMOTE-PROMOTE"} {
		if !worker.Contains(marker) {
			t.Errorf("remote queue control did not deliver input %q to active worker", marker)
		}
	}
	return manager, worker, mutationIDs
}
