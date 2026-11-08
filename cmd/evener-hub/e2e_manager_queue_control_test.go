package hub

import (
	"context"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/test/e2e/fakellm"
)

type acceptanceManagerInputMutation struct {
	Mode         string
	MutationID   string
	Payload      string
	QueueEntryID string
}

// Runs inside the real SSH acceptance fixture with the worker held at the model
// boundary. Queue changes cannot be mistaken for normal next-turn consumption.
func acceptanceRemoteQueueControls(ctx context.Context, t *testing.T, provider *fakellm.Server, human *appwire.Client, manager, worker *fakellm.Call, localRef, target, instance, opening, task, expectedActor string) (*fakellm.Call, *fakellm.Call, []string, []acceptanceManagerInputMutation) {
	t.Helper()
	var mutationIDs []string
	var directInputs []acceptanceManagerInputMutation
	for _, tc := range []struct{ mode, payload string }{
		{mode: "queue", payload: "ACCEPT-REMOTE-MANAGER-QUEUED"},
		{mode: "steer", payload: "ACCEPT-REMOTE-MANAGER-STEERED"},
	} {
		mutationID := newMutationID(t)
		manager.RespondToolCall("delegate_send", map[string]any{"to": target, "input_mode": tc.mode, "message": tc.payload, "client_mutation_id": mutationID, "expected_instance_id": instance})
		var err error
		manager, err = provider.Next(ctx.Done())
		if err != nil || !manager.Contains(opening) {
			t.Fatalf("Manager %s input driver: call=%v err=%v", tc.mode, manager, err)
		}
		var result appwire.SessionOperationResponse
		if !managerToolResult(manager, &result) {
			t.Fatalf("Manager %s input attribution: result=%+v refusal=%s", tc.mode, result, managerToolRefusal(manager))
		}
		if result.Actor.Kind != "manager" || result.Actor.ManagerRef != expectedActor {
			t.Fatalf("Manager %s input attribution actor=%+v, want manager %s", tc.mode, result.Actor, expectedActor)
		}
		var receipt *appwire.MutationReceipt
		if tc.mode == "queue" && result.Queue != nil {
			receipt = &result.Queue.Receipt
		} else if tc.mode == "steer" && result.Steer != nil {
			receipt = &result.Steer.Receipt
		}
		targetRef, err := appwire.ParseRef(target)
		if err != nil {
			t.Fatal(err)
		}
		if receipt == nil || receipt.Disposition != appwire.MutationDispositionApplied || receipt.ClientMutationID != mutationID || receipt.ThreadID != targetRef.ThreadID {
			t.Fatalf("Manager %s input receipt: result=%+v", tc.mode, result)
		}
		direct := acceptanceManagerInputMutation{Mode: tc.mode, MutationID: mutationID, Payload: tc.payload}
		if tc.mode == "queue" {
			if len(receipt.QueueEntryIDs) != 1 {
				t.Fatalf("Manager queue receipt entry IDs: %+v", receipt)
			}
			direct.QueueEntryID = receipt.QueueEntryIDs[0]
			awaitQueueDepth(ctx, t, human, localRef, 1)
		}
		directInputs = append(directInputs, direct)
	}
	for _, action := range []string{"drain_queue", "promote_queued"} {
		queuedText := "ACCEPT-REMOTE-DRAIN-QUEUED"
		if action == "promote_queued" {
			queuedText = "ACCEPT-REMOTE-PROMOTE"
		}
		queued, err := clientRequest[appwire.TurnQueueResponse](ctx, human, appwire.MethodTurnQueue, appwire.TurnQueueParams{Ref: localRef, ExpectedInstanceID: instance, ClientMutationID: newMutationID(t), Input: []appwire.InputItem{{Type: "text", Text: queuedText}}})
		if err != nil || len(queued.Receipt.QueueEntryIDs) != 1 {
			t.Fatalf("seed remote queue: %+v %v", queued, err)
		}
		queueDepth := 1
		if action == "drain_queue" {
			queueDepth = 2 // Manager's queued input plus the preserved human-seeded entry.
		}
		queue := awaitQueueDepth(ctx, t, human, localRef, queueDepth)
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
	for _, marker := range []string{"ACCEPT-REMOTE-DRAIN-QUEUED", "ACCEPT-REMOTE-DRAIN-INPUT", "ACCEPT-REMOTE-PROMOTE", "ACCEPT-REMOTE-MANAGER-QUEUED", "ACCEPT-REMOTE-MANAGER-STEERED"} {
		if !worker.Contains(marker) {
			t.Errorf("remote queue control did not deliver input %q to active worker", marker)
		}
	}
	return manager, worker, mutationIDs, directInputs
}
