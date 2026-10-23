package hub

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/internal/e2ecap"
	"primeradiant.com/evener/test/e2e/fakellm"
)

// Only the model is scripted. The SSH host, two hubs, daemons, human RPC,
// Manager binding, observation lease, input, sandbox and histories are real.
func TestE2E_ManagerAcceptance(t *testing.T) {
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	if testing.Short() {
		t.Skip("live-stack e2e: SSH host, two hubs and daemons")
	}
	probe, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	if err := exec.CommandContext(probe, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=3", "localhost", "true").Run(); err != nil {
		t.Fatalf("required remote host unavailable: %v", err)
	}
	provider, err := fakellm.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(provider.Close)
	local := startHubStack(t, provider)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	human := local.dialRPC(ctx, t)
	// A private, initially empty real SSH host. No session is created there until
	// after the Manager has discovered the host and selected its model catalog.
	host := newHostSSH(t, "localhost", "")
	hostDir := filepath.Join(os.TempDir(), "evener-manager-accept-"+hostDeployRunID())
	host.prepareDeployDir(hostDir)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if stopHostListener(t, host, addr, hostDir+"/"+hostDeployToml) {
			// Stopping the listener does not stop the session daemons the hub
			// started over SSH; deleting the directory underneath them leaves
			// them running with no owner.
			if err := host.tryRun(hostDaemonReapCommand(hostDir)); err != nil {
				t.Errorf("reap SSH-hosted daemons: %v", err)
				return
			}
			if err := host.tryRun("rm -rf " + shellquote.RemoteWord(hostDir)); err != nil {
				t.Errorf("clean SSH fixture: %v", err)
			}
		}
	})
	host.writeFile(hostDir+"/"+hostDeployToml, hostDeployConfig(hostDir, addr))
	host.mustRun("cp " + shellquote.RemoteWord(filepath.Join(local.binDir, "evener")) + " " + shellquote.RemoteWord(hostDir+"/bin/evener-real"))
	host.mustRun("mkdir -p " + shellquote.RemoteWord(hostDir+"/home/config/evener") + " " + shellquote.RemoteWord(hostDir+"/workspace"))
	host.writeFile(hostDir+"/home/config/evener/providers.toml", []byte(fmt.Sprintf("default = \"fake\"\n[providers.fake]\nbase = \"openai-compatible\"\nbase_url = %q\napi_key = \"fakellm-not-a-secret\"\n", provider.BaseURL())))
	host.writeFile(hostDir+"/bin/evener", []byte(fmt.Sprintf("#!/bin/sh\nexport HOME=%s XDG_CONFIG_HOME=%s XDG_STATE_HOME=%s XDG_CACHE_HOME=%s EVENER_PROVIDERS_CONFIG=%s\nexec %s \"$@\"\n", shellquote.RemoteWord(hostDir+"/home"), shellquote.RemoteWord(hostDir+"/home/config"), shellquote.RemoteWord(hostDir+"/home/state"), shellquote.RemoteWord(hostDir+"/home/cache"), shellquote.RemoteWord(hostDir+"/home/config/evener/providers.toml"), shellquote.RemoteWord(hostDir+"/bin/evener-real"))))
	host.mustRun("chmod 700 " + shellquote.RemoteWord(hostDir+"/bin/evener"))
	sshDir := hostDeploySSHWrapper(t, hostDir)
	t.Setenv("PATH", sshDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	const remoteHost = "accept-remote"
	if _, err := hostAddCommittedRow(ctx, human, appwire.HostAddParams{Entry: appwire.HostEntry{Name: remoteHost, Address: "localhost", EvenerPath: hostDir + "/bin/evener", ConfigPath: hostDir + "/" + hostDeployToml, Addr: addr}}); err != nil {
		t.Fatal(err)
	}
	awaitHostAttachedWithin(ctx, t, human, remoteHost, hostDeployAttachTimeout)
	if _, err := clientRequest[appwire.LaunchConfigResolved](ctx, human, appwire.MethodEvenerLaunchSetLayer, appwire.LaunchConfigSetLayerParams{CWD: local.workDir, Layer: "global", Config: appwire.LaunchConfigLayer{Sandbox: "restricted"}}); err != nil {
		t.Fatal(err)
	}
	const opening = "ACCEPT-MANAGER-OPENING"
	managerRef := startManagerThread(ctx, t, human, local, opening)
	next := func(marker string) *fakellm.Call {
		t.Helper()
		c, e := provider.Next(ctx.Done())
		if e != nil || !c.Contains(marker) {
			t.Fatalf("expected provider request for %q: call=%v err=%v", marker, c, e)
		}
		return c
	}
	outcome := func(c *fakellm.Call) appwire.SessionOperationResponse {
		t.Helper()
		var result appwire.SessionOperationResponse
		if !managerToolResult(c, &result) {
			t.Fatalf("Manager operation: %s, messages=%v", managerToolRefusal(c), c.Texts())
		}
		if result.Actor.Kind != "manager" || result.Actor.ManagerRef != managerRef {
			t.Fatalf("wrong Manager actor: %+v", result.Actor)
		}
		return result
	}
	call := next(opening)
	call.RespondToolCall("job_list", map[string]any{"hub": map[string]any{"mode": "discovery"}})
	call = next(opening)
	discovered := outcome(call)
	if discovered.Discover == nil {
		t.Fatalf("missing discovery: %+v", discovered)
	}
	hasEmptyRemote := false
	for _, source := range discovered.Discover.Sources {
		if source.ID == remoteHost {
			hasEmptyRemote = true
		}
	}
	for _, thread := range discovered.Discover.Threads.Data {
		if strings.HasPrefix(thread.Evener.Ref, remoteHost+":") {
			t.Fatalf("host was not empty at discovery: %+v", thread.Evener)
		}
	}
	if !hasEmptyRemote {
		t.Fatalf("empty remote host absent from source discovery: %+v", discovered.Discover.Sources)
	}
	call.RespondToolCall("model_list", map[string]any{"hub": map[string]any{"host": remoteHost, "refresh": true}})
	call = next(opening)
	models := outcome(call)
	selectedModel := ""
	if models.Models != nil {
		for _, model := range models.Models.Data {
			if model.Provider == "fake" && model.Model == fakellm.ModelID {
				selectedModel = model.Provider + "/" + model.Model
			}
		}
	}
	if selectedModel == "" {
		t.Fatalf("remote model unavailable: %+v", models.Models)
	}
	// The local worker uses a local observation lease: remote job_watch is not
	// advertised, and this test does not claim a remote approval watch works.
	const task = "ACCEPT-WORKER-TASK"
	call.RespondToolCall("delegate", map[string]any{"prompt": task, "hub": map[string]any{"cwd": local.workDir, "model": local.model, "watch": map[string]any{"events": []string{"question", "approval"}, "completion": true}}})
	var workerFirst *fakellm.Call
	var workerRef, workerInstance, watchID string
	for workerFirst == nil || workerRef == "" {
		call, e := provider.Next(ctx.Done())
		if e != nil {
			t.Fatal(e)
		}
		if call.Contains(task) {
			workerFirst = call
			continue
		}
		if !call.Contains(opening) {
			t.Fatalf("unexpected provider request: %v", call.Texts())
		}
		var created struct {
			Created struct{ Status, Ref, InstanceID string } `json:"created"`
			Watch   struct {
				Status  string `json:"status"`
				WatchID string `json:"watch_id"`
			} `json:"watch"`
			Input struct {
				Status string `json:"status"`
			} `json:"input"`
		}
		if !managerToolResult(call, &created) || created.Created.Status != "created" || created.Created.Ref == "" || created.Created.InstanceID == "" || created.Watch.Status != "attached" || created.Watch.WatchID == "" || created.Input.Status != "submitted" {
			t.Fatalf("create/watch/input: %+v messages=%v", created, call.Texts())
		}
		workerRef, workerInstance, watchID = created.Created.Ref, created.Created.InstanceID, created.Watch.WatchID
		call.RespondToolCall("communicate", communicateArgs("worker started"))
	}
	t.Cleanup(func() {
		stop, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		if _, err := clientRequest[appwire.EmptyResponse](stop, human, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: workerRef}); err != nil {
			t.Errorf("worker shutdown: %v", err)
		}
	})
	var worker appwire.Thread
	awaitThread(ctx, t, human, workerRef, "independent restricted worker", func(thread appwire.Thread) bool {
		worker = thread
		return thread.Evener.InstanceID == workerInstance && thread.Evener.Access != nil
	})
	if worker.Evener.Manager || worker.Evener.ParentRef != "" || worker.Evener.Access.Sandbox != "restricted" || !worker.Evener.Capabilities.Queue || !worker.Evener.Capabilities.Steer {
		t.Fatalf("independent worker capabilities: %+v", worker.Evener)
	}
	const question = "ACCEPT-WORKER-QUESTION"
	workerFirst.RespondToolCall("ask_user", map[string]any{"questions": []any{map[string]any{"question": question, "options": []any{map[string]any{"label": "Proceed", "detail": "Continue"}, map[string]any{"label": "Wait", "detail": "Pause"}}}}})
	awaitThread(ctx, t, human, workerRef, "question pending", func(thread appwire.Thread) bool {
		return thread.Evener.AskPending && thread.Evener.PendingQuestion != nil && thread.Evener.PendingQuestion.Question == question
	})
	call = next(opening)
	if !call.Contains(watchID) {
		t.Fatal("Manager missed question watch")
	}
	call.RespondToolCall("job_status", map[string]any{"hub": map[string]any{"ref": workerRef}})
	call = next(opening)
	pending := outcome(call)
	if pending.Read == nil || !pending.Read.Thread.Evener.AskPending || pending.Read.Thread.Evener.PendingQuestion == nil || pending.Read.Thread.Evener.PendingQuestion.Question != question {
		t.Fatalf("Manager question status: %+v", pending.Read)
	}
	const answer = "ACCEPT-WORKER-ANSWER"
	call.RespondToolCall("delegate_send", map[string]any{"to": workerRef, "input_mode": "start", "message": answer, "client_mutation_id": newMutationID(t), "expected_instance_id": workerInstance})
	var afterAnswer *fakellm.Call
	for afterAnswer == nil {
		call, e := provider.Next(ctx.Done())
		if e != nil {
			t.Fatal(e)
		}
		if call.Contains(task) && call.Contains(answer) {
			afterAnswer = call
		} else if call.Contains(opening) {
			call.RespondToolCall("communicate", communicateArgs("answer sent"))
		} else {
			t.Fatalf("unexpected answer provider request: %v", call.Texts())
		}
	}
	outside := filepath.Join(t.TempDir(), "approval-target.txt")
	if err := os.WriteFile(outside, []byte("before approval\n"), 0600); err != nil {
		t.Fatal(err)
	}
	const approved = "ACCEPT-APPROVED-WRITE\n"
	afterAnswer.RespondToolCall("write_file", map[string]any{"file_path": outside, "content": approved})
	call = next(opening)
	if !call.Contains(watchID) {
		t.Fatal("Manager missed no-browser approval watch")
	}
	call.RespondToolCall("job_status", map[string]any{"hub": map[string]any{"ref": workerRef}})
	call = next(opening)
	approval := outcome(call)
	if approval.Read == nil || len(approval.Read.Thread.Evener.PendingEscalations) != 1 {
		t.Fatalf("pending approval status: %+v", approval.Read)
	}
	escalation := approval.Read.Thread.Evener.PendingEscalations[0]
	if escalation.DeniedPath != outside || escalation.Tool != "write_file" || escalation.EscalationID == "" {
		t.Fatalf("wrong approval: %+v", escalation)
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "before approval\n" {
		t.Fatalf("write before approval: %q %v", data, err)
	}
	call.RespondToolCall("session_control", map[string]any{"action": "approval", "arguments": map[string]any{"ref": workerRef, "escalationId": escalation.EscalationID, "approve": true}})
	var afterApproval *fakellm.Call
	approvedByManager := false
	for afterApproval == nil || !approvedByManager {
		call, e := provider.Next(ctx.Done())
		if e != nil {
			t.Fatal(e)
		}
		if call.Contains(task) {
			afterApproval = call
			continue
		}
		if !call.Contains(opening) {
			t.Fatalf("unexpected approval provider request: %v", call.Texts())
		}
		control := outcome(call)
		if control.Control == nil || control.Control.Actor.ManagerRef != managerRef {
			t.Fatalf("approval actor: %+v", control.Control)
		}
		approvedByManager = true
		call.RespondToolCall("communicate", communicateArgs("approval granted"))
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != approved {
		t.Fatalf("approved write: %q %v", data, err)
	}
	const result = "ACCEPT-WORKER-RESULT"
	afterApproval.RespondToolCall("communicate", communicateArgs(result))
	for {
		call = next(opening)
		if !call.Contains(watchID) {
			t.Fatalf("completion notice missing watch: %v", call.Texts())
		}
		finished := strings.Contains(strings.Join(call.Texts(), "\n"), "completed: accepted terminal done")
		call.RespondToolCall("communicate", communicateArgs("worker observed"))
		if finished {
			break
		}
	}
	awaitThread(ctx, t, human, managerRef, "Manager idle", func(thread appwire.Thread) bool { return thread.Status.Type == appwire.ThreadStatusIdle })
	// Read history through the Manager, then compare it with both owners' saved
	// histories. This checks actor attribution and accepted completion, not prose.
	if _, err := clientRequest[appwire.TurnStartResponse](ctx, human, appwire.MethodTurnStart, appwire.TurnStartParams{Ref: managerRef, ExpectedInstanceID: localInstanceIDForTestRef(managerRef), ClientMutationID: newMutationID(t), Input: []appwire.InputItem{{Type: "text", Text: "ACCEPT-HISTORY-READ"}}}); err != nil {
		t.Fatal(err)
	}
	call = next("ACCEPT-HISTORY-READ")
	call.RespondToolCall("read_transcript", map[string]any{"hub": map[string]any{"ref": workerRef}})
	call = next("ACCEPT-HISTORY-READ")
	history := outcome(call)
	if history.Turns == nil || !workerTerminalAccepted(history.Turns.Data, result) {
		t.Fatalf("Manager transcript read missing: %+v", history)
	}
	call.RespondToolCall("communicate", communicateArgs("history read"))
	workerTurns, err := readTranscriptTurns(ctx, human, workerRef, "")
	if err != nil {
		t.Fatal(err)
	}
	managerTurns, err := readTranscriptTurns(ctx, human, managerRef, "")
	if err != nil {
		t.Fatal(err)
	}
	answered, decisions := false, 0
	for _, turn := range workerTurns {
		for _, item := range turn.Items {
			if item.Text == answer && item.Actor != nil && item.Actor.Kind == "manager" && item.Actor.ManagerRef == managerRef {
				answered = true
			}
			if item.EventKind == appwire.ThreadItemEventKindApprovalDecision {
				decisions++
				if item.Actor == nil || item.Actor.Kind != "manager" || item.Actor.ManagerRef != managerRef {
					t.Fatalf("wrong approval history actor: %+v", item.Actor)
				}
			}
		}
	}
	if !answered || decisions != 1 || !workerTerminalAccepted(workerTurns, result) || managerWatchNotices(managerTurns, watchID) < 2 {
		t.Fatalf("saved workflow: answer=%v approvals=%d completed=%v watch=%d worker=%v manager=%v", answered, decisions, workerTerminalAccepted(workerTurns, result), managerWatchNotices(managerTurns, watchID), managerItemSummary(workerTurns), managerItemSummary(managerTurns))
	}
	for _, name := range []string{"ask_user", "write_file"} {
		if !acceptanceHasTool(workerTurns, name) {
			t.Fatalf("worker history lacks actual %s call: %v", name, managerItemSummary(workerTurns))
		}
	}
	for _, name := range []string{"job_list", "model_list", "delegate", "job_status", "delegate_send", "session_control", "read_transcript"} {
		if !acceptanceHasTool(managerTurns, name) {
			t.Fatalf("Manager history lacks actual %s call: %v", name, managerItemSummary(managerTurns))
		}
	}
	// Add a second local Manager and a remote Manager as readable Manager targets.
	other, err := clientRequest[appwire.ThreadStartResponse](ctx, human, appwire.MethodThreadStart, appwire.ThreadStartParams{Harness: "evener", CWD: local.workDir, Model: local.model, Manager: true})
	if err != nil {
		t.Fatal(err)
	}
	otherRef := other.Thread.Evener.Ref
	t.Cleanup(func() {
		stop, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		_, _ = clientRequest[appwire.EmptyResponse](stop, human, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: otherRef})
	})
	if _, err := clientRequest[appwire.ThreadManagerSetResponse](ctx, human, appwire.MethodEvenerThreadManagerSet, appwire.ThreadManagerSetParams{Ref: otherRef, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	token, err := os.ReadFile(hostDir + "/state/auth-token")
	if err != nil {
		t.Fatal(err)
	}
	remote := hubStack{addr: addr, token: strings.TrimSpace(string(token)), workDir: hostDir + "/workspace", model: selectedModel}
	remoteHuman := remote.dialRPC(ctx, t)
	remoteStarted, err := clientRequest[appwire.ThreadStartResponse](ctx, remoteHuman, appwire.MethodThreadStart, appwire.ThreadStartParams{Harness: "evener", CWD: remote.workDir, Model: selectedModel, Manager: true})
	if err != nil {
		t.Fatal(err)
	}
	remoteLocalRef := remoteStarted.Thread.Evener.Ref
	t.Cleanup(func() {
		stop, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		_, _ = clientRequest[appwire.EmptyResponse](stop, remoteHuman, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: remoteLocalRef})
	})
	remoteID, err := appwire.ParseRef(remoteLocalRef)
	if err != nil {
		t.Fatal(err)
	}
	remoteRef := (appwire.Ref{SourceID: remoteHost, ThreadID: remoteID.ThreadID}).String()
	for _, target := range []string{otherRef, remoteRef} {
		read, err := clientRequest[appwire.ThreadReadResponse](ctx, human, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: target})
		if err != nil {
			t.Fatalf("read Manager target %s: %v", target, err)
		}
		caps := read.Thread.Evener.Capabilities
		// The Manager bit is the session's own start-time property as its daemon
		// reports it; the store-projected ManagerSetting is transitional and not
		// the source of truth.
		if !read.Thread.Evener.Manager || !caps.Queue || !caps.Steer || !caps.SkillInput || !caps.CommandInput || target == remoteRef && (caps.Clear || caps.Interrupt || caps.ChangeModel) {
			t.Fatalf("Manager target %s: %+v %v", target, read.Thread.Evener, err)
		}
	}
	awaitThread(ctx, t, human, managerRef, "Manager idle after history read", func(thread appwire.Thread) bool { return thread.Status.Type == appwire.ThreadStatusIdle })
	if _, err := clientRequest[appwire.TurnStartResponse](ctx, human, appwire.MethodTurnStart, appwire.TurnStartParams{Ref: managerRef, ExpectedInstanceID: localInstanceIDForTestRef(managerRef), ClientMutationID: newMutationID(t), Input: []appwire.InputItem{{Type: "text", Text: "ACCEPT-MANAGER-TARGETS"}}}); err != nil {
		t.Fatal(err)
	}
	for i, target := range []string{otherRef, remoteRef} {
		call = next("ACCEPT-MANAGER-TARGETS")
		if i == 0 {
			call.RespondToolCall("job_status", map[string]any{"hub": map[string]any{"ref": target}})
		} else {
			read := outcome(call)
			if read.Read == nil || read.Read.Thread.Evener.Ref != otherRef || !read.Read.Thread.Evener.Manager {
				t.Fatalf("local Manager target: %+v", read.Read)
			}
			call.RespondToolCall("job_status", map[string]any{"hub": map[string]any{"ref": target}})
		}
	}
	call = next("ACCEPT-MANAGER-TARGETS")
	remoteTarget := outcome(call)
	if remoteTarget.Read == nil || remoteTarget.Read.Thread.Evener.Ref != remoteRef || !remoteTarget.Read.Thread.Evener.Manager {
		t.Fatalf("remote Manager target: %+v", remoteTarget.Read)
	}
	call.RespondToolCall("communicate", communicateArgs("targets read"))
	// The remote Manager operates through its own hub under the start-flag
	// model: it reaches the sessions that hub reaches, starting with itself.
	// No cross-hub enable or peer binding participates.
	if _, err := clientRequest[appwire.TurnStartResponse](ctx, remoteHuman, appwire.MethodTurnStart, appwire.TurnStartParams{Ref: remoteLocalRef, ExpectedInstanceID: remoteStarted.Thread.Evener.InstanceID, ClientMutationID: newMutationID(t), Input: []appwire.InputItem{{Type: "text", Text: "ACCEPT-REMOTE-MANAGER"}}}); err != nil {
		t.Fatal(err)
	}
	call = next("ACCEPT-REMOTE-MANAGER")
	call.RespondToolCall("job_status", map[string]any{"hub": map[string]any{"ref": remoteLocalRef}})
	call = next("ACCEPT-REMOTE-MANAGER")
	var remoteRead appwire.SessionOperationResponse
	if !managerToolResult(call, &remoteRead) || remoteRead.Actor.Kind != "manager" || remoteRead.Actor.ManagerRef != remoteLocalRef || remoteRead.Read == nil || remoteRead.Read.Thread.Evener.Ref != remoteLocalRef || !remoteRead.Read.Thread.Evener.Manager {
		t.Fatalf("remote Manager read through its own hub: %+v refusal=%s", remoteRead, managerToolRefusal(call))
	}
	call.RespondToolCall("communicate", communicateArgs("remote Manager read"))
	revoked, err := clientRequest[appwire.ThreadManagerSetResponse](ctx, human, appwire.MethodEvenerThreadManagerSet, appwire.ThreadManagerSetParams{Ref: managerRef, Enabled: false})
	if err != nil || revoked.View.Setting.Enabled {
		t.Fatalf("human revocation: %+v %v", revoked, err)
	}
	awaitThread(ctx, t, human, managerRef, "revoked Manager idle", func(thread appwire.Thread) bool { return thread.Status.Type == appwire.ThreadStatusIdle })
	const ordinary = "ACCEPT-POST-REVOCATION"
	if _, err := clientRequest[appwire.TurnStartResponse](ctx, human, appwire.MethodTurnStart, appwire.TurnStartParams{Ref: managerRef, ExpectedInstanceID: localInstanceIDForTestRef(managerRef), ClientMutationID: newMutationID(t), Input: []appwire.InputItem{{Type: "text", Text: ordinary}}}); err != nil {
		t.Fatal(err)
	}
	call = next(ordinary)
	call.RespondToolCall("read_file", map[string]any{"file_path": local.readableFile})
	call = next(ordinary)
	// This must be an ordinary local tool result, not a stale Manager binding.
	if !call.Contains("notes for the fake tool round") {
		t.Fatalf("ordinary read_file returned no fixture bytes: %v", call.Texts())
	}
	var last map[string]any
	if managerToolResult(call, &last) {
		if _, ok := last["actor"]; ok {
			t.Fatalf("revoked request still used Manager operation: %v", last)
		}
	}
	call.RespondToolCall("communicate", communicateArgs("ordinary request succeeded"))
	awaitThread(ctx, t, human, managerRef, "ordinary request complete", func(thread appwire.Thread) bool { return thread.Status.Type == appwire.ThreadStatusIdle })
	final, err := readTranscriptTurns(ctx, human, managerRef, "")
	if err != nil {
		t.Fatal(err)
	}
	ordinarySucceeded := false
	for _, turn := range final {
		for _, item := range turn.Items {
			if item.Text == ordinary && item.Actor != nil && item.Actor.Kind == "human" {
				ordinarySucceeded = true
			}
		}
	}
	if !ordinarySucceeded {
		t.Fatalf("ordinary post-revocation request not saved: %v", managerItemSummary(final))
	}
	// The Manager bit is the session's own start-time property, so a store
	// change cannot demote it: the session reads Manager true for life. The
	// ordinary request above already proved its ordinary tools still serve.
	if read, err := clientRequest[appwire.ThreadReadResponse](ctx, human, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: managerRef}); err != nil || !read.Thread.Evener.Manager {
		t.Fatalf("Manager session demoted by a store change: %+v %v", read.Thread.Evener, err)
	}
	if read, err := clientRequest[appwire.ThreadReadResponse](ctx, human, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: workerRef}); err != nil || !read.Thread.Evener.Capabilities.Send || read.Thread.Evener.Manager {
		t.Fatalf("worker state after ordinary round: %+v %v", read.Thread.Evener, err)
	}
	// Remote model and selected Manager statuses were checked before creation;
	// persisted remote Manager input confirms its turn ran on the remote daemon.
	remoteTurns, err := readTranscriptTurns(ctx, remoteHuman, remoteLocalRef, "")
	if err != nil {
		t.Fatal(err)
	}
	foundRemote := false
	for _, turn := range remoteTurns {
		for _, item := range turn.Items {
			if item.Text == "ACCEPT-REMOTE-MANAGER" {
				foundRemote = true
			}
		}
	}
	if !foundRemote {
		t.Fatalf("remote Manager history missing: %v", managerItemSummary(remoteTurns))
	}
	if !acceptanceHasTool(remoteTurns, "job_status") || !acceptanceHasTool(final, "read_file") {
		t.Fatalf("actual remote status or ordinary post-revocation file call absent: remote=%v local=%v", managerItemSummary(remoteTurns), managerItemSummary(final))
	}
}

func acceptanceHasTool(turns []appwire.Turn, name string) bool {
	for _, turn := range turns {
		for _, item := range turn.Items {
			if item.ToolName == name && item.CallID != "" && item.Error == "" {
				return true
			}
		}
	}
	return false
}
