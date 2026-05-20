package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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

func TestAppItemsFromReplayTurnConvertsCommunicateToAgentMessage(t *testing.T) {
	toolNames := map[string]string{}
	items := appItemsFromReplayTurn("turn_1", 1, replayTurn{
		Kind: "ASSISTANT",
		Message: replayMessage{Content: []replayPart{{
			Kind: "commandExecution",
			ToolCall: &replayToolCall{
				ID:        "call_1",
				Name:      "communicate",
				Arguments: []byte(`{"message":"done","await_reply":false}`),
			},
		}}},
	}, toolNames)

	if len(items) != 1 || items[0].Type != "agentMessage" || items[0].Text != "done" {
		t.Fatalf("communicate items=%+v", items)
	}

	results := appItemsFromReplayTurn("turn_2", 2, replayTurn{
		Kind: "TOOL_RESULTS",
		Message: replayMessage{Content: []replayPart{{
			Kind:       "tool_result",
			ToolResult: &replayToolResult{ToolCallID: "call_1", Content: `{"accepted":true}`},
		}}},
	}, toolNames)
	if len(results) != 0 {
		t.Fatalf("communicate tool results should be hidden, got %+v", results)
	}
}

func TestAppItemsFromReplayTurnAcceptsCurrentToolCallKind(t *testing.T) {
	toolNames := map[string]string{}
	items := appItemsFromReplayTurn("turn_1", 1, replayTurn{
		Kind: "ASSISTANT",
		Message: replayMessage{Content: []replayPart{{
			Kind: "tool_call",
			ToolCall: &replayToolCall{
				ID:        "call_read",
				Name:      "read_file",
				Arguments: []byte(`{"file_path":"/tmp/example.txt"}`),
			},
		}}},
	}, toolNames)
	if len(items) != 1 {
		t.Fatalf("items=%+v, want one tool item", items)
	}
	if got := items[0]; got.Type != "commandExecution" || got.CallID != "call_read" || !strings.Contains(got.ArgumentsJSON, "/tmp/example.txt") {
		t.Fatalf("tool item=%+v", got)
	}
}

func TestAppItemsFromReplayTurnSteeringCarriesImageMetadata(t *testing.T) {
	img := []byte("png")
	items := appItemsFromReplayTurn("turn_3", 3, replayTurn{
		Kind: "STEERING",
		Message: replayMessage{Content: []replayPart{{
			Kind: "image",
			Image: &replayImage{
				Data:      img,
				MediaType: "image/png",
				Name:      "steer.png",
			},
		}}},
	}, map[string]string{})

	if len(items) != 1 {
		t.Fatalf("items=%+v, want one steering item", items)
	}
	got := items[0]
	if got.Type != "steering" || got.Text != "[image]" || len(got.Images) != 1 {
		t.Fatalf("steering item=%+v, want image placeholder and image metadata", got)
	}
	if got.Images[0].Metadata["sha"] != imageSha(img) || got.Images[0].Metadata["size"] != strconv.Itoa(len(img)) {
		t.Fatalf("image metadata=%+v, want sha/size", got.Images[0].Metadata)
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
	roster := NewRoster(runDir, fakeProber{sessionID: "01NEW", status: appwire.ThreadStatusAwaiting})
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
	if len(resp.Data) != 1 || resp.Data[0].ID != sessionID || resp.Data[0].Status.Type != appwire.ThreadStatusNotLoaded {
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

func TestHubThreadListIncludesManagedCodexLaunchThreads(t *testing.T) {
	launcher := NewCodexLauncher([]CodexLaunchConfig{fakeCodexLaunchConfig("codex-managed", "ready")})
	defer shutdownCodexLauncher(t, launcher)
	cfg := WebConfig{
		Past:          NewPastIndex(""),
		CodexLaunches: []CodexLaunchConfig{fakeCodexLaunchConfig("codex-managed", "ready")},
		CodexLauncher: launcher,
	}
	sources := newHubSourceRegistry(cfg)

	resp, err := hubThreadList(context.Background(), cfg, sources, appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("hubThreadList: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].Serf.Ref != "codex-managed:th_fake" {
		t.Fatalf("threads=%+v", resp.Data)
	}
	if _, ok := sources.Source("codex-managed"); !ok {
		t.Fatal("managed Codex source was not registered")
	}
}

func TestHubThreadListDoesNotLaunchManagedCodexOutsideSourceFilter(t *testing.T) {
	launcher := NewCodexLauncher([]CodexLaunchConfig{fakeCodexLaunchConfig("codex-managed", "ready")})
	defer shutdownCodexLauncher(t, launcher)
	cfg := WebConfig{
		Past:          NewPastIndex(""),
		CodexLaunches: []CodexLaunchConfig{fakeCodexLaunchConfig("codex-managed", "ready")},
		CodexLauncher: launcher,
	}
	localThread := appwire.Thread{
		ID:        "01LOCAL",
		SessionID: "01LOCAL",
		Source:    "local",
		Preview:   "local thread",
		Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
		Serf:      appwire.SerfThread{Ref: "local:01LOCAL"},
	}
	sources := appsource.NewRegistry()
	sources.Add(&listThreadSource{id: "local", thread: localThread})

	resp, err := hubThreadList(context.Background(), cfg, sources, appwire.ThreadListParams{SourceIDs: []string{"local"}})
	if err != nil {
		t.Fatalf("hubThreadList: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].Serf.Ref != "local:01LOCAL" {
		t.Fatalf("threads=%+v", resp.Data)
	}
	if _, ok := sources.Source("codex-managed"); ok {
		t.Fatal("managed Codex source was registered despite local-only source filter")
	}
}

func TestHubThreadListReturnsManagedCodexLaunchErrorWhenSelectedSourceFails(t *testing.T) {
	launcher := NewCodexLauncher([]CodexLaunchConfig{fakeCodexLaunchConfig("codex-managed", "exit")})
	defer shutdownCodexLauncher(t, launcher)
	cfg := WebConfig{
		Past:          NewPastIndex(""),
		CodexLaunches: []CodexLaunchConfig{fakeCodexLaunchConfig("codex-managed", "exit")},
		CodexLauncher: launcher,
	}
	sources := newHubSourceRegistry(cfg)

	_, err := hubThreadList(context.Background(), cfg, sources, appwire.ThreadListParams{SourceIDs: []string{"codex-managed"}})
	assertHubLaunchError(t, err)
}

func TestHubThreadListContinuesWhenOptionalSourceFails(t *testing.T) {
	localThread := appwire.Thread{
		ID:        "01LOCAL",
		SessionID: "01LOCAL",
		Source:    "local",
		Preview:   "local thread",
		Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
		Serf:      appwire.SerfThread{Ref: "local:01LOCAL"},
	}
	sources := appsource.NewRegistry()
	sources.Add(&listThreadSource{id: "local", thread: localThread})
	sources.Add(&listThreadSource{id: "codex", listErr: errors.New("codex offline")})

	resp, err := hubThreadList(context.Background(), WebConfig{Past: NewPastIndex("")}, sources, appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("hubThreadList: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].Serf.Ref != "local:01LOCAL" {
		t.Fatalf("threads=%+v", resp.Data)
	}
}

func TestHubThreadListReturnsErrorWhenOnlySelectedSourceFails(t *testing.T) {
	sources := appsource.NewRegistry()
	sources.Add(&listThreadSource{id: "codex", listErr: errors.New("codex offline")})

	_, err := hubThreadList(context.Background(), WebConfig{Past: NewPastIndex("")}, sources, appwire.ThreadListParams{SourceIDs: []string{"codex"}})
	if err == nil || !strings.Contains(err.Error(), "codex offline") {
		t.Fatalf("hubThreadList error=%v, want codex offline", err)
	}
}

func TestHubThreadListReturnsErrorWhenAnySelectedSourceFails(t *testing.T) {
	localThread := appwire.Thread{
		ID:        "01LOCAL",
		SessionID: "01LOCAL",
		Source:    "local",
		Preview:   "local thread",
		Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
		Serf:      appwire.SerfThread{Ref: "local:01LOCAL"},
	}
	sources := appsource.NewRegistry()
	sources.Add(&listThreadSource{id: "local", thread: localThread})
	sources.Add(&listThreadSource{id: "codex", listErr: errors.New("codex offline")})

	_, err := hubThreadList(context.Background(), WebConfig{Past: NewPastIndex("")}, sources, appwire.ThreadListParams{SourceIDs: []string{"local", "codex"}})
	if err == nil || !strings.Contains(err.Error(), "codex offline") {
		t.Fatalf("hubThreadList error=%v, want codex offline", err)
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
		{ID: "02OLD", CreatedAt: updated.Add(-2 * time.Hour), UpdatedAt: updated, OriginalPrompt: "beta task"},
		{ID: "01NEW", CreatedAt: updated.Add(-time.Hour), UpdatedAt: updated, OriginalPrompt: "alpha task"},
		{ID: "04TITLEB", CreatedAt: updated.Add(-3 * time.Hour), UpdatedAt: updated.Add(-time.Hour), OriginalPrompt: "bravo task"},
		{ID: "03TITLEA", CreatedAt: updated.Add(-3 * time.Hour), UpdatedAt: updated.Add(-time.Hour), OriginalPrompt: "alpha task"},
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

func TestHubThreadListSearchMatchesProviderOnlyProfile(t *testing.T) {
	sources := appsource.NewRegistry()
	sources.Add(&listThreadSource{id: "codex-local", thread: appwire.Thread{
		ID:        "th_codex",
		SessionID: "th_codex",
		Source:    "codex-local",
		Preview:   "codex replay",
		Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusNotLoaded},
		Serf: appwire.SerfThread{
			Ref:     "codex-local:th_codex",
			Profile: "openai",
		},
	}})

	resp, err := hubThreadList(context.Background(), WebConfig{Past: NewPastIndex("")}, sources, appwire.ThreadListParams{SearchTerm: "openai"})
	if err != nil {
		t.Fatalf("hubThreadList: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].Serf.Ref != "codex-local:th_codex" {
		t.Fatalf("threads=%+v", resp.Data)
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
		ID:             "01LIVE",
		CreatedAt:      base.Add(-2 * time.Hour),
		UpdatedAt:      liveUpdated,
		OriginalPrompt: "live task",
	}); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveSessionMeta(stateDir, agent.SessionMeta{
		ID:             "02PAST",
		CreatedAt:      base.Add(-3 * time.Hour),
		UpdatedAt:      pastUpdated,
		OriginalPrompt: "past task",
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
	if got := resp.Thread.Turns[0].Items[0]; got.Type != "userMessage" || got.Text != "first task" {
		t.Fatalf("first item=%+v", got)
	}
	if got := resp.Thread.Turns[1].Items[0]; got.Type != "agentMessage" || got.Text != "first reply" {
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

func TestHubRPCThreadReadUsesStructuredAPICallDiagnostic(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "failed")
	sessionID := buildRPCStructuredFailedSession(t, stateDir)
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
	if failed.Error == nil {
		t.Fatalf("failed turn=%+v", failed)
	}
	if failed.Error.Source != string(diagnostic.SourceProvider) || failed.Error.Title != "Provider error" || failed.Error.Hint != "structured provider diagnostic" {
		t.Fatalf("failed turn diagnostic=%+v", failed.Error)
	}
}

func TestSanitizeStaleProcessingStatusFlipsFailedAPICallToError(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "stuck")
	sessionID := buildRPCFailedSession(t, stateDir) // tail = api_call with Error
	past := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	thread := appwire.Thread{
		ID:        sessionID,
		SessionID: sessionID,
		Source:    "local",
		Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusActive},
		Serf:      appwire.SerfThread{Ref: "local:" + sessionID},
	}
	out := sanitizeStaleProcessingStatus(WebConfig{Past: past}, thread)
	if out.Status.Type != appwire.ThreadStatusSystemError {
		t.Fatalf("status=%q want=%q", out.Status.Type, appwire.ThreadStatusSystemError)
	}
}

func TestSanitizeStaleProcessingStatusLeavesCompletedAssistantTailAlone(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "midflight")
	sessionID := buildRPCParentSession(t, stateDir)
	// buildRPCParentSession's tail is a USER_INPUT entry. Append an
	// ASSISTANT turn so the tail is a successful assistant message.
	transcriptPath := filepath.Join(stateDir, "sessions", sessionID+".transcript.jsonl")
	appendAssistantToTranscript(t, transcriptPath)
	past := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	thread := appwire.Thread{
		ID:        sessionID,
		SessionID: sessionID,
		Source:    "local",
		Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusActive},
		Serf:      appwire.SerfThread{Ref: "local:" + sessionID},
	}
	out := sanitizeStaleProcessingStatus(WebConfig{Past: past}, thread)
	if out.Status.Type != appwire.ThreadStatusActive {
		t.Fatalf("status=%q want=%q (mid-tool processing must be preserved)", out.Status.Type, appwire.ThreadStatusActive)
	}
}

func TestSanitizeStaleProcessingStatusLeavesUserInputTailAlone(t *testing.T) {
	// USER_INPUT with no api_call yet could mean the agent is genuinely
	// preparing the first LLM call. Conservatively leave processing as-is.
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "userin")
	sessionID := buildRPCParentSession(t, stateDir) // tail is USER_INPUT
	past := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	thread := appwire.Thread{
		ID:        sessionID,
		SessionID: sessionID,
		Source:    "local",
		Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusActive},
		Serf:      appwire.SerfThread{Ref: "local:" + sessionID},
	}
	out := sanitizeStaleProcessingStatus(WebConfig{Past: past}, thread)
	if out.Status.Type != appwire.ThreadStatusActive {
		t.Fatalf("status=%q want=%q (USER_INPUT tail is not a stuck signal)", out.Status.Type, appwire.ThreadStatusActive)
	}
}

func TestSanitizeStaleProcessingStatusIgnoresNonLocalSources(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "codex")
	sessionID := buildRPCFailedSession(t, stateDir)
	past := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	thread := appwire.Thread{
		ID:        sessionID,
		SessionID: sessionID,
		Source:    "codex",
		Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusActive},
		Serf:      appwire.SerfThread{Ref: "codex:" + sessionID},
	}
	out := sanitizeStaleProcessingStatus(WebConfig{Past: past}, thread)
	if out.Status.Type != appwire.ThreadStatusActive {
		t.Fatalf("status=%q want=%q (only local sessions get the override)", out.Status.Type, appwire.ThreadStatusActive)
	}
}

func TestSanitizeStaleProcessingStatusLeavesNonProcessingAlone(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "idle")
	sessionID := buildRPCFailedSession(t, stateDir)
	past := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	// Daemon legitimately reports idle / awaiting / error — we must not
	// rewrite those.
	for _, status := range []string{appwire.ThreadStatusIdle, appwire.ThreadStatusAwaiting, appwire.ThreadStatusSystemError, appwire.ThreadStatusNotLoaded} {
		thread := appwire.Thread{
			ID:        sessionID,
			SessionID: sessionID,
			Source:    "local",
			Status:    appwire.ThreadStatus{Type: status},
			Serf:      appwire.SerfThread{Ref: "local:" + sessionID},
		}
		out := sanitizeStaleProcessingStatus(WebConfig{Past: past}, thread)
		if out.Status.Type != status {
			t.Fatalf("status %q overwritten to %q", status, out.Status.Type)
		}
	}
}

func TestHubRPCThreadReadFlipsStuckProcessingToError(t *testing.T) {
	// End-to-end: daemon claims active, transcript tail is a failed
	// api_call. MethodThreadRead must report error so the hub UI doesn't
	// disable steer/send forever (kata r6y9).
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "stuck")
	sessionID := buildRPCFailedSession(t, stateDir)
	past := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        sessionID,
			SessionID: sessionID,
			Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusActive},
			Source:    "local",
			Serf:      appwire.SerfThread{Ref: params.Ref},
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
	roster := NewRoster(runDir, nil)
	roster.Refresh()

	hub := newHubRPCTestServer(t, WebConfig{RunDir: runDir, Roster: roster, Past: past})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:" + sessionID})
	if err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	if resp.Thread.Status.Type != appwire.ThreadStatusSystemError {
		t.Fatalf("status=%q want=%q", resp.Thread.Status.Type, appwire.ThreadStatusSystemError)
	}
}

func appendAssistantToTranscript(t *testing.T, path string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(`{"kind":"entry","seq":99,"turn":{"kind":"ASSISTANT","message":{"role":"assistant","content":[{"kind":"text","text":"hi back"}]}}}` + "\n"); err != nil {
		t.Fatal(err)
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
	if got := resp.Thread.Turns[0].Items[0]; got.Type != "userMessage" || got.Text != "first task" {
		t.Fatalf("first item=%+v", got)
	}
}

func TestHubRPCThreadReadDoesNotReturnLocalPastForNonLocalMissingSource(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "local")
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
	resp, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + sessionID, IncludeTurns: true})
	if err == nil {
		t.Fatalf("ThreadRead returned local past for codex ref: %+v", resp.Thread)
	}
}

func TestHubRPCThreadReadDoesNotMergeLocalPastIntoNonLocalLiveThread(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "local")
	sessionID := buildRPCParentSession(t, stateDir)
	past := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	source := &relayBroadcastSource{
		thread: appwire.Thread{
			ID:        sessionID,
			SessionID: sessionID,
			Source:    "codex",
			Preview:   "live codex thread",
			Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			Serf:      appwire.SerfThread{Ref: "codex:" + sessionID, Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 4),
		canceled:      make(chan struct{}, 1),
	}
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(WebConfig{HubAddr: srv.Listener.Addr().String(), Past: past})
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPC(t, srv)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + sessionID, IncludeTurns: true})
	if err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	if len(resp.Thread.Turns) != 0 {
		t.Fatalf("non-local live thread received local past turns: %+v", resp.Thread.Turns)
	}
	if resp.Thread.Preview != "live codex thread" || resp.Thread.Serf.Ref != "codex:"+sessionID {
		t.Fatalf("thread=%+v", resp.Thread)
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

func TestHubRPCThreadReadRelaysNotificationsBySourceQualifiedThread(t *testing.T) {
	threadID := "shared_thread"
	sourceA := &relayBroadcastSource{
		id: "codex-a",
		thread: appwire.Thread{
			ID:        threadID,
			SessionID: threadID,
			Source:    "codex-a",
			Serf:      appwire.SerfThread{Ref: "codex-a:" + threadID, Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 4),
		canceled:      make(chan struct{}, 2),
	}
	sourceB := &relayBroadcastSource{
		id: "codex-b",
		thread: appwire.Thread{
			ID:        threadID,
			SessionID: threadID,
			Source:    "codex-b",
			Serf:      appwire.SerfThread{Ref: "codex-b:" + threadID, Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 4),
		canceled:      make(chan struct{}, 2),
	}
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(WebConfig{HubAddr: srv.Listener.Addr().String(), Past: NewPastIndex("")})
	web.sources.Add(sourceA)
	web.sources.Add(sourceB)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	clientA := dialHubRPC(t, srv)
	defer clientA.Close()
	if _, err := clientA.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize clientA: %v", err)
	}
	if _, err := clientA.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex-a:" + threadID}); err != nil {
		t.Fatalf("ThreadRead clientA: %v", err)
	}
	clientB := dialHubRPC(t, srv)
	defer clientB.Close()
	if _, err := clientB.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize clientB: %v", err)
	}
	if _, err := clientB.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex-b:" + threadID}); err != nil {
		t.Fatalf("ThreadRead clientB: %v", err)
	}

	sourceB.notifications <- appwire.Notification{
		Method: appwire.NotifyAgentMessageDelta,
		Params: testRawJSON(t, appwire.AgentMessageDeltaParams{
			ThreadID: threadID,
			Ref:      "codex-b:" + threadID,
			TurnID:   "turn_1",
			ItemID:   "item_1",
			Delta:    "from source b",
		}),
	}

	select {
	case got := <-clientB.Notifications():
		if got.Method != appwire.NotifyAgentMessageDelta {
			t.Fatalf("clientB method=%q", got.Method)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for source b notification")
	}
	select {
	case got := <-clientA.Notifications():
		t.Fatalf("clientA received cross-source notification: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHubRPCThreadReadSubscribeOverridesSourceReadRelayPolicy(t *testing.T) {
	threadID := "th_codex_live"
	source := &readRelayDisabledSource{
		relayBroadcastSource: relayBroadcastSource{
			id: "codex",
			thread: appwire.Thread{
				ID:        threadID,
				SessionID: threadID,
				Source:    "codex",
				Serf:      appwire.SerfThread{Ref: "codex:" + threadID, Capabilities: appwire.ThreadCapabilities{Send: true}},
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
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID, Subscribe: true}); err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	expectRelaySubscription(t, source.subscribed)

	source.notifications <- appwire.Notification{
		Method: appwire.NotifyAgentMessageDelta,
		Params: testRawJSON(t, appwire.AgentMessageDeltaParams{
			ThreadID: threadID,
			Ref:      "codex:" + threadID,
			TurnID:   "turn_1",
			ItemID:   "item_1",
			Delta:    "from codex",
		}),
	}

	select {
	case got := <-client.Notifications():
		if got.Method != appwire.NotifyAgentMessageDelta {
			t.Fatalf("method=%q", got.Method)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for subscribed codex notification")
	}
}

func TestHubRPCThreadReadReplaceSubscriptionDropsPreviousRelaySubscriber(t *testing.T) {
	sourceA := &relayBroadcastSource{
		id: "codex-a",
		thread: appwire.Thread{
			ID:        "th_a",
			SessionID: "th_a",
			Source:    "codex-a",
			Serf:      appwire.SerfThread{Ref: "codex-a:th_a", Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 4),
		subscribed:    make(chan struct{}, 1),
		canceled:      make(chan struct{}, 1),
	}
	sourceB := &relayBroadcastSource{
		id: "codex-b",
		thread: appwire.Thread{
			ID:        "th_b",
			SessionID: "th_b",
			Source:    "codex-b",
			Serf:      appwire.SerfThread{Ref: "codex-b:th_b", Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 4),
		subscribed:    make(chan struct{}, 1),
		canceled:      make(chan struct{}, 1),
	}
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(WebConfig{HubAddr: srv.Listener.Addr().String(), Past: NewPastIndex("")})
	web.sources.Add(sourceA)
	web.sources.Add(sourceB)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPC(t, srv)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex-a:th_a", Subscribe: true, ReplaceSubscription: true}); err != nil {
		t.Fatalf("ThreadRead sourceA: %v", err)
	}
	expectRelaySubscription(t, sourceA.subscribed)
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex-b:th_b", Subscribe: true, ReplaceSubscription: true}); err != nil {
		t.Fatalf("ThreadRead sourceB: %v", err)
	}
	expectRelaySubscription(t, sourceB.subscribed)

	sourceA.notifications <- appwire.Notification{
		Method: appwire.NotifyAgentMessageDelta,
		Params: testRawJSON(t, appwire.AgentMessageDeltaParams{
			ThreadID: "th_a",
			Ref:      "codex-a:th_a",
			TurnID:   "turn_a",
			ItemID:   "item_a",
			Delta:    "from source a",
		}),
	}
	select {
	case got := <-client.Notifications():
		t.Fatalf("client received notification for replaced subscription: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}

	sourceB.notifications <- appwire.Notification{
		Method: appwire.NotifyAgentMessageDelta,
		Params: testRawJSON(t, appwire.AgentMessageDeltaParams{
			ThreadID: "th_b",
			Ref:      "codex-b:th_b",
			TurnID:   "turn_b",
			ItemID:   "item_b",
			Delta:    "from source b",
		}),
	}
	select {
	case got := <-client.Notifications():
		if got.Method != appwire.NotifyAgentMessageDelta {
			t.Fatalf("notification method=%q", got.Method)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for active replacement subscription notification")
	}
	select {
	case <-sourceA.canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("replaced relay subscriber did not retire the old source relay")
	}
}

func TestHubRPCThreadReadRetiresRelayWhenClientDisconnects(t *testing.T) {
	source := &relayLifecycleSource{
		thread: appwire.Thread{
			ID:        "th_1",
			SessionID: "th_1",
			Source:    "codex",
			Serf:      appwire.SerfThread{Ref: "codex:th_1", Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		canceled: make(chan struct{}),
	}
	srv := httptest.NewUnstartedServer(nil)
	cfg := WebConfig{HubAddr: srv.Listener.Addr().String(), Past: NewPastIndex("")}
	web := NewWebServer(cfg)
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPC(t, srv)
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:th_1"}); err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("client close: %v", err)
	}

	select {
	case <-source.canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("source relay context was not canceled after client disconnect")
	}
}

func TestHubRPCThreadReadKeepsRelayWhenSubscriberArrivesDuringIdleRetirement(t *testing.T) {
	source := &relayBroadcastSource{
		thread: appwire.Thread{
			ID:        "th_1",
			SessionID: "th_1",
			Source:    "codex",
			Serf:      appwire.SerfThread{Ref: "codex:th_1", Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 4),
		canceled:      make(chan struct{}, 2),
	}
	srv := httptest.NewUnstartedServer(nil)
	cfg := WebConfig{HubAddr: srv.Listener.Addr().String(), Past: NewPastIndex("")}
	web := NewWebServer(cfg)
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	idleReached := make(chan struct{})
	releaseIdle := make(chan struct{})
	var idleOnce sync.Once
	oldHook := hubRelayIdleExitHook
	hubRelayIdleExitHook = func(threadID string) {
		if threadID != "th_1" {
			return
		}
		idleOnce.Do(func() { close(idleReached) })
		<-releaseIdle
	}
	t.Cleanup(func() { hubRelayIdleExitHook = oldHook })

	client1 := dialHubRPC(t, srv)
	if _, err := client1.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize client1: %v", err)
	}
	if _, err := client1.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:th_1"}); err != nil {
		t.Fatalf("ThreadRead client1: %v", err)
	}
	if err := client1.Close(); err != nil {
		t.Fatalf("client1 close: %v", err)
	}

	select {
	case <-idleReached:
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not reach idle retirement window")
	}

	client2 := dialHubRPC(t, srv)
	defer client2.Close()
	if _, err := client2.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize client2: %v", err)
	}
	if _, err := client2.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:th_1"}); err != nil {
		t.Fatalf("ThreadRead client2: %v", err)
	}
	close(releaseIdle)

	source.notifications <- appwire.Notification{
		Method: appwire.NotifyAgentMessageDelta,
		Params: testRawJSON(t, appwire.AgentMessageDeltaParams{
			ThreadID: "th_1",
			Ref:      "codex:th_1",
			TurnID:   "turn_1",
			ItemID:   "item_1",
			Delta:    "still live",
		}),
	}

	select {
	case got := <-client2.Notifications():
		if got.Method != appwire.NotifyAgentMessageDelta {
			t.Fatalf("method=%q", got.Method)
		}
	case <-source.canceled:
		t.Fatal("relay was canceled despite a new subscriber")
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for notification after idle-race subscriber")
	}
}

func TestHubRPCThreadReadKeepsReplacementRelayTrackedAfterIdleCleanup(t *testing.T) {
	const threadID = "th_cleanup"
	source := &relayBroadcastSource{
		thread: appwire.Thread{
			ID:        threadID,
			SessionID: threadID,
			Source:    "codex",
			Serf:      appwire.SerfThread{Ref: "codex:" + threadID, Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 4),
		subscribed:    make(chan struct{}, 4),
		canceled:      make(chan struct{}, 2),
	}
	srv := httptest.NewUnstartedServer(nil)
	cfg := WebConfig{HubAddr: srv.Listener.Addr().String(), Past: NewPastIndex("")}
	web := NewWebServer(cfg)
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	afterIdleDelete := make(chan struct{})
	releaseCleanup := make(chan struct{})
	var idleOnce sync.Once
	oldHook := hubRelayAfterIdleDeleteHook
	hubRelayAfterIdleDeleteHook = func(threadID string) {
		if threadID != "th_cleanup" {
			return
		}
		idleOnce.Do(func() { close(afterIdleDelete) })
		<-releaseCleanup
	}
	t.Cleanup(func() { hubRelayAfterIdleDeleteHook = oldHook })

	client1 := dialHubRPC(t, srv)
	if _, err := client1.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize client1: %v", err)
	}
	if _, err := client1.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID}); err != nil {
		t.Fatalf("ThreadRead client1: %v", err)
	}
	expectRelaySubscription(t, source.subscribed)
	if err := client1.Close(); err != nil {
		t.Fatalf("client1 close: %v", err)
	}

	select {
	case <-afterIdleDelete:
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not reach post-delete cleanup window")
	}

	client2 := dialHubRPC(t, srv)
	defer client2.Close()
	if _, err := client2.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize client2: %v", err)
	}
	if _, err := client2.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID}); err != nil {
		t.Fatalf("ThreadRead client2: %v", err)
	}
	expectRelaySubscription(t, source.subscribed)
	close(releaseCleanup)
	time.Sleep(50 * time.Millisecond)
	drainRelaySubscriptions(source.subscribed)

	if _, err := client2.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID}); err != nil {
		t.Fatalf("ThreadRead client2 again: %v", err)
	}
	select {
	case <-source.subscribed:
		t.Fatal("second read started a duplicate replacement relay")
	default:
	}
}

func TestHubRPCThreadReadPropagatesInFlightRelaySubscribeFailure(t *testing.T) {
	thread := appwire.Thread{
		ID:        "th_subscribe_fail",
		SessionID: "th_subscribe_fail",
		Source:    "codex",
		Serf:      appwire.SerfThread{Ref: "codex:th_subscribe_fail", Capabilities: appwire.ThreadCapabilities{Send: true}},
	}
	source := &blockingFailingRelaySource{
		relayLifecycleSource: relayLifecycleSource{thread: thread},
		started:              make(chan struct{}),
		release:              make(chan struct{}),
	}
	srv := httptest.NewUnstartedServer(nil)
	cfg := WebConfig{HubAddr: srv.Listener.Addr().String(), Past: NewPastIndex("")}
	web := NewWebServer(cfg)
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client1 := dialHubRPC(t, srv)
	defer client1.Close()
	if _, err := client1.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize client1: %v", err)
	}
	client2 := dialHubRPC(t, srv)
	defer client2.Close()
	if _, err := client2.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize client2: %v", err)
	}

	readErrs := make(chan error, 2)
	go func() {
		_, err := client1.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:th_subscribe_fail"})
		readErrs <- err
	}()
	select {
	case <-source.started:
	case <-time.After(time.Second):
		t.Fatal("first relay subscribe did not start")
	}

	go func() {
		_, err := client2.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:th_subscribe_fail"})
		readErrs <- err
	}()
	select {
	case err := <-readErrs:
		t.Fatalf("concurrent read returned before relay subscribe failed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(source.release)

	for i := 0; i < 2; i++ {
		select {
		case err := <-readErrs:
			if err == nil || !strings.Contains(err.Error(), "subscribe failed") {
				t.Fatalf("read error=%v, want subscribe failed", err)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for relay subscribe error")
		}
	}
	if calls := source.subscribeCalls(); calls != 1 {
		t.Fatalf("subscribe calls=%d, want 1", calls)
	}
}

func TestHubRPCThreadReadSubscribeFailureDoesNotLeaveClientSubscribed(t *testing.T) {
	threadID := "th_retry_subscribe"
	source := &failFirstRelaySource{
		relayBroadcastSource: relayBroadcastSource{
			thread: appwire.Thread{
				ID:        threadID,
				SessionID: threadID,
				Source:    "codex",
				Serf:      appwire.SerfThread{Ref: "codex:" + threadID, Capabilities: appwire.ThreadCapabilities{Send: true}},
			},
			notifications: make(chan appwire.Notification, 4),
			canceled:      make(chan struct{}, 2),
		},
	}
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(WebConfig{HubAddr: srv.Listener.Addr().String(), Past: NewPastIndex("")})
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	failedClient := dialHubRPC(t, srv)
	defer failedClient.Close()
	if _, err := failedClient.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize failedClient: %v", err)
	}
	if _, err := failedClient.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID}); err == nil || !strings.Contains(err.Error(), "subscribe failed once") {
		t.Fatalf("ThreadRead failedClient error=%v, want subscribe failed once", err)
	}

	okClient := dialHubRPC(t, srv)
	defer okClient.Close()
	if _, err := okClient.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize okClient: %v", err)
	}
	if _, err := okClient.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID}); err != nil {
		t.Fatalf("ThreadRead okClient: %v", err)
	}
	source.notifications <- appwire.Notification{
		Method: appwire.NotifyAgentMessageDelta,
		Params: testRawJSON(t, appwire.AgentMessageDeltaParams{
			ThreadID: threadID,
			Ref:      "codex:" + threadID,
			TurnID:   "turn_1",
			ItemID:   "item_1",
			Delta:    "after retry",
		}),
	}
	select {
	case got := <-okClient.Notifications():
		if got.Method != appwire.NotifyAgentMessageDelta {
			t.Fatalf("okClient method=%q", got.Method)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for retry relay notification")
	}
	select {
	case got := <-failedClient.Notifications():
		t.Fatalf("failed client received stale relay notification: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHubThreadListKeepsLocalPastWhenNonLocalLiveIDCollides(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "local")
	sessionID := buildRPCParentSession(t, stateDir)
	past := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	sources := appsource.NewRegistry()
	sources.Add(&relayBroadcastSource{
		thread: appwire.Thread{
			ID:        sessionID,
			SessionID: sessionID,
			Source:    "codex",
			Preview:   "live codex thread",
			Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			Serf:      appwire.SerfThread{Ref: "codex:" + sessionID},
		},
	})

	resp, err := hubThreadList(context.Background(), WebConfig{Past: past}, sources, appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("hubThreadList: %v", err)
	}
	var foundLocalPast, foundCodexLive bool
	for _, thread := range resp.Data {
		switch thread.Serf.Ref {
		case "local:" + sessionID:
			foundLocalPast = true
		case "codex:" + sessionID:
			foundCodexLive = true
		}
	}
	if !foundLocalPast || !foundCodexLive {
		t.Fatalf("found local past=%v codex live=%v threads=%+v", foundLocalPast, foundCodexLive, resp.Data)
	}
}

func TestHubThreadListMatchesCodexNativeStatusFilters(t *testing.T) {
	sources := appsource.NewRegistry()
	sources.Add(&listThreadSource{id: "codex", thread: appwire.Thread{
		ID:        "th_codex",
		SessionID: "th_codex",
		Source:    "codex",
		Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusActive},
		Serf:      appwire.SerfThread{Ref: "codex:th_codex"},
	}})

	resp, err := hubThreadList(context.Background(), WebConfig{Past: NewPastIndex("")}, sources, appwire.ThreadListParams{
		Statuses: []string{"active"},
	})
	if err != nil {
		t.Fatalf("hubThreadList: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].Serf.Ref != "codex:th_codex" {
		t.Fatalf("threads=%+v", resp.Data)
	}
}

type relayLifecycleSource struct {
	thread   appwire.Thread
	canceled chan struct{}
}

type listThreadSource struct {
	relayLifecycleSource
	id      string
	thread  appwire.Thread
	listErr error
}

func (s *listThreadSource) ID() string { return s.id }

func (s *listThreadSource) ListThreads(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	if s.listErr != nil {
		return appwire.ThreadListResponse{}, s.listErr
	}
	return appwire.ThreadListResponse{Data: []appwire.Thread{s.thread}}, nil
}

func (s *relayLifecycleSource) ID() string { return "codex" }

func (s *relayLifecycleSource) ListThreads(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	return appwire.ThreadListResponse{Data: []appwire.Thread{s.thread}}, nil
}

func (s *relayLifecycleSource) ReadThread(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	return appwire.ThreadReadResponse{Thread: s.thread}, nil
}

func (s *relayLifecycleSource) StartThread(context.Context, appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
	return appwire.ThreadStartResponse{}, appwire.Unavailable("relay lifecycle source does not start threads")
}

func (s *relayLifecycleSource) ResumeThread(context.Context, appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
	return appwire.ThreadResumeResponse{}, appwire.Unavailable("relay lifecycle source does not resume threads")
}

func (s *relayLifecycleSource) ForkThread(context.Context, appwire.ThreadForkParams) (appwire.ThreadForkResponse, error) {
	return appwire.ThreadForkResponse{}, appwire.Unavailable("relay lifecycle source does not fork threads")
}

func (s *relayLifecycleSource) StartTurn(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
	return appwire.TurnStartResponse{}, appwire.Unavailable("relay lifecycle source does not start turns")
}

func (s *relayLifecycleSource) SteerTurn(context.Context, appwire.TurnSteerParams) error {
	return appwire.Unavailable("relay lifecycle source does not steer turns")
}

func (s *relayLifecycleSource) InterruptTurn(context.Context, appwire.TurnInterruptParams) error {
	return appwire.Unavailable("relay lifecycle source does not interrupt turns")
}

func (s *relayLifecycleSource) QueueTurn(context.Context, appwire.TurnQueueParams) error {
	return appwire.Unavailable("relay lifecycle source does not queue turns")
}

func (s *relayLifecycleSource) DrainAsSteer(context.Context, appwire.TurnDrainAsSteerParams) error {
	return appwire.Unavailable("relay lifecycle source does not drain as steer")
}

func (s *relayLifecycleSource) CompactThread(context.Context, appwire.ThreadCompactStartParams) error {
	return appwire.Unavailable("relay lifecycle source does not compact threads")
}

func (s *relayLifecycleSource) ShutdownThread(context.Context, appwire.ThreadShutdownParams) error {
	return appwire.Unavailable("relay lifecycle source does not shut down threads")
}

func (s *relayLifecycleSource) SetThreadModel(context.Context, appwire.ThreadModelSetParams) error {
	return appwire.Unavailable("relay lifecycle source does not set models")
}

func (s *relayLifecycleSource) ClearThread(context.Context, appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
	return appwire.ThreadClearResponse{}, appwire.Unavailable("relay lifecycle source does not clear threads")
}

func (s *relayLifecycleSource) ListModels(context.Context, appwire.ModelListParams) (appwire.ModelListResponse, error) {
	return appwire.ModelListResponse{}, appwire.Unavailable("relay lifecycle source does not list models")
}

func (s *relayLifecycleSource) ListTasks(context.Context, appwire.TaskListParams) (appwire.TaskListResponse, error) {
	return appwire.TaskListResponse{}, appwire.Unavailable("relay lifecycle source does not list tasks")
}

func (s *relayLifecycleSource) SubscribeThread(ctx context.Context, _ appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	out := make(chan appwire.Notification)
	go func() {
		defer close(out)
		<-ctx.Done()
		close(s.canceled)
	}()
	return out, nil
}

type relayBroadcastSource struct {
	relayLifecycleSource
	id            string
	thread        appwire.Thread
	notifications chan appwire.Notification
	subscribed    chan struct{}
	canceled      chan struct{}
}

type readRelayDisabledSource struct {
	relayBroadcastSource
}

func (s *readRelayDisabledSource) RelayOnThreadRead() bool {
	return false
}

type blockingFailingRelaySource struct {
	relayLifecycleSource
	mu      sync.Mutex
	calls   int
	once    sync.Once
	started chan struct{}
	release chan struct{}
}

type failFirstRelaySource struct {
	relayBroadcastSource
	mu    sync.Mutex
	calls int
}

type resumeAfterSubscribeUnavailableSource struct {
	relayBroadcastSource
	mu          sync.Mutex
	calls       int
	startPrompt string
}

func inputTextForTest(input []appwire.InputItem) string {
	for _, item := range input {
		if item.Text != "" {
			return item.Text
		}
	}
	return ""
}

func (s *resumeAfterSubscribeUnavailableSource) ID() string { return "local" }

func (s *resumeAfterSubscribeUnavailableSource) SubscribeThread(ctx context.Context, params appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	s.mu.Lock()
	s.calls++
	calls := s.calls
	s.mu.Unlock()
	if calls == 1 {
		return nil, appwire.SessionUnavailable("stale relay subscription")
	}
	return s.relayBroadcastSource.SubscribeThread(ctx, params)
}

func (s *resumeAfterSubscribeUnavailableSource) StartTurn(_ context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
	s.mu.Lock()
	s.startPrompt = inputTextForTest(params.Input)
	s.mu.Unlock()
	return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_resumed"}}, nil
}

func (s *resumeAfterSubscribeUnavailableSource) subscribeCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *resumeAfterSubscribeUnavailableSource) lastStartPrompt() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startPrompt
}

func (s *failFirstRelaySource) SubscribeThread(ctx context.Context, params appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	s.mu.Lock()
	s.calls++
	calls := s.calls
	s.mu.Unlock()
	if calls == 1 {
		return nil, errors.New("subscribe failed once")
	}
	return s.relayBroadcastSource.SubscribeThread(ctx, params)
}

func (s *blockingFailingRelaySource) SubscribeThread(ctx context.Context, _ appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.release:
		return nil, errors.New("subscribe failed")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *blockingFailingRelaySource) subscribeCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *relayBroadcastSource) ID() string {
	if s.id != "" {
		return s.id
	}
	return "codex"
}

func (s *relayBroadcastSource) ListThreads(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	return appwire.ThreadListResponse{Data: []appwire.Thread{s.thread}}, nil
}

func (s *relayBroadcastSource) ReadThread(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	return appwire.ThreadReadResponse{Thread: s.thread}, nil
}

func (s *relayBroadcastSource) SubscribeThread(ctx context.Context, _ appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	out := make(chan appwire.Notification, 4)
	if s.subscribed != nil {
		s.subscribed <- struct{}{}
	}
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				select {
				case s.canceled <- struct{}{}:
				default:
				}
				return
			case notification := <-s.notifications:
				select {
				case out <- notification:
				case <-ctx.Done():
					select {
					case s.canceled <- struct{}{}:
					default:
					}
					return
				}
			}
		}
	}()
	return out, nil
}

type startResumeRelaySource struct {
	relayBroadcastSource
}

func (s *startResumeRelaySource) StartThread(context.Context, appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
	return appwire.ThreadStartResponse{
		Thread: s.thread,
		Turn:   appwire.Turn{ID: "turn_start"},
	}, nil
}

func (s *startResumeRelaySource) ResumeThread(context.Context, appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
	return appwire.ThreadResumeResponse{Thread: s.thread}, nil
}

type startRelayFailureSource struct {
	startResumeRelaySource
}

func (s *startRelayFailureSource) SubscribeThread(context.Context, appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	return nil, errors.New("subscribe failed after start")
}

type forkingRelaySource struct {
	relayBroadcastSource
	forkCalled bool
	forkParams appwire.ThreadForkParams
	response   appwire.ThreadForkResponse
}

func (s *forkingRelaySource) ForkThread(_ context.Context, params appwire.ThreadForkParams) (appwire.ThreadForkResponse, error) {
	s.forkCalled = true
	s.forkParams = params
	return s.response, nil
}

func expectRelaySubscription(t *testing.T, subscribed <-chan struct{}) {
	t.Helper()
	select {
	case <-subscribed:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for relay subscription")
	}
}

func drainRelaySubscriptions(subscribed <-chan struct{}) {
	for {
		select {
		case <-subscribed:
		default:
			return
		}
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

func TestHubRPCModelListUsesSerfLaunchContractWhenDaemonFails(t *testing.T) {
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

	spawner := &fakeRPCSpawner{
		launchModels: func(context.Context) ([]appwire.ModelDescriptor, error) {
			return []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5.5"}}, nil
		},
	}
	hub := newHubRPCTestServer(t, WebConfig{
		RunDir:  runDir,
		Roster:  roster,
		Spawner: spawner,
		Models:  []modelDescriptor{{Provider: "openai", Model: "gpt-stale"}},
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
	if len(resp.Data) != 1 || resp.Data[0].Provider != "openai" || resp.Data[0].Model != "gpt-5.5" {
		t.Fatalf("models=%+v", resp.Data)
	}
}

func TestHubRPCModelListFallsBackToLocalDaemonWithoutLaunchContract(t *testing.T) {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodModelList, func(context.Context, appwire.ModelListParams) (appwire.ModelListResponse, error) {
		return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-daemon"}}}, nil
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
		SessionID: "th_1",
	})
	roster := NewRoster(runDir, nil)
	roster.Refresh()

	hub := newHubRPCTestServer(t, WebConfig{RunDir: runDir, Roster: roster})
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
	if len(resp.Data) != 1 || resp.Data[0].Provider != "openai" || resp.Data[0].Model != "gpt-daemon" {
		t.Fatalf("models=%+v", resp.Data)
	}
}

func TestHubRPCModelListPrefersSerfLaunchContract(t *testing.T) {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodModelList, func(context.Context, appwire.ModelListParams) (appwire.ModelListResponse, error) {
		return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-stale"}}}, nil
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
		SessionID: "th_1",
	})
	roster := NewRoster(runDir, nil)
	roster.Refresh()

	spawner := &fakeRPCSpawner{
		launchModels: func(context.Context) ([]appwire.ModelDescriptor, error) {
			return []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5.5"}}, nil
		},
	}
	hub := newHubRPCTestServer(t, WebConfig{RunDir: runDir, Roster: roster, Spawner: spawner})
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
	if len(resp.Data) != 1 || resp.Data[0].Provider != "openai" || resp.Data[0].Model != "gpt-5.5" {
		t.Fatalf("models=%+v", resp.Data)
	}
}

func TestHubRPCModelListUsesWorkingDirForSerfLaunchContract(t *testing.T) {
	spawner := &fakeRPCWorkingDirModelContractSpawner{
		fallback: appwire.ModelListResponse{
			Data: []appwire.ModelDescriptor{{Provider: "stale", Model: "wrong"}},
		},
		contractForWorkingDir: func(_ context.Context, cwd string) (appwire.ModelListResponse, error) {
			if cwd != "/tmp/project-with-oauth" {
				return appwire.ModelListResponse{}, fmt.Errorf("cwd=%q, want /tmp/project-with-oauth", cwd)
			}
			return appwire.ModelListResponse{
				Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-visible-to-agent"}},
			}, nil
		},
	}
	hub := newHubRPCTestServer(t, WebConfig{Spawner: spawner})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ModelList(context.Background(), appwire.ModelListParams{CWD: "/tmp/project-with-oauth"})
	if err != nil {
		t.Fatalf("ModelList: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].Provider != "openai" || resp.Data[0].Model != "gpt-visible-to-agent" {
		t.Fatalf("models=%+v", resp.Data)
	}
}

func TestHubRPCModelListRoutesCodexHarnessToSource(t *testing.T) {
	codex := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex-local"})
	var gotParams appwire.ModelListParams
	appserver.HandleTyped(codex.Router(), appwire.MethodModelList, func(_ context.Context, params appwire.ModelListParams) (appwire.ModelListResponse, error) {
		gotParams = params
		return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "codex-local", Model: "gpt-5.3-codex"}}}, nil
	})
	codexHTTP := httptest.NewServer(http.HandlerFunc(codex.ServeWebSocket))
	defer codexHTTP.Close()

	hub := newHubRPCTestServer(t, WebConfig{
		CodexSources: []appsource.CodexSourceConfig{{
			ID:       "codex-local",
			Endpoint: "ws" + codexHTTP.URL[len("http"):],
		}},
		Spawner: &fakeRPCModelContractSpawner{
			contract: appwire.ModelListResponse{
				Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5.5"}},
			},
		},
	})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ModelList(context.Background(), appwire.ModelListParams{Harness: "codex-local"})
	if err != nil {
		t.Fatalf("ModelList: %v", err)
	}
	if gotParams.Harness != "" {
		t.Fatalf("codex source received hub harness routing field: %+v", gotParams)
	}
	if len(resp.Data) != 1 || resp.Data[0].Provider != "codex-local" || resp.Data[0].Model != "gpt-5.3-codex" {
		t.Fatalf("models=%+v", resp.Data)
	}
}

func TestHubRPCModelListDoesNotUseLocalDaemonWhenLaunchContractIsEmpty(t *testing.T) {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodModelList, func(context.Context, appwire.ModelListParams) (appwire.ModelListResponse, error) {
		return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-daemon"}}}, nil
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
		SessionID: "th_1",
	})
	roster := NewRoster(runDir, nil)
	roster.Refresh()

	hub := newHubRPCTestServer(t, WebConfig{
		RunDir:  runDir,
		Roster:  roster,
		Spawner: &fakeRPCModelContractSpawner{contract: appwire.ModelListResponse{}},
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
	if len(resp.Data) != 0 {
		t.Fatalf("resp=%+v", resp)
	}
}

func TestHubRPCModelListDoesNotUseLocalDaemonWhenLaunchContractHasOnlyDiagnostics(t *testing.T) {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodModelList, func(context.Context, appwire.ModelListParams) (appwire.ModelListResponse, error) {
		return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-daemon"}}}, nil
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
		SessionID: "th_1",
	})
	roster := NewRoster(runDir, nil)
	roster.Refresh()

	spawner := &fakeRPCModelContractSpawner{
		contract: appwire.ModelListResponse{
			Diagnostics: []appwire.ModelListDiagnostic{{
				Provider: "openai",
				Source:   "provider",
				Title:    "Provider error",
				Message:  "HTTP 403",
			}},
		},
	}
	hub := newHubRPCTestServer(t, WebConfig{RunDir: runDir, Roster: roster, Spawner: spawner})
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
	if len(resp.Data) != 0 {
		t.Fatalf("models=%+v", resp.Data)
	}
	if len(resp.Diagnostics) != 1 || resp.Diagnostics[0].Provider != "openai" || !strings.Contains(resp.Diagnostics[0].Message, "403") {
		t.Fatalf("diagnostics=%+v", resp.Diagnostics)
	}
}

func TestHubRPCModelListReportsSerfLaunchDiagnostics(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-serf")
	script := `#!/bin/sh
if [ "$1" = "launch-check" ]; then
  printf '{"protocol":"serf-appwire-v1","models":[{"provider":"ollama","model":"local"}],"diagnostics":[{"provider":"openai","source":"provider","title":"Provider error","message":"HTTP 403"}]}\n'
  exit 0
fi
exit 2
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	hub := newHubRPCTestServer(t, WebConfig{
		RunDir:  t.TempDir(),
		Spawner: &HubSpawner{Cfg: DefaultConfig(), SerfBinary: bin, RunDir: t.TempDir(), HubToken: "generated-token"},
		Past:    NewPastIndex(""),
	})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	var resp struct {
		Data []struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
		} `json:"data"`
		Diagnostics []struct {
			Provider string `json:"provider"`
			Source   string `json:"source"`
			Title    string `json:"title"`
			Message  string `json:"message"`
		} `json:"diagnostics"`
	}
	if err := client.Request(context.Background(), appwire.MethodModelList, appwire.ModelListParams{}, &resp); err != nil {
		t.Fatalf("model/list: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].Provider != "ollama" || resp.Data[0].Model != "local" {
		t.Fatalf("models=%+v", resp.Data)
	}
	if len(resp.Diagnostics) != 1 || resp.Diagnostics[0].Provider != "openai" || resp.Diagnostics[0].Source != "provider" || !strings.Contains(resp.Diagnostics[0].Message, "403") {
		t.Fatalf("diagnostics=%+v", resp.Diagnostics)
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
	if got.Resolved.Effective.Model != "openrouter/deepseek/deepseek-v4-flash" {
		t.Fatalf("spawn model=%q, want openrouter/deepseek/deepseek-v4-flash", got.Resolved.Effective.Model)
	}
}

func TestHubRPCThreadStartRejectsModelOutsideSerfLaunchContractBeforeSpawn(t *testing.T) {
	runDir := t.TempDir()
	var spawnCalled bool
	spawner := &fakeRPCSpawner{
		spawn: func(_ context.Context, req SpawnRequest) (rendezvous.Entry, error) {
			spawnCalled = true
			return rendezvous.Entry{}, nil
		},
		launchModels: func(context.Context) ([]appwire.ModelDescriptor, error) {
			return []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5"}}, nil
		},
	}
	hub := newHubRPCTestServer(t, WebConfig{
		RunDir:  runDir,
		Spawner: spawner,
		Past:    NewPastIndex(""),
	})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	_, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{
		ModelProvider: "openai",
		Model:         "gpt-stale",
		CWD:           "/tmp",
	})
	assertHubLaunchError(t, err)
	if spawnCalled {
		t.Fatal("spawn was called for a model outside the Serf launch contract")
	}
}

func TestHubRPCThreadStartAllowsModelWhenProviderDoesNotEnumerateLaunchModels(t *testing.T) {
	runDir := t.TempDir()
	var got SpawnRequest
	spawner := &fakeRPCModelContractSpawner{
		contract: appwire.ModelListResponse{
			Data: []appwire.ModelDescriptor{{Provider: "ollama", Model: "local"}},
			Diagnostics: []appwire.ModelListDiagnostic{{
				Provider: "openai",
				Source:   "provider",
				Title:    "Provider error",
				Message:  "HTTP 403",
			}},
		},
	}
	spawner.spawn = func(_ context.Context, req SpawnRequest) (rendezvous.Entry, error) {
		got = req
		return rendezvous.Entry{
			PID:       107,
			Protocol:  appwire.ProtocolVersion,
			SourceID:  "local",
			ThreadID:  "th_non_enumerable_model",
			SessionID: "sess_non_enumerable_model",
		}, nil
	}
	hub := newHubRPCTestServer(t, WebConfig{RunDir: runDir, Spawner: spawner, Past: NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{
		ModelProvider: "openai",
		Model:         "gpt-5.5",
		CWD:           "/tmp",
	}); err != nil {
		t.Fatalf("ThreadStart: %v", err)
	}
	if got.Resolved.Effective.Model != "openai/gpt-5.5" {
		t.Fatalf("spawn model=%q, want openai/gpt-5.5", got.Resolved.Effective.Model)
	}
}

func TestHubRPCThreadStartAllowsModelWhenProviderHasLaunchDiagnostic(t *testing.T) {
	runDir := t.TempDir()
	var got SpawnRequest
	spawner := &fakeRPCModelContractSpawner{
		contract: appwire.ModelListResponse{
			Diagnostics: []appwire.ModelListDiagnostic{{
				Provider: "openai",
				Source:   "provider",
				Title:    "Provider error",
				Message:  "HTTP 403",
			}},
		},
	}
	spawner.spawn = func(_ context.Context, req SpawnRequest) (rendezvous.Entry, error) {
		got = req
		return rendezvous.Entry{
			PID:       108,
			Protocol:  appwire.ProtocolVersion,
			SourceID:  "local",
			ThreadID:  "th_degraded_model",
			SessionID: "sess_degraded_model",
		}, nil
	}
	hub := newHubRPCTestServer(t, WebConfig{RunDir: runDir, Spawner: spawner, Past: NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{
		ModelProvider: "openai",
		Model:         "gpt-5.5",
		CWD:           "/tmp",
	}); err != nil {
		t.Fatalf("ThreadStart: %v", err)
	}
	if got.Resolved.Effective.Model != "openai/gpt-5.5" {
		t.Fatalf("spawn model=%q, want openai/gpt-5.5", got.Resolved.Effective.Model)
	}
}

func TestHubRPCThreadStartRejectsProviderMissingFromDegradedLaunchContract(t *testing.T) {
	runDir := t.TempDir()
	var spawnCalled bool
	spawner := &fakeRPCModelContractSpawner{
		contract: appwire.ModelListResponse{
			Data: []appwire.ModelDescriptor{{Provider: "ollama", Model: "local"}},
			Diagnostics: []appwire.ModelListDiagnostic{{
				Provider: "openai",
				Source:   "provider",
				Title:    "Provider error",
				Message:  "HTTP 403",
			}},
		},
	}
	spawner.spawn = func(_ context.Context, req SpawnRequest) (rendezvous.Entry, error) {
		spawnCalled = true
		return rendezvous.Entry{}, nil
	}
	hub := newHubRPCTestServer(t, WebConfig{RunDir: runDir, Spawner: spawner, Past: NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	_, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{
		ModelProvider: "anthropic",
		Model:         "claude-test",
		CWD:           "/tmp",
	})
	assertHubLaunchError(t, err)
	if !strings.Contains(err.Error(), "not reported by the Serf launch harness") {
		t.Fatalf("error=%v", err)
	}
	if spawnCalled {
		t.Fatal("spawn was called for provider missing from degraded launch contract")
	}
}

func TestHubRPCThreadStartAllowsIntentionallySkippedLaunchProvider(t *testing.T) {
	runDir := t.TempDir()
	var got SpawnRequest
	spawner := &fakeRPCModelContractSpawner{
		contract: appwire.ModelListResponse{
			Data: []appwire.ModelDescriptor{{Provider: "ollama", Model: "local"}},
			Diagnostics: []appwire.ModelListDiagnostic{{
				Provider: "openai",
				Source:   "provider",
				Title:    "Provider error",
				Message:  "HTTP 403",
			}},
		},
	}
	spawner.spawn = func(_ context.Context, req SpawnRequest) (rendezvous.Entry, error) {
		got = req
		return rendezvous.Entry{PID: 301, ThreadID: "th_openrouter_anthropic", SessionID: "th_openrouter_anthropic"}, nil
	}
	hub := newHubRPCTestServer(t, WebConfig{RunDir: runDir, Spawner: spawner, Past: NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{
		ModelProvider: "openrouter-anthropic",
		Model:         "anthropic/claude-3-5-sonnet",
		CWD:           "/tmp",
	})
	if err != nil {
		t.Fatalf("ThreadStart: %v", err)
	}
	if got.Resolved.Effective.Model != "openrouter-anthropic/anthropic/claude-3-5-sonnet" {
		t.Fatalf("spawn model=%q", got.Resolved.Effective.Model)
	}
	if resp.Thread.Serf.Ref != "local:th_openrouter_anthropic" {
		t.Fatalf("thread=%+v", resp.Thread)
	}
}

func TestHubRPCThreadStartRejectsMalformedModelBeforeSpawn(t *testing.T) {
	runDir := t.TempDir()
	var spawnCalled bool
	spawner := &fakeRPCSpawner{
		spawn: func(_ context.Context, req SpawnRequest) (rendezvous.Entry, error) {
			spawnCalled = true
			return rendezvous.Entry{}, nil
		},
	}
	hub := newHubRPCTestServer(t, WebConfig{
		RunDir:  runDir,
		Spawner: spawner,
		Past:    NewPastIndex(""),
	})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	_, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{
		Model: "openrouter",
		CWD:   "/tmp",
	})
	if err == nil {
		t.Fatal("ThreadStart succeeded for malformed model")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error %T does not preserve WireError: %v", err, err)
	}
	if wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("wire=%+v", wire)
	}
	if spawnCalled {
		t.Fatal("spawn was called for malformed model")
	}
}

func TestThreadStart_LaunchOverridesApplied(t *testing.T) {
	runDir := t.TempDir()
	var got SpawnRequest
	spawner := &fakeRPCSpawner{
		spawn: func(_ context.Context, req SpawnRequest) (rendezvous.Entry, error) {
			got = req
			return rendezvous.Entry{
				PID:       200,
				Protocol:  appwire.ProtocolVersion,
				SourceID:  "local",
				ThreadID:  "th_overrides",
				SessionID: "sess_overrides",
			}, nil
		},
	}
	maxRounds := 7
	hub := newHubRPCTestServer(t, WebConfig{
		RunDir:       runDir,
		HubStateRoot: t.TempDir(),
		Spawner:      spawner,
		Past:         NewPastIndex(""),
	})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{
		Model: "openai/gpt-5",
		CWD:   "/tmp",
		LaunchOverrides: &appwire.LaunchConfigLayer{
			SkillsDirs: []string{"/per-launch"},
			MaxRounds:  &maxRounds,
		},
	}); err != nil {
		t.Fatalf("ThreadStart: %v", err)
	}
	eff := got.Resolved.Effective
	found := false
	for _, d := range eff.SkillsDirs {
		if d == "/per-launch" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("SkillsDirs = %v, want /per-launch", eff.SkillsDirs)
	}
	if eff.MaxRounds == nil || *eff.MaxRounds != 7 {
		t.Errorf("MaxRounds = %v, want 7", eff.MaxRounds)
	}
	// Legacy scalar wins: model comes from params.Model, not launchOverrides.
	if eff.Model != "openai/gpt-5" {
		t.Errorf("Model = %q, want openai/gpt-5", eff.Model)
	}
}

func TestHubRPCThreadStartUsesGlobalLaunchDefaultModel(t *testing.T) {
	runDir := t.TempDir()
	stateRoot := t.TempDir()
	cwd := t.TempDir()
	c := newHubLaunchController(stateRoot)
	if _, err := c.SetLayer(context.Background(), appwire.LaunchConfigSetLayerParams{
		CWD:    cwd,
		Layer:  "global",
		Config: appwire.LaunchConfigLayer{Model: "openai/gpt-5"},
	}); err != nil {
		t.Fatalf("SetLayer: %v", err)
	}
	var got SpawnRequest
	spawner := &fakeRPCModelContractSpawner{
		fakeRPCSpawner: fakeRPCSpawner{
			spawn: func(_ context.Context, req SpawnRequest) (rendezvous.Entry, error) {
				got = req
				return rendezvous.Entry{
					PID:       201,
					Protocol:  appwire.ProtocolVersion,
					SourceID:  "local",
					ThreadID:  "th_default_model",
					SessionID: "sess_default_model",
				}, nil
			},
		},
		contract: appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{
			Provider: "openai",
			Model:    "gpt-5",
		}}},
	}
	hub := newHubRPCTestServer(t, WebConfig{
		RunDir:       runDir,
		HubStateRoot: stateRoot,
		Spawner:      spawner,
		Past:         NewPastIndex(""),
	})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{
		CWD: cwd,
	}); err != nil {
		t.Fatalf("ThreadStart: %v", err)
	}
	if got.Resolved.Effective.Model != "openai/gpt-5" {
		t.Errorf("Model = %q, want openai/gpt-5", got.Resolved.Effective.Model)
	}
	if got.Provider != "openai" {
		t.Errorf("Provider = %q, want openai", got.Provider)
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
		Input:   []appwire.InputItem{{Type: "text", Text: "hello codex"}},
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
		Input:   []appwire.InputItem{{Type: "text", Text: "hello launched codex"}},
	})
	if err != nil {
		t.Fatalf("ThreadStart: %v", err)
	}
	if resp.Thread.Serf.Ref != "codex-managed:th_fake" || resp.Turn.ID != "turn_fake" {
		t.Fatalf("resp=%+v", resp)
	}
}

func TestHubRPCThreadStartRelaunchesConfiguredCodexAppServerAfterExit(t *testing.T) {
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
	if _, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{
		Harness: "codex-managed",
		CWD:     "/tmp/project",
		Input:   []appwire.InputItem{{Type: "text", Text: "first launched codex"}},
	}); err != nil {
		t.Fatalf("first ThreadStart: %v", err)
	}
	first := launcherRunningProcess(t, launcher, "codex-managed")
	if err := first.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill first codex: %v", err)
	}
	waitLaunchedCodexExited(t, first)

	resp, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{
		Harness: "codex-managed",
		CWD:     "/tmp/project",
		Input:   []appwire.InputItem{{Type: "text", Text: "second launched codex"}},
	})
	if err != nil {
		t.Fatalf("second ThreadStart: %v", err)
	}
	if resp.Thread.Serf.Ref != "codex-managed:th_fake" || resp.Turn.ID != "turn_fake" {
		t.Fatalf("resp=%+v", resp)
	}
}

func TestHubRPCThreadResumeEnsuresManagedCodexAppServerAfterExit(t *testing.T) {
	launcher := NewCodexLauncher([]CodexLaunchConfig{fakeCodexLaunchConfig("codex-managed", "ready")})
	defer shutdownCodexLauncher(t, launcher)
	if _, err := launcher.EnsureSource(context.Background(), "codex-managed", nil); err != nil {
		t.Fatalf("EnsureSource: %v", err)
	}
	first := launcherRunningProcess(t, launcher, "codex-managed")
	if err := first.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill first codex: %v", err)
	}
	waitLaunchedCodexExited(t, first)

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
	resp, err := client.ThreadResume(context.Background(), appwire.ThreadResumeParams{Ref: "codex-managed:th_fake"})
	if err != nil {
		t.Fatalf("ThreadResume: %v", err)
	}
	if resp.Thread.Serf.Ref != "codex-managed:th_fake" {
		t.Fatalf("thread=%+v", resp.Thread)
	}
}

func TestHubRPCThreadForkEnsuresManagedCodexAppServerAfterExit(t *testing.T) {
	launcher := NewCodexLauncher([]CodexLaunchConfig{fakeCodexLaunchConfig("codex-managed", "ready")})
	defer shutdownCodexLauncher(t, launcher)
	if _, err := launcher.EnsureSource(context.Background(), "codex-managed", nil); err != nil {
		t.Fatalf("EnsureSource: %v", err)
	}
	first := launcherRunningProcess(t, launcher, "codex-managed")
	if err := first.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill first codex: %v", err)
	}
	waitLaunchedCodexExited(t, first)

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
	resp, err := client.ThreadFork(context.Background(), appwire.ThreadForkParams{Ref: "codex-managed:th_fake"})
	if err != nil {
		t.Fatalf("ThreadFork: %v", err)
	}
	if resp.Thread.Serf.Ref != "codex-managed:th_fake_child" {
		t.Fatalf("thread=%+v", resp.Thread)
	}
}

func TestHubRPCTurnStartEnsuresManagedCodexAppServerAfterExit(t *testing.T) {
	launcher := NewCodexLauncher([]CodexLaunchConfig{fakeCodexLaunchConfig("codex-managed", "ready")})
	defer shutdownCodexLauncher(t, launcher)
	web := NewWebServer(WebConfig{
		Past:          NewPastIndex(""),
		CodexLaunches: []CodexLaunchConfig{fakeCodexLaunchConfig("codex-managed", "ready")},
		CodexLauncher: launcher,
	})
	if _, err := launcher.EnsureSource(context.Background(), "codex-managed", web.sources); err != nil {
		t.Fatalf("EnsureSource: %v", err)
	}
	first := launcherRunningProcess(t, launcher, "codex-managed")
	if err := first.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill first codex: %v", err)
	}
	waitLaunchedCodexExited(t, first)

	hub := httptest.NewUnstartedServer(nil)
	web.cfg.HubAddr = hub.Listener.Addr().String()
	hub.Config.Handler = web.Handler()
	hub.Start()
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.TurnStart(context.Background(), appwire.TurnStartParams{Ref: "codex-managed:th_fake", Input: []appwire.InputItem{{Type: "text", Text: "continue"}}})
	if err != nil {
		t.Fatalf("TurnStart: %v", err)
	}
	if resp.Turn.ID != "turn_fake" {
		t.Fatalf("turn=%+v", resp.Turn)
	}
	next := launcherRunningProcess(t, launcher, "codex-managed")
	if next == first {
		t.Fatal("turn/start reused the exited managed Codex process")
	}
}

func TestResumeRequestForConfigDoesNotTreatProfileIDAsProvider(t *testing.T) {
	root := t.TempDir()
	sessionID := "01PROFILE0000000000000001"
	stateDir := filepath.Join(root, "projects", "profile-id")
	if err := agent.SaveSessionMeta(stateDir, agent.SessionMeta{
		ID:        sessionID,
		ProfileID: "openai-gpt-5",
		Model:     "gpt-5.2",
		EnvInfo:   agent.EnvironmentInfo{WorkingDir: "/tmp/project"},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	past := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	req := resumeRequestForConfig(WebConfig{Past: past}, sessionID)
	if req.Resolved.Effective.Model != "" {
		t.Fatalf("resume model=%q, want empty for non-provider profile id", req.Resolved.Effective.Model)
	}
	if req.WorkingDir != "/tmp/project" || req.StateDir != stateDir {
		t.Fatalf("resume request=%+v", req)
	}
}

func TestHubRPCThreadStartAllowsBlankCodexPromptWithoutTurnStart(t *testing.T) {
	codex := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex"})
	var startCalled bool
	var turnCalled bool
	appserver.HandleTyped(codex.Router(), appwire.MethodThreadStart, func(_ context.Context, _ map[string]any) (map[string]any, error) {
		startCalled = true
		return map[string]any{"thread": map[string]any{
			"id":        "th_blank",
			"sessionId": "th_blank",
			"status":    map[string]any{"type": "idle"},
			"source":    "appServer",
		}}, nil
	})
	appserver.HandleTyped(codex.Router(), appwire.MethodTurnStart, func(_ context.Context, _ map[string]any) (map[string]any, error) {
		turnCalled = true
		return nil, errors.New("blank prompt should not start a turn")
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
	})
	if err != nil {
		t.Fatalf("ThreadStart: %v", err)
	}
	if !startCalled {
		t.Fatal("Codex source was not started for blank prompt")
	}
	if turnCalled {
		t.Fatal("Codex turn was started for blank prompt")
	}
	if resp.Thread.Serf.Ref != "codex:th_blank" || resp.Turn.ID != "" {
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

func TestHubRPCThreadCompactRoutesConfiguredCodexSource(t *testing.T) {
	codex := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex"})
	var compactCalled bool
	appserver.HandleTyped(codex.Router(), appwire.MethodThreadRead, func(_ context.Context, params map[string]any) (map[string]any, error) {
		if params["threadId"] != "th_codex" {
			t.Fatalf("thread/read params=%+v", params)
		}
		return map[string]any{"thread": map[string]any{
			"id":        "th_codex",
			"sessionId": "th_codex",
			"preview":   "compact codex",
			"status":    map[string]any{"type": "idle"},
			"source":    "appServer",
		}}, nil
	})
	appserver.HandleTyped(codex.Router(), appwire.MethodThreadCompactStart, func(_ context.Context, params map[string]any) (map[string]any, error) {
		compactCalled = true
		if params["threadId"] != "th_codex" {
			t.Fatalf("thread/compact/start params=%+v", params)
		}
		return map[string]any{}, nil
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
	if err := client.ThreadCompactStart(context.Background(), appwire.ThreadCompactStartParams{Ref: "codex:th_codex"}); err != nil {
		t.Fatalf("ThreadCompactStart: %v", err)
	}
	if !compactCalled {
		t.Fatal("configured Codex source was not compacted")
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
	resp, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{Harness: "codex", CWD: "/work", Input: []appwire.InputItem{{Type: "text", Text: "hello"}}})
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
	resp, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{Harness: "codex", CWD: "/work", Input: []appwire.InputItem{{Type: "text", Text: "hello"}}})
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
		gotPrompt = inputTextForTest(params.Input)
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
	if _, err := client.TurnStart(context.Background(), appwire.TurnStartParams{Ref: "local:" + sessionID, Input: []appwire.InputItem{{Type: "text", Text: "resume work"}}}); err != nil {
		t.Fatalf("TurnStart: %v", err)
	}
	if gotPrompt != "resume work" {
		t.Fatalf("prompt=%q", gotPrompt)
	}
}

func TestHubRPCTurnStartResumesPastThreadAfterRelaySubscribeUnavailable(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "past")
	sessionID := buildRPCParentSession(t, stateDir)
	past := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	source := &resumeAfterSubscribeUnavailableSource{
		relayBroadcastSource: relayBroadcastSource{
			thread: appwire.Thread{
				ID:        sessionID,
				SessionID: sessionID,
				Source:    "local",
				Serf:      appwire.SerfThread{Ref: "local:" + sessionID, Capabilities: appwire.ThreadCapabilities{Send: true}},
			},
			notifications: make(chan appwire.Notification, 1),
		},
	}
	spawner := &fakeRPCSpawner{
		resume: func(context.Context, ResumeRequest) (rendezvous.Entry, error) {
			return rendezvous.Entry{
				Protocol:  appwire.ProtocolVersion,
				SourceID:  "local",
				ThreadID:  sessionID,
				SessionID: sessionID,
			}, nil
		},
	}
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(WebConfig{HubAddr: srv.Listener.Addr().String(), Spawner: spawner, Past: past})
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()
	client := dialHubRPC(t, srv)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.TurnStart(context.Background(), appwire.TurnStartParams{Ref: "local:" + sessionID, Input: []appwire.InputItem{{Type: "text", Text: "resume after relay"}}}); err != nil {
		t.Fatalf("TurnStart: %v", err)
	}
	if prompt := source.lastStartPrompt(); prompt != "resume after relay" {
		t.Fatalf("start prompt=%q", prompt)
	}
	if calls := source.subscribeCalls(); calls != 2 {
		t.Fatalf("subscribe calls=%d, want 2", calls)
	}
}

func TestHubRPCTurnStartDoesNotResumePastThreadOnLiveStartError(t *testing.T) {
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
		return appwire.TurnStartResponse{}, appwire.Unavailable("session is processing")
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       107,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws" + daemonHTTP.URL[len("http"):],
		SourceID:  "local",
		ThreadID:  sessionID,
		SessionID: sessionID,
	})
	roster := NewRoster(runDir, nil)
	roster.Refresh()
	resumeCalled := false
	spawner := &fakeRPCSpawner{
		resume: func(context.Context, ResumeRequest) (rendezvous.Entry, error) {
			resumeCalled = true
			return rendezvous.Entry{}, errors.New("resume should not be called")
		},
	}
	hub := newHubRPCTestServer(t, WebConfig{RunDir: runDir, Roster: roster, Spawner: spawner, Past: past})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	_, err := client.TurnStart(context.Background(), appwire.TurnStartParams{Ref: "local:" + sessionID, Input: []appwire.InputItem{{Type: "text", Text: "resume work"}}})
	if err == nil || !strings.Contains(err.Error(), "session is processing") {
		t.Fatalf("TurnStart err=%v, want live start error", err)
	}
	if resumeCalled {
		t.Fatal("resume was called for a non-stale live StartTurn error")
	}
}

func TestHubRPCTurnStartDoesNotResumePastThreadOnGenericSubstringError(t *testing.T) {
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
		return appwire.TurnStartResponse{}, appwire.InternalError("tool output included connection refused")
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       108,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws" + daemonHTTP.URL[len("http"):],
		SourceID:  "local",
		ThreadID:  sessionID,
		SessionID: sessionID,
	})
	roster := NewRoster(runDir, nil)
	roster.Refresh()
	resumeCalled := false
	spawner := &fakeRPCSpawner{
		resume: func(context.Context, ResumeRequest) (rendezvous.Entry, error) {
			resumeCalled = true
			return rendezvous.Entry{}, errors.New("resume should not be called")
		},
	}
	hub := newHubRPCTestServer(t, WebConfig{RunDir: runDir, Roster: roster, Spawner: spawner, Past: past})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	_, err := client.TurnStart(context.Background(), appwire.TurnStartParams{Ref: "local:" + sessionID, Input: []appwire.InputItem{{Type: "text", Text: "resume work"}}})
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("TurnStart err=%v, want live start error", err)
	}
	if resumeCalled {
		t.Fatal("resume was called for a generic live StartTurn error")
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
	if _, err := client.TurnStart(context.Background(), appwire.TurnStartParams{Ref: "local:" + sessionID, Input: []appwire.InputItem{{Type: "text", Text: "resume work"}}}); err != nil {
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

func TestHubRPCTurnStartResumesPastThreadAfterLocalTransportError(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "past")
	sessionID := buildRPCParentSession(t, stateDir)
	past := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	staleEndpoint := "ws://" + ln.Addr().String() + "/rpc"
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}

	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		appserver.Subscribe(ctx, sessionID)
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: sessionID, SessionID: sessionID, Source: "local", Serf: appwire.SerfThread{Ref: params.Ref, Capabilities: appwire.ThreadCapabilities{Send: true}}}}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodTurnStart, func(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_recovered"}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       109,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  staleEndpoint,
		SourceID:  "local",
		ThreadID:  sessionID,
		SessionID: sessionID,
	})
	roster := NewRoster(runDir, nil)
	roster.Refresh()
	resumeCalled := false
	spawner := &fakeRPCSpawner{
		resume: func(context.Context, ResumeRequest) (rendezvous.Entry, error) {
			resumeCalled = true
			entry := rendezvous.Entry{
				PID:       110,
				Protocol:  appwire.ProtocolVersion,
				Endpoint:  "ws" + daemonHTTP.URL[len("http"):],
				SourceID:  "local",
				ThreadID:  sessionID,
				SessionID: sessionID,
			}
			writeRendezvous(t, runDir, entry)
			roster.Refresh()
			return entry, nil
		},
	}
	hub := newHubRPCTestServer(t, WebConfig{RunDir: runDir, Roster: roster, Spawner: spawner, Past: past})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.TurnStart(context.Background(), appwire.TurnStartParams{Ref: "local:" + sessionID, Input: []appwire.InputItem{{Type: "text", Text: "resume work"}}})
	if err != nil {
		t.Fatalf("TurnStart: %v", err)
	}
	if !resumeCalled {
		t.Fatal("resume was not called after local transport error")
	}
	if resp.Turn.ID != "turn_recovered" {
		t.Fatalf("turn=%+v", resp.Turn)
	}
}

// TestHubKnowsRefAcceptsManagedLaunchRefWithoutPastEntry guards the kata ws5f
// fix: the MethodTurnStart resume gate must accept managed-launch refs (e.g.
// codex:thread_xxx) even when they aren't in the local past index, so that an
// auto-resume retry fires when the managed daemon dies mid-turn.
func TestHubKnowsRefAcceptsManagedLaunchRefWithoutPastEntry(t *testing.T) {
	launcher := NewCodexLauncher([]CodexLaunchConfig{{ID: "codex-managed"}})
	cfg := WebConfig{Past: NewPastIndex(""), CodexLauncher: launcher}
	if !hubKnowsRef(cfg, "codex-managed:th_known") {
		t.Fatal("hubKnowsRef should accept managed-launch ref")
	}
	if hubKnowsRef(cfg, "codex-unknown:th_known") {
		t.Fatal("hubKnowsRef should reject ref whose source is not managed")
	}
	if hubKnowsRef(cfg, "local:th_not_in_past") {
		t.Fatal("hubKnowsRef should reject local ref with no past entry")
	}
}

// resumeAfterSessionUnavailableManagedSource simulates a managed codex daemon
// that returns SessionUnavailable on the first StartTurn (the daemon just
// died), then succeeds after the hub calls ResumeThread.
type resumeAfterSessionUnavailableManagedSource struct {
	relayLifecycleSource
	id           string
	mu           sync.Mutex
	startCalls   int
	resumeCalls  int
	thread       appwire.Thread
	startPrompts []string
}

func (s *resumeAfterSessionUnavailableManagedSource) ID() string { return s.id }

func (s *resumeAfterSessionUnavailableManagedSource) ReadThread(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	return appwire.ThreadReadResponse{Thread: s.thread}, nil
}

func (s *resumeAfterSessionUnavailableManagedSource) ResumeThread(_ context.Context, _ appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
	s.mu.Lock()
	s.resumeCalls++
	s.mu.Unlock()
	return appwire.ThreadResumeResponse{Thread: s.thread}, nil
}

func (s *resumeAfterSessionUnavailableManagedSource) StartTurn(_ context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
	s.mu.Lock()
	s.startCalls++
	calls := s.startCalls
	s.startPrompts = append(s.startPrompts, inputTextForTest(params.Input))
	s.mu.Unlock()
	if calls == 1 {
		return appwire.TurnStartResponse{}, appwire.SessionUnavailable("managed daemon went away")
	}
	return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_after_resume"}}, nil
}

func (s *resumeAfterSessionUnavailableManagedSource) counts() (start, resume int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startCalls, s.resumeCalls
}

// seedManagedSource pokes a fake source into the CodexLauncher's caches so
// that EnsureSource returns it without spawning a real process.
func seedManagedSource(t *testing.T, launcher *CodexLauncher, sourceID string, source appsource.Source) {
	t.Helper()
	launcher.mu.Lock()
	defer launcher.mu.Unlock()
	launcher.sources[sourceID] = source
	launcher.running[sourceID] = &launchedCodex{
		cmd:    &exec.Cmd{},
		exited: make(chan error),
	}
}

// TestHubRPCTurnStartResumesManagedLaunchRefOnSessionUnavailable verifies that
// the auto-resume retry fires for a non-local managed-launch ref when the
// backing daemon returns SessionUnavailable. Previously the past-index gate
// at MethodTurnStart skipped the retry entirely for any non-local ref (kata
// ws5f).
func TestHubRPCTurnStartResumesManagedLaunchRefOnSessionUnavailable(t *testing.T) {
	launcher := NewCodexLauncher([]CodexLaunchConfig{{ID: "codex-managed"}})
	fake := &resumeAfterSessionUnavailableManagedSource{
		relayLifecycleSource: relayLifecycleSource{canceled: make(chan struct{}, 1)},
		id:                   "codex-managed",
		thread: appwire.Thread{
			ID:        "th_managed",
			SessionID: "th_managed",
			Source:    "codex-managed",
			Serf: appwire.SerfThread{
				Ref:          "codex-managed:th_managed",
				Capabilities: appwire.ThreadCapabilities{Send: true},
			},
		},
	}
	seedManagedSource(t, launcher, "codex-managed", fake)

	hub := newHubRPCTestServer(t, WebConfig{
		Past:          NewPastIndex(""),
		CodexLaunches: []CodexLaunchConfig{{ID: "codex-managed"}},
		CodexLauncher: launcher,
	})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.TurnStart(context.Background(), appwire.TurnStartParams{
		Ref:   "codex-managed:th_managed",
		Input: []appwire.InputItem{{Type: "text", Text: "keep going"}},
	})
	if err != nil {
		t.Fatalf("TurnStart: %v", err)
	}
	if resp.Turn.ID != "turn_after_resume" {
		t.Fatalf("turn=%+v, want turn_after_resume", resp.Turn)
	}
	starts, resumes := fake.counts()
	if starts != 2 {
		t.Fatalf("StartTurn calls=%d, want 2 (initial + retry after resume)", starts)
	}
	if resumes != 1 {
		t.Fatalf("ResumeThread calls=%d, want 1", resumes)
	}
}

// sessionUnavailableOnceSource returns SessionUnavailable on the first
// StartTurn and tracks ResumeThread calls.
type sessionUnavailableOnceSource struct {
	relayLifecycleSource
	id          string
	mu          sync.Mutex
	startCalls  int
	resumeCalls int
	thread      appwire.Thread
}

func (s *sessionUnavailableOnceSource) ID() string { return s.id }

func (s *sessionUnavailableOnceSource) ReadThread(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	return appwire.ThreadReadResponse{Thread: s.thread}, nil
}

func (s *sessionUnavailableOnceSource) ResumeThread(context.Context, appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
	s.mu.Lock()
	s.resumeCalls++
	s.mu.Unlock()
	return appwire.ThreadResumeResponse{Thread: s.thread}, nil
}

func (s *sessionUnavailableOnceSource) StartTurn(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
	s.mu.Lock()
	s.startCalls++
	calls := s.startCalls
	s.mu.Unlock()
	if calls == 1 {
		return appwire.TurnStartResponse{}, appwire.SessionUnavailable("daemon went away")
	}
	return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_recovered"}}, nil
}

func (s *sessionUnavailableOnceSource) counts() (start, resume int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startCalls, s.resumeCalls
}

// TestHubRPCTurnStartDoesNotResumeUnknownNonLocalRef confirms the resume gate
// still refuses non-local refs the hub does not know about (no managed launch,
// no past entry) even after widening the gate to include managed-launch
// sources (kata ws5f). The hub should bubble up the original SessionUnavailable
// error without attempting a resume.
func TestHubRPCTurnStartDoesNotResumeUnknownNonLocalRef(t *testing.T) {
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(WebConfig{HubAddr: srv.Listener.Addr().String(), Past: NewPastIndex("")})
	fake := &sessionUnavailableOnceSource{
		relayLifecycleSource: relayLifecycleSource{canceled: make(chan struct{}, 1)},
		id:                   "codex",
		thread: appwire.Thread{
			ID:        "th_unknown",
			SessionID: "th_unknown",
			Source:    "codex",
			Serf: appwire.SerfThread{
				Ref:          "codex:th_unknown",
				Capabilities: appwire.ThreadCapabilities{Send: true},
			},
		},
	}
	web.sources.Add(fake)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPC(t, srv)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	_, err := client.TurnStart(context.Background(), appwire.TurnStartParams{
		Ref:   "codex:th_unknown",
		Input: []appwire.InputItem{{Type: "text", Text: "should not resume"}},
	})
	if err == nil {
		t.Fatal("TurnStart succeeded, want SessionUnavailable error")
	}
	if !strings.Contains(err.Error(), "daemon went away") {
		t.Fatalf("err=%v, want daemon went away", err)
	}
	starts, resumes := fake.counts()
	if starts != 1 {
		t.Fatalf("StartTurn calls=%d, want 1 (no retry for unknown ref)", starts)
	}
	if resumes != 0 {
		t.Fatalf("ResumeThread calls=%d, want 0 (gate must reject unknown non-local ref)", resumes)
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

func TestHubRPCThreadForkRoutesNonLocalCapableSource(t *testing.T) {
	source := &forkingRelaySource{
		relayBroadcastSource: relayBroadcastSource{
			id: "codex",
			thread: appwire.Thread{
				ID:        "th_fork",
				SessionID: "th_fork",
				Source:    "codex",
				Serf:      appwire.SerfThread{Ref: "codex:th_fork", Capabilities: appwire.ThreadCapabilities{ForkFromTurn: true}},
			},
			notifications: make(chan appwire.Notification, 1),
			canceled:      make(chan struct{}, 1),
		},
		response: appwire.ThreadForkResponse{Thread: appwire.Thread{
			ID:        "th_child",
			SessionID: "th_child",
			Source:    "codex",
			Serf:      appwire.SerfThread{Ref: "codex:th_child"},
		}},
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
	resp, err := client.ThreadFork(context.Background(), appwire.ThreadForkParams{
		Ref:          "codex:th_fork",
		SourceTurnID: "codex-turn-1",
		Model:        "gpt-5-codex",
	})
	if err != nil {
		t.Fatalf("ThreadFork: %v", err)
	}
	if !source.forkCalled {
		t.Fatal("non-local source ForkThread was not called")
	}
	if source.forkParams.SourceTurnID != "codex-turn-1" || source.forkParams.EditedInput != "" {
		t.Fatalf("fork params=%+v", source.forkParams)
	}
	if resp.Thread.Serf.Ref != "codex:th_child" {
		t.Fatalf("thread=%+v", resp.Thread)
	}
}

func TestHubRPCThreadForkRoutesNonLocalWholeThreadForkWithoutTurnForkCapability(t *testing.T) {
	source := &forkingRelaySource{
		relayBroadcastSource: relayBroadcastSource{
			id: "codex",
			thread: appwire.Thread{
				ID:        "th_whole_fork",
				SessionID: "th_whole_fork",
				Source:    "codex",
				Serf:      appwire.SerfThread{Ref: "codex:th_whole_fork", Capabilities: appwire.ThreadCapabilities{Send: true}},
			},
			notifications: make(chan appwire.Notification, 1),
			canceled:      make(chan struct{}, 1),
		},
		response: appwire.ThreadForkResponse{Thread: appwire.Thread{
			ID:        "th_whole_child",
			SessionID: "th_whole_child",
			Source:    "codex",
			Serf:      appwire.SerfThread{Ref: "codex:th_whole_child"},
		}},
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
	resp, err := client.ThreadFork(context.Background(), appwire.ThreadForkParams{Ref: "codex:th_whole_fork"})
	if err != nil {
		t.Fatalf("ThreadFork: %v", err)
	}
	if !source.forkCalled {
		t.Fatal("whole-thread fork was not routed to source")
	}
	if source.forkParams.SourceTurnID != "" || source.forkParams.EditedInput != "" || source.forkParams.Label != "" {
		t.Fatalf("fork params=%+v", source.forkParams)
	}
	if resp.Thread.Serf.Ref != "codex:th_whole_child" {
		t.Fatalf("thread=%+v", resp.Thread)
	}
}

func TestHubRPCThreadForkReturnsUnavailableWhenNonLocalSourceCannotFork(t *testing.T) {
	source := &forkingRelaySource{
		relayBroadcastSource: relayBroadcastSource{
			id: "codex",
			thread: appwire.Thread{
				ID:        "th_no_fork",
				SessionID: "th_no_fork",
				Source:    "codex",
				Serf:      appwire.SerfThread{Ref: "codex:th_no_fork", Capabilities: appwire.ThreadCapabilities{Send: true}},
			},
			notifications: make(chan appwire.Notification, 1),
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
	err := client.Request(context.Background(), appwire.MethodThreadFork, appwire.ThreadForkParams{
		Ref:          "codex:th_no_fork",
		SourceTurnID: "codex-turn-1",
	}, &appwire.ThreadForkResponse{})
	if err == nil {
		t.Fatal("ThreadFork succeeded for source without fork capability")
	}
	if source.forkCalled {
		t.Fatal("fork reached source despite missing capability")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error %T does not preserve WireError: %v", err, err)
	}
	if wire.Code != appwire.CodeUnavailable {
		t.Fatalf("wire=%+v", wire)
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
	spawn        func(context.Context, SpawnRequest) (rendezvous.Entry, error)
	resume       func(context.Context, ResumeRequest) (rendezvous.Entry, error)
	launchModels func(context.Context) ([]appwire.ModelDescriptor, error)
}

type fakeRPCModelContractSpawner struct {
	fakeRPCSpawner
	contract appwire.ModelListResponse
	err      error
}

func (f *fakeRPCModelContractSpawner) ListLaunchModelContract(context.Context) (appwire.ModelListResponse, error) {
	if f.err != nil {
		return appwire.ModelListResponse{}, f.err
	}
	return f.contract, nil
}

type fakeRPCWorkingDirModelContractSpawner struct {
	fakeRPCSpawner
	fallback              appwire.ModelListResponse
	contractForWorkingDir func(context.Context, string) (appwire.ModelListResponse, error)
}

func (f *fakeRPCWorkingDirModelContractSpawner) ListLaunchModelContract(context.Context) (appwire.ModelListResponse, error) {
	return f.fallback, nil
}

func (f *fakeRPCWorkingDirModelContractSpawner) ListLaunchModelContractForWorkingDir(ctx context.Context, cwd string) (appwire.ModelListResponse, error) {
	if f.contractForWorkingDir == nil {
		return appwire.ModelListResponse{}, nil
	}
	return f.contractForWorkingDir(ctx, cwd)
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

func (f *fakeRPCSpawner) ListLaunchModels(ctx context.Context) ([]appwire.ModelDescriptor, error) {
	if f.launchModels != nil {
		return f.launchModels(ctx)
	}
	return nil, appwire.Unavailable("launch model contract not configured")
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
		ID:             parentID,
		ProfileID:      "openai",
		Model:          "gpt-5",
		EnvInfo:        agent.EnvironmentInfo{WorkingDir: "/tmp/project"},
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
		TurnCount:      2,
		OriginalPrompt: "second task",
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
		ID:             sessionID,
		ProfileID:      "openai",
		Model:          "gpt-5",
		EnvInfo:        agent.EnvironmentInfo{WorkingDir: "/tmp/project"},
		CreatedAt:      now,
		UpdatedAt:      now,
		TurnCount:      1,
		OriginalPrompt: "hello",
	}); err != nil {
		t.Fatal(err)
	}
	return sessionID
}

func buildRPCStructuredFailedSession(t *testing.T, stateDir string) string {
	t.Helper()
	sessionID := "01FAILED0000000000000002"
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	transcriptPath := filepath.Join(stateDir, "sessions", sessionID+".transcript.jsonl")
	writer, err := agent.NewTranscriptWriter(transcriptPath, agent.TranscriptHeader{
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
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(transcriptPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"kind":"api_call","seq":1,"round":1,"request":{"provider":"openai","model":"gpt-5"},"error":"configuration error: unknown provider: openai","source":"provider","title":"Provider error","hint":"structured provider diagnostic"}` + "\n"); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := agent.SaveSessionMeta(stateDir, agent.SessionMeta{
		ID:             sessionID,
		ProfileID:      "openai",
		Model:          "gpt-5",
		EnvInfo:        agent.EnvironmentInfo{WorkingDir: "/tmp/project"},
		CreatedAt:      now,
		UpdatedAt:      now,
		TurnCount:      1,
		OriginalPrompt: "hello",
	}); err != nil {
		t.Fatal(err)
	}
	return sessionID
}

func launcherRunningProcess(t *testing.T, launcher *CodexLauncher, sourceID string) *launchedCodex {
	t.Helper()
	launcher.mu.Lock()
	defer launcher.mu.Unlock()
	launched := launcher.running[sourceID]
	if launched == nil || launched.cmd == nil || launched.cmd.Process == nil {
		t.Fatalf("launcher has no running process for %s", sourceID)
	}
	return launched
}

func waitLaunchedCodexExited(t *testing.T, launched *launchedCodex) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if launched.cmd.ProcessState != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for launched codex process to exit")
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
