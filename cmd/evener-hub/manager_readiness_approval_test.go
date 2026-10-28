package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/e2ecap"
	"primeradiant.com/evener/test/e2e/fakellm"
)

// The only scripted component is the provider. B observes and approves the
// restricted A worker through B's actual reverse-attached host registry.
func TestE2E_ManagerReadinessRemoteApprovalSSH(t *testing.T) {
	if os.Getenv("EVENER_SSH_E2E") != "1" || os.Getenv("EVENER_SSH_E2E_DEPLOY") != "1" {
		t.Skip("requires both SSH E2E opt-ins; uses only disposable localhost SSH fixtures")
	}
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	if testing.Short() {
		t.Skip("live-stack e2e: localhost SSH, two hubs and daemons")
	}
	probe, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	if err := exec.CommandContext(probe, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=3", "localhost", "true").Run(); err != nil {
		t.Fatalf("required localhost SSH unavailable: %v", err)
	}
	provider, err := fakellm.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(provider.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	a, aHuman, b, bHuman, _, bHubID := startManagerReadinessAttachedFixture(ctx, t, provider)

	// The denied target is owned by this test, outside A's worker workspace.
	outside := filepath.Join(t.TempDir(), "remote-approval-target.txt")
	before := []byte("before remote approval\n")
	approved := []byte("approved by B Manager\n")
	if err := os.WriteFile(outside, before, 0o600); err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(a.workDir, outside)
	if err != nil || rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		t.Fatalf("approval target %q must be outside worker workspace %q: relative=%q err=%v", outside, a.workDir, rel, err)
	}
	worker, err := clientRequest[appwire.ThreadStartResponse](ctx, aHuman, appwire.MethodThreadStart, appwire.ThreadStartParams{
		Harness: "evener", CWD: a.workDir, Model: a.model,
		LaunchOverrides: &appwire.LaunchConfigLayer{Sandbox: "restricted"},
	})
	if err != nil {
		t.Fatal(err)
	}
	workerRef := worker.Thread.Evener.Ref
	t.Cleanup(func() {
		shutdown, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		if _, err := clientRequest[appwire.EmptyResponse](shutdown, aHuman, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: workerRef}); err != nil {
			t.Errorf("A worker shutdown: %v", err)
		}
	})
	workerParsed, err := appwire.ParseRef(workerRef)
	if err != nil {
		t.Fatal(err)
	}
	remoteRef := (appwire.Ref{SourceID: "readiness-a", ThreadID: workerParsed.ThreadID}).String()
	owner, err := clientRequest[appwire.ThreadReadResponse](ctx, aHuman, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: workerRef})
	if err != nil {
		t.Fatal(err)
	}
	if owner.Thread.Evener.Ref != workerRef || owner.Thread.Evener.InstanceID == "" || owner.Thread.Evener.InstanceID != worker.Thread.Evener.InstanceID || owner.Thread.Evener.Manager || owner.Thread.Evener.ParentRef != "" || owner.Thread.Evener.Access == nil || owner.Thread.Evener.Access.Sandbox != "restricted" {
		t.Fatalf("A-owned independent restricted worker: %+v", owner.Thread.Evener)
	}

	const opening = "READINESS-B-REMOTE-APPROVAL"
	const task = "READINESS-A-RESTRICTED-WRITE"
	const finished = "READINESS-A-WRITE-FINISHED"
	manager, err := clientRequest[appwire.ThreadStartResponse](ctx, bHuman, appwire.MethodThreadStart, appwire.ThreadStartParams{
		Harness: "evener", CWD: b.workDir, Model: b.model, Manager: true,
		LaunchOverrides: &appwire.LaunchConfigLayer{Sandbox: "off"},
		Input:           []appwire.InputItem{{Type: "text", Text: opening}},
	})
	if err != nil {
		t.Fatal(err)
	}
	managerRef := manager.Thread.Evener.Ref
	t.Cleanup(func() {
		shutdown, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		if _, err := clientRequest[appwire.EmptyResponse](shutdown, bHuman, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: managerRef}); err != nil {
			t.Errorf("B Manager shutdown: %v", err)
		}
	})
	managerID, err := appwire.ParseRef(managerRef)
	if err != nil {
		t.Fatal(err)
	}
	wantActor := (appwire.Ref{SourceID: bHubID, ThreadID: managerID.ThreadID}).String()
	call, err := provider.Next(ctx.Done())
	if err != nil || !call.Contains(opening) || !managerRequestHasTool(call, "job_watch") {
		t.Fatalf("B Manager opening: call=%v err=%v", call, err)
	}
	call.RespondToolCall("job_watch", map[string]any{"operation": "create", "hub": map[string]any{"ref": remoteRef, "events": []string{"approval"}}})
	call, err = provider.Next(ctx.Done())
	if err != nil || !call.Contains(opening) {
		t.Fatalf("B watch registration: call=%v err=%v", call, err)
	}
	watches, err := clientRequest[appwire.SessionWatchesResponse](ctx, bHuman, appwire.MethodEvenerThreadWatchesList, appwire.SessionActivityListParams{Ref: managerRef})
	if err != nil {
		t.Fatal(err)
	}
	watchID := ""
	for _, row := range watches.Watches {
		if row.Watch.Target == remoteRef && row.Watch.Active && row.State == appwire.SessionWatchStateArmed && row.SourceRef == managerRef && row.ReceiverRef == managerRef {
			watchID = row.Watch.ID
		}
	}
	if watchID == "" {
		t.Fatalf("B shared watch not armed for remote A worker: %+v", watches.Watches)
	}
	call.RespondToolCall("communicate", communicateArgs("remote approval watch armed"))
	awaitThread(ctx, t, bHuman, managerRef, "B Manager waiting with approval watch", func(thread appwire.Thread) bool { return thread.Status.Type == appwire.ThreadStatusIdle })

	// A's ordinary human RPC starts the worker but never subscribes to its
	// thread or resolves the approval. B's watch is already armed.
	if _, err := clientRequest[appwire.TurnStartResponse](ctx, aHuman, appwire.MethodTurnStart, appwire.TurnStartParams{
		Ref: workerRef, ExpectedInstanceID: owner.Thread.Evener.InstanceID, ClientMutationID: newMutationID(t),
		Input: []appwire.InputItem{{Type: "text", Text: task}},
	}); err != nil {
		t.Fatal(err)
	}
	workerFirst, err := provider.Next(ctx.Done())
	if err != nil || !workerFirst.Contains(task) || !managerRequestHasTool(workerFirst, "write_file") {
		t.Fatalf("real A worker provider request: call=%v err=%v", workerFirst, err)
	}
	workerFirst.RespondToolCall("write_file", map[string]any{"file_path": outside, "content": string(approved)})
	// Only the remote approval event can wake the idle Manager. The event's
	// watch ID is independently obtained from B's durable watch activity.
	call, err = provider.Next(ctx.Done())
	if err != nil || !call.Contains(opening) || !call.Contains(watchID) {
		t.Fatalf("B Manager missed A approval watch %q: call=%v err=%v", watchID, call, err)
	}
	call.RespondToolCall("job_status", map[string]any{"hub": map[string]any{"ref": remoteRef}})
	call, err = provider.Next(ctx.Done())
	if err != nil || !call.Contains(opening) {
		t.Fatalf("B read pending A escalation: call=%v err=%v", call, err)
	}
	var pending appwire.SessionOperationResponse
	if !managerToolResult(call, &pending) || pending.Read == nil || pending.Read.Thread.Evener.Ref != remoteRef || pending.Read.Thread.Evener.InstanceID != owner.Thread.Evener.InstanceID || len(pending.Read.Thread.Evener.PendingEscalations) != 1 {
		t.Fatalf("B Manager pending remote escalation: %+v refusal=%s", pending, managerToolRefusal(call))
	}
	escalation := pending.Read.Thread.Evener.PendingEscalations[0]
	if escalation.EscalationID == "" || escalation.Tool != "write_file" || escalation.DeniedPath != outside {
		t.Fatalf("pending escalation not the real denied A write: %+v", escalation)
	}
	actual, err := os.ReadFile(outside)
	if err != nil || !bytes.Equal(actual, before) {
		t.Fatalf("A target changed while approval pending: bytes=%q err=%v", actual, err)
	}
	beforeTurns, err := readTranscriptTurns(ctx, aHuman, workerRef, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, turn := range beforeTurns {
		for _, item := range turn.Items {
			if item.EventKind == appwire.ThreadItemEventKindApprovalDecision {
				t.Fatalf("A saved a decision before B approved: %+v", item)
			}
		}
	}
	call.RespondToolCall("session_control", map[string]any{"action": "approval", "arguments": map[string]any{"ref": remoteRef, "escalationId": escalation.EscalationID, "approve": true}})
	var workerAfter *fakellm.Call
	confirmed := false
	for workerAfter == nil || !confirmed {
		call, err = provider.Next(ctx.Done())
		if err != nil {
			t.Fatalf("await approved A worker/B Manager: %v", err)
		}
		switch {
		case call.Contains(opening):
			var controlled appwire.SessionOperationResponse
			if !managerToolResult(call, &controlled) || controlled.Control == nil || controlled.Control.Actor.Kind != "manager" || controlled.Control.Actor.ManagerRef != managerRef {
				t.Fatalf("B Manager approval control: %+v refusal=%s", controlled, managerToolRefusal(call))
			}
			confirmed = true
			call.RespondToolCall("communicate", communicateArgs("A approval granted"))
		case call.Contains(task):
			workerAfter = call
		default:
			t.Fatalf("unexpected provider call after B approval: %v", call.Texts())
		}
	}
	actual, err = os.ReadFile(outside)
	if err != nil || !bytes.Equal(actual, approved) {
		t.Fatalf("approved A write not on disk: bytes=%q err=%v", actual, err)
	}
	workerAfter.RespondToolCall("communicate", communicateArgs(finished))
	awaitThread(ctx, t, aHuman, workerRef, "A worker terminal", func(thread appwire.Thread) bool { return thread.Status.Type == appwire.ThreadStatusIdle })
	workerTurns, err := readTranscriptTurns(ctx, aHuman, workerRef, "")
	if err != nil {
		t.Fatal(err)
	}
	approvalDecisions, successfulWrites := 0, 0
	for _, turn := range workerTurns {
		calls := map[string]appwire.ThreadItem{}
		for _, item := range turn.Items {
			if item.ToolName == "write_file" && item.CallID != "" && item.ArgumentsJSON != "" {
				calls[item.CallID] = item
			}
			if item.EventKind == appwire.ThreadItemEventKindApprovalDecision {
				approvalDecisions++
				var raw struct {
					ApprovalDecision struct {
						EscalationID string `json:"escalationId"`
						Approved     bool   `json:"approved"`
						Tool         string `json:"tool"`
						DeniedPath   string `json:"deniedPath"`
					} `json:"approvalDecision"`
				}
				if err := json.Unmarshal(item.Raw, &raw); err != nil || raw.ApprovalDecision.EscalationID != escalation.EscalationID || !raw.ApprovalDecision.Approved || raw.ApprovalDecision.Tool != "write_file" || raw.ApprovalDecision.DeniedPath != outside || item.Actor == nil || item.Actor.Kind != "manager" || item.Actor.ManagerRef != wantActor {
					t.Fatalf("A saved approval decision differs from B actor/escalation: item=%+v decoded=%+v err=%v wantActor=%s", item, raw, err, wantActor)
				}
			}
		}
		for _, item := range turn.Items {
			call, exists := calls[item.CallID]
			if !exists || item.ToolName != "write_file" || item.Status != appwire.TurnStatusCompleted || item.Error != "" {
				continue
			}
			var args struct {
				FilePath string `json:"file_path"`
				Content  string `json:"content"`
			}
			if err := json.Unmarshal([]byte(call.ArgumentsJSON), &args); err != nil || args.FilePath != outside || args.Content != string(approved) {
				t.Fatalf("saved A write call is not denied target/content: %+v args=%+v err=%v", call, args, err)
			}
			successfulWrites++
		}
	}
	if approvalDecisions != 1 || successfulWrites != 1 || !workerTerminalAccepted(workerTurns, finished) {
		t.Fatalf("A saved result: decisions=%d writes=%d terminal=%t items=%v", approvalDecisions, successfulWrites, workerTerminalAccepted(workerTurns, finished), managerItemSummary(workerTurns))
	}
	managerTurns, err := readTranscriptTurns(ctx, bHuman, managerRef, "")
	if err != nil {
		t.Fatal(err)
	}
	assertSavedRemoteApprovalManager(t, managerTurns, managerRef, remoteRef, outside, watchID, escalation.EscalationID)
	watches, err = clientRequest[appwire.SessionWatchesResponse](ctx, bHuman, appwire.MethodEvenerThreadWatchesList, appwire.SessionActivityListParams{Ref: managerRef})
	if err != nil {
		t.Fatal(err)
	}
	foundWatch := false
	for _, row := range watches.Watches {
		if row.Watch.ID == watchID && row.Watch.Target == remoteRef && row.Watch.Deliveries > 0 && row.SourceRef == managerRef && row.ReceiverRef == managerRef {
			foundWatch = true
		}
	}
	if !foundWatch {
		t.Fatalf("B watch delivery missing from saved activity: %+v", watches.Watches)
	}
	// Ordinary non-subscribing human reads must still work after the Manager's
	// independent watch and control completed.
	last, err := clientRequest[appwire.ThreadReadResponse](ctx, aHuman, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: workerRef})
	if err != nil || last.Thread.Evener.InstanceID != owner.Thread.Evener.InstanceID {
		t.Fatalf("ordinary A human read after approval: %+v %v", last, err)
	}
	t.Logf("remote approval evidence: watch=%s escalation=%s savedDecisionActor=%s decisions=%d successfulWrites=%d terminalAccepted=true", watchID, escalation.EscalationID, wantActor, approvalDecisions, successfulWrites)
}

func assertSavedRemoteApprovalManager(t *testing.T, turns []appwire.Turn, managerRef, remoteRef, outside, watchID, escalationID string) {
	t.Helper()
	if managerWatchNotices(turns, watchID) < 1 {
		t.Fatalf("B saved transcript has no correlated watch wake: %v", managerItemSummary(turns))
	}
	watchCalls, reads, controls := 0, 0, 0
	for _, turn := range turns {
		calls := map[string]appwire.ThreadItem{}
		for _, item := range turn.Items {
			if item.CallID != "" && item.ArgumentsJSON != "" {
				calls[item.CallID] = item
			}
		}
		for _, item := range turn.Items {
			call, ok := calls[item.CallID]
			if !ok || item.ToolName != call.ToolName || item.Output == "" {
				continue
			}
			switch call.ToolName {
			case "job_watch":
				var args struct {
					Operation string `json:"operation"`
					Hub       struct {
						Ref    string   `json:"ref"`
						Events []string `json:"events"`
					} `json:"hub"`
				}
				if err := json.Unmarshal([]byte(call.ArgumentsJSON), &args); err != nil || args.Operation != "create" || args.Hub.Ref != remoteRef || len(args.Hub.Events) != 1 || args.Hub.Events[0] != "approval" {
					t.Fatalf("B saved watch did not target A approval: %+v err=%v", call, err)
				}
				watchCalls++
			case "job_status":
				var response appwire.SessionOperationResponse
				if err := json.Unmarshal([]byte(item.Output), &response); err != nil || response.Actor.Kind != "manager" || response.Actor.ManagerRef != managerRef || response.Read == nil || response.Read.Thread.Evener.Ref != remoteRef || len(response.Read.Thread.Evener.PendingEscalations) != 1 || response.Read.Thread.Evener.PendingEscalations[0].EscalationID != escalationID || response.Read.Thread.Evener.PendingEscalations[0].DeniedPath != outside {
					t.Fatalf("B saved read not A pending escalation: %+v err=%v", item, err)
				}
				reads++
			case "session_control":
				var args struct {
					Action    string `json:"action"`
					Arguments struct {
						Ref          string `json:"ref"`
						EscalationID string `json:"escalationId"`
						Approve      bool   `json:"approve"`
					} `json:"arguments"`
				}
				var response appwire.SessionOperationResponse
				if err := json.Unmarshal([]byte(call.ArgumentsJSON), &args); err != nil || args.Action != "approval" || args.Arguments.Ref != remoteRef || args.Arguments.EscalationID != escalationID || !args.Arguments.Approve {
					t.Fatalf("B saved control not A approval: %+v args=%+v err=%v", call, args, err)
				}
				if err := json.Unmarshal([]byte(item.Output), &response); err != nil || response.Control == nil || response.Control.Actor.Kind != "manager" || response.Control.Actor.ManagerRef != managerRef {
					t.Fatalf("B saved control result not Manager: %+v err=%v", item, err)
				}
				controls++
			}
		}
	}
	if watchCalls != 1 || reads != 1 || controls != 1 {
		t.Fatalf("B saved call/result counts: watch=%d read=%d control=%d items=%v", watchCalls, reads, controls, managerItemSummary(turns))
	}
}
