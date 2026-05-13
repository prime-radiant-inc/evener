package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/internal/appsource"
	"primeradiant.com/serf/internal/appwire"
	"primeradiant.com/serf/internal/diagnostic"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/rendezvous"
)

func TestHubRPCThreadListUsesAppWireRendezvous(t *testing.T) {
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       101,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws://127.0.0.1:1/rpc",
		SourceID:  "local",
		ThreadID:  "th_1",
		SessionID: "sess_1",
	})
	roster := NewRoster(runDir, nil)
	roster.Refresh()

	hub := newHubRPCTestServer(t, WebConfig{
		RunDir: runDir,
		Roster: roster,
		Past:   NewPastIndex(""),
	})
	defer hub.Close()

	client := dialHubRPC(t, hub)
	defer client.Close()

	init, err := client.Initialize(context.Background(), appwire.InitializeParams{ClientInfo: appwire.ClientInfo{Name: "test", Version: "test"}})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if init.ProtocolVersion != appwire.ProtocolVersion {
		t.Fatalf("protocol=%q", init.ProtocolVersion)
	}
	resp, err := client.ThreadList(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ThreadList: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != "th_1" || resp.Data[0].Serf.Ref != "local:th_1" {
		t.Fatalf("threads=%+v", resp.Data)
	}
}

func TestHubRPCDoesNotAdvertiseUnsupportedTurnLists(t *testing.T) {
	hub := newHubRPCTestServer(t, WebConfig{Past: NewPastIndex("")})
	defer hub.Close()

	client := dialHubRPC(t, hub)
	defer client.Close()

	init, err := client.Initialize(context.Background(), appwire.InitializeParams{})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if init.Features.ThreadTurnsList {
		t.Fatalf("ThreadTurnsList advertised without Hub handlers: %+v", init.Features)
	}
}

func TestHubRPCThreadListUsesRosterStatusAndSessionID(t *testing.T) {
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       101,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws://127.0.0.1:1/rpc",
		SourceID:  "local",
		ThreadID:  "01OLD",
		SessionID: "01OLD",
	})
	roster := NewRoster(runDir, fakeProber{sessionID: "01NEW", status: "AWAITING_REPLY"})
	roster.Refresh()

	hub := newHubRPCTestServer(t, WebConfig{
		RunDir: runDir,
		Roster: roster,
		Past:   NewPastIndex(""),
	})
	defer hub.Close()

	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadList(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ThreadList: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("threads=%+v", resp.Data)
	}
	thread := resp.Data[0]
	if thread.ID != "01NEW" || thread.SessionID != "01NEW" || thread.Serf.Ref != "local:01NEW" {
		t.Fatalf("thread identity=%+v", thread)
	}
	if thread.Status.Type != appwire.ThreadStatusAwaiting {
		t.Fatalf("status=%q, want %q", thread.Status.Type, appwire.ThreadStatusAwaiting)
	}
}

func TestHubRPCThreadListIncludesPastThreads(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "past")
	sessionID := buildRPCParentSession(t, stateDir)
	past := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	hub := newHubRPCTestServer(t, WebConfig{Past: past})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadList(context.Background(), appwire.ThreadListParams{SearchTerm: "second task"})
	if err != nil {
		t.Fatalf("ThreadList: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != sessionID || resp.Data[0].Status.Type != appwire.ThreadStatusEnded {
		t.Fatalf("threads=%+v", resp.Data)
	}
}

func TestHubRPCThreadListOrdersLiveThreadsDeterministically(t *testing.T) {
	runDir := t.TempDir()
	base := time.Now().UTC()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       101,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws://127.0.0.1:1/rpc",
		SourceID:  "local",
		ThreadID:  "01OLD",
		SessionID: "01OLD",
		StartedAt: base.Add(-time.Hour),
	})
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       102,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws://127.0.0.1:2/rpc",
		SourceID:  "local",
		ThreadID:  "02NEW",
		SessionID: "02NEW",
		StartedAt: base,
	})

	hub := newHubRPCTestServer(t, WebConfig{RunDir: runDir, Past: NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadList(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ThreadList: %v", err)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("threads=%+v", resp.Data)
	}
	if resp.Data[0].ID != "02NEW" || resp.Data[1].ID != "01OLD" {
		t.Fatalf("order=%s,%s", resp.Data[0].ID, resp.Data[1].ID)
	}
}

func TestHubThreadListIncludesEveryRegisteredSource(t *testing.T) {
	base := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	entries := []rendezvous.Entry{
		{
			PID:       101,
			Protocol:  appwire.ProtocolVersion,
			Endpoint:  "ws://127.0.0.1:1/rpc",
			SourceID:  "local",
			ThreadID:  "01SERF",
			SessionID: "01SERF",
			StartedAt: base.Add(-time.Minute),
		},
		{
			PID:       102,
			Protocol:  appwire.ProtocolVersion,
			Endpoint:  "ws://127.0.0.1:2/rpc",
			SourceID:  "codex",
			ThreadID:  "02CODEX",
			SessionID: "02CODEX",
			StartedAt: base,
		},
	}
	sources := appsource.NewRegistry()
	sources.Add(appsource.NewLocalDaemonSource("local", func() []rendezvous.Entry { return entries }, nil))
	sources.Add(appsource.NewLocalDaemonSource("codex", func() []rendezvous.Entry { return entries }, nil))

	resp, err := hubThreadList(context.Background(), WebConfig{Past: NewPastIndex("")}, sources, appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("hubThreadList: %v", err)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("threads=%+v", resp.Data)
	}
	if resp.Data[0].Serf.Ref != "codex:02CODEX" || resp.Data[1].Serf.Ref != "local:01SERF" {
		t.Fatalf("refs=%s,%s", resp.Data[0].Serf.Ref, resp.Data[1].Serf.Ref)
	}
}

func TestNewHubSourceRegistryAddsConfiguredCodexSources(t *testing.T) {
	sources := newHubSourceRegistry(WebConfig{
		CodexSources: []appsource.CodexSourceConfig{{
			ID:       "codex-local",
			Endpoint: "ws://127.0.0.1:9900",
		}},
	})
	if _, ok := sources.Source("local"); !ok {
		t.Fatal("local source missing")
	}
	if source, ok := sources.Source("codex-local"); !ok {
		t.Fatal("codex source missing")
	} else if source.ID() != "codex-local" {
		t.Fatalf("source=%q", source.ID())
	}
}

func TestHubThreadListOrdersPastSearchByUpdatedCreatedTitleAndID(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "x")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	updated := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	for _, meta := range []agent.SessionMeta{
		{ID: "02OLD", CreatedAt: updated.Add(-2 * time.Hour), UpdatedAt: updated, OriginalTask: "beta task"},
		{ID: "01NEW", CreatedAt: updated.Add(-time.Hour), UpdatedAt: updated, OriginalTask: "alpha task"},
		{ID: "04TITLEB", CreatedAt: updated.Add(-3 * time.Hour), UpdatedAt: updated.Add(-time.Hour), OriginalTask: "bravo task"},
		{ID: "03TITLEA", CreatedAt: updated.Add(-3 * time.Hour), UpdatedAt: updated.Add(-time.Hour), OriginalTask: "alpha task"},
	} {
		if err := agent.SaveSessionMeta(stateDir, meta); err != nil {
			t.Fatal(err)
		}
	}
	past := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	sources := appsource.NewRegistry()

	resp, err := hubThreadList(context.Background(), WebConfig{Past: past}, sources, appwire.ThreadListParams{SearchTerm: "task"})
	if err != nil {
		t.Fatalf("hubThreadList: %v", err)
	}
	got := make([]string, 0, len(resp.Data))
	for _, thread := range resp.Data {
		got = append(got, thread.ID)
	}
	want := []string{"01NEW", "02OLD", "03TITLEA", "04TITLEB"}
	if len(got) != len(want) {
		t.Fatalf("order=%v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order=%v, want %v", got, want)
		}
	}
}

func TestHubThreadListOrdersLiveThreadsUsingPastTimestamps(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "x")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	base := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	liveUpdated := base
	pastUpdated := base.Add(-time.Hour)
	liveStarted := base.Add(-24 * time.Hour)

	if err := agent.SaveSessionMeta(stateDir, agent.SessionMeta{
		ID:           "01LIVE",
		CreatedAt:    base.Add(-2 * time.Hour),
		UpdatedAt:    liveUpdated,
		OriginalTask: "live task",
	}); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveSessionMeta(stateDir, agent.SessionMeta{
		ID:           "02PAST",
		CreatedAt:    base.Add(-3 * time.Hour),
		UpdatedAt:    pastUpdated,
		OriginalTask: "past task",
	}); err != nil {
		t.Fatal(err)
	}
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       501,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws://127.0.0.1:501/rpc",
		SourceID:  "local",
		ThreadID:  "01LIVE",
		SessionID: "01LIVE",
		StartedAt: liveStarted,
	})
	past := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	sources := newHubSourceRegistry(WebConfig{RunDir: runDir})

	resp, err := hubThreadList(context.Background(), WebConfig{Past: past}, sources, appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("hubThreadList: %v", err)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("threads=%+v", resp.Data)
	}
	if resp.Data[0].ID != "01LIVE" || resp.Data[1].ID != "02PAST" {
		t.Fatalf("order=%s,%s", resp.Data[0].ID, resp.Data[1].ID)
	}
	if resp.Data[0].UpdatedAt != liveUpdated.Unix() || resp.Data[0].CreatedAt != base.Add(-2*time.Hour).Unix() {
		t.Fatalf("live timestamps=%+v", resp.Data[0])
	}
}

func TestHubRPCThreadReadRoutesToDaemon(t *testing.T) {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		if params.Ref != "local:th_1" {
			t.Fatalf("ref=%q", params.Ref)
		}
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "th_1", SessionID: "sess_1", Serf: appwire.SerfThread{Ref: "local:th_1"}}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       102,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws" + daemonHTTP.URL[len("http"):],
		SourceID:  "local",
		ThreadID:  "th_1",
		SessionID: "sess_1",
	})
	roster := NewRoster(runDir, nil)
	roster.Refresh()

	hub := newHubRPCTestServer(t, WebConfig{
		RunDir: runDir,
		Roster: roster,
		Past:   NewPastIndex(""),
	})
	defer hub.Close()

	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:th_1"})
	if err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	if resp.Thread.ID != "th_1" || resp.Thread.Serf.Ref != "local:th_1" {
		t.Fatalf("thread=%+v", resp.Thread)
	}
}

func TestHubRPCThreadReadReturnsPastTranscript(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "past")
	sessionID := buildRPCParentSession(t, stateDir)
	past := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	hub := newHubRPCTestServer(t, WebConfig{Past: past})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:" + sessionID, IncludeTurns: true, ItemsView: "full"})
	if err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	if resp.Thread.ID != sessionID || len(resp.Thread.Turns) != 3 {
		t.Fatalf("thread=%+v", resp.Thread)
	}
	if got := resp.Thread.Turns[0].Items[0]; got.Type != "user_message" || got.Text != "first task" {
		t.Fatalf("first item=%+v", got)
	}
	if got := resp.Thread.Turns[1].Items[0]; got.Type != "agent_message" || got.Text != "first reply" {
		t.Fatalf("second item=%+v", got)
	}
}

func TestHubRPCThreadReadIncludesAPICallErrorAsFailedTurn(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "failed")
	sessionID := buildRPCFailedSession(t, stateDir)
	past := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	hub := newHubRPCTestServer(t, WebConfig{Past: past})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:" + sessionID, IncludeTurns: true, ItemsView: "full"})
	if err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	if len(resp.Thread.Turns) != 2 {
		t.Fatalf("turns=%+v", resp.Thread.Turns)
	}
	failed := resp.Thread.Turns[1]
	if failed.Status != appwire.TurnStatusFailed || failed.Error == nil || failed.Error.Message != "configuration error: unknown provider: openai" {
		t.Fatalf("failed turn=%+v", failed)
	}
	if failed.Error.Source != string(diagnostic.SourceSerf) || failed.Error.Title != "Serf configuration error" {
		t.Fatalf("failed turn diagnostic=%+v", failed.Error)
	}
}

func TestHubRPCThreadReadMergesPastTurnsForLiveDaemon(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "past")
	sessionID := buildRPCParentSession(t, stateDir)
	past := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        sessionID,
			SessionID: sessionID,
			Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusClosed},
			Source:    "local",
			Serf:      appwire.SerfThread{Ref: params.Ref},
		}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       103,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws" + daemonHTTP.URL[len("http"):],
		SourceID:  "local",
		ThreadID:  sessionID,
		SessionID: sessionID,
	})
	roster := NewRoster(runDir, nil)
	roster.Refresh()

	hub := newHubRPCTestServer(t, WebConfig{RunDir: runDir, Roster: roster, Past: past})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:" + sessionID, IncludeTurns: true, ItemsView: "full"})
	if err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	if resp.Thread.Status.Type != appwire.ThreadStatusClosed {
		t.Fatalf("status=%q", resp.Thread.Status.Type)
	}
	if len(resp.Thread.Turns) != 3 {
		t.Fatalf("turns=%d thread=%+v", len(resp.Thread.Turns), resp.Thread)
	}
	if got := resp.Thread.Turns[0].Items[0]; got.Type != "user_message" || got.Text != "first task" {
		t.Fatalf("first item=%+v", got)
	}
}

func TestHubRPCThreadReadRelaysDaemonNotifications(t *testing.T) {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(ctx context.Context, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		appserver.Subscribe(ctx, "th_1")
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "th_1", SessionID: "sess_1", Serf: appwire.SerfThread{Ref: "local:th_1"}}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       103,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws" + daemonHTTP.URL[len("http"):],
		SourceID:  "local",
		ThreadID:  "th_1",
		SessionID: "sess_1",
	})
	roster := NewRoster(runDir, nil)
	roster.Refresh()

	hub := newHubRPCTestServer(t, WebConfig{
		RunDir: runDir,
		Roster: roster,
		Past:   NewPastIndex(""),
	})
	defer hub.Close()

	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:th_1"}); err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}

	daemon.Broadcast("th_1", appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{
		ThreadID: "th_1",
		Ref:      "local:th_1",
		TurnID:   "turn_1",
		ItemID:   "item_1",
		Delta:    "hi",
	})

	select {
	case got := <-client.Notifications():
		if got.Method != appwire.NotifyAgentMessageDelta {
			t.Fatalf("method=%q", got.Method)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for relayed notification")
	}
}

func TestHubRPCThreadActionsRouteToDaemon(t *testing.T) {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	compactCalled := false
	shutdownCalled := false
	modelCalled := ""
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		if params.Ref != "local:th_1" {
			t.Fatalf("read ref=%q", params.Ref)
		}
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        "th_1",
			SessionID: "sess_1",
			Serf: appwire.SerfThread{
				Ref: "local:th_1",
				Capabilities: appwire.ThreadCapabilities{
					Send:         true,
					Steer:        true,
					Interrupt:    true,
					Compact:      true,
					Clear:        true,
					ForkFromTurn: true,
					Shutdown:     true,
					ChangeModel:  true,
				},
			},
		}}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadCompactStart, func(_ context.Context, params appwire.ThreadCompactStartParams) (appwire.EmptyResponse, error) {
		if params.Ref != "local:th_1" {
			t.Fatalf("compact ref=%q", params.Ref)
		}
		compactCalled = true
		return appwire.EmptyResponse{}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadModelSet, func(_ context.Context, params appwire.ThreadModelSetParams) (appwire.EmptyResponse, error) {
		if params.Ref != "local:th_1" {
			t.Fatalf("model ref=%q", params.Ref)
		}
		modelCalled = params.ModelProvider + "/" + params.Model
		return appwire.EmptyResponse{}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadShutdown, func(_ context.Context, params appwire.ThreadShutdownParams) (appwire.EmptyResponse, error) {
		if params.Ref != "local:th_1" {
			t.Fatalf("shutdown ref=%q", params.Ref)
		}
		shutdownCalled = true
		return appwire.EmptyResponse{}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       104,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws" + daemonHTTP.URL[len("http"):],
		SourceID:  "local",
		ThreadID:  "th_1",
		SessionID: "sess_1",
	})
	roster := NewRoster(runDir, nil)
	roster.Refresh()

	hub := newHubRPCTestServer(t, WebConfig{RunDir: runDir, Roster: roster, Past: NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := client.ThreadCompactStart(context.Background(), appwire.ThreadCompactStartParams{Ref: "local:th_1"}); err != nil {
		t.Fatalf("ThreadCompactStart: %v", err)
	}
	if !compactCalled {
		t.Fatal("compact was not routed")
	}
	if err := client.ThreadModelSet(context.Background(), appwire.ThreadModelSetParams{
		Ref:           "local:th_1",
		ModelProvider: "openai",
		Model:         "gpt-5",
	}); err != nil {
		t.Fatalf("ThreadModelSet: %v", err)
	}
	if modelCalled != "openai/gpt-5" {
		t.Fatalf("modelCalled=%q", modelCalled)
	}
	if err := client.ThreadShutdown(context.Background(), appwire.ThreadShutdownParams{Ref: "local:th_1"}); err != nil {
		t.Fatalf("ThreadShutdown: %v", err)
	}
	if !shutdownCalled {
		t.Fatal("shutdown was not routed")
	}
}

func TestHubRPCUnsupportedThreadActionReturnsStructuredUnavailable(t *testing.T) {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	shutdownCalled := false
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		if params.Ref != "local:th_1" {
			t.Fatalf("read ref=%q", params.Ref)
		}
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        "th_1",
			SessionID: "sess_1",
			Serf: appwire.SerfThread{
				Ref: "local:th_1",
				Capabilities: appwire.ThreadCapabilities{
					Send:    true,
					Compact: true,
				},
			},
		}}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadShutdown, func(_ context.Context, params appwire.ThreadShutdownParams) (appwire.EmptyResponse, error) {
		shutdownCalled = true
		return appwire.EmptyResponse{}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       105,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws" + daemonHTTP.URL[len("http"):],
		SourceID:  "local",
		ThreadID:  "th_1",
		SessionID: "sess_1",
	})
	roster := NewRoster(runDir, nil)
	roster.Refresh()

	hub := newHubRPCTestServer(t, WebConfig{RunDir: runDir, Roster: roster, Past: NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	err := client.ThreadShutdown(context.Background(), appwire.ThreadShutdownParams{Ref: "local:th_1"})
	if err == nil {
		t.Fatal("ThreadShutdown succeeded for unsupported action")
	}
	if shutdownCalled {
		t.Fatal("unsupported shutdown reached source")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error %T does not preserve WireError: %v", err, err)
	}
	if wire.Code != appwire.CodeUnavailable {
		t.Fatalf("wire=%+v", wire)
	}
	data, ok := wire.Data.(map[string]any)
	if !ok || data["serfErrorInfo"] != string(appwire.ErrorActionUnavailable) {
		t.Fatalf("wire data=%#v", wire.Data)
	}
}

func TestHubRPCModelListFallsBackToConfigWhenDaemonFails(t *testing.T) {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodModelList, func(context.Context, appwire.ModelListParams) (appwire.ModelListResponse, error) {
		return appwire.ModelListResponse{}, appwire.InternalError("provider unavailable")
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       104,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws" + daemonHTTP.URL[len("http"):],
		SourceID:  "local",
		ThreadID:  "th_1",
		SessionID: "th_1",
	})
	roster := NewRoster(runDir, nil)
	roster.Refresh()

	hub := newHubRPCTestServer(t, WebConfig{
		RunDir: runDir,
		Roster: roster,
		Models: []modelDescriptor{{Provider: "openai", Model: "gpt-5.2"}},
	})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ModelList(context.Background(), appwire.ModelListParams{})
	if err != nil {
		t.Fatalf("ModelList: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].Provider != "openai" || resp.Data[0].Model != "gpt-5.2" {
		t.Fatalf("models=%+v", resp.Data)
	}
}

func TestHubRPCThreadStartKeepsProviderForModelIDsWithSlashes(t *testing.T) {
	runDir := t.TempDir()
	var got SpawnRequest
	spawner := &fakeRPCSpawner{
		spawn: func(_ context.Context, req SpawnRequest) (rendezvous.Entry, error) {
			got = req
			return rendezvous.Entry{
				PID:       106,
				Protocol:  appwire.ProtocolVersion,
				SourceID:  "local",
				ThreadID:  "th_slash_model",
				SessionID: "sess_slash_model",
			}, nil
		},
	}
	hub := newHubRPCTestServer(t, WebConfig{RunDir: runDir, Spawner: spawner, Past: NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{
		ModelProvider: "openrouter",
		Model:         "deepseek/deepseek-v4-flash",
		CWD:           "/tmp",
	}); err != nil {
		t.Fatalf("ThreadStart: %v", err)
	}
	if got.Model != "openrouter/deepseek/deepseek-v4-flash" {
		t.Fatalf("spawn model=%q, want openrouter/deepseek/deepseek-v4-flash", got.Model)
	}
}

func TestHubRPCThreadStartRoutesByHarnessToConfiguredCodexSource(t *testing.T) {
	codex := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex"})
	var startCalled bool
	var turnCalled bool
	appserver.HandleTyped(codex.Router(), appwire.MethodThreadStart, func(_ context.Context, params map[string]any) (map[string]any, error) {
		startCalled = true
		if params["cwd"] != "/work/project" || params["model"] != "gpt-5.1-codex" {
			t.Fatalf("thread/start params=%+v", params)
		}
		if _, ok := params["harness"]; ok {
			t.Fatalf("codex thread/start should not receive hub harness routing field: %+v", params)
		}
		if _, ok := params["sourceId"]; ok {
			t.Fatalf("codex thread/start should not receive hub source routing field: %+v", params)
		}
		return map[string]any{"thread": map[string]any{
			"id":            "th_codex",
			"sessionId":     "th_codex",
			"preview":       "codex task",
			"modelProvider": "openai",
			"createdAt":     100,
			"updatedAt":     100,
			"status":        map[string]any{"type": "idle"},
			"cwd":           "/work/project",
			"cliVersion":    "codex-test",
			"source":        "appServer",
		}}, nil
	})
	appserver.HandleTyped(codex.Router(), appwire.MethodTurnStart, func(_ context.Context, params map[string]any) (map[string]any, error) {
		turnCalled = true
		if params["threadId"] != "th_codex" {
			t.Fatalf("turn/start params=%+v", params)
		}
		return map[string]any{"turn": map[string]any{
			"id":        "turn_codex",
			"items":     []any{},
			"itemsView": "full",
			"status":    "inProgress",
		}}, nil
	})
	codexHTTP := httptest.NewServer(http.HandlerFunc(codex.ServeWebSocket))
	defer codexHTTP.Close()

	hub := newHubRPCTestServer(t, WebConfig{
		Past: NewPastIndex(""),
		CodexSources: []appsource.CodexSourceConfig{{
			ID:       "codex",
			Endpoint: "ws" + strings.TrimPrefix(codexHTTP.URL, "http"),
		}},
	})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{
		Harness: "codex",
		CWD:     "/work/project",
		Prompt:  "hello codex",
		Model:   "gpt-5.1-codex",
	})
	if err != nil {
		t.Fatalf("ThreadStart: %v", err)
	}
	if !startCalled || !turnCalled {
		t.Fatalf("startCalled=%v turnCalled=%v", startCalled, turnCalled)
	}
	if resp.Thread.Serf.Ref != "codex:th_codex" || resp.Turn.ID != "turn_codex" {
		t.Fatalf("resp=%+v", resp)
	}
}

func TestHubRPCThreadStartLaunchesConfiguredCodexAppServer(t *testing.T) {
	launcher := NewCodexLauncher([]CodexLaunchConfig{fakeCodexLaunchConfig("codex-managed", "ready")})
	defer shutdownCodexLauncher(t, launcher)
	hub := newHubRPCTestServer(t, WebConfig{
		Past:          NewPastIndex(""),
		CodexLaunches: []CodexLaunchConfig{fakeCodexLaunchConfig("codex-managed", "ready")},
		CodexLauncher: launcher,
	})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{
		Harness: "codex-managed",
		CWD:     "/tmp/project",
		Prompt:  "hello launched codex",
	})
	if err != nil {
		t.Fatalf("ThreadStart: %v", err)
	}
	if resp.Thread.Serf.Ref != "codex-managed:th_fake" || resp.Turn.ID != "turn_fake" {
		t.Fatalf("resp=%+v", resp)
	}
}

func TestHubRPCHarnessListIncludesConfiguredCodexSources(t *testing.T) {
	hub := newHubRPCTestServer(t, WebConfig{
		CodexSources: []appsource.CodexSourceConfig{{
			ID: "codex-local",
		}, {}},
		CodexLaunches: []CodexLaunchConfig{{ID: "codex-managed"}},
	})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	var resp struct {
		Data []struct {
			ID    string `json:"id"`
			Label string `json:"label"`
			Kind  string `json:"kind"`
		} `json:"data"`
	}
	if err := client.Request(context.Background(), appwire.MethodSerfHarnessesList, map[string]any{}, &resp); err != nil {
		t.Fatalf("serf/harnesses/list: %v", err)
	}
	got := map[string]string{}
	for _, h := range resp.Data {
		got[h.ID] = h.Kind
	}
	if got["serf"] != "serf" || got["codex-local"] != "codex" || got["codex"] != "codex" || got["codex-managed"] != "codex" {
		t.Fatalf("harnesses=%+v", resp.Data)
	}
}

func TestHubRPCThreadResumeSpawnsAndReadsDaemon(t *testing.T) {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		if params.Ref != "local:th_resumed" {
			t.Fatalf("ref=%q", params.Ref)
		}
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "th_resumed", SessionID: "sess_resumed", Serf: appwire.SerfThread{Ref: "local:th_resumed"}}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	spawner := &fakeRPCSpawner{
		resume: func(ctx context.Context, req ResumeRequest) (rendezvous.Entry, error) {
			if req.SessionID != "sess_old" {
				t.Fatalf("resume session=%q", req.SessionID)
			}
			entry := rendezvous.Entry{
				PID:       105,
				Protocol:  appwire.ProtocolVersion,
				Endpoint:  "ws" + daemonHTTP.URL[len("http"):],
				SourceID:  "local",
				ThreadID:  "th_resumed",
				SessionID: "sess_resumed",
			}
			writeRendezvous(t, runDir, entry)
			return entry, nil
		},
	}

	hub := newHubRPCTestServer(t, WebConfig{RunDir: runDir, Spawner: spawner, Past: NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadResume(context.Background(), appwire.ThreadResumeParams{Session: "sess_old"})
	if err != nil {
		t.Fatalf("ThreadResume: %v", err)
	}
	if resp.Thread.ID != "th_resumed" || resp.Thread.Serf.Ref != "local:th_resumed" {
		t.Fatalf("thread=%+v", resp.Thread)
	}
}

func TestHubRPCThreadResumeRoutesConfiguredCodexSource(t *testing.T) {
	codex := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex"})
	var resumeCalled bool
	appserver.HandleTyped(codex.Router(), appwire.MethodThreadResume, func(_ context.Context, params map[string]any) (map[string]any, error) {
		resumeCalled = true
		if params["threadId"] != "th_codex" {
			t.Fatalf("thread/resume params=%+v", params)
		}
		return map[string]any{"thread": map[string]any{
			"id":            "th_codex",
			"sessionId":     "th_codex",
			"preview":       "resumed codex",
			"modelProvider": "openai",
			"status":        map[string]any{"type": "idle"},
			"source":        "appServer",
		}}, nil
	})
	codexHTTP := httptest.NewServer(http.HandlerFunc(codex.ServeWebSocket))
	defer codexHTTP.Close()

	hub := newHubRPCTestServer(t, WebConfig{
		Past: NewPastIndex(""),
		CodexSources: []appsource.CodexSourceConfig{{
			ID:       "codex",
			Endpoint: "ws" + strings.TrimPrefix(codexHTTP.URL, "http"),
		}},
	})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadResume(context.Background(), appwire.ThreadResumeParams{Ref: "codex:th_codex"})
	if err != nil {
		t.Fatalf("ThreadResume: %v", err)
	}
	if !resumeCalled {
		t.Fatal("codex resume was not routed")
	}
	if resp.Thread.Serf.Ref != "codex:th_codex" {
		t.Fatalf("thread=%+v", resp.Thread)
	}
}

func TestHubRPCThreadReadDoesNotResumeConfiguredCodexSource(t *testing.T) {
	codex := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex"})
	var readCalled bool
	appserver.HandleTyped(codex.Router(), appwire.MethodThreadRead, func(_ context.Context, params map[string]any) (map[string]any, error) {
		readCalled = true
		if params["threadId"] != "th_codex" {
			t.Fatalf("thread/read params=%+v", params)
		}
		return map[string]any{"thread": map[string]any{
			"id":        "th_codex",
			"sessionId": "th_codex",
			"preview":   "read-only codex",
			"status":    map[string]any{"type": "idle"},
			"source":    "appServer",
		}}, nil
	})
	codexHTTP := httptest.NewServer(http.HandlerFunc(codex.ServeWebSocket))
	defer codexHTTP.Close()

	hub := newHubRPCTestServer(t, WebConfig{
		Past: NewPastIndex(""),
		CodexSources: []appsource.CodexSourceConfig{{
			ID:       "codex",
			Endpoint: "ws" + strings.TrimPrefix(codexHTTP.URL, "http"),
		}},
	})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:th_codex"})
	if err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	if !readCalled {
		t.Fatal("codex read was not routed")
	}
	if resp.Thread.Serf.Ref != "codex:th_codex" {
		t.Fatalf("thread=%+v", resp.Thread)
	}
}

func TestHubRPCThreadForkRoutesConfiguredCodexSource(t *testing.T) {
	codex := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex"})
	var forkCalled bool
	appserver.HandleTyped(codex.Router(), appwire.MethodThreadRead, func(_ context.Context, params map[string]any) (map[string]any, error) {
		if params["threadId"] != "th_codex" {
			t.Fatalf("thread/read params=%+v", params)
		}
		return map[string]any{"thread": map[string]any{
			"id":        "th_codex",
			"sessionId": "th_codex",
			"status":    map[string]any{"type": "idle"},
			"source":    "appServer",
		}}, nil
	})
	appserver.HandleTyped(codex.Router(), appwire.MethodThreadFork, func(_ context.Context, params map[string]any) (map[string]any, error) {
		forkCalled = true
		if params["threadId"] != "th_codex" {
			t.Fatalf("thread/fork params=%+v", params)
		}
		return map[string]any{"thread": map[string]any{
			"id":        "th_codex_child",
			"sessionId": "th_codex_child",
			"status":    map[string]any{"type": "idle"},
			"source":    "appServer",
		}}, nil
	})
	codexHTTP := httptest.NewServer(http.HandlerFunc(codex.ServeWebSocket))
	defer codexHTTP.Close()

	hub := newHubRPCTestServer(t, WebConfig{
		Past: NewPastIndex(""),
		CodexSources: []appsource.CodexSourceConfig{{
			ID:       "codex",
			Endpoint: "ws" + strings.TrimPrefix(codexHTTP.URL, "http"),
		}},
	})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadFork(context.Background(), appwire.ThreadForkParams{Ref: "codex:th_codex"})
	if err != nil {
		t.Fatalf("ThreadFork: %v", err)
	}
	if !forkCalled {
		t.Fatal("configured Codex source was not forked")
	}
	if resp.Thread.Serf.Ref != "codex:th_codex_child" {
		t.Fatalf("thread=%+v", resp.Thread)
	}
}

func TestHubRPCThreadStartRelaysReturnedSourceThread(t *testing.T) {
	source := &startResumeRelaySource{
		relayBroadcastSource: relayBroadcastSource{
			id: "codex",
			thread: appwire.Thread{
				ID:        "th_start_relay",
				SessionID: "th_start_relay",
				Source:    "codex",
				Serf:      appwire.SerfThread{Ref: "codex:th_start_relay", Capabilities: appwire.ThreadCapabilities{Send: true}},
			},
			notifications: make(chan appwire.Notification, 4),
			subscribed:    make(chan struct{}, 1),
			canceled:      make(chan struct{}, 1),
		},
	}
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(WebConfig{HubAddr: srv.Listener.Addr().String(), Past: NewPastIndex("")})
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPC(t, srv)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{Harness: "codex", CWD: "/work", Prompt: "hello"})
	if err != nil {
		t.Fatalf("ThreadStart: %v", err)
	}
	if resp.Thread.Serf.Ref != "codex:th_start_relay" {
		t.Fatalf("thread=%+v", resp.Thread)
	}
	expectRelaySubscription(t, source.subscribed)

	source.notifications <- appwire.Notification{
		Method: appwire.NotifyAgentMessageDelta,
		Params: testRawJSON(t, appwire.AgentMessageDeltaParams{
			ThreadID: "th_start_relay",
			Ref:      "codex:th_start_relay",
			TurnID:   "turn_1",
			ItemID:   "item_1",
			Delta:    "after start",
		}),
	}
	select {
	case got := <-client.Notifications():
		if got.Method != appwire.NotifyAgentMessageDelta {
			t.Fatalf("method=%q", got.Method)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for start relay notification")
	}
}

func TestHubRPCThreadStartReturnsThreadWhenPostStartRelayFails(t *testing.T) {
	source := &startRelayFailureSource{
		startResumeRelaySource: startResumeRelaySource{
			relayBroadcastSource: relayBroadcastSource{
				id: "codex",
				thread: appwire.Thread{
					ID:        "th_start_relay_fail",
					SessionID: "th_start_relay_fail",
					Source:    "codex",
					Serf:      appwire.SerfThread{Ref: "codex:th_start_relay_fail", Capabilities: appwire.ThreadCapabilities{Send: true}},
				},
			},
		},
	}
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(WebConfig{HubAddr: srv.Listener.Addr().String(), Past: NewPastIndex("")})
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPC(t, srv)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{Harness: "codex", CWD: "/work", Prompt: "hello"})
	if err != nil {
		t.Fatalf("ThreadStart: %v", err)
	}
	if resp.Thread.Serf.Ref != "codex:th_start_relay_fail" {
		t.Fatalf("thread=%+v", resp.Thread)
	}
	select {
	case got := <-client.Notifications():
		if got.Method != appwire.NotifyWarning {
			t.Fatalf("method=%q, want warning", got.Method)
		}
		if !strings.Contains(string(got.Params), "subscribe failed after start") || !strings.Contains(string(got.Params), `"source":"hub"`) {
			t.Fatalf("warning params=%s", got.Params)
		}
		payload := warningPayload(got.Params)
		if payload["source"] != "hub" || payload["title"] != "Live updates unavailable" {
			t.Fatalf("warning payload=%+v", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for relay warning")
	}
}

func TestHubRPCThreadResumeRelaysReturnedSourceThread(t *testing.T) {
	source := &startResumeRelaySource{
		relayBroadcastSource: relayBroadcastSource{
			id: "codex",
			thread: appwire.Thread{
				ID:        "th_resume_relay",
				SessionID: "th_resume_relay",
				Source:    "codex",
				Serf:      appwire.SerfThread{Ref: "codex:th_resume_relay", Capabilities: appwire.ThreadCapabilities{Send: true}},
			},
			notifications: make(chan appwire.Notification, 4),
			subscribed:    make(chan struct{}, 1),
			canceled:      make(chan struct{}, 1),
		},
	}
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(WebConfig{HubAddr: srv.Listener.Addr().String(), Past: NewPastIndex("")})
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPC(t, srv)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadResume(context.Background(), appwire.ThreadResumeParams{Ref: "codex:th_resume_relay"})
	if err != nil {
		t.Fatalf("ThreadResume: %v", err)
	}
	if resp.Thread.Serf.Ref != "codex:th_resume_relay" {
		t.Fatalf("thread=%+v", resp.Thread)
	}
	expectRelaySubscription(t, source.subscribed)

	source.notifications <- appwire.Notification{
		Method: appwire.NotifyAgentMessageDelta,
		Params: testRawJSON(t, appwire.AgentMessageDeltaParams{
			ThreadID: "th_resume_relay",
			Ref:      "codex:th_resume_relay",
			TurnID:   "turn_1",
			ItemID:   "item_1",
			Delta:    "after resume",
		}),
	}
	select {
	case got := <-client.Notifications():
		if got.Method != appwire.NotifyAgentMessageDelta {
			t.Fatalf("method=%q", got.Method)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for resume relay notification")
	}
}
func TestHubRPCTurnStartResumesPastThread(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "past")
	sessionID := buildRPCParentSession(t, stateDir)
	past := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: sessionID, SessionID: sessionID, Source: "local", Serf: appwire.SerfThread{Ref: params.Ref, Capabilities: appwire.ThreadCapabilities{Send: true}}}}, nil
	})
	var gotPrompt string
	appserver.HandleTyped(daemon.Router(), appwire.MethodTurnStart, func(_ context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		gotPrompt = params.Prompt
		return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_4"}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	spawner := &fakeRPCSpawner{
		resume: func(context.Context, ResumeRequest) (rendezvous.Entry, error) {
			entry := rendezvous.Entry{
				PID:       106,
				Protocol:  appwire.ProtocolVersion,
				Endpoint:  "ws" + daemonHTTP.URL[len("http"):],
				SourceID:  "local",
				ThreadID:  sessionID,
				SessionID: sessionID,
			}
			writeRendezvous(t, runDir, entry)
			return entry, nil
		},
	}
	roster := NewRoster(runDir, nil)
	hub := newHubRPCTestServer(t, WebConfig{RunDir: runDir, Roster: roster, Spawner: spawner, Past: past})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.TurnStart(context.Background(), appwire.TurnStartParams{Ref: "local:" + sessionID, Prompt: "resume work"}); err != nil {
		t.Fatalf("TurnStart: %v", err)
	}
	if gotPrompt != "resume work" {
		t.Fatalf("prompt=%q", gotPrompt)
	}
}

func TestHubRPCTurnStartResumesPastThreadAndRelaysNotifications(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "past")
	sessionID := buildRPCParentSession(t, stateDir)
	past := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		appserver.Subscribe(ctx, sessionID)
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: sessionID, SessionID: sessionID, Source: "local", Serf: appwire.SerfThread{Ref: params.Ref, Capabilities: appwire.ThreadCapabilities{Send: true}}}}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodTurnStart, func(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_4"}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	spawner := &fakeRPCSpawner{
		resume: func(context.Context, ResumeRequest) (rendezvous.Entry, error) {
			entry := rendezvous.Entry{
				PID:       107,
				Protocol:  appwire.ProtocolVersion,
				Endpoint:  "ws" + daemonHTTP.URL[len("http"):],
				SourceID:  "local",
				ThreadID:  sessionID,
				SessionID: sessionID,
			}
			writeRendezvous(t, runDir, entry)
			return entry, nil
		},
	}
	roster := NewRoster(runDir, nil)
	hub := newHubRPCTestServer(t, WebConfig{RunDir: runDir, Roster: roster, Spawner: spawner, Past: past})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:" + sessionID, IncludeTurns: true}); err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	if _, err := client.TurnStart(context.Background(), appwire.TurnStartParams{Ref: "local:" + sessionID, Prompt: "resume work"}); err != nil {
		t.Fatalf("TurnStart: %v", err)
	}

	daemon.Broadcast(sessionID, appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{
		ThreadID: sessionID,
		Ref:      "local:" + sessionID,
		TurnID:   "turn_4",
		ItemID:   "item_1",
		Delta:    "live update",
	})

	select {
	case got := <-client.Notifications():
		if got.Method != appwire.NotifyAgentMessageDelta {
			t.Fatalf("method=%q", got.Method)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for resumed turn notification")
	}
}

func TestHubRPCDirsCompleteReturnsMatchingDirectories(t *testing.T) {
	root := t.TempDir()
	alpha := filepath.Join(root, "alpha")
	if err := os.Mkdir(alpha, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "beta"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "alpine.txt"), []byte("no"), 0o644); err != nil {
		t.Fatal(err)
	}

	hub := newHubRPCTestServer(t, WebConfig{Past: NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.DirsComplete(context.Background(), appwire.DirsCompleteParams{Prefix: filepath.Join(root, "a")})
	if err != nil {
		t.Fatalf("DirsComplete: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0] != alpha {
		t.Fatalf("dirs=%+v, want [%s]", resp.Data, alpha)
	}
}

func TestHubRPCThreadForkCreatesForkedThread(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "fork")
	parentID := buildRPCParentSession(t, stateDir)
	past := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	hub := newHubRPCTestServer(t, WebConfig{Past: past})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadFork(context.Background(), appwire.ThreadForkParams{
		Ref:          "local:" + parentID,
		SourceTurnID: "3",
		EditedInput:  "second task, edited",
		Label:        "before edit",
	})
	if err != nil {
		t.Fatalf("ThreadFork: %v", err)
	}
	if resp.Thread.ID == "" || resp.Thread.ID == parentID || resp.Thread.Serf.Ref != "local:"+resp.Thread.ID {
		t.Fatalf("thread=%+v", resp.Thread)
	}
	childMeta, err := agent.LoadSessionMeta(stateDir, resp.Thread.ID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(child): %v", err)
	}
	if childMeta.ParentSessionID != parentID || childMeta.DivergenceTurn != 3 {
		t.Fatalf("child meta=%+v", childMeta)
	}
}

type fakeRPCSpawner struct {
	spawn  func(context.Context, SpawnRequest) (rendezvous.Entry, error)
	resume func(context.Context, ResumeRequest) (rendezvous.Entry, error)
}

func (f *fakeRPCSpawner) Spawn(ctx context.Context, req SpawnRequest) (rendezvous.Entry, error) {
	if f.spawn != nil {
		return f.spawn(ctx, req)
	}
	return rendezvous.Entry{}, appwire.Unavailable("spawn not configured")
}

func (f *fakeRPCSpawner) Resume(ctx context.Context, req ResumeRequest) (rendezvous.Entry, error) {
	if f.resume != nil {
		return f.resume(ctx, req)
	}
	return rendezvous.Entry{}, appwire.Unavailable("resume not configured")
}

func buildRPCParentSession(t *testing.T, stateDir string) string {
	t.Helper()
	parentID := "01PARENT00000000000000001"
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	writer, err := agent.NewTranscriptWriter(filepath.Join(stateDir, "sessions", parentID+".transcript.jsonl"), agent.TranscriptHeader{
		SessionID:  parentID,
		CreatedAt:  time.Now().UTC(),
		ProfileID:  "openai",
		Model:      "gpt-5",
		WorkingDir: "/tmp/project",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, turn := range []agent.Turn{
		agent.NewTurn(agent.TurnUserInput, llm.User("first task")),
		agent.NewTurn(agent.TurnAssistant, llm.Assistant("first reply")),
		agent.NewTurn(agent.TurnUserInput, llm.User("second task")),
	} {
		if err := writer.Append(turn); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveSessionMeta(stateDir, agent.SessionMeta{
		ID:           parentID,
		ProfileID:    "openai",
		Model:        "gpt-5",
		EnvInfo:      agent.EnvironmentInfo{WorkingDir: "/tmp/project"},
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
		TurnCount:    2,
		OriginalTask: "second task",
	}); err != nil {
		t.Fatal(err)
	}
	return parentID
}

func buildRPCFailedSession(t *testing.T, stateDir string) string {
	t.Helper()
	sessionID := "01FAILED0000000000000001"
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	writer, err := agent.NewTranscriptWriter(filepath.Join(stateDir, "sessions", sessionID+".transcript.jsonl"), agent.TranscriptHeader{
		SessionID:  sessionID,
		CreatedAt:  time.Now().UTC(),
		ProfileID:  "openai",
		Model:      "gpt-5",
		WorkingDir: "/tmp/project",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Append(agent.NewTurn(agent.TurnUserInput, llm.User("hello"))); err != nil {
		t.Fatal(err)
	}
	if err := writer.AppendAPICall(agent.TranscriptAPICall{
		Round: 1,
		Request: llm.APILogRequest{
			Provider: "openai",
			Model:    "gpt-5",
		},
		Error: "configuration error: unknown provider: openai",
	}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := agent.SaveSessionMeta(stateDir, agent.SessionMeta{
		ID:           sessionID,
		ProfileID:    "openai",
		Model:        "gpt-5",
		EnvInfo:      agent.EnvironmentInfo{WorkingDir: "/tmp/project"},
		CreatedAt:    now,
		UpdatedAt:    now,
		TurnCount:    1,
		OriginalTask: "hello",
	}); err != nil {
		t.Fatal(err)
	}
	return sessionID
}

func dialHubRPC(t *testing.T, hub *httptest.Server) *appwire.Client {
	t.Helper()
	transport, err := appwire.DialWebSocket(context.Background(), "ws"+hub.URL[len("http"):]+"/rpc", hub.Client())
	if err != nil {
		t.Fatalf("dial hub rpc: %v", err)
	}
	client := appwire.NewClient(transport)
	client.Start(context.Background())
	return client
}

func newHubRPCTestServer(t *testing.T, cfg WebConfig) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(nil)
	cfg.HubAddr = srv.Listener.Addr().String()
	srv.Config.Handler = NewWebServer(cfg).Handler()
	srv.Start()
	return srv
}
