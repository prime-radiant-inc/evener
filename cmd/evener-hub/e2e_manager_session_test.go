package hub

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/e2ecap"
	"primeradiant.com/evener/test/e2e/fakellm"
)

// The only script is at the model boundary. Creation, input, subscription,
// question delivery and history are the real hub and daemon paths.
func TestE2E_ManagerLocalQuestionLoop(t *testing.T) {
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	if testing.Short() {
		t.Skip("live-stack e2e: builds binaries and runs a hub + daemons")
	}
	provider, err := fakellm.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(provider.Close)
	stack := startHubStack(t, provider)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	client := stack.dialRPC(ctx, t)
	const managerOpening = "EVENER-MANAGER-OPENING-4D"
	const workerTask = "EVENER-WORKER-TASK-4D"
	const question = "EVENER-WORKER-QUESTION-4D"
	const answer = "EVENER-MANAGER-ANSWER-4D"
	const result = "EVENER-WORKER-RESULT-4D"
	const ownedTask = "EVENER-OWNED-TASK-4D"
	const ownedReport = "EVENER-OWNED-REPORT-4D"
	managerRef := startManagerThread(ctx, t, client, stack, managerOpening)
	opening, err := provider.Next(ctx.Done())
	if err != nil || !opening.Contains(managerOpening) {
		t.Fatalf("manager opening request: call=%v err=%v", opening, err)
	}
	opening.RespondToolCall("delegate", map[string]any{
		"prompt": workerTask,
		"hub": map[string]any{
			"cwd": stack.workDir, "model": stack.model,
			"watch": map[string]any{"events": []string{"question", "communicate"}, "completion": true},
		},
	})
	var workerRef, workerInstance, watchID string
	var workerFirst *fakellm.Call
	// The new worker can reach its provider before the manager receives the
	// delegate tool result. Identify rounds by the user data, not timing.
	for workerFirst == nil || workerRef == "" {
		call, nextErr := provider.Next(ctx.Done())
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		if call.Contains(workerTask) {
			workerFirst = call
			continue
		}
		if !call.Contains(managerOpening) {
			t.Fatalf("unexpected provider request: %v", call.Texts())
		}
		var outcome struct {
			Created struct {
				Ref        string `json:"ref"`
				InstanceID string `json:"instanceId"`
			} `json:"created"`
			Watch struct {
				WatchID string `json:"watch_id"`
			} `json:"watch"`
			Input struct {
				Disposition string `json:"disposition"`
			} `json:"input"`
		}
		if !managerToolResult(call, &outcome) || outcome.Created.Ref == "" || outcome.Created.InstanceID == "" || outcome.Watch.WatchID == "" || outcome.Input.Disposition != string(appwire.MutationDispositionApplied) {
			t.Fatalf("creation, watch and input were not each reported: %+v, messages=%v", outcome, call.Texts())
		}
		workerRef, workerInstance, watchID = outcome.Created.Ref, outcome.Created.InstanceID, outcome.Watch.WatchID
		call.RespondToolCall("communicate", communicateArgs("worker started"))
	}
	t.Cleanup(func() {
		shutdownCtx, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		if _, err := clientRequest[appwire.EmptyResponse](shutdownCtx, client, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: workerRef}); err != nil {
			t.Errorf("worker shutdown: %v", err)
		}
	})
	var worker appwire.Thread
	awaitThread(ctx, t, client, workerRef, "independent worker", func(thread appwire.Thread) bool {
		worker = thread
		return thread.Evener.InstanceID == workerInstance
	})
	if worker.Evener.Kind == "subagent" || worker.Evener.Manager || worker.Evener.ParentRef != "" || worker.Evener.Ref == managerRef {
		t.Fatalf("worker is not an independent top-level session: %+v", worker.Evener)
	}
	workerFirst.RespondToolCall("ask_user", map[string]any{"questions": []any{map[string]any{"question": question, "options": []any{map[string]any{"label": "Proceed", "detail": "Continue the task"}, map[string]any{"label": "Wait", "detail": "Do not continue"}}}}})
	awaitThread(ctx, t, client, workerRef, "worker question", func(thread appwire.Thread) bool {
		return thread.Evener.AskPending && thread.Evener.PendingQuestion != nil && thread.Evener.PendingQuestion.Question == question
	})
	// There is no browser subscriber. The watch must wake the manager with the
	// question so its next tool call can answer through the human input owner.
	questionRound, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatalf("manager was not woken for the question: %v", err)
	}
	if !questionRound.Contains(watchID) {
		t.Fatal("manager did not receive its question watch notification")
	}
	questionRound.RespondToolCall("delegate", map[string]any{"prompt": ownedTask})
	ownedReportSeen := false
	ownedReportAcknowledged := false
	for !ownedReportSeen {
		call, nextErr := provider.Next(ctx.Done())
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		if !call.Contains(managerOpening) {
			if !call.Contains(ownedTask) {
				t.Fatalf("unexpected owned delegate request: %v", call.Texts())
			}
			call.RespondToolCall("communicate", communicateArgs(ownedReport))
			continue
		}
		if !call.Contains(ownedReport) {
			call.RespondToolCall("communicate", communicateArgs("owned delegate launched"))
			continue
		}
		// An owned delegate's report is not an answer to another, independent
		// worker's pending question. Only Manager input can answer that ask.
		read, readErr := clientRequest[appwire.ThreadReadResponse](ctx, client, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: workerRef})
		if readErr != nil {
			t.Fatal(readErr)
		}
		ownedReportAcknowledged = !read.Thread.Evener.AskPending || read.Thread.Evener.PendingQuestion == nil || read.Thread.Evener.PendingQuestion.Question != question
		if ownedReportAcknowledged {
			t.Fatalf("owned delegate report dismissed the worker question: %+v", read.Thread.Evener.PendingQuestion)
		}
		ownedReportSeen = true
		call.RespondToolCall("job_status", map[string]any{"hub": map[string]any{"ref": workerRef}})
	}
	statusRound, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatal(err)
	}
	var status appwire.SessionOperationResponse
	if !managerToolResult(statusRound, &status) || status.Read == nil || !status.Read.Thread.Evener.AskPending || status.Read.Thread.Evener.PendingQuestion == nil || status.Read.Thread.Evener.PendingQuestion.Question != question {
		t.Fatalf("manager status did not see the question: %+v", status.Read)
	}
	statusRound.RespondToolCall("delegate_send", map[string]any{
		"to": workerRef, "input_mode": "start", "message": answer,
		"client_mutation_id": newMutationID(t), "expected_instance_id": workerInstance,
	})
	var answered *fakellm.Call
	for answered == nil {
		call, nextErr := provider.Next(ctx.Done())
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		if call.Contains(managerOpening) {
			call.RespondToolCall("communicate", communicateArgs("answer sent"))
		} else if call.Contains(answer) && call.Contains(workerTask) {
			answered = call
		} else {
			t.Fatalf("unexpected call after answer: %v", call.Texts())
		}
	}
	beforeResult, err := readTranscriptTurns(ctx, client, managerRef, "")
	if err != nil {
		t.Fatal(err)
	}
	beforeNotices := managerWatchNotices(beforeResult, watchID)
	answered.RespondToolCall("communicate", communicateArgs(result))
	var workerTurns []appwire.Turn
	for {
		workerTurns, err = readTranscriptTurns(ctx, client, workerRef, "")
		if err != nil {
			t.Fatal(err)
		}
		if workerTerminalAccepted(workerTurns, result) {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("worker terminal result was not recorded")
		case <-time.After(50 * time.Millisecond):
		}
	}
	for {
		resultRound, nextErr := provider.Next(ctx.Done())
		if nextErr != nil || !resultRound.Contains(managerOpening) || !resultRound.Contains(watchID) {
			t.Fatalf("Manager did not receive the worker watch after its result: err=%v, call=%v", nextErr, resultRound)
		}
		if strings.Contains(strings.Join(resultRound.Texts(), "\n"), ":communicate") {
			resultRound.RespondToolCall("communicate", communicateArgs("result observed"))
			break
		}
		// The question may already have emitted another status transition when
		// the answer arrived. That notice cannot answer a question or substitute
		// for the later accepted terminal completion.
		resultRound.RespondToolCall("communicate", communicateArgs("late question notice"))
	}
	answerInWorkerHistory := false
	for _, turn := range workerTurns {
		for _, item := range turn.Items {
			if item.Text == answer && item.Actor != nil && item.Actor.Kind == "manager" && item.Actor.ManagerRef == managerRef {
				answerInWorkerHistory = true
			}
		}
	}
	managerTurns, err := readTranscriptTurns(ctx, client, managerRef, "")
	if err != nil {
		t.Fatal(err)
	}
	resultInManagerHistory := managerWatchNotices(managerTurns, watchID) > beforeNotices
	terminalAccepted := workerTerminalAccepted(workerTurns, result)
	if !ownedReportSeen || !answerInWorkerHistory || !terminalAccepted || !resultInManagerHistory || ownedReportAcknowledged {
		t.Fatalf("incomplete independent loop: answerInWorkerHistory=%v terminalAccepted=%v resultInManagerHistory=%v beforeNotices=%d afterNotices=%d ownedReportAcknowledged=%v worker=%v manager=%v", answerInWorkerHistory, terminalAccepted, resultInManagerHistory, beforeNotices, managerWatchNotices(managerTurns, watchID), ownedReportAcknowledged, managerItemSummary(workerTurns), managerItemSummary(managerTurns))
	}
}

func workerTerminalAccepted(turns []appwire.Turn, result string) bool {
	for _, turn := range turns {
		for _, item := range turn.Items {
			if item.Text == result && item.Observation != nil && item.Observation.TerminalAccepted != nil && *item.Observation.TerminalAccepted && item.Observation.EndTurn && item.Observation.EndReason == "done" {
				return true
			}
		}
	}
	return false
}

func managerItemSummary(turns []appwire.Turn) []string {
	var items []string
	for _, turn := range turns {
		for _, item := range turn.Items {
			if item.Type == "systemMessage" {
				continue
			}
			text := item.Text
			if len(text) > 100 {
				text = text[:100]
			}
			items = append(items, turn.Status+":"+item.Type+":"+item.ToolName+":"+text)
		}
	}
	return items
}

func managerWatchNotices(turns []appwire.Turn, watchID string) int {
	count := 0
	for _, turn := range turns {
		for _, item := range turn.Items {
			if item.Type == "steering" && strings.Contains(item.Text, watchID) {
				count++
			}
		}
	}
	return count
}

// A watch rejected after a successful empty launch must leave the new worker
// empty. In particular, the adapter must not reinterpret a watch refusal as a
// failed launch or submit the task despite having no observer.
func TestManagerCreateWatchFailure(t *testing.T) {
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	if testing.Short() {
		t.Skip("live-stack e2e: builds binaries and runs a hub + daemons")
	}
	provider, err := fakellm.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(provider.Close)
	stack := startHubStack(t, provider)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	client := stack.dialRPC(ctx, t)
	const task = "EVENER-NOT-SUBMITTED-4D"
	startManagerThread(ctx, t, client, stack, "EVENER-MANAGER-WATCH-FAILURE-4D")
	opening, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatal(err)
	}
	opening.RespondToolCall("delegate", map[string]any{
		"prompt": task,
		"hub": map[string]any{
			"cwd": stack.workDir, "model": stack.model,
			"watch": map[string]any{"events": []string{"not-a-watch-event"}},
		},
	})
	call, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatal(err)
	}
	var outcome struct {
		Created struct {
			Ref string `json:"ref"`
		} `json:"created"`
		Watch struct {
			Error string `json:"error"`
		} `json:"watch"`
		Input struct {
			Disposition string `json:"disposition"`
		} `json:"input"`
	}
	if !managerToolResult(call, &outcome) {
		t.Fatalf("delegate result missing: %v", call.Texts())
	}
	createdRef := outcome.Created.Ref
	if createdRef != "" {
		t.Cleanup(func() {
			shutdownCtx, done := context.WithTimeout(context.Background(), 20*time.Second)
			defer done()
			if _, err := clientRequest[appwire.EmptyResponse](shutdownCtx, client, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: createdRef}); err != nil {
				t.Errorf("worker shutdown: %v", err)
			}
		})
	}
	watchErr := outcome.Watch.Error
	submittedInputs := 0
	if createdRef != "" {
		read, readErr := clientRequest[appwire.ThreadReadResponse](ctx, client, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: createdRef, IncludeTurns: true})
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, turn := range read.Thread.Turns {
			for _, item := range turn.Items {
				if item.Type == "userMessage" {
					submittedInputs++
				}
			}
		}
	}
	if createdRef == "" || submittedInputs != 0 || watchErr == "" || outcome.Input.Disposition != "" {
		t.Fatalf("partial creation misreported: createdRef=%q submittedInputs=%d watchErr=%q input=%+v", createdRef, submittedInputs, watchErr, outcome.Input)
	}
	call.RespondToolCall("communicate", communicateArgs("creation was partial"))
}

func managerToolResult(call *fakellm.Call, out any) bool {
	messages, _ := call.Body["messages"].([]any)
	for i := len(messages) - 1; i >= 0; i-- {
		raw := messages[i]
		message, _ := raw.(map[string]any)
		if message["role"] != "tool" {
			continue
		}
		body, _ := message["content"].(string)
		if json.Unmarshal([]byte(body), out) == nil {
			return true
		}
	}
	return false
}

// The Manager switch must be committed before the first model turn begins.
// Starting with input and toggling afterward races tool-schema construction.
func startManagerThread(ctx context.Context, t *testing.T, client *appwire.Client, stack hubStack, opening string) string {
	t.Helper()
	started, err := clientRequest[appwire.ThreadStartResponse](ctx, client, appwire.MethodThreadStart, appwire.ThreadStartParams{
		Harness: "evener", CWD: stack.workDir, Model: stack.model,
		LaunchOverrides: &appwire.LaunchConfigLayer{Sandbox: "off"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := started.Thread.Evener.Ref
	if ref == "" {
		t.Fatal("empty Manager launch returned no ref")
	}
	t.Cleanup(func() {
		shutdownCtx, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		shutdown := stack.dialRPC(shutdownCtx, t)
		if _, err := clientRequest[appwire.EmptyResponse](shutdownCtx, shutdown, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: ref}); err != nil {
			t.Errorf("manager shutdown: %v", err)
		}
	})
	if _, err := clientRequest[appwire.ThreadManagerSetResponse](ctx, client, appwire.MethodEvenerThreadManagerSet, appwire.ThreadManagerSetParams{Ref: ref, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := clientRequest[appwire.TurnStartResponse](ctx, client, appwire.MethodTurnStart, appwire.TurnStartParams{
		Ref: ref, ClientMutationID: newMutationID(t), ExpectedInstanceID: started.Thread.Evener.InstanceID,
		Input: []appwire.InputItem{{Type: "text", Text: opening}},
	}); err != nil {
		t.Fatal(err)
	}
	return ref
}
