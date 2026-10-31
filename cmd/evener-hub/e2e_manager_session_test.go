package hub

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
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
			Created appwire.ManagerView `json:"created"`
			Watch   struct {
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
	// The Manager's completion watch is emitted only after the worker's
	// accepted terminal record. Await that causal signal before reading history.
	for {
		resultRound, nextErr := provider.Next(ctx.Done())
		if nextErr != nil || !resultRound.Contains(managerOpening) || !resultRound.Contains(watchID) {
			t.Fatalf("Manager did not receive the worker watch after its result: err=%v, call=%v", nextErr, resultRound)
		}
		if strings.Contains(strings.Join(resultRound.Texts(), "\n"), "completed: accepted terminal done") {
			resultRound.RespondToolCall("communicate", communicateArgs("result observed"))
			break
		}
		// The question may already have emitted another status transition when
		// the answer arrived. That notice cannot answer a question or substitute
		// for the later accepted terminal completion.
		resultRound.RespondToolCall("communicate", communicateArgs("late question notice"))
	}
	workerTurns, err := readTranscriptTurns(ctx, client, workerRef, "")
	if err != nil {
		t.Fatal(err)
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

// The browser never subscribes to the worker. Its Manager watch is attached
// before the first prompt, and is the only subscriber eligible to receive a
// sandbox card. A real restricted file-tool denial must block until the
// Manager resolves it through the daemon's approval owner.
func TestE2E_ManagerNoBrowserSandboxApproval(t *testing.T) {
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
	// The Manager overrides this global policy to off in startManagerThread;
	// its independently created worker inherits the real restricted backend.
	if _, err := clientRequest[appwire.LaunchConfigResolved](ctx, client, appwire.MethodEvenerLaunchSetLayer, appwire.LaunchConfigSetLayerParams{
		CWD: stack.workDir, Layer: "global", Config: appwire.LaunchConfigLayer{Sandbox: "restricted"},
	}); err != nil {
		t.Fatal(err)
	}
	const managerOpening = "EVENER-MANAGER-APPROVAL-4D"
	const workerTask = "EVENER-WORKER-APPROVAL-TASK-4D"
	const approvedContent = "EVENER-MANAGER-APPROVED-WRITE-4D\n"
	const result = "EVENER-WORKER-APPROVAL-DONE-4D"
	outside := filepath.Join(t.TempDir(), "approval-target.txt")
	if err := os.WriteFile(outside, []byte("before approval\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	managerRef := startManagerThread(ctx, t, client, stack, managerOpening)
	opening, err := provider.Next(ctx.Done())
	if err != nil || !opening.Contains(managerOpening) {
		t.Fatalf("manager opening request: call=%v err=%v", opening, err)
	}
	opening.RespondToolCall("delegate", map[string]any{
		"prompt": workerTask,
		"hub": map[string]any{
			"cwd": stack.workDir, "model": stack.model,
			"watch": map[string]any{"events": []string{"approval"}, "completion": true},
		},
	})
	var workerFirst *fakellm.Call
	var workerRef, workerInstance, watchID string
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
				Status string `json:"status"`
				appwire.ManagerView
			} `json:"created"`
			Watch struct {
				Status  string `json:"status"`
				WatchID string `json:"watch_id"`
			} `json:"watch"`
			Input struct {
				Status string `json:"status"`
			} `json:"input"`
		}
		if !managerToolResult(call, &outcome) || outcome.Created.Status != "created" || outcome.Created.Ref == "" || outcome.Created.InstanceID == "" || outcome.Watch.Status != "attached" || outcome.Watch.WatchID == "" || outcome.Input.Status != "submitted" {
			t.Fatalf("creation, watch and input did not succeed in order: %+v, messages=%v", outcome, call.Texts())
		}
		workerRef, workerInstance, watchID = outcome.Created.Ref, outcome.Created.InstanceID, outcome.Watch.WatchID
		call.RespondToolCall("communicate", communicateArgs("worker launched"))
	}
	t.Cleanup(func() {
		shutdownCtx, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		if _, err := clientRequest[appwire.EmptyResponse](shutdownCtx, client, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: workerRef}); err != nil {
			t.Errorf("worker shutdown: %v", err)
		}
	})
	var worker appwire.Thread
	awaitThread(ctx, t, client, workerRef, "restricted independent worker", func(thread appwire.Thread) bool {
		worker = thread
		return thread.Evener.InstanceID == workerInstance && thread.Evener.Access != nil
	})
	if worker.Evener.Kind == "subagent" || worker.Evener.Manager || worker.Evener.ParentRef != "" || worker.Evener.Ref == managerRef || worker.Evener.Access.Sandbox != "restricted" {
		t.Fatalf("worker is not an independent restricted session: %+v", worker.Evener)
	}
	workerFirst.RespondToolCall("write_file", map[string]any{"file_path": outside, "content": approvedContent})
	approvalRound, err := provider.Next(ctx.Done())
	if err != nil || !approvalRound.Contains(managerOpening) || !approvalRound.Contains(watchID) || !strings.Contains(strings.Join(approvalRound.Texts(), "\n"), ":approval") {
		t.Fatalf("Manager did not receive the first-turn sandbox approval watch: call=%v err=%v", approvalRound, err)
	}
	approvalRound.RespondToolCall("job_status", map[string]any{"hub": map[string]any{"ref": workerRef}})
	statusRound, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatal(err)
	}
	var status appwire.SessionOperationResponse
	if !managerToolResult(statusRound, &status) || status.Read == nil || len(status.Read.Thread.Evener.PendingEscalations) != 1 {
		t.Fatalf("Manager status did not expose pending sandbox approval: read=%+v messages=%v", status.Read, statusRound.Texts())
	}
	escalation := status.Read.Thread.Evener.PendingEscalations[0]
	if escalation.EscalationID == "" || escalation.DeniedPath != outside || escalation.Tool != "write_file" {
		t.Fatalf("approval does not belong to worker's denied write: %+v", escalation)
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "before approval\n" {
		t.Fatalf("worker wrote before approval: data=%q err=%v", data, err)
	}
	statusRound.RespondToolCall("session_control", map[string]any{
		"action": "approval", "arguments": map[string]any{"ref": workerRef, "escalationId": escalation.EscalationID, "approve": true},
	})
	var workerAfter *fakellm.Call
	controlConfirmed := false
	for workerAfter == nil || !controlConfirmed {
		call, nextErr := provider.Next(ctx.Done())
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		if call.Contains(managerOpening) {
			var control appwire.SessionOperationResponse
			if !managerToolResult(call, &control) || control.Control == nil || control.Control.Actor.Kind != "manager" || control.Control.Actor.ManagerRef != managerRef {
				t.Fatalf("approval was not accepted as Manager: response=%+v messages=%v", control, call.Texts())
			}
			controlConfirmed = true
			call.RespondToolCall("communicate", communicateArgs("approval granted"))
		} else if call.Contains(workerTask) {
			workerAfter = call
		} else {
			t.Fatalf("unexpected provider request after approval: %v", call.Texts())
		}
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != approvedContent {
		t.Fatalf("approved sandbox write did not execute: data=%q err=%v", data, err)
	}
	workerAfter.RespondToolCall("communicate", communicateArgs(result))
	completionRound, err := provider.Next(ctx.Done())
	if err != nil || !completionRound.Contains(managerOpening) || !completionRound.Contains(watchID) || !strings.Contains(strings.Join(completionRound.Texts(), "\n"), "completed: accepted terminal done") {
		t.Fatalf("Manager did not receive accepted worker completion: call=%v err=%v", completionRound, err)
	}
	completionRound.RespondToolCall("communicate", communicateArgs("worker complete"))
	turns, err := readTranscriptTurns(ctx, client, workerRef, "")
	if err != nil {
		t.Fatal(err)
	}
	decisions := 0
	for _, turn := range turns {
		for _, item := range turn.Items {
			if item.EventKind != appwire.ThreadItemEventKindApprovalDecision {
				continue
			}
			decisions++
			if item.Actor == nil || item.Actor.Kind != "manager" || item.Actor.ManagerRef != managerRef {
				t.Fatalf("approval decision attributed to someone other than Manager: %+v", item)
			}
		}
	}
	if decisions != 1 || !workerTerminalAccepted(turns, result) {
		t.Fatalf("worker approval/terminal record: decisions=%d terminal=%v turns=%v", decisions, workerTerminalAccepted(turns, result), managerItemSummary(turns))
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

// Invalid selectors are rejected before the hub creates a worker. The separate
// in-process creation test covers a watch refusal after successful creation.
func TestManagerCreateRejectsInvalidWatchSelectorBeforeCreate(t *testing.T) {
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
	managerRef := startManagerThread(ctx, t, client, stack, "EVENER-MANAGER-WATCH-FAILURE-4D")
	listRoster := func(listCtx context.Context) (map[string]appwire.Thread, error) {
		threads := make(map[string]appwire.Thread)
		cursor := ""
		for {
			page, err := clientRequest[appwire.ThreadListResponse](listCtx, client, appwire.MethodThreadList, appwire.ThreadListParams{Cursor: cursor, Limit: 100})
			if err != nil {
				return nil, err
			}
			for _, thread := range page.Data {
				threads[thread.Evener.Ref] = thread
			}
			if page.NextCursor == "" {
				return threads, nil
			}
			cursor = page.NextCursor
		}
	}
	before, err := listRoster(ctx)
	if err != nil {
		t.Fatal(err)
	}
	manager, managerPresent := before[managerRef]
	if !managerPresent || !manager.Evener.Manager || manager.Evener.InstanceID == "" {
		t.Fatalf("Manager identity missing from initial hub roster: ref=%q roster=%v", managerRef, before)
	}
	// If a regression creates a worker despite rejecting this request, shut it
	// down before the earlier Manager/stack cleanup runs.
	t.Cleanup(func() {
		cleanupCtx, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		after, err := listRoster(cleanupCtx)
		if err != nil {
			t.Errorf("list hub roster for unexpected-worker cleanup: %v", err)
			return
		}
		for ref := range after {
			if _, existed := before[ref]; existed {
				continue
			}
			if _, err := clientRequest[appwire.EmptyResponse](cleanupCtx, client, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: ref}); err != nil {
				t.Errorf("shutdown unexpected worker %q: %v", ref, err)
			}
		}
	})
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
	call.RespondToolCall("communicate", communicateArgs("invalid selector handled"))
	awaitThread(ctx, t, client, managerRef, "Manager idle after selector rejection", func(thread appwire.Thread) bool {
		return thread.Status.Type == appwire.ThreadStatusIdle
	})
	turns, err := readTranscriptTurns(ctx, client, managerRef, "full")
	if err != nil {
		t.Fatal(err)
	}
	submittedSelector, rejectedBeforeDispatch := false, false
	for _, turn := range turns {
		for _, item := range turn.Items {
			if item.ToolName != "delegate" {
				continue
			}
			var args struct {
				Prompt string `json:"prompt"`
				Hub    struct {
					CWD   string `json:"cwd"`
					Model string `json:"model"`
					Watch struct {
						Events []string `json:"events"`
					} `json:"watch"`
				} `json:"hub"`
			}
			if err := json.Unmarshal([]byte(item.ArgumentsJSON), &args); err != nil {
				continue
			}
			if args.Prompt != task || args.Hub.CWD != stack.workDir || args.Hub.Model != stack.model || !slices.Equal(args.Hub.Watch.Events, []string{"not-a-watch-event"}) {
				continue
			}
			submittedSelector = true
			if item.PrevalOnly && item.Error != "" {
				rejectedBeforeDispatch = true
			}
		}
	}
	if !submittedSelector || !rejectedBeforeDispatch {
		t.Fatalf("invalid selector was not recorded as a pre-dispatch rejection: submitted=%v rejected=%v turns=%+v", submittedSelector, rejectedBeforeDispatch, turns)
	}
	after, err := listRoster(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("hub roster changed after rejected selector: before=%v after=%v", before, after)
	}
	for ref, prior := range before {
		current, ok := after[ref]
		if !ok || current.Evener.InstanceID != prior.Evener.InstanceID {
			t.Fatalf("hub roster identity changed after rejected selector: ref=%q before=%+v after=%+v", ref, prior.Evener, current.Evener)
		}
	}
	if !after[managerRef].Evener.Manager {
		t.Fatalf("Manager role missing after rejected selector: before=%+v after=%+v", manager.Evener, after[managerRef].Evener)
	}
}

func managerToolResult(call *fakellm.Call, out any) bool {
	messages, _ := call.Body["messages"].([]any)
	for _, raw := range slices.Backward(messages) {
		message, _ := raw.(map[string]any)
		if message["role"] != "tool" {
			continue
		}
		body, _ := message["content"].(string)
		return json.Unmarshal([]byte(body), out) == nil
	}
	return false
}

func managerToolRefusal(call *fakellm.Call) string {
	messages, _ := call.Body["messages"].([]any)
	for _, raw := range slices.Backward(messages) {
		message, _ := raw.(map[string]any)
		if message["role"] != "tool" {
			continue
		}
		body, _ := message["content"].(string)
		if json.Valid([]byte(body)) {
			return "latest tool returned JSON without the expected operation"
		}
		return body
	}
	return "no tool result"
}

func TestManagerToolResultRejectsLatestRefusal(t *testing.T) {
	call := &fakellm.Call{Body: map[string]any{"messages": []any{
		map[string]any{"role": "tool", "content": `{"actor":{"kind":"manager"},"read":{}}`},
		map[string]any{"role": "tool", "content": "appwire thread/read: thread subscription is unavailable"},
	}}}
	var response appwire.SessionOperationResponse
	if managerToolResult(call, &response) {
		t.Fatal("parsed a stale successful operation after the current refusal")
	}
	if got := managerToolRefusal(call); got != "appwire thread/read: thread subscription is unavailable" {
		t.Fatalf("latest refusal=%q", got)
	}
}

// The Manager switch must be committed before the first model turn begins.
// Starting with input and toggling afterward races tool-schema construction.
func startManagerThread(ctx context.Context, t *testing.T, client *appwire.Client, stack hubStack, opening string) string {
	t.Helper()
	started, err := clientRequest[appwire.ThreadStartResponse](ctx, client, appwire.MethodThreadStart, appwire.ThreadStartParams{
		Harness: "evener", CWD: stack.workDir, Model: stack.model,
		LaunchOverrides: &appwire.LaunchConfigLayer{Sandbox: "off"},
		Manager:         true,
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
	if _, err := clientRequest[appwire.TurnStartResponse](ctx, client, appwire.MethodTurnStart, appwire.TurnStartParams{
		Ref: ref, ClientMutationID: newMutationID(t), ExpectedInstanceID: started.Thread.Evener.InstanceID,
		Input: []appwire.InputItem{{Type: "text", Text: opening}},
	}); err != nil {
		t.Fatal(err)
	}
	return ref
}
