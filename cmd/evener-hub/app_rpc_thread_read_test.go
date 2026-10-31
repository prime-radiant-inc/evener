package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/rendezvous"
)

func TestHubRPCThreadReadRoutesToDaemon(t *testing.T) {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		if params.Ref != "local:th_1" {
			t.Fatalf("ref=%q", params.Ref)
		}
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "th_1", SessionID: "sess_1", Evener: appwire.EvenerThread{Ref: "local:th_1"}}}, nil
	})
	_, roster, runDir := startDaemonRoster(t, daemon, 102, "th_1", "sess_1")

	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir: runDir,
		Roster: roster,
		Past:   hubcore.NewPastIndex(""),
	})
	defer hub.Close()

	client := dialHubRPCInitialized(t, hub)
	resp, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:th_1"})
	if err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	if resp.Thread.ID != "th_1" || resp.Thread.Evener.Ref != "local:th_1" {
		t.Fatalf("thread=%+v", resp.Thread)
	}
}

func TestSessionOperationsReadPreservesHumanAndManagerSubscriptionModes(t *testing.T) {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		ref, err := appwire.ParseRef(params.Ref)
		if err != nil {
			return appwire.ThreadReadResponse{}, err
		}
		if err := appserver.SubscribeWithAdmission(ctx, ref.ThreadID, false); err != nil {
			return appwire.ThreadReadResponse{}, err
		}
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: ref.ThreadID, SessionID: ref.ThreadID, Evener: appwire.EvenerThread{Ref: params.Ref}}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()
	endpoint := "ws" + strings.TrimPrefix(daemonHTTP.URL, "http")
	entries := []appsource.LocalDaemonEntry{
		{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: endpoint, SourceID: "local", ThreadID: "human", SessionID: "human", WorkspaceRef: "local:human"}},
		{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: endpoint, SourceID: "local", ThreadID: "manager", SessionID: "manager", WorkspaceRef: "local:manager"}},
	}
	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry { return entries }, http.DefaultClient)
	sources := appsource.NewRegistry()
	sources.Add(source)
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex("")}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", Version: "test", SourceID: "local"})
	relays := newHubRelay(server, cfg, sources)
	operations := &sessionOperations{cfg: cfg, sources: sources, relays: relays, server: server}
	appserver.HandleTyped(server.Router(), appwire.MethodThreadRead, operations.Read)
	// This test-local route exercises the same internal snapshot-only path used
	// by Manager operations without adding a production hook.
	appserver.HandleTyped(server.Router(), "test/managerSnapshotRead", func(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return operations.read(ctx, params, false)
	})
	hub := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	defer client.Close()
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:human"}); err != nil {
		t.Fatalf("ordinary human ThreadRead: %v", err)
	}
	if got := server.SubscriberCount("local:human"); got != 1 {
		t.Fatalf("ordinary human subscriptions = %d, want 1", got)
	}
	var snapshot appwire.ThreadReadResponse
	if err := client.Request(context.Background(), "test/managerSnapshotRead", appwire.ThreadReadParams{Ref: "local:manager"}, &snapshot); err != nil {
		t.Fatalf("Manager snapshot read: %v", err)
	}
	if got := server.SubscriberCount("local:manager"); got != 0 {
		t.Fatalf("Manager snapshot subscriptions = %d, want 0", got)
	}
}

func TestHubRPCThreadReadRoutesReachableErroredDaemon(t *testing.T) {
	const sessionID = "02wMz5Txv9yYdSRJat13MZ"
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        sessionID,
			SessionID: sessionID,
			Evener:    appwire.EvenerThread{Ref: params.Ref},
		}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{
		Entry: rendezvous.Entry{
			PID:       103,
			Protocol:  appwire.ProtocolVersion,
			Endpoint:  "ws" + daemonHTTP.URL[len("http"):],
			SourceID:  "local",
			ThreadID:  sessionID,
			SessionID: sessionID,
		},
		SessionID: sessionID,
		Status:    "errored",
	})
	hub := newHubRPCTestServer(t, hubcore.WebConfig{Roster: roster, Past: hubcore.NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)

	response, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:" + sessionID})
	if err != nil {
		t.Fatalf("thread/read: %v", err)
	}
	if response.Thread.ID != sessionID {
		t.Fatalf("thread = %+v, want reachable errored daemon", response.Thread)
	}
}

func TestHubRPCThreadReadReturnsPastTranscript(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-past-0000000000")
	sessionID := buildRPCParentSession(t, stateDir)
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))
	hub := newHubRPCTestServer(t, hubcore.WebConfig{Past: past})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	resp, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:" + sessionID, IncludeTurns: true, ItemsView: "full"})
	if err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	// The user+assistant exchange is one logical turn; the trailing user
	// input is its own open group.
	if resp.Thread.ID != sessionID || len(resp.Thread.Turns) != 2 {
		t.Fatalf("thread=%+v", resp.Thread)
	}
	if got := resp.Thread.Turns[0].Items[0]; got.Type != "userMessage" || got.Text != "first task" {
		t.Fatalf("first item=%+v", got)
	}
	if got := resp.Thread.Turns[0].Items[1]; got.Type != "agentMessage" || got.Text != "first reply" {
		t.Fatalf("second item=%+v", got)
	}
	if got := resp.Thread.Turns[1].Items[0]; got.Type != "userMessage" || got.Text != "second task" {
		t.Fatalf("third item=%+v", got)
	}
}

func TestHubRPCSubscribedReadReturnsPastForCrashMarker(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-crashed-past-0000000000")
	sessionID := buildRPCParentSession(t, stateDir)
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{
		Entry: rendezvous.Entry{
			PID:       104,
			Protocol:  appwire.ProtocolVersion,
			Endpoint:  "ws://127.0.0.1:1/rpc",
			SourceID:  "local",
			ThreadID:  sessionID,
			SessionID: sessionID,
		},
		SessionID: sessionID,
		Status:    "errored",
		Crashed:   true,
	})
	hub := newHubRPCTestServer(t, hubcore.WebConfig{Roster: roster, Past: past})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)

	response, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{
		Ref:          "local:" + sessionID,
		IncludeTurns: true,
		ItemsView:    "full",
		Subscribe:    true,
		ItemLimit:    40,
	})
	if err != nil {
		t.Fatalf("subscribed thread/read: %v", err)
	}
	// The user+assistant exchange is one logical turn; the trailing user
	// input is its own open group.
	if response.Thread.ID != sessionID || len(response.Thread.Turns) != 2 {
		t.Fatalf("saved thread = %+v", response.Thread)
	}
}

func TestHubRPCSubscribedAtomicFailuresDoNotFallBackToPastAndCanRetry(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-atomic-retry-0000000000")
	sessionID := buildRPCParentSession(t, stateDir)
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))

	thread := appwire.Thread{
		ID:        sessionID,
		SessionID: sessionID,
		Source:    "local",
		Evener:    appwire.EvenerThread{Ref: "local:" + sessionID},
	}
	handoff := &recordingRelayHandoff{
		committed: make(chan struct{}),
		aborted:   make(chan struct{}),
	}
	var lease *scriptedRelaySessionLease
	lease = &scriptedRelaySessionLease{
		readErr:    appwire.SessionUnavailable("canonical actor read failed"),
		deliveries: make(chan appsource.RelayDelivery),
		readHook: func() {
			lease.mu.Lock()
			lease.readErr = nil
			lease.readResult = appsource.RelayReadResult{
				Response: appwire.ThreadReadResponse{Thread: thread},
			}
			lease.readHook = func() {
				lease.mu.Lock()
				lease.readHook = nil
				lease.readResult.Handoff = handoff
				lease.mu.Unlock()
			}
			lease.mu.Unlock()
		},
	}
	source := &relaySessionTestSource{
		thread: thread,
		lease:  lease,
	}
	sources := appsource.NewRegistry()
	sources.Add(source)
	appServer := newHubAppServer(hubcore.WebConfig{
		HubStateRoot: t.TempDir(),
		Past:         past,
	}, sources)
	hub := httptest.NewServer(http.HandlerFunc(appServer.ServeWebSocket))
	defer hub.Close()

	client := dialHubRPCInitialized(t, hub)
	params := appwire.ThreadReadParams{
		Ref:          thread.Evener.Ref,
		IncludeTurns: true,
		ItemsView:    "full",
		Subscribe:    true,
	}
	if response, err := client.ThreadRead(context.Background(), params); err == nil {
		t.Errorf("first subscribed thread/read returned saved transcript %+v after its canonical actor failed", response.Thread)
	}
	if response, err := client.ThreadRead(context.Background(), params); err == nil {
		t.Errorf("second subscribed thread/read returned saved transcript %+v without a live handoff", response.Thread)
	}
	response, err := client.ThreadRead(context.Background(), params)
	if err != nil {
		t.Fatalf("retry subscribed thread/read: %v", err)
	}
	if response.Thread.ID != sessionID {
		t.Fatalf("retry thread ID = %q, want %q", response.Thread.ID, sessionID)
	}
	// The hub commits the handoff after the response enters the connection's
	// send queue, so the client can observe the response before Commit runs.
	// Wait for the commit signal instead of assuming that ordering.
	select {
	case <-handoff.committed:
	case <-time.After(time.Second):
		t.Fatal("successful retry did not commit its live handoff")
	}
	if got := lease.readCallCount(); got != 3 {
		t.Fatalf("canonical actor read calls = %d, want read failure, missing handoff, and successful retry", got)
	}
}

func TestHubRPCNonSubscribedAtomicReadFailureCanReturnPastTranscript(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-nonsubscribed-past-0000000000")
	sessionID := buildRPCParentSession(t, stateDir)
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))

	thread := appwire.Thread{
		ID:        sessionID,
		SessionID: sessionID,
		Source:    "local",
		Evener:    appwire.EvenerThread{Ref: "local:" + sessionID},
	}
	source := &relaySessionTestSource{
		thread: thread,
		lease: &scriptedRelaySessionLease{
			readErr:    appwire.SessionUnavailable("canonical actor read failed"),
			deliveries: make(chan appsource.RelayDelivery),
		},
	}
	sources := appsource.NewRegistry()
	sources.Add(source)
	appServer := newHubAppServer(hubcore.WebConfig{
		HubStateRoot: t.TempDir(),
		Past:         past,
	}, sources)
	hub := httptest.NewServer(http.HandlerFunc(appServer.ServeWebSocket))
	defer hub.Close()

	client := dialHubRPCInitialized(t, hub)
	response, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{
		Ref:          thread.Evener.Ref,
		IncludeTurns: true,
		ItemsView:    "full",
	})
	if err != nil {
		t.Fatalf("non-subscribed thread/read: %v", err)
	}
	if response.Thread.ID != sessionID || len(response.Thread.Turns) != 2 {
		t.Fatalf("non-subscribed saved thread = %+v", response.Thread)
	}
}

func TestHubRPCSubscribedNonAtomicReadFailureCanReturnPastTranscript(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-non-atomic-past-0000000000")
	sessionID := buildRPCParentSession(t, stateDir)
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))

	thread := appwire.Thread{
		ID:        sessionID,
		SessionID: sessionID,
		Source:    "local",
		Evener:    appwire.EvenerThread{Ref: "local:" + sessionID},
	}
	sources := appsource.NewRegistry()
	sources.Add(&pastFallbackRelaySource{
		thread:  thread,
		readErr: appwire.SessionUnavailable("non-atomic live read failed"),
	})
	appServer := newHubAppServer(hubcore.WebConfig{
		HubStateRoot: t.TempDir(),
		Past:         past,
	}, sources)
	hub := httptest.NewServer(http.HandlerFunc(appServer.ServeWebSocket))
	defer hub.Close()

	client := dialHubRPCInitialized(t, hub)
	response, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{
		Ref:          thread.Evener.Ref,
		IncludeTurns: true,
		ItemsView:    "full",
		Subscribe:    true,
	})
	if err != nil {
		t.Fatalf("non-atomic subscribed thread/read: %v", err)
	}
	if response.Thread.ID != sessionID || len(response.Thread.Turns) != 2 {
		t.Fatalf("non-atomic saved thread = %+v", response.Thread)
	}
}

func TestHubRPCThreadReadEnrichesReplayToolOutputImagesFromFiles(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-images-0000000000")
	cwd := filepath.Join(root, "work")
	sessionID := "02wMz5TxvCu3kdckfnw0Gh"
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 'p', 'a', 'y'}
	if err := os.WriteFile(filepath.Join(cwd, "plot.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID:        sessionID,
		ProfileID: "openai",
		Model:     "gpt-5",
		EnvInfo:   schema.EnvironmentInfo{WorkingDir: cwd},
		CreatedAt: now,
		UpdatedAt: now,
		TurnCount: 2,
	}); err != nil {
		t.Fatal(err)
	}
	writer, err := transcript.NewWriter(filepath.Join(stateDir, "sessions", sessionID+".transcript.jsonl"), transcript.Header{
		SessionID:  sessionID,
		CreatedAt:  now,
		ProfileID:  "openai",
		Model:      "gpt-5",
		WorkingDir: cwd,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Append(schema.Turn{
		Kind: schema.TurnAssistant,
		Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
			Kind: llm.ContentToolCall,
			ToolCall: &llm.ToolCallData{
				ID:        "call_plot",
				Name:      "shell",
				Arguments: json.RawMessage(`{"command":"python plot.py"}`),
			},
		}}},
	}); err != nil {
		t.Fatal(err)
	}
	toolPNG := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 't', 'o', 'o', 'l'}
	if err := writer.Append(schema.Turn{
		Kind: schema.TurnToolResults,
		Message: llm.Message{Role: llm.RoleTool, ToolCallID: "call_plot", Content: []llm.ContentPart{{
			Kind: llm.ContentToolResult,
			ToolResult: &llm.ToolResultData{
				ToolCallID:     "call_plot",
				Name:           "shell",
				Content:        "created plot.png",
				ImageData:      toolPNG,
				ImageMediaType: "image/png",
			},
		}}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))
	hub := newHubRPCTestServer(t, hubcore.WebConfig{Past: past})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)

	resp, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:" + sessionID, IncludeTurns: true, ItemsView: "full"})
	if err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	// The assistant tool call and its result are one logical turn with one
	// merged command-execution item.
	if len(resp.Thread.Turns) != 1 || len(resp.Thread.Turns[0].Items) != 1 {
		t.Fatalf("turns=%+v", resp.Thread.Turns)
	}
	item := resp.Thread.Turns[0].Items[0]
	if len(item.OutputImages) != 2 {
		t.Fatalf("OutputImages=%+v, want tool-result then file-backed descriptors", item.OutputImages)
	}
	if item.OutputImages[0].Source != "tool-result" || item.OutputImages[0].URL == "" {
		t.Fatalf("first output image=%+v, want existing tool-result descriptor first", item.OutputImages[0])
	}
	if item.OutputImages[1].Source != "shell-path" || item.OutputImages[1].Path != "plot.png" || item.OutputImages[1].URL != "/doc/image?session="+sessionID+"&path=plot.png" {
		t.Fatalf("second output image=%+v, want shell-path plot.png descriptor", item.OutputImages[1])
	}
}

func TestHubRPCThreadReadEnrichesLiveToolOutputImagesFromFiles(t *testing.T) {
	cwd := t.TempDir()
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 'p', 'a', 'y'}
	if err := os.WriteFile(filepath.Join(cwd, "plot.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	sessionID := "02wMz5Txv9yYdSRJat13MZ"
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        sessionID,
			SessionID: sessionID,
			CWD:       cwd,
			Source:    "local",
			Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			Evener:    appwire.EvenerThread{Ref: params.Ref},
			Turns: []appwire.Turn{{
				ID: "turn_1",
				Items: []appwire.ThreadItem{{
					Type:          "commandExecution",
					ID:            "item_shell",
					TurnID:        "turn_1",
					Position:      &appwire.ThreadItemPosition{Entry: 0, Item: 0},
					TranscriptKey: "live-item:item_shell",
					ToolName:      "shell",
					CallID:        "call_shell",
					ArgumentsJSON: `{}`,
					Output:        "created plot.png",
					Status:        appwire.TurnStatusCompleted,
				}},
				Status: appwire.TurnStatusCompleted,
			}},
		}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       17 * 1000,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws" + daemonHTTP.URL[len("http"):],
		SourceID:  "local",
		ThreadID:  sessionID,
		SessionID: sessionID,
	})
	roster := hubcore.NewRoster(runDir, nil)
	roster.Refresh()
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)

	resp, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:" + sessionID, IncludeTurns: true, ItemsView: "full"})
	if err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	if len(resp.Thread.Turns) != 1 || len(resp.Thread.Turns[0].Items) != 1 {
		t.Fatalf("turns=%+v", resp.Thread.Turns)
	}
	imgs := resp.Thread.Turns[0].Items[0].OutputImages
	if len(imgs) != 1 || imgs[0].Source != "shell-path" || imgs[0].Path != "plot.png" || imgs[0].URL != "/doc/image?session="+sessionID+"&path=plot.png" {
		t.Fatalf("OutputImages=%+v, want live shell-path plot.png descriptor", imgs)
	}
}

// TestHubRPCThreadReadStampsTheSHARouteOnLiveDaemonTurns is the reload-into-a-
// running-session half of the sha-addressed path. The daemon's own snapshot
// carries tool-result descriptors that name their bytes by sha and no route;
// the hub is what turns them into something the browser can fetch.
func TestHubRPCThreadReadStampsTheSHARouteOnLiveDaemonTurns(t *testing.T) {
	sessionID := "02wMz5Txv9yYdSRJat13MZ"
	sha := strings.Repeat("f", 64)
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        sessionID,
			SessionID: sessionID,
			Source:    "local",
			Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			Evener:    appwire.EvenerThread{Ref: params.Ref},
			Turns: []appwire.Turn{{
				ID: "turn_1",
				Items: []appwire.ThreadItem{{
					Type: "commandExecution", ID: "item_shot", TurnID: "turn_1",
					Position: &appwire.ThreadItemPosition{Entry: 0, Item: 0}, TranscriptKey: "live-item:item_shot",
					ToolName: "screenshot", CallID: "call_shot", ArgumentsJSON: `{}`,
					Status:       appwire.TurnStatusCompleted,
					OutputImages: []appwire.OutputImage{{Source: "tool-result", Name: "screenshot", MediaType: "image/png", Size: 12, SHA: sha}},
				}},
				Status: appwire.TurnStatusCompleted,
			}},
		}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       19 * 1000,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws" + daemonHTTP.URL[len("http"):],
		SourceID:  "local",
		ThreadID:  sessionID,
		SessionID: sessionID,
	})
	roster := hubcore.NewRoster(runDir, nil)
	roster.Refresh()
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)

	resp, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:" + sessionID, IncludeTurns: true, ItemsView: "full"})
	if err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	if len(resp.Thread.Turns) != 1 || len(resp.Thread.Turns[0].Items) != 1 {
		t.Fatalf("turns=%+v", resp.Thread.Turns)
	}
	imgs := resp.Thread.Turns[0].Items[0].OutputImages
	if len(imgs) != 1 || imgs[0].URL != "/s/"+sessionID+"/images/"+sha {
		t.Fatalf("OutputImages=%+v, want the sha route stamped onto the daemon's descriptor", imgs)
	}
}

func TestHubRPCThreadReadMergesPastTurnsForLiveDaemon(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-past-0000000000")
	sessionID := buildRPCParentSession(t, stateDir)
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))

	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        sessionID,
			SessionID: sessionID,
			Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusClosed},
			Source:    "local",
			Evener:    appwire.EvenerThread{Ref: params.Ref},
		}}, nil
	})
	_, roster, runDir := startDaemonRoster(t, daemon, 103, sessionID, sessionID)

	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, Past: past})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	resp, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:" + sessionID, IncludeTurns: true, ItemsView: "full"})
	if err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	if resp.Thread.Status.Type != appwire.ThreadStatusClosed {
		t.Fatalf("status=%q", resp.Thread.Status.Type)
	}
	if len(resp.Thread.Turns) != 2 {
		t.Fatalf("turns=%d thread=%+v", len(resp.Thread.Turns), resp.Thread)
	}
	if got := resp.Thread.Turns[0].Items[0]; got.Type != "userMessage" || got.Text != "first task" {
		t.Fatalf("first item=%+v", got)
	}
}

func TestHubRPCThreadReadDoesNotReturnLocalPastForNonLocalMissingSource(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-local-0000000000")
	sessionID := buildRPCParentSession(t, stateDir)
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))

	hub := newHubRPCTestServer(t, hubcore.WebConfig{Past: past})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	resp, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + sessionID, IncludeTurns: true})
	if err == nil {
		t.Fatalf("ThreadRead returned local past for codex ref: %+v", resp.Thread)
	}
}

func TestHubRPCThreadReadDoesNotMergeLocalPastIntoNonLocalLiveThread(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-local-0000000000")
	sessionID := buildRPCParentSession(t, stateDir)
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))

	source := &relayBroadcastSource{
		thread: appwire.Thread{
			ID:        sessionID,
			SessionID: sessionID,
			Source:    "codex",
			Preview:   "live codex thread",
			Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			Evener:    appwire.EvenerThread{Ref: "codex:" + sessionID, Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 4),
		canceled:      make(chan struct{}, 1),
	}
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: past})
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPCInitialized(t, srv)
	resp, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + sessionID, IncludeTurns: true})
	if err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	if len(resp.Thread.Turns) != 0 {
		t.Fatalf("non-local live thread received local past turns: %+v", resp.Thread.Turns)
	}
	if resp.Thread.Preview != "live codex thread" || resp.Thread.Evener.Ref != "codex:"+sessionID {
		t.Fatalf("thread=%+v", resp.Thread)
	}
}

// A stable workspace ref and its current session ID cancel the same pending
// read, without changing the response identity used for notification delivery.
func TestHubRPCStableCurrentAdmissionCancellation(t *testing.T) {
	for _, tc := range []struct {
		name        string
		read        appwire.ThreadReadParams
		unsubscribe appwire.ThreadUnsubscribeParams
	}{
		{"stable_ref_current_id", appwire.ThreadReadParams{Ref: "local:stable"}, appwire.ThreadUnsubscribeParams{ThreadID: "current"}},
		{"current_id_stable_ref", appwire.ThreadReadParams{ThreadID: "current"}, appwire.ThreadUnsubscribeParams{Ref: "local:stable"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			entered, release := make(chan struct{}), make(chan struct{})
			var releaseOnce, firstRead sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
			appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
				id := "current"
				if params.Ref == "local:other" || params.ThreadID == "other" {
					id = "other"
				} else {
					firstRead.Do(func() {
						close(entered)
						select {
						case <-release:
						case <-ctx.Done():
						}
					})
				}
				appserver.SubscribeWithAdmission(ctx, id, false)
				return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: id, SessionID: id, Evener: appwire.EvenerThread{Ref: "local:" + id}}}, nil
			})
			daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
			defer daemonHTTP.Close()
			source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
				return []appsource.LocalDaemonEntry{
					{Entry: rendezvous.Entry{SourceID: "local", ThreadID: "stable", SessionID: "current", WorkspaceRef: "local:stable", Endpoint: daemonHTTP.URL, Protocol: appwire.ProtocolVersion}},
					{Entry: rendezvous.Entry{SourceID: "local", ThreadID: "other", SessionID: "other", WorkspaceRef: "local:other", Endpoint: daemonHTTP.URL, Protocol: appwire.ProtocolVersion}},
				}
			}, http.DefaultClient)
			sources := appsource.NewRegistry()
			sources.Add(source)
			appServer := newHubAppServer(hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex("")}, sources)
			hub := httptest.NewServer(http.HandlerFunc(appServer.ServeWebSocket))
			defer hub.Close()
			client := dialHubRPC(t, hub)
			defer client.Close()
			if _, err := client.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
				t.Fatal(err)
			}
			if _, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:other"}); err != nil {
				t.Fatalf("unrelated read: %v", err)
			}
			readDone := make(chan error, 1)
			go func() {
				_, err := client.ThreadRead(ctx, tc.read)
				readDone <- err
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("read did not reach the pre-capture daemon barrier")
			}
			if _, err := client.ThreadUnsubscribe(ctx, tc.unsubscribe); err != nil {
				t.Fatalf("unsubscribe before capture: %v", err)
			}
			unblock()
			var readErr error
			select {
			case readErr = <-readDone:
			case <-ctx.Done():
				t.Fatal("read did not finish after barrier release")
			}
			if got := appServer.SubscriberCount("local:stable"); got != 0 {
				t.Fatalf("canceled stable subscriptions = %d", got)
			}
			if got := appServer.SubscriberCount("local:current"); got != 0 {
				t.Fatalf("old stable/current read registered %d stale subscriptions after unsubscribe", got)
			}
			// The hub already reports an unavailable subscription when its
			// handoff capture is rejected; cancellation preserves that contract.
			var wire appwire.WireError
			if !errors.As(readErr, &wire) || wire.Code != appwire.CodeUnavailable {
				t.Fatalf("canceled read error = %v, want unavailable subscription", readErr)
			}
			data, ok := wire.Data.(map[string]any)
			if !ok || data["evenerErrorInfo"] != string(appwire.ErrorSessionUnavailable) {
				t.Fatalf("canceled read error data = %#v, want sessionUnavailable", wire.Data)
			}
			if got := appServer.SubscriberCount("local:other"); got != 1 {
				t.Fatalf("unrelated subscriptions = %d, want 1", got)
			}
			appServer.Broadcast("local:stable", "test/stale-admission", map[string]any{})
			appServer.Broadcast("local:current", "test/stale-admission", map[string]any{})
			appServer.BroadcastAll("test/alias-barrier", map[string]any{})
			if got := aliasMethodCountUntilBarrier(t, client, "test/stale-admission"); got != 0 {
				t.Fatalf("canceled read delivered %d notifications", got)
			}
			if _, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:stable"}); err != nil {
				t.Fatalf("later read: %v", err)
			}
			if got := appServer.SubscriberCount("local:stable"); got != 1 {
				t.Fatalf("later read subscriptions = %d, want 1", got)
			}
			awaitLiveHubSubscriptions(t, appServer, 2)
			daemon.Broadcast("current", "test/later-admission", map[string]any{"threadId": "current"})
			daemon.Broadcast("current", "test/alias-barrier", map[string]any{"threadId": "current"})
			if got := aliasMethodCountUntilBarrier(t, client, "test/later-admission"); got != 1 {
				t.Fatalf("later read delivered %d notifications, want 1", got)
			}
		})
	}
}

func TestHubRPCRootChildAdmissionIsolation(t *testing.T) {
	for _, tc := range []struct {
		name        string
		read        appwire.ThreadReadParams
		unsubscribe appwire.ThreadUnsubscribeParams
	}{
		{"child_ref", appwire.ThreadReadParams{Ref: "local:stable"}, appwire.ThreadUnsubscribeParams{Ref: "local:child"}},
		{"child_id", appwire.ThreadReadParams{ThreadID: "current"}, appwire.ThreadUnsubscribeParams{ThreadID: "child"}},
		{"unsubscribe_ref_precedence", appwire.ThreadReadParams{Ref: "local:stable"}, appwire.ThreadUnsubscribeParams{Ref: "local:child", ThreadID: "current"}},
		{"read_ref_precedence", appwire.ThreadReadParams{Ref: "local:stable", ThreadID: "child"}, appwire.ThreadUnsubscribeParams{Ref: "local:child"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			entered, release := make(chan struct{}), make(chan struct{})
			var releaseOnce, firstRead sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
			appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
				id := "current"
				if params.Ref == "local:other" || params.ThreadID == "other" {
					id = "other"
				} else {
					firstRead.Do(func() {
						close(entered)
						select {
						case <-release:
						case <-ctx.Done():
						}
					})
				}
				appserver.SubscribeWithAdmission(ctx, id, false)
				return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: id, SessionID: id, Evener: appwire.EvenerThread{Ref: "local:" + id}}}, nil
			})
			daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
			defer daemonHTTP.Close()
			source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
				return []appsource.LocalDaemonEntry{
					{Entry: rendezvous.Entry{SourceID: "local", ThreadID: "stable", SessionID: "current", WorkspaceRef: "local:stable", Endpoint: daemonHTTP.URL, Protocol: appwire.ProtocolVersion}},
					{Entry: rendezvous.Entry{SourceID: "local", ThreadID: "stable", SessionID: "current", WorkspaceRef: "local:stable", Endpoint: daemonHTTP.URL, Protocol: appwire.ProtocolVersion}, SessionID: "child", OwnerSessionID: "current", ReadOnlyAlias: true},
					{Entry: rendezvous.Entry{SourceID: "local", ThreadID: "other", SessionID: "other", WorkspaceRef: "local:other", Endpoint: daemonHTTP.URL, Protocol: appwire.ProtocolVersion}},
				}
			}, http.DefaultClient)
			sources := appsource.NewRegistry()
			sources.Add(source)
			appServer := newHubAppServer(hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex("")}, sources)
			hub := httptest.NewServer(http.HandlerFunc(appServer.ServeWebSocket))
			defer hub.Close()
			client := dialHubRPC(t, hub)
			defer client.Close()
			if _, err := client.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
				t.Fatal(err)
			}
			if _, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:other"}); err != nil {
				t.Fatalf("unrelated read: %v", err)
			}
			readDone := make(chan error, 1)
			go func() {
				_, err := client.ThreadRead(ctx, tc.read)
				readDone <- err
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("read did not reach the pre-capture daemon barrier")
			}
			if _, err := client.ThreadUnsubscribe(ctx, tc.unsubscribe); err != nil {
				t.Fatalf("unsubscribe before capture: %v", err)
			}
			unblock()
			var readErr error
			select {
			case readErr = <-readDone:
			case <-ctx.Done():
				t.Fatal("read did not finish after barrier release")
			}
			if readErr != nil {
				t.Fatalf("child unsubscribe canceled unrelated pending root read: %v", readErr)
			}
			deliveryKey := "local:stable"
			if tc.read.ThreadID != "" {
				deliveryKey = "local:" + tc.read.ThreadID
			}
			if got := appServer.SubscriberCount(deliveryKey); got != 1 {
				t.Fatalf("root subscriptions = %d, want 1", got)
			}
			if got := appServer.SubscriberCount("local:other"); got != 1 {
				t.Fatalf("unrelated subscriptions = %d, want 1", got)
			}
			awaitLiveHubSubscriptions(t, appServer, 2)
			daemon.Broadcast("current", "test/root-admission", map[string]any{"threadId": "current"})
			daemon.Broadcast("current", "test/alias-barrier", map[string]any{"threadId": "current"})
			if got := aliasMethodCountUntilBarrier(t, client, "test/root-admission"); got != 1 {
				t.Fatalf("root read delivered %d notifications, want 1", got)
			}
		})
	}
}

func TestHubSourceRegistryRoutesRunningSubagentThroughOwnerDaemon(t *testing.T) {
	app := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		ref, err := appwire.ParseRef(params.Ref)
		if err != nil {
			return appwire.ThreadReadResponse{}, err
		}
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        ref.ThreadID,
			SessionID: ref.ThreadID,
			Source:    "local",
			Evener:    appwire.EvenerThread{Ref: params.Ref},
		}}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(app.ServeWebSocket))
	defer httpServer.Close()
	entry := rendezvous.Entry{
		PID:       1,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws" + httpServer.URL[len("http"):],
		SourceID:  "local",
		ThreadID:  "root",
		SessionID: "root",
	}
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{
		Entry:              entry,
		SessionID:          "root",
		RunningSubagentIDs: []string{"child"},
		RunningJobs: []appwire.EvenerJobInfo{{
			JobID: "job_shell", JobType: "shell", Status: "running",
		}},
	})
	registry := newHubSourceRegistry(hubcore.WebConfig{Roster: roster})
	source, ok := registry.Source("local")
	if !ok {
		t.Fatal("local source missing")
	}
	listed, err := source.ListThreads(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("list root and running subagent: %v", err)
	}
	rootCarriedJob := false
	childListed := false
	for _, thread := range listed.Data {
		switch thread.ID {
		case "root":
			rootCarriedJob = thread.Evener.Diagnostics != nil && len(thread.Evener.Diagnostics.Jobs) == 1
		case "child":
			childListed = true
			if thread.Evener.Diagnostics != nil {
				t.Fatalf("child alias duplicated owner jobs: %+v", thread.Evener.Diagnostics.Jobs)
			}
		}
	}
	if !rootCarriedJob || !childListed {
		t.Fatalf("listed threads = %+v, want root job and job-free child alias", listed.Data)
	}
	read, err := source.ReadThread(context.Background(), appwire.ThreadReadParams{Ref: "local:child"})
	if err != nil {
		t.Fatalf("read running subagent through owner daemon: %v", err)
	}
	if read.Thread.ID != "child" || read.Thread.Evener.Ref != "local:child" {
		t.Fatalf("child read = %+v", read.Thread)
	}
	_, err = source.StartTurn(context.Background(), appwire.TurnStartParams{Ref: "local:child", ClientMutationID: "must-not-hit-root", Input: []appwire.InputItem{{Type: "text", Text: "hello"}}})
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("child mutation error = %T %v, want session unavailable instead of owner mutation", err, err)
	}
	data, _ := wire.Data.(appwire.ErrorData)
	if data.EvenerErrorInfo != appwire.ErrorSessionUnavailable {
		t.Fatalf("child mutation error = %T %v, want session unavailable instead of owner mutation", err, err)
	}
}

func TestHubRPCSubscribedReadRefreshesReplacedDaemonOwnership(t *testing.T) {
	for _, sameEndpoint := range []bool{false, true} {
		t.Run(fmt.Sprint("same endpoint=", sameEndpoint), func(t *testing.T) {
			testHubSubscribedReadReplacedOwner(t, sameEndpoint, appwire.MethodThreadRead, serveProtocolMismatch)
		})
	}
}
