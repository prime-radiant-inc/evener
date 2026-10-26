package hub

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/e2ecap"
	"primeradiant.com/evener/test/e2e/fakellm"
)

// Script only the external model. The answer passes through the Manager's
// delegate_send, real input admission, transcript and shared observation owner.
func TestE2E_ManagerQuestionContinuation(t *testing.T) {
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	if testing.Short() {
		t.Skip("live-stack e2e: builds binaries and runs hub + daemons")
	}
	for _, scenario := range []struct{ pending, twice bool }{{false, false}, {true, false}, {false, true}} {
		pending := scenario.pending
		name := "progress_before_question"
		if pending {
			name = "attach_pending_question"
		}
		if scenario.twice {
			name = "two_successive_questions"
		}
		t.Run(name, func(t *testing.T) {
			provider, err := fakellm.New()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(provider.Close)
			stack := startHubStack(t, provider)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			human := stack.dialRPC(ctx, t)
			worker := startLiveThread(ctx, t, human, stack, "CONTINUATION-WORKER")
			workerCall, err := provider.Next(ctx.Done())
			if err != nil {
				t.Fatal(err)
			}
			manager := startManagerThread(ctx, t, human, stack, "CONTINUATION-MANAGER")
			managerCall, err := provider.Next(ctx.Done())
			if err != nil {
				t.Fatal(err)
			}
			watchID := ""
			register := func() {
				managerCall.RespondToolCall("job_watch", map[string]any{"operation": "create", "hub": map[string]any{"ref": worker, "events": []string{"question", "communicate"}, "completion": true}})
				managerCall, err = provider.Next(ctx.Done())
				if err != nil {
					t.Fatal(err)
				}
				watches, err := clientRequest[appwire.SessionWatchesResponse](ctx, human, appwire.MethodEvenerThreadWatchesList, appwire.SessionActivityListParams{Ref: manager})
				if err != nil {
					t.Fatal(err)
				}
				for _, row := range watches.Watches {
					if row.State == appwire.SessionWatchStateArmed {
						watchID = row.Watch.ID
					}
				}
				if watchID == "" {
					t.Fatalf("no armed watch: %+v", watches)
				}
			}
			if !pending {
				register()
				managerCall.RespondToolCall("communicate", communicateArgs("await question"))
			}
			progress := communicateArgs("CONTINUATION-PROGRESS")
			progress["end_turn"] = false
			delete(progress, "end_reason")
			workerCall.RespondToolCall("communicate", progress)
			for {
				call, err := provider.Next(ctx.Done())
				if err != nil {
					t.Fatal(err)
				}
				if call.Contains("CONTINUATION-WORKER") {
					workerCall = call
					break
				}
				call.RespondToolCall("communicate", communicateArgs("progress observed"))
			}
			workerCall.RespondToolCall("ask_user", map[string]any{"questions": []any{map[string]any{"question": "CONTINUATION-QUESTION", "options": []any{map[string]any{"label": "Yes", "detail": "continue"}, map[string]any{"label": "No", "detail": "stop"}}}}})
			var questionTurn, instance string
			awaitThread(ctx, t, human, worker, "pending question", func(thread appwire.Thread) bool {
				instance = thread.Evener.InstanceID
				return thread.Evener.AskPending
			})
			if pending {
				register()
			} else {
				managerCall, err = provider.Next(ctx.Done())
				if err != nil {
					t.Fatal(err)
				}
			}
			managerCall.RespondToolCall("delegate_send", map[string]any{"to": worker, "input_mode": "start", "message": "CONTINUATION-ANSWER", "client_mutation_id": newMutationID(t), "expected_instance_id": instance})
			for {
				call, err := provider.Next(ctx.Done())
				if err != nil {
					t.Fatal(err)
				}
				if call.Contains("CONTINUATION-WORKER") && call.Contains("CONTINUATION-ANSWER") {
					workerCall = call
					break
				}
				call.RespondToolCall("communicate", communicateArgs("answer admitted"))
			}
			turns, err := readTranscriptTurns(ctx, human, worker, "")
			if err != nil {
				t.Fatal(err)
			}
			answerTurn := ""
			for _, turn := range turns {
				for _, item := range turn.Items {
					if item.ToolName == "ask_user" {
						questionTurn = turn.ID
					}
					if item.Text == "CONTINUATION-ANSWER" {
						if item.Actor == nil || item.Actor.ManagerRef != manager {
							t.Fatalf("answer actor: %+v", item.Actor)
						}
						answerTurn = turn.ID
						t.Logf("persisted answer: questionTurn=%s answerTurn=%s", questionTurn, answerTurn)
					}
				}
			}
			if questionTurn == "" || answerTurn == "" || answerTurn == questionTurn {
				t.Fatalf("execution transition absent: question=%s answer=%s", questionTurn, answerTurn)
			}
			if scenario.twice {
				workerCall.RespondToolCall("ask_user", map[string]any{"questions": []any{map[string]any{"question": "SECOND-QUESTION", "options": []any{map[string]any{"label": "Yes", "detail": "continue"}, map[string]any{"label": "No", "detail": "stop"}}}}})
				awaitThread(ctx, t, human, worker, "second pending question", func(thread appwire.Thread) bool { return thread.Evener.AskPending })
				managerCall, err = provider.Next(ctx.Done())
				if err != nil {
					t.Fatal(err)
				}
				managerCall.RespondToolCall("delegate_send", map[string]any{"to": worker, "input_mode": "start", "message": "SECOND-ANSWER", "client_mutation_id": newMutationID(t), "expected_instance_id": instance})
				for {
					call, err := provider.Next(ctx.Done())
					if err != nil {
						t.Fatal(err)
					}
					if call.Contains("CONTINUATION-WORKER") && call.Contains("SECOND-ANSWER") {
						workerCall = call
						break
					}
					call.RespondToolCall("communicate", communicateArgs("second answer admitted"))
				}
			}
			workerCall.RespondToolCall("communicate", communicateArgs("CONTINUATION-DONE"))
			// Both cases select communicate, so even the broken completion matcher
			// wakes the Manager. Inspect the durable terminal state, not its prose.
			for {
				call, err := provider.Next(ctx.Done())
				if err != nil {
					t.Fatal(err)
				}
				call.RespondToolCall("communicate", communicateArgs("worker event observed"))
				turns, err = readTranscriptTurns(ctx, human, worker, "")
				if err != nil {
					t.Fatal(err)
				}
				if workerTerminalAccepted(turns, "CONTINUATION-DONE") {
					break
				}
			}
			deadline := time.Now().Add(2 * time.Second)
			for {
				watches, err := clientRequest[appwire.SessionWatchesResponse](ctx, human, appwire.MethodEvenerThreadWatchesList, appwire.SessionActivityListParams{Ref: manager})
				if err != nil {
					t.Fatal(err)
				}
				if acceptanceWatchCompleted(watches, watchID) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("accepted answer completion did not end watch: question=%s answer=%s watches=%+v", questionTurn, answerTurn, watches.Watches)
				}
				time.Sleep(20 * time.Millisecond)
			}
		})
	}
}
