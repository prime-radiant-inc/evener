package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/internal/transcriptindex"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/rendezvous"
	daemonserver "primeradiant.com/evener/server"
)

// These tests cross the actual daemon websocket, canonical relay and bound
// Manager operation. Channel deliveries and closure order the assertions.
func TestManagerObservationRecordedCompletionCut(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	const ref, instance = "local:manager-workspace", "manager-instance"
	daemon := daemonserver.NewServer(daemonserver.ServerConfig{HubToken: "test-token"})
	defer daemon.Close()
	identity, err := daemonserver.PrepareAppIdentityForRef("local", instance, ref, "")
	if err != nil {
		t.Fatal(err)
	}
	daemon.ReplaceAppIdentity(identity.WithBootGeneration("7@manager"), nil)
	upstream := httptest.NewServer(http.HandlerFunc(daemon.AppServer().ServeWebSocket))
	defer upstream.Close()
	entry := rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: upstream.URL, SourceID: "local", ThreadID: instance, SessionID: instance, WorkspaceRef: ref, InstanceID: instance, HubToken: "test-token"}
	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{Entry: entry, SessionID: instance, Manager: true}}
	}, upstream.Client())
	configureObservationTargetSessionTools(daemon, source)
	sources := appsource.NewRegistry()
	sources.Add(source)
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex(""), HubID: "hub-test"}
	server := appserver.NewServer(appserver.ServerConfig{})
	owner := &sessionOperations{cfg: cfg, sources: sources, relays: newHubRelay(server, cfg, sources), server: server}
	appserver.HandleTyped(server.Router(), appwire.MethodThreadRead, owner.Read)
	binding := &boundSessionOperations{owner: owner, local: source, binding: appwire.SessionBinding{Ref: ref, InstanceID: instance, HubID: "hub-test", BindingID: "binding"}}
	for _, invalid := range []string{"unknown", "assistant.tool"} {
		if _, err := binding.Watch(ctx, appwire.SessionWatchRequest{Ref: ref, Events: []string{invalid}}); err == nil {
			t.Fatalf("unsupported selector %q accepted", invalid)
		}
	}
	updates, err := binding.Watch(ctx, appwire.SessionWatchRequest{Ref: ref, Completion: true})
	if err != nil {
		t.Fatalf("completion watch through real connection: %v", err)
	}
	var initial appwire.SessionWatchUpdate
	select {
	case got, ok := <-updates:
		initial = got
		if !ok || initial.Snapshot == nil || initial.Metadata.InstanceID != instance {
			t.Fatalf("initial snapshot=%+v, open=%t", initial, ok)
		}
	case <-ctx.Done():
		t.Fatal("initial observation blocked")
	}
	if n := daemon.AppServer().SubscriberCount(ref); n != 1 {
		t.Fatalf("canonical upstream subscriptions=%d, want 1", n)
	}
	falseValue, trueValue := false, true
	for _, observation := range []struct {
		id       string
		accepted *bool
		reason   string
	}{
		{"rejected-done", &falseValue, "done"},
		{"accepted-wait", &trueValue, "wait"},
		{"accepted-done", &trueValue, "done"},
	} {
		item := managerEchoObservationItem(t, observation.id, observation.reason, *observation.accepted)
		daemon.AppServer().Broadcast(ref, appwire.NotifyHistoryUpdated, appwire.HistoryUpdatedParams{
			Ref: ref, ThreadID: instance, Items: []appwire.ThreadItem{item},
		})
		select {
		case got, open := <-updates:
			if !open || got.Event != "communicate" || got.Metadata.EntryID != item.TranscriptKey || got.Metadata.TerminalAccepted == nil || *got.Metadata.TerminalAccepted != *observation.accepted || got.Metadata.EndReason != observation.reason || got.Metadata.GenerationID != initial.Metadata.GenerationID {
				t.Fatalf("recorded observation lost verdict %+v: %+v, open=%t", observation, got, open)
			}
		case <-ctx.Done():
			t.Fatal("recorded observation not delivered across relay")
		}
	}
}

func TestManagerRemoteObservationWatchRequiresNegotiatedSessionTools(t *testing.T) {
	for _, supported := range []bool{false, true} {
		t.Run(fmt.Sprintf("session-tools-%t", supported), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			const managerRef, instance = "local:manager-workspace", "manager-instance"
			daemon := daemonserver.NewServer(daemonserver.ServerConfig{HubToken: "test-token"})
			defer daemon.Close()
			identity, err := daemonserver.PrepareAppIdentityForRef("local", instance, managerRef, "")
			if err != nil {
				t.Fatal(err)
			}
			daemon.ReplaceAppIdentity(identity.WithBootGeneration("7@manager"), nil)
			upstream := httptest.NewServer(http.HandlerFunc(daemon.AppServer().ServeWebSocket))
			defer upstream.Close()
			entry := rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: upstream.URL, SourceID: "local", ThreadID: instance, SessionID: instance, WorkspaceRef: managerRef, InstanceID: instance, HubToken: "test-token"}
			local := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
				return []appsource.LocalDaemonEntry{{Entry: entry, SessionID: instance, Manager: true}}
			}, upstream.Client())
			configureObservationTargetSessionTools(daemon, local)
			remoteClient, _ := newScriptedRemoteHub(t, func(method string, _ json.RawMessage) any {
				switch method {
				case appwire.MethodInitialize:
					return appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion, SourceID: "local", Features: appwire.FeatureSet{SessionTools: supported}}
				case appwire.MethodThreadRead:
					return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "target", SessionID: "target", Evener: appwire.EvenerThread{Ref: "local:target", InstanceID: "target"}}, TargetSessionToolsSupported: new(supported)}
				default:
					return appwire.EmptyResponse{}
				}
			})
			remote := appsource.NewRemoteHubSource("remote", nil, func(context.Context, string) (*appwire.Client, error) { return remoteClient, nil })
			sources := appsource.NewRegistry()
			sources.Add(local)
			sources.Add(remote)
			cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex(""), HubID: "hub-test"}
			server := newHubAppServer(cfg, sources)
			defer server.Shutdown(ctx)
			owner := &sessionOperations{cfg: cfg, sources: sources, server: server}
			bound := &boundSessionOperations{owner: owner, local: local, binding: appwire.SessionBinding{Ref: managerRef, InstanceID: instance, HubID: "hub-test", BindingID: "binding"}}
			updates, err := bound.Watch(ctx, appwire.SessionWatchRequest{Ref: "remote:target", Completion: true})
			if !supported {
				var wire appwire.WireError
				if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidRequest {
					t.Fatalf("Watch error = %v, want WireError code %d", err, appwire.CodeInvalidRequest)
				}
				return
			}
			if err != nil {
				t.Fatalf("supported remote completion watch: %v", err)
			}
			select {
			case initial := <-updates:
				if initial.Snapshot == nil || initial.Metadata.Ref != "remote:target" {
					t.Fatalf("initial remote snapshot = %+v", initial)
				}
			case <-ctx.Done():
				t.Fatal("supported remote completion watch did not attach")
			}
		})
	}
}

type observationTestSessionHubClient struct{}

func configureObservationTargetSessionTools(daemon *daemonserver.Server, source *appsource.LocalDaemonSource) {
	daemon.AppServer().InstallSessionTools(func(context.Context, appwire.SessionBinding, appwire.SessionHubClient) error { return nil })
	if source != nil {
		source.SetSessionTools(func(appwire.SessionBinding) (appwire.SessionHubClient, error) {
			return observationTestSessionHubClient{}, nil
		}, func() (string, error) { return "hub-test", nil })
	}
}

func (observationTestSessionHubClient) State(context.Context) (appwire.ManagerView, error) {
	return appwire.ManagerView{Manager: true}, nil
}
func (observationTestSessionHubClient) Invoke(context.Context, appwire.SessionOperationRequest) (appwire.SessionOperationResponse, error) {
	return appwire.SessionOperationResponse{}, nil
}
func (observationTestSessionHubClient) Watch(context.Context, appwire.SessionWatchRequest) (<-chan appwire.SessionWatchUpdate, error) {
	return nil, nil
}

func TestManagerObservationClearRotatesHistoryAndEndsWatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	const ref, instance = "local:manager-workspace", "manager-instance"
	daemon := daemonserver.NewServer(daemonserver.ServerConfig{HubToken: "test-token", StateDir: t.TempDir()})
	defer daemon.Close()
	initialPath := filepath.Join(t.TempDir(), "initial.transcript.jsonl")
	initialWriter, err := transcript.NewWriter(initialPath, transcript.Header{SessionID: instance})
	if err != nil {
		t.Fatal(err)
	}
	if err := initialWriter.Append(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "seed-turn", Kind: schema.TurnUserInput, Message: llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "seed history"}}}}); err != nil {
		t.Fatal(err)
	}
	initialLength := initialWriter.RecordedLength()
	if err := initialWriter.Close(); err != nil {
		t.Fatal(err)
	}
	identity, err := daemonserver.PrepareAppIdentityForRef("local", instance, ref, initialPath)
	if err != nil {
		t.Fatal(err)
	}
	daemon.ReplaceAppIdentity(identity.WithBootGeneration("7@manager").WithRecordedLength(initialLength), nil)
	upstream := httptest.NewServer(http.HandlerFunc(daemon.AppServer().ServeWebSocket))
	defer upstream.Close()
	entry := rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: upstream.URL, SourceID: "local", ThreadID: instance, SessionID: instance, WorkspaceRef: ref, InstanceID: instance, HubToken: "test-token"}
	// The listing closure runs on several goroutines at once (a relay read
	// and the watch's state read both list entries), and the clear handler
	// below swaps the instance from its own goroutine, so the current
	// instance is atomic and the entry template is copied, never mutated:
	// sharing a mutated capture is a data race the detector caught in CI.
	var currentInstance atomic.Value
	currentInstance.Store(instance)
	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		current := currentInstance.Load().(string)
		listed := entry
		listed.InstanceID, listed.SessionID, listed.ThreadID = current, current, current
		return []appsource.LocalDaemonEntry{{Entry: listed, SessionID: current, Manager: true}}
	}, upstream.Client())
	configureObservationTargetSessionTools(daemon, source)
	replacementPath := filepath.Join(t.TempDir(), "replacement.transcript.jsonl")
	replacementWriter, err := transcript.NewWriter(replacementPath, transcript.Header{SessionID: "replacement-instance"})
	if err != nil {
		t.Fatal(err)
	}
	replacementLength := replacementWriter.RecordedLength()
	if err := replacementWriter.Close(); err != nil {
		t.Fatal(err)
	}
	newIdentity, err := daemonserver.PrepareAppIdentityForRef("local", "replacement-instance", ref, replacementPath)
	if err != nil {
		t.Fatal(err)
	}
	daemon.SetClearFunc(func(context.Context, appwire.ThreadClearParams) error {
		currentInstance.Store("replacement-instance")
		daemon.ReplaceAppIdentity(newIdentity.WithBootGeneration("8@manager").WithRecordedLength(replacementLength), nil)
		return nil
	})
	sources := appsource.NewRegistry()
	sources.Add(source)
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex(""), HubID: "hub-test"}
	server := appserver.NewServer(appserver.ServerConfig{})
	defer server.Shutdown(ctx)
	owner := &sessionOperations{cfg: cfg, sources: sources, relays: newHubRelay(server, cfg, sources), server: server}
	var targetReads atomic.Int32
	appserver.HandleTyped(server.Router(), appwire.MethodThreadRead, func(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		targetReads.Add(1)
		return owner.Read(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadClear, func(ctx context.Context, params appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
		return clearThreadControl(ctx, cfg, sources, params)
	})
	hubHTTP := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer hubHTTP.Close()
	binding := &boundSessionOperations{owner: owner, local: source, binding: appwire.SessionBinding{Ref: ref, InstanceID: instance, HubID: "hub-test", BindingID: "binding"}}
	updates, err := binding.Watch(ctx, appwire.SessionWatchRequest{Ref: ref, Completion: true})
	if err != nil {
		t.Fatalf("completion watch: %v", err)
	}
	var initial appwire.SessionWatchUpdate
	select {
	case initial = <-updates:
		if initial.Snapshot == nil || initial.Snapshot.Snapshot == nil {
			t.Fatalf("initial snapshot lacks history identity: %+v", initial)
		}
	case <-ctx.Done():
		t.Fatal("initial observation blocked")
	}
	clearClient := dialHubRPC(t, hubHTTP)
	defer clearClient.Close()
	if _, err := clearClient.Initialize(ctx, appwire.InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	cleared, err := clearClient.ThreadClear(ctx, appwire.ThreadClearParams{Ref: ref, ClientMutationID: "clear-observation-target", ExpectedInstanceID: instance})
	if err != nil {
		t.Fatalf("clear target: %v", err)
	}
	if cleared.Thread.Evener.InstanceID != "replacement-instance" {
		t.Fatalf("clear returned instance %q, want replacement-instance", cleared.Thread.Evener.InstanceID)
	}
	replacementRead, err := clearClient.ThreadRead(ctx, appwire.ThreadReadParams{Ref: ref, IncludeTurns: true})
	if err != nil {
		t.Fatalf("read replacement history: %v", err)
	}
	if replacementRead.Snapshot == nil || replacementRead.Snapshot.Incarnation == initial.Snapshot.Snapshot.Incarnation || replacementRead.Epoch == initial.Snapshot.Epoch {
		t.Fatalf("clear did not rotate history identity: before=%+v after=%+v", initial.Snapshot, replacementRead)
	}
	select {
	case got, open := <-updates:
		if open && (got.Snapshot == nil || got.Metadata.InstanceID != cleared.Thread.Evener.InstanceID) {
			t.Fatalf("clear delivered non-replacement update %+v", got)
		}
		if open {
			select {
			case _, stillOpen := <-updates:
				if stillOpen {
					t.Fatal("replacement watch remained open")
				}
			case <-ctx.Done():
				t.Fatal("replacement watch did not close")
			}
		}
	case <-ctx.Done():
		t.Fatal("watch did not end after clear")
	}
	if targetReads.Load() != 2 {
		t.Fatalf("hub reads=%d, want initial watch read and independent replacement read only", targetReads.Load())
	}
}

func TestObservationSnapshotDispositionPrioritizesReplacement(t *testing.T) {
	initial := appwire.SessionObservationMetadata{Ref: "local:target", InstanceID: "old", GenerationID: "1"}
	held := appwire.ThreadReadResponse{Epoch: 3, Snapshot: &appwire.SnapshotIdentity{Incarnation: "old-history", Length: 12}}
	replacement := appwire.ThreadReadResponse{Epoch: 4, BootGeneration: "2", Snapshot: &appwire.SnapshotIdentity{Incarnation: "new-history", Length: 0}, Thread: appwire.Thread{Evener: appwire.EvenerThread{Ref: initial.Ref, InstanceID: "new"}}}
	if forward, replaced := observationSnapshotDisposition(initial, held, replacement); !forward || !replaced {
		t.Fatalf("replacement disposition=(forward %t, replaced %t), want both true despite rotated history", forward, replaced)
	}
	rotatedHistory := replacement
	rotatedHistory.Thread.Evener.InstanceID = initial.InstanceID
	rotatedHistory.BootGeneration = initial.GenerationID
	if forward, replaced := observationSnapshotDisposition(initial, held, rotatedHistory); forward || replaced {
		t.Fatalf("same-target history discontinuity disposition=(forward %t, replaced %t), want both false", forward, replaced)
	}
}

func managerEchoObservationItem(t *testing.T, callID, reason string, accepted bool) appwire.ThreadItem {
	t.Helper()
	path := filepath.Join(t.TempDir(), "echo.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "echo-session"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	result := `{"accepted":false,"end_turn":true}`
	if accepted {
		result = `{"accepted":true,"end_turn":true}`
	}
	entries := []schema.Turn{
		{Format: schema.TurnFormatIdentity, TurnID: "echo-turn", TurnKind: schema.TurnSpanExecution, Kind: schema.TurnAssistant,
			Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				{Kind: llm.ContentText, Text: "echo-sentinel"},
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: callID, Name: "communicate", Arguments: []byte(`{"message":"echo-sentinel"}`)}},
			}}},
		{Format: schema.TurnFormatIdentity, TurnID: "echo-turn", Kind: schema.TurnCommunicate, CausalKnown: true,
			Communicate: &schema.CommunicateInfo{CallID: callID, Message: "echo-sentinel", EndTurn: true, EndReason: reason}},
		{Format: schema.TurnFormatIdentity, TurnID: "echo-turn", Kind: schema.TurnToolResults,
			Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult,
				ToolResult: &llm.ToolResultData{ToolCallID: callID, Name: "communicate", Content: result}}}}},
	}
	for _, entry := range entries {
		if err := w.Append(entry); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	x, err := transcriptindex.Open(path, filepath.Join(t.TempDir(), "index"))
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	window, err := x.Latest(20)
	if err != nil {
		t.Fatal(err)
	}
	if len(window.Candidates) != 1 || window.Candidates[0].Item.Observation == nil {
		t.Fatalf("echo projection lost evidence or duplicated display: %+v", window)
	}
	return window.Candidates[0].Item
}

func TestManagerWatchRebindCancellationAndHubRestart(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	const ref, instance = "local:manager-workspace", "manager-instance"
	daemon := daemonserver.NewServer(daemonserver.ServerConfig{HubToken: "test-token"})
	defer daemon.Close()
	identity, err := daemonserver.PrepareAppIdentityForRef("local", instance, ref, "")
	if err != nil {
		t.Fatal(err)
	}
	daemon.ReplaceAppIdentity(identity.WithBootGeneration("7@manager"), nil)
	upstream := httptest.NewServer(http.HandlerFunc(daemon.AppServer().ServeWebSocket))
	defer upstream.Close()
	entry := rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: upstream.URL, SourceID: "local", ThreadID: instance, SessionID: instance, WorkspaceRef: ref, InstanceID: instance, HubToken: "test-token"}
	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{Entry: entry, SessionID: instance, Manager: true}}
	}, upstream.Client())
	sources := appsource.NewRegistry()
	sources.Add(source)
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex(""), HubID: "hub-test"}
	server := appserver.NewServer(appserver.ServerConfig{})
	owner := &sessionOperations{cfg: cfg, sources: sources, relays: newHubRelay(server, cfg, sources), server: server}
	appserver.HandleTyped(server.Router(), appwire.MethodThreadRead, owner.Read)
	binding := &boundSessionOperations{owner: owner, local: source, binding: appwire.SessionBinding{Ref: ref, InstanceID: instance, HubID: "hub-test", BindingID: "binding"}}
	readFirst := func(updates <-chan appwire.SessionWatchUpdate) appwire.SessionWatchUpdate {
		t.Helper()
		select {
		case update, open := <-updates:
			if !open || update.Snapshot == nil || update.Metadata.Ref != ref {
				t.Fatalf("watch initial cut: update=%+v open=%t", update, open)
			}
			return update
		case <-ctx.Done():
			t.Fatal("initial cut never arrived")
			return appwire.SessionWatchUpdate{}
		}
	}
	awaitClosed := func(updates <-chan appwire.SessionWatchUpdate) {
		t.Helper()
		select {
		case update, open := <-updates:
			if open {
				t.Fatalf("cancelled observation delivered %+v", update)
			}
		case <-ctx.Done():
			t.Fatal("observation not closed")
		}
	}
	request := appwire.SessionWatchRequest{Ref: ref, Events: []string{"status"}}
	firstCtx, cancelFirst := context.WithCancel(ctx)
	defer cancelFirst()
	updates, err := binding.Watch(firstCtx, request)
	if err != nil {
		t.Fatal(err)
	}
	first := readFirst(updates)
	cancelFirst()
	awaitClosed(updates)
	secondCtx, cancelSecond := context.WithCancel(ctx)
	defer cancelSecond()
	rebound, err := binding.Watch(secondCtx, request)
	if err != nil {
		t.Fatalf("rebound observation: %v", err)
	}
	second := readFirst(rebound)
	if first.Metadata.InstanceID != second.Metadata.InstanceID || first.Metadata.GenerationID != second.Metadata.GenerationID {
		t.Fatalf("same-generation rebound switched target: first=%+v second=%+v", first.Metadata, second.Metadata)
	}
	if n := daemon.AppServer().SubscriberCount(ref); n != 1 {
		t.Fatalf("rebound opened %d upstream subscriptions, want 1", n)
	}
	// A new hub owner opens its own subscription after the old one closes.
	cancelSecond()
	awaitClosed(rebound)
	restarted := &sessionOperations{cfg: cfg, sources: sources, relays: newHubRelay(server, cfg, sources), server: server}
	appserver.HandleTyped(server.Router(), appwire.MethodThreadRead, restarted.Read)
	restartedBinding := &boundSessionOperations{owner: restarted, local: source, binding: binding.binding}
	thirdCtx, cancelThird := context.WithCancel(ctx)
	defer cancelThird()
	restored, err := restartedBinding.Watch(thirdCtx, request)
	if err != nil {
		t.Fatalf("hub restart observation: %v", err)
	}
	third := readFirst(restored)
	if third.Metadata.InstanceID != first.Metadata.InstanceID {
		t.Fatalf("hub restart switched target: %+v", third.Metadata)
	}
	cancelThird()
	awaitClosed(restored)
}
