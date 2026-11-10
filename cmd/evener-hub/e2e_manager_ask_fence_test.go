package hub

import (
	"context"
	"fmt"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/e2ecap"
	"primeradiant.com/evener/test/e2e/fakellm"
)

// Only external model responses are scripted. Manager schemas, runtime transport,
// hub paging, daemon admission, queue consumption and transcript ownership run live.
func TestE2E_ManagerPagesPendingCallsAndQueuesOneAnswer(t *testing.T) {
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	if testing.Short() {
		t.Skip("live-stack e2e: real hub and daemon processes")
	}
	provider := newFakeLLMProvider(t)
	batches := make(chan []map[string]any, 1)
	var batch []map[string]any
	for i := range appwire.MaxPendingAskComposites + 1 {
		batch = append(batch, voiceQuestionTool(fmt.Sprintf("manager-page-call-%d", i), "ask_user", map[string]any{
			"question": fmt.Sprintf("MANAGER_QUESTION_TOKEN_%d", i),
			"options":  []any{map[string]any{"label": "OPTION_TOKEN_A", "detail": ""}, map[string]any{"label": "OPTION_TOKEN_B", "detail": ""}},
		}))
	}
	batches <- batch
	baseURL := voiceQuestionBatchProvider(t, provider.BaseURL(), batches)
	stack := startHubStackOnProvider(t, fmt.Sprintf(`
default = "fake"
[providers.fake]
base = "openai-compatible"
base_url = %q
api_key = "fakellm-not-a-secret"
`, baseURL), "fake/"+fakellm.ModelID)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	human := stack.dialRPC(ctx, t)
	const workerOpening = "MANAGER_PAGE_WORKER_TOKEN"
	const managerOpening = "MANAGER_PAGE_OWNER_TOKEN"
	worker := startLiveThread(ctx, t, human, stack, workerOpening)
	awaitThread(ctx, t, human, worker, "pending multi-call set", func(thread appwire.Thread) bool {
		return thread.Evener.ActiveTurnID == "" && thread.Evener.PendingAsk != nil && thread.Evener.PendingAsk.CompositeTotal == len(batch)
	})
	manager := startManagerThread(ctx, t, human, stack, managerOpening)
	call, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatal(err)
	}
	if !call.Contains(managerOpening) {
		t.Fatal("fixture did not reach the Manager's model boundary")
	}
	call.RespondToolCall("job_status", map[string]any{"hub": map[string]any{"ref": worker}})
	call, err = provider.Next(ctx.Done())
	if err != nil {
		t.Fatal(err)
	}
	var status appwire.SessionOperationResponse
	if !managerToolResult(call, &status) || status.Read == nil {
		t.Fatalf("Manager status did not return the worker: %s", managerToolRefusal(call))
	}
	state := status.Read.Thread.Evener.PendingAsk
	if state == nil || state.BootGeneration == "" || !state.CallsTruncated || len(state.Calls) != appwire.MaxPendingAskComposites || state.CompositeTotal != len(batch) {
		t.Fatalf("Manager did not receive the bounded live set: %+v", state)
	}
	seen := make(map[appwire.PendingAskCall]bool)
	offset := 0
	for {
		call.RespondToolCall("job_status", map[string]any{"hub": map[string]any{
			"ref": worker, "expected_ask_revision": state.Revision, "expected_ask_boot_generation": state.BootGeneration, "offset": offset, "limit": 3,
		}})
		call, err = provider.Next(ctx.Done())
		if err != nil {
			t.Fatal(err)
		}
		var result appwire.SessionOperationResponse
		if !managerToolResult(call, &result) || result.Read == nil || result.Read.PendingAskPage == nil {
			t.Fatalf("Manager page did not survive the complete tool route: %s", managerToolRefusal(call))
		}
		page := result.Read.PendingAskPage
		if page.Total != len(batch) || page.CalllessCount != 0 || len(page.Composites) > 3 {
			t.Fatalf("invalid bounded pending-call page: %+v", page)
		}
		for _, composite := range page.Composites {
			if seen[composite] {
				t.Fatalf("duplicate pending composite: %+v", composite)
			}
			seen[composite] = true
		}
		if page.NextOffset == nil {
			break
		}
		if *page.NextOffset <= offset {
			t.Fatalf("page did not advance: %+v", page)
		}
		offset = *page.NextOffset
	}
	if len(seen) != len(batch) {
		t.Fatalf("paged %d composites, want %d", len(seen), len(batch))
	}
	const answer = "MANAGER_QUEUED_ANSWER_TOKEN"
	mutationID := newMutationID(t)
	call.RespondToolCall("delegate_send", map[string]any{
		"to": worker, "input_mode": "queue", "message": answer, "client_mutation_id": mutationID,
		"expected_instance_id":  status.Read.Thread.Evener.InstanceID,
		"expected_ask_revision": state.Revision, "expected_ask_boot_generation": state.BootGeneration,
	})
	var managerAfter, workerAfter *fakellm.Call
	for managerAfter == nil || workerAfter == nil {
		next, err := provider.Next(ctx.Done())
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case next.Contains(managerOpening):
			managerAfter = next
		case next.Contains(workerOpening) && next.Contains(answer):
			workerAfter = next
		default:
			t.Fatal("unexpected model request while consuming queued answer")
		}
	}
	var admitted appwire.SessionOperationResponse
	if !managerToolResult(managerAfter, &admitted) || admitted.Queue == nil || admitted.Queue.Receipt.ClientMutationID != mutationID || admitted.Queue.Receipt.Disposition != appwire.MutationDispositionApplied || admitted.Actor.ManagerRef != manager {
		t.Fatalf("Manager queue receipt lost admission or attribution: %+v", admitted)
	}
	var cleared appwire.Thread
	awaitThread(ctx, t, human, worker, "captured questions consumed", func(thread appwire.Thread) bool {
		cleared = thread
		return !thread.Evener.AskPending && thread.Evener.PendingQuestion == nil && thread.Evener.PendingAsk != nil && thread.Evener.PendingAsk.CompositeTotal == 0
	})
	if cleared.Evener.PendingAsk.Revision <= state.Revision {
		t.Fatal("queue consumption did not advance the pending-set revision")
	}
	workerAfter.RespondToolCall("communicate", communicateArgs("MANAGER_ANSWER_CONSUMED_TOKEN"))
	managerAfter.RespondToolCall("communicate", communicateArgs("MANAGER_QUEUE_VERIFIED_TOKEN"))
}
