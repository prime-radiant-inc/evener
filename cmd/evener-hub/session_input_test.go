package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/rendezvous"
	daemonserver "primeradiant.com/evener/server"
)

func TestManagerInputClosedOperationAndAdmission(t *testing.T) {
	manager := appwire.ManagerView{Ref: "local:manager", InstanceID: "manager-instance"}
	owner := &sessionOperations{cfg: hubcore.WebConfig{HubID: "hub-test"}, sources: appsource.NewRegistry()}
	input := []appwire.InputItem{{Type: "text", Text: "answer"}}
	start := appwire.SessionOperationRequest{Start: &appwire.TurnStartParams{Ref: "local:worker", ClientMutationID: "one", Input: input}}
	if err := start.Validate(); err != nil {
		t.Fatalf("start is a closed union member: %v", err)
	}
	if _, err := owner.Invoke(context.Background(), manager, start); err == nil {
		t.Fatal("Manager input admitted while disabled")
	}
	if _, err := owner.Invoke(context.Background(), manager, start); err == nil {
		t.Fatal("missing source accepted Manager input")
	}
	if err := (appwire.SessionOperationRequest{Start: start.Start, Queue: &appwire.TurnQueueParams{}}).Validate(); err == nil {
		t.Fatal("multiple input actions accepted")
	}
	if err := (appwire.SessionOperationRequest{Control: &appwire.SessionControlRequest{Action: "approval", Approval: &appwire.SandboxEscalationResolveParams{Ref: "local:worker", EscalationID: "esc", Approve: true}}}).Validate(); err != nil {
		t.Fatalf("real approval action missing: %v", err)
	}
}

func TestManagerApprovalClosedAction(t *testing.T) {
	if err := (appwire.SessionControlRequest{}).Validate(); err == nil {
		t.Fatal("empty approval action accepted")
	}
	if err := (appwire.SessionControlRequest{Action: "approval", Approval: &appwire.SandboxEscalationResolveParams{Ref: "local:worker", EscalationID: "esc", Approve: true}}).Validate(); err != nil {
		t.Fatal(err)
	}
}

// Both sockets are real: the manager's authenticated binding invokes the hub,
// which relays through the worker's own AppWire runtime and mutation journal.
func TestManagerInputRealHubRuntimePersistsCallerNotTarget(t *testing.T) {
	// TRIPWIRE: socket handshakes and durable mutation admission normally take milliseconds.
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	const managerRef, managerID = "local:manager-origin", "manager-instance"
	managerDaemon := daemonserver.NewServer(daemonserver.ServerConfig{HubToken: "test-token"})
	defer managerDaemon.Close()
	managerIdentity, err := daemonserver.PrepareAppIdentityForRef("local", managerID, managerRef, "")
	if err != nil {
		t.Fatal(err)
	}
	managerDaemon.ReplaceAppIdentity(managerIdentity, nil)
	managerDaemon.UpdateSessionInfo(managerID, "", "")
	managerDaemon.SetState("idle")
	managerClients := make(chan appwire.SessionHubClient, 1)
	managerDaemon.AppServer().InstallSessionTools(func(_ context.Context, binding appwire.SessionBinding, client appwire.SessionHubClient) error {
		if binding.Ref != managerRef {
			return appwire.InvalidRequest("unexpected manager binding")
		}
		managerClients <- client
		return nil
	})
	managerHTTP := httptest.NewServer(http.HandlerFunc(managerDaemon.AppServer().ServeWebSocket))
	defer managerHTTP.Close()

	stateDir, workDir := t.TempDir(), t.TempDir()
	worker, err := agent.NewSession(llm.NewClient(), provider.NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workDir), agent.SessionConfig{StateDir: stateDir, Sandbox: "off"})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	workerRef := "local:" + worker.ID()
	if workerRef == managerRef {
		t.Fatal("worker unexpectedly shares the manager's identity")
	}
	workerDaemon := daemonserver.NewServer(daemonserver.ServerConfig{HubToken: "test-token"})
	defer workerDaemon.Close()
	workerIdentity, err := daemonserver.PrepareAppIdentityForRef("local", worker.ID(), workerRef, worker.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	workerDaemon.ReplaceAppIdentity(workerIdentity, nil)
	workerDaemon.UpdateSessionInfo(worker.ID(), "", "")
	workerDaemon.SetState("idle")
	workerDaemon.SetWorkingDir(workDir)
	workerDaemon.SetSessionTools(func() *agent.Session { return worker })
	workerDaemon.SetRetrySafeTurnFunctions(daemonserver.RetrySafeTurnFunctions{
		Start: worker.AcceptClientMutationStart,
		StartAs: func(p appwire.TurnStartParams, actor schema.SessionActor) (appwire.TurnStartResponse, error) {
			return worker.AcceptClientMutationStartAs(p, actor)
		},
		Drain:     worker.AcceptClientMutationDrainAsSteer,
		DrainAs:   worker.AcceptClientMutationDrainAsSteerAs,
		Promote:   worker.AcceptClientMutationPromoteQueuedAsSteer,
		PromoteAs: worker.AcceptClientMutationPromoteQueuedAsSteerAs,
	})
	workerDaemon.WireTranscriptHistory(worker)
	workerHTTP := httptest.NewServer(http.HandlerFunc(workerDaemon.AppServer().ServeWebSocket))
	defer workerHTTP.Close()

	entries := []appsource.LocalDaemonEntry{
		{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: managerHTTP.URL, SourceID: "local", ThreadID: managerID, SessionID: managerID, WorkspaceRef: managerRef, InstanceID: managerID, HubToken: "test-token"}, SessionID: managerID, Manager: true},
		{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: workerHTTP.URL, SourceID: "local", ThreadID: worker.ID(), SessionID: worker.ID(), WorkspaceRef: workerRef, InstanceID: worker.ID(), HubToken: "test-token", WorkingDir: workDir, StateDir: stateDir}, SessionID: worker.ID()},
	}
	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry { return entries }, workerHTTP.Client())
	sources := appsource.NewRegistry()
	sources.Add(source)
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex(""), HubID: "hub-test",
		Roster: hubcore.NewRosterWithEntries(
			hubcore.LiveEntry{Entry: entries[0].Entry, SessionID: managerID, Status: "active"},
			hubcore.LiveEntry{Entry: entries[1].Entry, SessionID: worker.ID(), Status: "active"},
		),
	}
	web := NewWebServer(cfg)
	_ = newHubAppServerWithNavigation(cfg, sources, web.navigation, web.resolveTopLevelSessionRef)
	var workers sync.WaitGroup
	startSessionTools(ctx, sources, func(f func()) { workers.Go(f) })
	defer func() { cancel(); workers.Wait() }()
	var managerClient appwire.SessionHubClient
	select {
	case managerClient = <-managerClients:
	case <-ctx.Done():
		t.Fatal("manager runtime did not bind to hub")
	}
	// A canonical read makes the worker's websocket and runtime binding live.
	parsed, err := appwire.ParseRef(workerRef)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := source.AcquireRelaySession(parsed)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	read, err := lease.Read(ctx, appwire.ThreadReadParams{Ref: workerRef})
	if err != nil {
		t.Fatal(err)
	}
	if !read.Handoff.Commit() {
		t.Fatal("worker's canonical snapshot did not commit")
	}
	const mutationID = "manager-to-worker"
	response, err := managerClient.Invoke(ctx, appwire.SessionOperationRequest{Start: &appwire.TurnStartParams{
		Ref: workerRef, ClientMutationID: mutationID, ExpectedInstanceID: worker.ID(), Input: []appwire.InputItem{{Type: "text", Text: "answer"}},
	}})
	if err != nil || response.Start == nil {
		t.Fatalf("real manager-to-worker input: response=%+v err=%v", response, err)
	}
	data, err := os.ReadFile(filepath.Join(stateDir, "mutations", worker.ID()+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Journal map[string]struct {
			Actor schema.SessionActor `json:"actor"`
		} `json:"journal"`
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	record, ok := saved.Journal[mutationID]
	if !ok {
		t.Fatalf("mutation %q absent from persisted journal", mutationID)
	}
	if got := record.Actor; got.Kind != "manager" || got.ManagerRef != managerRef {
		t.Fatalf("durable actor=%+v, want manager caller %q, not worker %q", got, managerRef, workerRef)
	}
	// A drain can append new input; a promotion creates a new steering record
	// from queued input. Check their durable actors, not just hub receipts.
	assertActor := func(id, kind, ref string) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(stateDir, "mutations", worker.ID()+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var saved struct {
			Journal map[string]struct {
				Actor schema.SessionActor `json:"actor"`
			} `json:"journal"`
		}
		if err := json.Unmarshal(data, &saved); err != nil {
			t.Fatal(err)
		}
		if got, ok := saved.Journal[id]; !ok || got.Actor.Kind != kind || got.Actor.ManagerRef != ref {
			t.Fatalf("durable mutation %q actor=%+v exists=%v, want %s ref %q", id, got.Actor, ok, kind, ref)
		}
	}
	queue, _ := worker.ClientMutationProjection()
	managerDrain := appwire.TurnDrainAsSteerParams{Ref: workerRef, ClientMutationID: "manager-drain", ExpectedInstanceID: worker.ID(), ExpectedQueueRevision: queue.Revision, Input: []appwire.InputItem{{Type: "text", Text: "manager drain input"}}}
	if response, err := managerClient.Invoke(ctx, appwire.SessionOperationRequest{Control: &appwire.SessionControlRequest{Action: "drain_queue", DrainQueue: &managerDrain}}); err != nil || response.Control == nil || response.Control.DrainQueue == nil {
		t.Fatalf("manager drain response=%+v err=%v", response, err)
	}
	assertActor("manager-drain", "manager", managerRef)
	queue, _ = worker.ClientMutationProjection()
	humanDrain := appwire.TurnDrainAsSteerParams{Ref: workerRef, ClientMutationID: "human-drain", ExpectedInstanceID: worker.ID(), ExpectedQueueRevision: queue.Revision, Input: []appwire.InputItem{{Type: "text", Text: "human drain input"}}}
	if result, err := worker.AcceptClientMutationDrainAsSteer(humanDrain); err != nil {
		t.Fatalf("human drain: %v", err)
	} else if result.Receipt.Actor.Kind != "" || result.Receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("human drain receipt changed: %+v", result.Receipt)
	}
	assertActor("human-drain", "human", "")
	for _, id := range []string{"manager-promote", "human-promote"} {
		if _, err := worker.AcceptClientMutationQueue(appwire.TurnQueueParams{Ref: workerRef, ClientMutationID: "queue-" + id, ExpectedInstanceID: worker.ID(), Input: []appwire.InputItem{{Type: "text", Text: id}}}); err != nil {
			t.Fatalf("seed queue for %s: %v", id, err)
		}
		queue, _ = worker.ClientMutationProjection()
		promote := appwire.TurnPromoteQueuedAsSteerParams{Ref: workerRef, ClientMutationID: id, ExpectedInstanceID: worker.ID(), ExpectedEntryID: queue.IDs[0]}
		if id == "manager-promote" {
			if response, err := managerClient.Invoke(ctx, appwire.SessionOperationRequest{Control: &appwire.SessionControlRequest{Action: "promote_queued", PromoteQueued: &promote}}); err != nil || response.Control == nil || response.Control.PromoteQueued == nil {
				t.Fatalf("manager promote response=%+v err=%v", response, err)
			}
			assertActor(id, "manager", managerRef)
		} else {
			if result, err := worker.AcceptClientMutationPromoteQueuedAsSteer(promote); err != nil {
				t.Fatalf("human promote: %v", err)
			} else if result.Receipt.Actor.Kind != "" || result.Receipt.Disposition != appwire.MutationDispositionApplied {
				t.Fatalf("human promote receipt changed: %+v", result.Receipt)
			}
			assertActor(id, "human", "")
		}
	}
	const childID = "02wMz5Txv1C3Hut0M8GCeB"
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{ID: childID, ParentSessionID: worker.ID(), EnvInfo: schema.EnvironmentInfo{WorkingDir: workDir}}); err != nil {
		t.Fatal(err)
	}
	beforeChildWrite, err := os.ReadFile(filepath.Join(stateDir, "mutations", worker.ID()+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := managerClient.Invoke(ctx, appwire.SessionOperationRequest{Start: &appwire.TurnStartParams{
		Ref: "local:" + childID, ClientMutationID: "unsupported-child", ExpectedInstanceID: worker.ID(), Input: []appwire.InputItem{{Type: "text", Text: "do not deliver to parent"}},
	}}); err == nil {
		t.Fatal("unsupported descendant write was accepted")
	}
	afterChildWrite, err := os.ReadFile(filepath.Join(stateDir, "mutations", worker.ID()+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeChildWrite, afterChildWrite) {
		t.Fatal("rejected descendant input altered the parent's durable mutations")
	}
	items := apptranscript.ProjectTurn("turn_1", 0, schema.Turn{Kind: schema.TurnUserInput, Actor: record.Actor}, apptranscript.NewToolCallRegistry(), nil, nil)
	if len(items) != 1 || items[0].Actor == nil || items[0].Actor.ManagerRef != managerRef {
		t.Fatalf("rendered transcript actor=%+v, want caller %q, not target %q", items, managerRef, workerRef)
	}
}
