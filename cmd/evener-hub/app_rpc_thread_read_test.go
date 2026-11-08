package hub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appitempaging"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/rendezvous"
)

type windowReadFlipProber struct {
	result     hubcore.ProbeResult
	windowRead atomic.Bool
	probes     atomic.Int64
}

func (p *windowReadFlipProber) Probe(rendezvous.Entry) hubcore.ProbeResult {
	p.probes.Add(1)
	result := p.result
	result.Capabilities.WindowRead = p.windowRead.Load()
	return result
}

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

func TestSessionOperationsFetchesServingDaemonItem(t *testing.T) {
	position := appwire.ThreadItemPosition{Entry: 7}
	item := appwire.ThreadItem{
		ID: "tool-large", Type: "commandExecution", TurnID: "turn_1", TranscriptKey: "tool-large", ToolName: "shell", Output: strings.Repeat("output", 50_000),
		Position: &position, Version: 4,
	}
	inFlightPosition := appwire.ThreadItemPosition{Entry: 8}
	inFlight := appwire.ThreadItem{
		ID: "tool-running", Type: "commandExecution", TurnID: "turn_1", TranscriptKey: "tool-running", ToolName: "shell",
		Output: strings.Repeat("running", 20_000), Position: &inFlightPosition,
	}
	const nativeIncarnation = "daemon-index-incarnation"

	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		thread := appwire.Thread{ID: "windowed", SessionID: "windowed", Evener: appwire.EvenerThread{Ref: params.Ref}}
		snapshot := &appwire.SnapshotIdentity{Incarnation: nativeIncarnation, Length: 10}
		if params.FetchCursor == "" {
			thread.Turns = []appwire.Turn{{ID: "turn_1", Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFull, Items: []appwire.ThreadItem{item, inFlight}}}
			return appwire.ThreadReadResponse{Thread: thread, Snapshot: snapshot}, nil
		}
		cursor, identity, err := appitempaging.DecodeFetchCursorForRef(params.FetchCursor, params.Ref)
		if err != nil {
			return appwire.ThreadReadResponse{}, err
		}
		sourceIncarnation, ok := appwire.ThreadWindowSourceIncarnation(identity.Incarnation)
		if !ok || sourceIncarnation != nativeIncarnation {
			return appwire.ThreadReadResponse{}, appwire.TranscriptItemCursorStale()
		}
		fetch, err := appitempaging.BuildFetchResponse(item, identity, cursor)
		return appwire.ThreadReadResponse{Thread: thread, Snapshot: snapshot, Fetch: &fetch}, err
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()
	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{Entry: rendezvous.Entry{
			Protocol: appwire.ProtocolVersion, Endpoint: "ws" + strings.TrimPrefix(daemonHTTP.URL, "http"), SourceID: "local",
			ThreadID: "windowed", SessionID: "windowed", WorkspaceRef: "local:windowed",
		}}}
	}, http.DefaultClient)
	sources := appsource.NewRegistry()
	sources.Add(source)
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex("")}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", Version: "test", SourceID: "local"})
	operations := &sessionOperations{cfg: cfg, sources: sources, relays: newHubRelay(server, cfg, sources), server: server}
	caller := appwire.ManagerView{Ref: "local:manager", Manager: true}

	window, err := operations.Invoke(t.Context(), caller, appwire.SessionOperationRequest{Turns: &appwire.ThreadTurnsListParams{
		Ref: "local:windowed",
		ThreadWindowParams: appwire.ThreadWindowParams{
			TurnWindow: &appwire.ThreadTurnWindow{Turn: "turn_1"}, ItemTypes: []string{"commandExecution"}, PreviewBytes: 64,
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if window.Turns == nil || len(window.Turns.Data) != 1 || len(window.Turns.Data[0].Items) != 2 || window.Turns.Data[0].Items[0].FetchCursor == "" {
		t.Fatalf("bounded window = %+v, want fetchable settled and labeled in-flight tool items", window.Turns)
	}
	if got := window.Turns.Data[0].Items[1]; !got.InFlight || got.FetchCursor != "" {
		t.Fatalf("in-flight bounded item = %+v, want label without fetch cursor", got)
	}

	want, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	cursor := window.Turns.Data[0].Items[0].FetchCursor
	var got []byte
	for cursor != "" {
		response, err := operations.Invoke(t.Context(), caller, appwire.SessionOperationRequest{Fetch: &appwire.SessionFetchRequest{
			Ref: "local:windowed", FetchCursor: cursor,
		}})
		if err != nil {
			t.Fatal(err)
		}
		fragment, err := base64.StdEncoding.DecodeString(response.Fetch.Data)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, fragment...)
		if response.Fetch.FetchCursor != "" {
			if len(response.FollowUps) != 1 || response.FollowUps[0].Tool != "read_transcript" ||
				response.FollowUps[0].Arguments.Hub.Ref != "local:windowed" ||
				response.FollowUps[0].Arguments.Hub.FetchCursor != response.Fetch.FetchCursor {
				t.Fatalf("fetch follow-up = %+v", response.FollowUps)
			}
		} else if len(response.FollowUps) != 0 {
			t.Fatalf("final fetch follow-up = %+v, want none", response.FollowUps)
		}
		cursor = response.Fetch.FetchCursor
	}
	if !slices.Equal(got, want) {
		t.Fatalf("fetched canonical bytes differ: got %d bytes, want %d", len(got), len(want))
	}
}

func TestSessionOperationsUsesVisibleHubCompatibilityForOlderDaemon(t *testing.T) {
	turns := make([]appwire.Turn, 0, 3)
	for i, id := range []string{"turn_1", "turn_2", "turn_3"} {
		position := appwire.ThreadItemPosition{Entry: uint64(i + 1)}
		turns = append(turns, appwire.Turn{
			ID: id, Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFull,
			Items: []appwire.ThreadItem{{
				ID: id + "-message", Type: "userMessage", TurnID: id, TranscriptKey: id + "-message",
				Text: id, Position: &position, Version: 1,
			}},
		})
	}

	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "old-daemon", SourceID: "local"})
	legacyReads := 0
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		if params.TurnWindow != nil || len(params.ItemTypes) != 0 || params.PreviewBytes != 0 || params.MessageBytes != 0 || params.WindowBudgetBytes != 0 {
			return appwire.ThreadReadResponse{}, appwire.InvalidParams("older daemon rejected window fields")
		}
		legacyReads++
		return appwire.ThreadReadResponse{
			Thread: appwire.Thread{
				ID: "legacy-window", SessionID: "legacy-window",
				Evener: appwire.EvenerThread{Ref: params.Ref}, Turns: turns,
			},
			Snapshot: &appwire.SnapshotIdentity{Incarnation: "legacy-window-snapshot", Length: 3},
		}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()
	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{
			Entry: rendezvous.Entry{
				Protocol: appwire.ProtocolVersion, Endpoint: "ws" + strings.TrimPrefix(daemonHTTP.URL, "http"), SourceID: "local",
				ThreadID: "legacy-window", SessionID: "legacy-window", WorkspaceRef: "local:legacy-window",
			},
			CapabilitiesKnown: true,
		}}
	}, http.DefaultClient)
	sources := appsource.NewRegistry()
	sources.Add(source)
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex("")}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", Version: "test", SourceID: "local"})
	operations := &sessionOperations{cfg: cfg, sources: sources, relays: newHubRelay(server, cfg, sources), server: server}

	response, err := operations.Invoke(t.Context(), appwire.ManagerView{Ref: "local:manager", Manager: true}, appwire.SessionOperationRequest{
		Turns: &appwire.ThreadTurnsListParams{
			Ref: "local:legacy-window",
			ThreadWindowParams: appwire.ThreadWindowParams{
				TurnWindow: &appwire.ThreadTurnWindow{Latest: 2}, ItemTypes: []string{"messages"}, MessageBytes: 64,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if legacyReads != 1 {
		t.Fatalf("legacy reads = %d, want one compatibility page", legacyReads)
	}
	if response.Turns == nil || response.Turns.Window == nil {
		t.Fatalf("compatibility response = %+v, want visible window metadata", response.Turns)
	}
	if got := response.Turns.Window.AppliedBy; got != transcriptWindowAppliedByHubCompat {
		t.Fatalf("appliedBy = %q, want %q", got, transcriptWindowAppliedByHubCompat)
	}
	if got := []string{response.Turns.Data[0].ID, response.Turns.Data[1].ID}; !slices.Equal(got, []string{"turn_2", "turn_3"}) {
		t.Fatalf("compatibility turn IDs = %v, want newest two in transcript order", got)
	}
}

func TestSessionOperationsRejectsUnsupportedOlderDaemonWindows(t *testing.T) {
	legacyReads := 0
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "old-daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		legacyReads++
		return appwire.ThreadReadResponse{}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()
	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{
			Entry: rendezvous.Entry{
				Protocol: appwire.ProtocolVersion, Endpoint: "ws" + strings.TrimPrefix(daemonHTTP.URL, "http"), SourceID: "local",
				ThreadID: "legacy-window", SessionID: "legacy-window", WorkspaceRef: "local:legacy-window",
			},
			CapabilitiesKnown: true,
		}}
	}, http.DefaultClient)
	sources := appsource.NewRegistry()
	sources.Add(source)
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex("")}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", Version: "test", SourceID: "local"})
	operations := &sessionOperations{cfg: cfg, sources: sources, relays: newHubRelay(server, cfg, sources), server: server}

	for name, window := range map[string]*appwire.ThreadTurnWindow{
		"since": {Since: "turn_1", Limit: 2},
		"turn":  {Turn: "turn_1"},
		"before": {Before: &appwire.ThreadTurnBoundary{
			TurnID: "turn_2", Limit: 2,
		}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := operations.Invoke(t.Context(), appwire.ManagerView{Ref: "local:manager", Manager: true}, appwire.SessionOperationRequest{
				Turns: &appwire.ThreadTurnsListParams{Ref: "local:legacy-window", ThreadWindowParams: appwire.ThreadWindowParams{TurnWindow: window}},
			})
			wire, ok := errors.AsType[appwire.WireError](err)
			if !ok {
				t.Fatalf("unsupported window error = %T %v, want typed wire error", err, err)
			}
			data, _ := appwire.DecodedErrorData(wire.Data)
			if data.EvenerErrorInfo != appwire.ErrorInfo("window-unsupported-on-daemon") {
				t.Fatalf("unsupported window error info = %q", data.EvenerErrorInfo)
			}
		})
	}
	if legacyReads != 0 {
		t.Fatalf("unsupported windows reached older daemon %d times", legacyReads)
	}
}

func TestSessionOperationsLabelsIncompleteOlderDaemonLatestWindow(t *testing.T) {
	position := appwire.ThreadItemPosition{Entry: 2}
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "old-daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		if params.TurnWindow != nil {
			return appwire.ThreadReadResponse{}, appwire.InvalidParams("older daemon rejected window fields")
		}
		return appwire.ThreadReadResponse{
			Thread: appwire.Thread{ID: "legacy-partial", SessionID: "legacy-partial", Evener: appwire.EvenerThread{Ref: params.Ref}, Turns: []appwire.Turn{{
				ID: "turn_2", Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFull,
				Items: []appwire.ThreadItem{{ID: "message_2", Type: "agentMessage", TurnID: "turn_2", TranscriptKey: "message_2", Position: &position, Version: 1, Text: "newest"}},
			}}},
			OlderCursor: "legacy-older-page", Snapshot: &appwire.SnapshotIdentity{Incarnation: "legacy-partial-snapshot", Length: 2},
		}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()
	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{
			Entry:             rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws" + strings.TrimPrefix(daemonHTTP.URL, "http"), SourceID: "local", ThreadID: "legacy-partial", SessionID: "legacy-partial", WorkspaceRef: "local:legacy-partial"},
			CapabilitiesKnown: true,
		}}
	}, http.DefaultClient)
	sources := appsource.NewRegistry()
	sources.Add(source)
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex("")}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", Version: "test", SourceID: "local"})
	operations := &sessionOperations{cfg: cfg, sources: sources, relays: newHubRelay(server, cfg, sources), server: server}

	request := appwire.ThreadTurnsListParams{
		Ref: "local:legacy-partial", ItemLimit: 7,
		ThreadWindowParams: appwire.ThreadWindowParams{
			TurnWindow: &appwire.ThreadTurnWindow{Latest: 2}, ItemTypes: []string{"messages"},
			PreviewBytes: 321, MessageBytes: 654, WindowBudgetBytes: 16 * 1024,
		},
	}
	response, err := operations.Invoke(t.Context(), appwire.ManagerView{Ref: "local:manager", Manager: true}, appwire.SessionOperationRequest{Turns: &request})
	if err != nil {
		t.Fatal(err)
	}
	window := response.Turns.Window
	if window == nil || window.ServedTurns != 1 || window.WithheldTurns != 1 || window.TrimmedEnd != "oldest" || window.ContinuationCursor == "" {
		t.Fatalf("partial compatibility window = %+v", window)
	}
	if len(response.FollowUps) != 1 {
		t.Fatalf("partial compatibility follow-ups = %+v, want one shaped continuation", response.FollowUps)
	}
	encoded, err := json.Marshal(response.FollowUps[0].Arguments.Hub)
	if err != nil {
		t.Fatal(err)
	}
	var shaped map[string]any
	if err := json.Unmarshal(encoded, &shaped); err != nil {
		t.Fatal(err)
	}
	turnWindow, _ := shaped["turnWindow"].(map[string]any)
	if shaped["cursor"] != window.ContinuationCursor || turnWindow["latest"] != float64(2) ||
		!slices.Equal(shaped["itemTypes"].([]any), []any{"messages"}) || shaped["previewBytes"] != float64(321) ||
		shaped["messageBytes"] != float64(654) || shaped["windowBudgetBytes"] != float64(16*1024) || shaped["itemLimit"] != float64(7) {
		t.Fatalf("window continuation lost request shape: %s", encoded)
	}
}

func TestSessionOperationsRetriesStalePositiveWindowProbeAsHubCompatibility(t *testing.T) {
	position := appwire.ThreadItemPosition{Entry: 1}
	var requests atomic.Int64
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "old-daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		requests.Add(1)
		if params.TurnWindow != nil {
			return appwire.ThreadReadResponse{}, appwire.InvalidParams("older daemon rejected window fields")
		}
		return appwire.ThreadReadResponse{
			Thread: appwire.Thread{ID: "stale-positive", SessionID: "stale-positive", Evener: appwire.EvenerThread{Ref: params.Ref}, Turns: []appwire.Turn{{
				ID: "turn_1", Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFull,
				Items: []appwire.ThreadItem{{ID: "message_1", Type: "agentMessage", TurnID: "turn_1", TranscriptKey: "message_1", Position: &position, Version: 1, Text: "legacy"}},
			}}},
			Snapshot: &appwire.SnapshotIdentity{Incarnation: "stale-positive-snapshot", Length: 1},
		}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID: os.Getpid(), Protocol: appwire.ProtocolVersion, Endpoint: "ws" + strings.TrimPrefix(daemonHTTP.URL, "http"),
		SourceID: "local", ThreadID: "stale-positive", SessionID: "stale-positive", WorkspaceRef: "local:stale-positive",
	})
	prober := &windowReadFlipProber{result: hubcore.ProbeResult{
		SessionID: "stale-positive", Status: appwire.ThreadStatusIdle, OK: true, CapabilitiesKnown: true,
	}}
	prober.windowRead.Store(true)
	roster := hubcore.NewRoster(runDir, prober).SetProcessAlive(func(int) bool { return true })
	if err := roster.RefreshAndWait(t.Context()); err != nil {
		t.Fatal(err)
	}
	prober.windowRead.Store(false)
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), RunDir: runDir, Roster: roster, Past: hubcore.NewPastIndex("")}
	sources := newHubSourceRegistry(cfg)
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", Version: "test", SourceID: "local"})
	operations := &sessionOperations{cfg: cfg, sources: sources, relays: newHubRelay(server, cfg, sources), server: server}

	response, err := operations.Invoke(t.Context(), appwire.ManagerView{Ref: "local:manager", Manager: true}, appwire.SessionOperationRequest{
		Turns: &appwire.ThreadTurnsListParams{Ref: "local:stale-positive", ThreadWindowParams: appwire.ThreadWindowParams{TurnWindow: &appwire.ThreadTurnWindow{Latest: 1}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("daemon read requests = %d, want one window attempt and one legacy retry", got)
	}
	if got := prober.probes.Load(); got != 2 {
		t.Fatalf("capability probes = %d, want initial probe and one failure re-probe", got)
	}
	if response.Turns.Window == nil || response.Turns.Window.AppliedBy != transcriptWindowAppliedByHubCompat {
		t.Fatalf("stale-positive response window = %+v", response.Turns.Window)
	}
}

func TestSessionOperationsKeepsWindowFailureWhenFreshProbeStillAdvertisesSupport(t *testing.T) {
	var requests atomic.Int64
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "window-daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		requests.Add(1)
		return appwire.ThreadReadResponse{}, appwire.InvalidParams("unrelated window request failure")
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID: os.Getpid(), Protocol: appwire.ProtocolVersion, Endpoint: "ws" + strings.TrimPrefix(daemonHTTP.URL, "http"),
		SourceID: "local", ThreadID: "window-failure", SessionID: "window-failure", WorkspaceRef: "local:window-failure",
	})
	prober := &windowReadFlipProber{result: hubcore.ProbeResult{
		SessionID: "window-failure", Status: appwire.ThreadStatusIdle, OK: true, CapabilitiesKnown: true,
	}}
	prober.windowRead.Store(true)
	roster := hubcore.NewRoster(runDir, prober).SetProcessAlive(func(int) bool { return true })
	if err := roster.RefreshAndWait(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), RunDir: runDir, Roster: roster, Past: hubcore.NewPastIndex("")}
	sources := newHubSourceRegistry(cfg)
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", Version: "test", SourceID: "local"})
	operations := &sessionOperations{cfg: cfg, sources: sources, relays: newHubRelay(server, cfg, sources), server: server}

	_, err := operations.Invoke(t.Context(), appwire.ManagerView{Ref: "local:manager", Manager: true}, appwire.SessionOperationRequest{
		Turns: &appwire.ThreadTurnsListParams{Ref: "local:window-failure", ThreadWindowParams: appwire.ThreadWindowParams{TurnWindow: &appwire.ThreadTurnWindow{Latest: 1}}},
	})
	wire, ok := errors.AsType[appwire.WireError](err)
	if !ok {
		t.Fatalf("window failure = %T %v, want original wire error", err, err)
	}
	data, _ := appwire.DecodedErrorData(wire.Data)
	if data.EvenerErrorInfo != appwire.ErrorInvalidParams {
		t.Fatalf("window failure info = %q, want %q", data.EvenerErrorInfo, appwire.ErrorInvalidParams)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("daemon read requests = %d, want no legacy retry", got)
	}
	if got := prober.probes.Load(); got != 2 {
		t.Fatalf("capability probes = %d, want initial probe and one failure re-probe", got)
	}
}

func TestSessionOperationsFetchesHubCompatibilityItemFromNewestLegacyPage(t *testing.T) {
	position := appwire.ThreadItemPosition{Entry: 7}
	item := appwire.ThreadItem{
		ID: "compat-large", Type: "agentMessage", TurnID: "turn-compat", TranscriptKey: "compat-large",
		Position: &position, Version: 4, Text: strings.Repeat("compat-bytes", 20_000),
	}
	var requests atomic.Int64
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "old-daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		requests.Add(1)
		if params.FetchCursor != "" {
			t.Fatalf("old daemon received fetch cursor")
		}
		if params.ItemLimit != appwire.TranscriptItemPageLimit {
			t.Fatalf("compat fetch item limit = %d, want %d", params.ItemLimit, appwire.TranscriptItemPageLimit)
		}
		return appwire.ThreadReadResponse{
			Thread: appwire.Thread{ID: "compat-fetch", SessionID: "compat-fetch", Evener: appwire.EvenerThread{Ref: params.Ref}, Turns: []appwire.Turn{{
				ID: "turn-compat", Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFull, Items: []appwire.ThreadItem{item},
			}}},
			Snapshot: &appwire.SnapshotIdentity{Incarnation: "compat-fetch-snapshot", Length: 8},
		}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()
	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{
			Entry:             rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws" + strings.TrimPrefix(daemonHTTP.URL, "http"), SourceID: "local", ThreadID: "compat-fetch", SessionID: "compat-fetch", WorkspaceRef: "local:compat-fetch"},
			CapabilitiesKnown: true,
		}}
	}, http.DefaultClient)
	sources := appsource.NewRegistry()
	sources.Add(source)
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex("")}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", Version: "test", SourceID: "local"})
	operations := &sessionOperations{cfg: cfg, sources: sources, relays: newHubRelay(server, cfg, sources), server: server}

	windowResponse, err := operations.Invoke(t.Context(), appwire.ManagerView{Ref: "local:manager", Manager: true}, appwire.SessionOperationRequest{
		Turns: &appwire.ThreadTurnsListParams{
			Ref: "local:compat-fetch", ThreadWindowParams: appwire.ThreadWindowParams{TurnWindow: &appwire.ThreadTurnWindow{Latest: 1}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if windowResponse.Turns.Window == nil || windowResponse.Turns.Window.AppliedBy != transcriptWindowAppliedByHubCompat {
		t.Fatalf("compat window = %+v", windowResponse.Turns.Window)
	}
	fetchCursor := windowResponse.Turns.Data[0].Items[0].FetchCursor
	decoded, _, err := appitempaging.DecodeFetchCursorForRef(fetchCursor, "local:compat-fetch")
	if err != nil || decoded.Origin != appitempaging.FetchCursorOriginHubCompat {
		t.Fatalf("compat fetch cursor origin = %q err %v", decoded.Origin, err)
	}
	fetched, err := operations.fetch(t.Context(), appwire.SessionFetchRequest{Ref: "local:compat-fetch", FetchCursor: fetchCursor})
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	got, err := base64.StdEncoding.DecodeString(fetched.Data)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want[:len(got)]) || fetched.TotalLength != uint64(len(want)) {
		t.Fatalf("compat fetch returned offset=%d total=%d bytes=%d, want canonical prefix of %d", fetched.Offset, fetched.TotalLength, len(got), len(want))
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("old daemon reads = %d, want one window read and one cursorless fetch re-read", got)
	}
}

func TestSessionOperationsFetchesHubCompatibilityItemOnePageBack(t *testing.T) {
	targetPosition := appwire.ThreadItemPosition{Entry: 7}
	target := appwire.ThreadItem{
		ID: "compat-target", Type: "commandExecution", TurnID: "turn-target", TranscriptKey: "compat-target",
		Position: &targetPosition, Version: 6, Output: strings.Repeat("older-output", 15_000),
	}
	newer := make([]appwire.ThreadItem, appwire.TranscriptItemPageLimit)
	for i := range newer {
		position := appwire.ThreadItemPosition{Entry: uint64(8 + i)}
		newer[i] = appwire.ThreadItem{
			ID: fmt.Sprintf("newer-%02d", i), Type: "agentMessage", TurnID: "turn-newer", TranscriptKey: fmt.Sprintf("newer-%02d", i),
			Position: &position, Version: 1, Text: "newer",
		}
	}
	var reads, pages atomic.Int64
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "old-daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		reads.Add(1)
		if params.FetchCursor != "" {
			t.Fatal("old daemon received fetch cursor")
		}
		return appwire.ThreadReadResponse{
			Thread: appwire.Thread{ID: "compat-page-back", SessionID: "compat-page-back", Evener: appwire.EvenerThread{Ref: params.Ref}, Turns: []appwire.Turn{{
				ID: "turn-newer", Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFragment, Items: newer, HasEarlierItems: true,
			}}},
			OlderCursor: "native-older", Snapshot: &appwire.SnapshotIdentity{Incarnation: "compat-page-back-snapshot", Length: 48},
		}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadTurnsList, func(_ context.Context, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
		pages.Add(1)
		if params.Before == nil || *params.Before != *newer[0].Position || params.Cursor != "" {
			t.Fatalf("compat page params = %+v, want exclusive before %+v without cursor", params, *newer[0].Position)
		}
		return appwire.ThreadTurnsListResponse{
			Data:     []appwire.Turn{{ID: "turn-target", Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFragment, Items: []appwire.ThreadItem{target}}},
			Snapshot: &appwire.SnapshotIdentity{Incarnation: "compat-page-back-snapshot", Length: 48},
		}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()
	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{
			Entry:             rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws" + strings.TrimPrefix(daemonHTTP.URL, "http"), SourceID: "local", ThreadID: "compat-page-back", SessionID: "compat-page-back", WorkspaceRef: "local:compat-page-back"},
			Capabilities:      appwire.ThreadCapabilities{PageBefore: true},
			CapabilitiesKnown: true,
		}}
	}, http.DefaultClient)
	sources := appsource.NewRegistry()
	sources.Add(source)
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex("")}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", Version: "test", SourceID: "local"})
	operations := &sessionOperations{cfg: cfg, sources: sources, relays: newHubRelay(server, cfg, sources), server: server}
	window := appwire.ThreadWindowParams{TurnWindow: &appwire.ThreadTurnWindow{Latest: 1}}
	identity := appitempaging.CursorIdentity{
		ThreadRef: "local:compat-page-back", Incarnation: window.ViewIncarnation("compat-page-back-snapshot", appwire.TranscriptItemPageLimit), ProjectionVersion: appitempaging.TranscriptItemProjectionVersion,
	}
	fetchCursor, err := appitempaging.EncodeFetchCursor(identity, appitempaging.FetchCursor{
		Position: targetPosition, ItemVersion: target.Version, Origin: appitempaging.FetchCursorOriginHubCompat,
	})
	if err != nil {
		t.Fatal(err)
	}
	fetched, err := operations.fetch(t.Context(), appwire.SessionFetchRequest{Ref: identity.ThreadRef, FetchCursor: fetchCursor})
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	got, err := base64.StdEncoding.DecodeString(fetched.Data)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want[:len(got)]) || fetched.TotalLength != uint64(len(want)) {
		t.Fatalf("page-back fetch returned total=%d bytes=%d, want canonical prefix of %d", fetched.TotalLength, len(got), len(want))
	}
	if reads.Load() != 1 || pages.Load() != 1 {
		t.Fatalf("compat fetch calls = reads %d pages %d, want 1 and 1", reads.Load(), pages.Load())
	}
}

func TestSessionOperationsBoundsPreBeforeHubCompatibilityFetchToNewestPage(t *testing.T) {
	targetPosition := appwire.ThreadItemPosition{Entry: 1}
	newestPosition := appwire.ThreadItemPosition{Entry: 41}
	var reads, pages atomic.Int64
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "old-daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		reads.Add(1)
		if params.FetchCursor != "" {
			t.Fatal("old daemon received fetch cursor")
		}
		return appwire.ThreadReadResponse{
			Thread: appwire.Thread{ID: "compat-pre-before", SessionID: "compat-pre-before", Evener: appwire.EvenerThread{Ref: params.Ref}, Turns: []appwire.Turn{{
				ID: "turn-newest", Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFragment, Items: []appwire.ThreadItem{{
					ID: "newest", Type: "agentMessage", TurnID: "turn-newest", TranscriptKey: "newest", Position: &newestPosition, Version: 1, Text: "newest",
				}}, HasEarlierItems: true,
			}}},
			OlderCursor: "native-older", Snapshot: &appwire.SnapshotIdentity{Incarnation: "compat-pre-before-snapshot", Length: 41},
		}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadTurnsList, func(_ context.Context, _ appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
		pages.Add(1)
		return appwire.ThreadTurnsListResponse{}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()
	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{
			Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws" + strings.TrimPrefix(daemonHTTP.URL, "http"), SourceID: "local", ThreadID: "compat-pre-before", SessionID: "compat-pre-before", WorkspaceRef: "local:compat-pre-before"},
			CapabilitiesKnown: true,
		}}
	}, http.DefaultClient)
	sources := appsource.NewRegistry()
	sources.Add(source)
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex("")}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", Version: "test", SourceID: "local"})
	operations := &sessionOperations{cfg: cfg, sources: sources, relays: newHubRelay(server, cfg, sources), server: server}
	window := appwire.ThreadWindowParams{TurnWindow: &appwire.ThreadTurnWindow{Latest: 1}}
	identity := appitempaging.CursorIdentity{
		ThreadRef: "local:compat-pre-before", Incarnation: window.ViewIncarnation("compat-pre-before-snapshot", appwire.TranscriptItemPageLimit), ProjectionVersion: appitempaging.TranscriptItemProjectionVersion,
	}
	fetchCursor, err := appitempaging.EncodeFetchCursor(identity, appitempaging.FetchCursor{
		Position: targetPosition, ItemVersion: 1, Origin: appitempaging.FetchCursorOriginHubCompat,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = operations.fetch(t.Context(), appwire.SessionFetchRequest{Ref: identity.ThreadRef, FetchCursor: fetchCursor})
	wire, ok := errors.AsType[appwire.WireError](err)
	if !ok {
		t.Fatalf("pre-Before compatibility fetch = %T %v, want typed wire error", err, err)
	}
	data, _ := appwire.DecodedErrorData(wire.Data)
	if data.EvenerErrorInfo != appwire.ErrorItemUnreachableOnCompat {
		t.Fatalf("pre-Before compatibility fetch info = %q", data.EvenerErrorInfo)
	}
	if reads.Load() != 1 || pages.Load() != 0 {
		t.Fatalf("pre-Before compatibility fetch calls = reads %d pages %d, want 1 and 0", reads.Load(), pages.Load())
	}
}

func TestSessionOperationsBoundsUnreachableHubCompatibilityFetch(t *testing.T) {
	targetPosition := appwire.ThreadItemPosition{Entry: 1}
	target := appwire.ThreadItem{
		ID: "compat-unreachable", Type: "agentMessage", TurnID: "turn-all", TranscriptKey: "compat-unreachable",
		Position: &targetPosition, Version: 9, Text: "old target",
	}
	items := []appwire.ThreadItem{target}
	newerCount := (hubCompatFetchPageLimit + 1) * appwire.TranscriptItemPageLimit
	for i := range newerCount {
		position := appwire.ThreadItemPosition{Entry: uint64(i + 2)}
		items = append(items, appwire.ThreadItem{
			ID: fmt.Sprintf("arrival-%03d", i), Type: "agentMessage", TurnID: "turn-all", TranscriptKey: fmt.Sprintf("arrival-%03d", i),
			Position: &position, Version: 1, Text: "new",
		})
	}
	newest := items[len(items)-appwire.TranscriptItemPageLimit:]
	var reads, pages atomic.Int64
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "old-daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		reads.Add(1)
		return appwire.ThreadReadResponse{
			Thread: appwire.Thread{ID: "compat-unreachable", SessionID: "compat-unreachable", Evener: appwire.EvenerThread{Ref: params.Ref}, Turns: []appwire.Turn{{
				ID: "turn-all", Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFragment, Items: newest, HasEarlierItems: true,
			}}},
			OlderCursor: "native-older", Snapshot: &appwire.SnapshotIdentity{Incarnation: "compat-unreachable-snapshot", Length: int64(len(items))},
		}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadTurnsList, func(_ context.Context, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
		pages.Add(1)
		if params.Before == nil || params.Cursor != "" {
			t.Fatalf("compat traversal params = %+v, want exclusive before without cursor", params)
		}
		eligible := make([]appwire.ThreadItem, 0, appwire.TranscriptItemPageLimit)
		for _, item := range items {
			if appitempaging.ComparePosition(*item.Position, *params.Before) < 0 {
				eligible = append(eligible, item)
			}
		}
		if len(eligible) > appwire.TranscriptItemPageLimit {
			eligible = eligible[len(eligible)-appwire.TranscriptItemPageLimit:]
		}
		return appwire.ThreadTurnsListResponse{
			Data:     []appwire.Turn{{ID: "turn-all", Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFragment, Items: eligible}},
			Snapshot: &appwire.SnapshotIdentity{Incarnation: "compat-unreachable-snapshot", Length: int64(len(items))},
		}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()
	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{
			Entry:             rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws" + strings.TrimPrefix(daemonHTTP.URL, "http"), SourceID: "local", ThreadID: "compat-unreachable", SessionID: "compat-unreachable", WorkspaceRef: "local:compat-unreachable"},
			Capabilities:      appwire.ThreadCapabilities{PageBefore: true},
			CapabilitiesKnown: true,
		}}
	}, http.DefaultClient)
	sources := appsource.NewRegistry()
	sources.Add(source)
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex("")}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", Version: "test", SourceID: "local"})
	operations := &sessionOperations{cfg: cfg, sources: sources, relays: newHubRelay(server, cfg, sources), server: server}
	window := appwire.ThreadWindowParams{TurnWindow: &appwire.ThreadTurnWindow{Latest: 1}}
	identity := appitempaging.CursorIdentity{
		ThreadRef: "local:compat-unreachable", Incarnation: window.ViewIncarnation("compat-unreachable-snapshot", appwire.TranscriptItemPageLimit), ProjectionVersion: appitempaging.TranscriptItemProjectionVersion,
	}
	fetchCursor, err := appitempaging.EncodeFetchCursor(identity, appitempaging.FetchCursor{
		Position: targetPosition, ItemVersion: target.Version - 1, Origin: appitempaging.FetchCursorOriginHubCompat,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = operations.fetch(t.Context(), appwire.SessionFetchRequest{Ref: identity.ThreadRef, FetchCursor: fetchCursor})
	wire, ok := errors.AsType[appwire.WireError](err)
	if !ok {
		t.Fatalf("cap-exceeded compatibility fetch = %T %v, want typed wire error", err, err)
	}
	data, _ := appwire.DecodedErrorData(wire.Data)
	if data.EvenerErrorInfo != appwire.ErrorInfo("item-unreachable-on-compat") {
		t.Fatalf("cap-exceeded compatibility fetch info = %q", data.EvenerErrorInfo)
	}
	if reads.Load() != 1 || pages.Load() != int64(hubCompatFetchPageLimit) {
		t.Fatalf("bounded compat calls = reads %d pages %d, want 1 and %d", reads.Load(), pages.Load(), hubCompatFetchPageLimit)
	}
}

func TestSessionOperationsFetchesTranslatedRemoteItem(t *testing.T) {
	position := appwire.ThreadItemPosition{Entry: 11, Item: 2}
	item := appwire.ThreadItem{
		ID: "remote-tool", Type: "commandExecution", TurnID: "turn-remote", TranscriptKey: "remote-tool", ToolName: "shell",
		ArgumentsJSON: `{"emoji":"🌒"}`, Output: strings.Repeat("remote-output", 30_000), Raw: json.RawMessage(`{"nested":"remote"}`),
		Actor: &appwire.SessionActor{Kind: "manager", ManagerRef: "local:manager-on-remote"}, Position: &position, Version: 9,
	}
	remote, calls := newScriptedRemoteHub(t, func(method string, params json.RawMessage) any {
		switch method {
		case appwire.MethodInitialize:
			return appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion, SourceID: "local"}
		case appwire.MethodThreadRead:
			var read appwire.ThreadReadParams
			if err := json.Unmarshal(params, &read); err != nil {
				t.Fatal(err)
			}
			if read.FetchCursor != "" {
				t.Fatalf("controller forwarded fetch cursor to remote hub: %+v", read)
			}
			return appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID: "remote", Source: "local", Evener: appwire.EvenerThread{Ref: "local:remote"},
				Turns: []appwire.Turn{{ID: "turn-remote", Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFull, Items: []appwire.ThreadItem{item}}},
			}}
		default:
			return appwire.EmptyResponse{}
		}
	})
	source := appsource.NewRemoteHubSource("host-b", nil, func(context.Context, string) (*appwire.Client, error) { return remote, nil })
	sources := appsource.NewRegistry()
	sources.Add(source)
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex("")}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", Version: "test", SourceID: "local"})
	operations := &sessionOperations{cfg: cfg, sources: sources, relays: newHubRelay(server, cfg, sources), server: server}
	caller := appwire.ManagerView{Ref: "local:manager", Manager: true}

	window, err := operations.Invoke(t.Context(), caller, appwire.SessionOperationRequest{Turns: &appwire.ThreadTurnsListParams{
		Ref: "host-b:remote", ThreadWindowParams: appwire.ThreadWindowParams{
			TurnWindow: &appwire.ThreadTurnWindow{Turn: "turn-remote"}, ItemTypes: []string{"commandExecution"}, PreviewBytes: 64,
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if window.Turns == nil || len(window.Turns.Data) != 1 || len(window.Turns.Data[0].Items) != 1 || window.Turns.Data[0].Items[0].FetchCursor == "" {
		t.Fatalf("bounded remote window = %+v, want one fetchable tool item", window.Turns)
	}

	wantItem := item
	wantActor := *item.Actor
	wantActor.ManagerRef = "host-b:manager-on-remote"
	wantItem.Actor = &wantActor
	want, err := json.Marshal(wantItem)
	if err != nil {
		t.Fatal(err)
	}
	cursor := window.Turns.Data[0].Items[0].FetchCursor
	var got []byte
	for cursor != "" {
		response, err := operations.Invoke(t.Context(), caller, appwire.SessionOperationRequest{Fetch: &appwire.SessionFetchRequest{Ref: "host-b:remote", FetchCursor: cursor}})
		if err != nil {
			t.Fatal(err)
		}
		fragment, err := base64.StdEncoding.DecodeString(response.Fetch.Data)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, fragment...)
		cursor = response.Fetch.FetchCursor
	}
	if !slices.Equal(got, want) {
		t.Fatalf("fetched translated canonical bytes differ: got %d bytes, want %d", len(got), len(want))
	}
	reads := 0
	for _, call := range calls() {
		if call.method == appwire.MethodThreadRead {
			reads++
		}
	}
	if reads < 2 {
		t.Fatalf("remote thread reads = %d, want initial window plus fetch reread", reads)
	}
}

func TestSessionOperationsReportsRemoteItemLostBeforeFetch(t *testing.T) {
	position := appwire.ThreadItemPosition{Entry: 3}
	item := appwire.ThreadItem{
		ID: "remote-tool", Type: "commandExecution", TurnID: "turn-remote", TranscriptKey: "remote-tool", ToolName: "shell",
		Output: strings.Repeat("remote-output", 100), Position: &position, Version: 2,
	}
	var reads atomic.Int32
	remote, _ := newScriptedRemoteHub(t, func(method string, _ json.RawMessage) any {
		switch method {
		case appwire.MethodInitialize:
			return appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion, SourceID: "local"}
		case appwire.MethodThreadRead:
			thread := appwire.Thread{ID: "remote", Source: "local", Evener: appwire.EvenerThread{Ref: "local:remote"}}
			if reads.Add(1) == 1 {
				thread.Turns = []appwire.Turn{{ID: "turn-remote", Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFull, Items: []appwire.ThreadItem{item}}}
			}
			return appwire.ThreadReadResponse{Thread: thread}
		default:
			return appwire.EmptyResponse{}
		}
	})
	source := appsource.NewRemoteHubSource("host-b", nil, func(context.Context, string) (*appwire.Client, error) { return remote, nil })
	sources := appsource.NewRegistry()
	sources.Add(source)
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex("")}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", Version: "test", SourceID: "local"})
	operations := &sessionOperations{cfg: cfg, sources: sources, relays: newHubRelay(server, cfg, sources), server: server}
	caller := appwire.ManagerView{Ref: "local:manager", Manager: true}

	window, err := operations.Invoke(t.Context(), caller, appwire.SessionOperationRequest{Turns: &appwire.ThreadTurnsListParams{
		Ref: "host-b:remote", ThreadWindowParams: appwire.ThreadWindowParams{
			TurnWindow: &appwire.ThreadTurnWindow{Turn: "turn-remote"}, ItemTypes: []string{"commandExecution"}, PreviewBytes: 8,
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	cursor := window.Turns.Data[0].Items[0].FetchCursor
	_, err = operations.Invoke(t.Context(), caller, appwire.SessionOperationRequest{Fetch: &appwire.SessionFetchRequest{Ref: "host-b:remote", FetchCursor: cursor}})
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) {
		t.Fatalf("lost remote item error = %T %v", err, err)
	}
	data, ok := appwire.DecodedErrorData(wireErr.Data)
	if !ok || data.EvenerErrorInfo != appwire.ErrorFetchUnavailableRemote {
		t.Fatalf("lost remote item error data = %#v", wireErr.Data)
	}
}

func TestSessionOperationsTurnsPreservesWindowSelectorsOnInitialRead(t *testing.T) {
	readParams := make(chan appwire.ThreadReadParams, 1)
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		readParams <- params
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "windowed", SessionID: "windowed", Evener: appwire.EvenerThread{Ref: params.Ref}}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()
	endpoint := "ws" + strings.TrimPrefix(daemonHTTP.URL, "http")
	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{Entry: rendezvous.Entry{
			Protocol: appwire.ProtocolVersion, Endpoint: endpoint, SourceID: "local",
			ThreadID: "windowed", SessionID: "windowed", WorkspaceRef: "local:windowed",
		}}}
	}, http.DefaultClient)
	sources := appsource.NewRegistry()
	sources.Add(source)
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex("")}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", Version: "test", SourceID: "local"})
	operations := &sessionOperations{cfg: cfg, sources: sources, relays: newHubRelay(server, cfg, sources), server: server}

	window := appwire.ThreadWindowParams{
		TurnWindow:        &appwire.ThreadTurnWindow{Since: "turn_2", Limit: 4},
		ItemTypes:         []string{"messages", "reasoning"},
		PreviewBytes:      1234,
		MessageBytes:      5678,
		WindowBudgetBytes: 90 << 10,
	}
	_, err := operations.turns(t.Context(), appwire.ThreadTurnsListParams{
		Ref: "local:windowed", ItemLimit: 7, ThreadWindowParams: window,
	})
	if err != nil {
		t.Fatalf("initial windowed turns read: %v", err)
	}
	got := <-readParams
	if !got.IncludeTurns || got.Ref != "local:windowed" || got.ItemLimit != 7 {
		t.Fatalf("base read params = %+v", got)
	}
	if got.TurnWindow == nil || *got.TurnWindow != *window.TurnWindow ||
		!slices.Equal(got.ItemTypes, window.ItemTypes) ||
		got.PreviewBytes != window.PreviewBytes || got.MessageBytes != window.MessageBytes ||
		got.WindowBudgetBytes != window.WindowBudgetBytes {
		t.Fatalf("window selectors = %+v, want %+v", got.ThreadWindowParams, window)
	}
}

func TestSessionOperationsTurnsPacksWindowByExactType(t *testing.T) {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		turnID := "turn_1"
		items := []appwire.ThreadItem{
			{Type: "agentMessage", ID: "message", TurnID: turnID, TranscriptKey: "message", Position: &appwire.ThreadItemPosition{Entry: 1}, Text: strings.Repeat("answer", 2_000), Version: 1},
			{Type: "commandExecution", ID: "tool", TurnID: turnID, TranscriptKey: "tool", Position: &appwire.ThreadItemPosition{Entry: 2}, Output: strings.Repeat("tool output", 20_000), Version: 2},
		}
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID: "windowed", SessionID: "windowed", Evener: appwire.EvenerThread{Ref: params.Ref},
			Turns: []appwire.Turn{{ID: turnID, Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFull, Items: items}},
		}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()
	endpoint := "ws" + strings.TrimPrefix(daemonHTTP.URL, "http")
	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{Entry: rendezvous.Entry{
			Protocol: appwire.ProtocolVersion, Endpoint: endpoint, SourceID: "local",
			ThreadID: "windowed", SessionID: "windowed", WorkspaceRef: "local:windowed",
		}}}
	}, http.DefaultClient)
	sources := appsource.NewRegistry()
	sources.Add(source)
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex("")}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", Version: "test", SourceID: "local"})
	operations := &sessionOperations{cfg: cfg, sources: sources, relays: newHubRelay(server, cfg, sources), server: server}

	page, err := operations.turns(t.Context(), appwire.ThreadTurnsListParams{
		Ref: "local:windowed",
		ThreadWindowParams: appwire.ThreadWindowParams{
			TurnWindow: &appwire.ThreadTurnWindow{Latest: 1},
			ItemTypes:  []string{"messages"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Data) != 1 {
		t.Fatalf("turn count = %d, want 1", len(page.Data))
	}
	ids := make([]string, len(page.Data[0].Items))
	for i := range page.Data[0].Items {
		ids[i] = page.Data[0].Items[i].ID
	}
	if !slices.Equal(ids, []string{"message"}) {
		t.Fatalf("packed item IDs = %v, want [message]", ids)
	}
	if page.Data[0].Elision == nil || page.Data[0].Elision.ExcludedByType["commandExecution"] != 1 {
		t.Fatalf("turn elision = %+v, want one excluded commandExecution", page.Data[0].Elision)
	}
	if item := page.Data[0].Items[0]; !item.Truncated || item.FetchCursor == "" {
		t.Fatalf("bounded message = %+v, want labeled truncation with fetch cursor", item)
	}
	if page.Window == nil || page.Window.AppliedBy != "serving-daemon" {
		t.Fatalf("window result = %+v, want serving-daemon", page.Window)
	}
}

func TestSessionOperationsTurnsContinuesInsideWindowedTurn(t *testing.T) {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		items := make([]appwire.ThreadItem, 25)
		for i := range items {
			position := appwire.ThreadItemPosition{Entry: uint64(i)}
			items[i] = appwire.ThreadItem{
				Type: "agentMessage", ID: fmt.Sprintf("message-%02d", i), TurnID: "turn_1",
				TranscriptKey: fmt.Sprintf("message-%02d", i), Position: &position, Text: "answer", Version: uint64(i + 1),
			}
		}
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID: "windowed", SessionID: "windowed", Evener: appwire.EvenerThread{Ref: params.Ref},
			Turns: []appwire.Turn{{ID: "turn_1", Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFull, Items: items}},
		}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()
	endpoint := "ws" + strings.TrimPrefix(daemonHTTP.URL, "http")
	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{Entry: rendezvous.Entry{
			Protocol: appwire.ProtocolVersion, Endpoint: endpoint, SourceID: "local",
			ThreadID: "windowed", SessionID: "windowed", WorkspaceRef: "local:windowed",
		}}}
	}, http.DefaultClient)
	sources := appsource.NewRegistry()
	sources.Add(source)
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex("")}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", Version: "test", SourceID: "local"})
	operations := &sessionOperations{cfg: cfg, sources: sources, relays: newHubRelay(server, cfg, sources), server: server}

	params := appwire.ThreadTurnsListParams{
		Ref: "local:windowed", ItemLimit: 10,
		ThreadWindowParams: appwire.ThreadWindowParams{TurnWindow: &appwire.ThreadTurnWindow{Turn: "turn_1"}, ItemTypes: []string{"messages"}},
	}
	var got []string
	for {
		page, err := operations.turns(t.Context(), params)
		if err != nil {
			t.Fatalf("window page %d: %v", len(got)/10+1, err)
		}
		if len(page.Data) != 1 {
			t.Fatalf("window page turn count = %d, want 1", len(page.Data))
		}
		for _, item := range page.Data[0].Items {
			got = append(got, item.ID)
		}
		if page.Data[0].Elision == nil || page.Data[0].Elision.ContinuationCursor == "" {
			break
		}
		params.Cursor = page.Data[0].Elision.ContinuationCursor
	}
	want := make([]string, 25)
	for i := range want {
		want[i] = fmt.Sprintf("message-%02d", i)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("continued item IDs = %v, want every item once in transcript order", got)
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

// TestManagerSnapshotReadDoesNotAskDaemonForSubscription pins the upstream
// half of the snapshot rule. A Manager status read without a watch has no
// consumer, so it must not ask the daemon for a subscription: while it did,
// the daemon counted a subscriber for the read's duration, and a curable
// sandbox denial in that window raised an approval card nobody received.
func TestManagerSnapshotReadDoesNotAskDaemonForSubscription(t *testing.T) {
	var mu sync.Mutex
	asked := map[string][]bool{}
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		ref, err := appwire.ParseRef(params.Ref)
		if err != nil {
			return appwire.ThreadReadResponse{}, err
		}
		mu.Lock()
		asked[params.Ref] = append(asked[params.Ref], params.Subscribe)
		mu.Unlock()
		if params.Subscribe {
			if err := appserver.SubscribeWithAdmission(ctx, ref.ThreadID, false); err != nil {
				return appwire.ThreadReadResponse{}, err
			}
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

	// An ordinary human read captures a downstream subscription, so its
	// relay read legitimately asks the daemon for the feed.
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:human"}); err != nil {
		t.Fatalf("ordinary human ThreadRead: %v", err)
	}
	for _, params := range []appwire.ThreadReadParams{
		{Ref: "local:manager"},
		// The snapshot path must not subscribe even when the caller's own
		// params carry a stale Subscribe flag.
		{Ref: "local:manager", Subscribe: true},
	} {
		var snapshot appwire.ThreadReadResponse
		if err := client.Request(context.Background(), "test/managerSnapshotRead", params, &snapshot); err != nil {
			t.Fatalf("Manager snapshot read: %v", err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	flags := asked["local:manager"]
	if len(flags) == 0 {
		t.Fatal("manager snapshot read never reached the daemon")
	}
	for _, f := range flags {
		if f {
			t.Fatal("manager snapshot read asked the daemon for a subscription")
		}
	}
	human := asked["local:human"]
	if len(human) == 0 {
		t.Fatal("ordinary human read never reached the daemon")
	}
	fed := false
	for _, f := range human {
		fed = fed || f
	}
	if !fed {
		t.Fatal("ordinary human read no longer asks the daemon for the feed")
	}
}

// TestManagerRemoteSnapshotReadLeavesTargetDaemonUnsubscribed is the two-hub
// half of the snapshot rule pinned above. An unwatched Manager read crosses
// the hub bridge as an ordinary thread/read whose subscribe flag the remote
// source strips, so the target hub must treat a bridge-origin read as the
// snapshot it is. Capturing a downstream subscription there would register a
// daemon-side feed and a bridge-connection subscription that no hub owns a
// consumer for and no side can retire.
func TestManagerRemoteSnapshotReadLeavesTargetDaemonUnsubscribed(t *testing.T) {
	var mu sync.Mutex
	asked := map[string][]bool{}
	daemonB := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemonB.Router(), appwire.MethodThreadRead, func(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		ref, err := appwire.ParseRef(params.Ref)
		if err != nil {
			return appwire.ThreadReadResponse{}, err
		}
		mu.Lock()
		asked[params.Ref] = append(asked[params.Ref], params.Subscribe)
		mu.Unlock()
		if params.Subscribe {
			if err := appserver.SubscribeWithAdmission(ctx, ref.ThreadID, false); err != nil {
				return appwire.ThreadReadResponse{}, err
			}
		}
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: ref.ThreadID, SessionID: ref.ThreadID, Evener: appwire.EvenerThread{Ref: params.Ref}}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemonB.ServeWebSocket))
	defer daemonHTTP.Close()
	endpoint := "ws" + strings.TrimPrefix(daemonHTTP.URL, "http")
	sourcesB := appsource.NewRegistry()
	sourcesB.Add(appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: endpoint, SourceID: "local", ThreadID: "manager", SessionID: "manager", WorkspaceRef: "local:manager"}}}
	}, http.DefaultClient))
	cfgB := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex("")}
	serverB := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", Version: "test", SourceID: "local"})
	relaysB := newHubRelay(serverB, cfgB, sourcesB)
	operationsB := &sessionOperations{cfg: cfgB, sources: sourcesB, relays: relaysB, server: serverB}
	appserver.HandleTyped(serverB.Router(), appwire.MethodThreadRead, operationsB.Read)
	// The target hub's edge stamps the bridge marker on the connection, as
	// serveAppWireRPC does for `evener hub attach --stdio`.
	hubB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverB.ServeWebSocket(w, r.WithContext(withHostRoutingOrigin(r.Context(), hostRoutingOriginBridge)))
	}))
	defer hubB.Close()

	// The controller hub: a remote source over the bridge connection, and the
	// same session operations an unwatched Manager status read takes.
	sourcesA := appsource.NewRegistry()
	sourcesA.Add(appsource.NewRemoteHubSource("host-b", nil, func(ctx context.Context, host string) (*appwire.Client, error) {
		return dialHubRPCInitialized(t, hubB), nil
	}))
	cfgA := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex("")}
	serverA := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", Version: "test", SourceID: "local"})
	relaysA := newHubRelay(serverA, cfgA, sourcesA)
	operationsA := &sessionOperations{cfg: cfgA, sources: sourcesA, relays: relaysA, server: serverA}
	if _, err := operationsA.read(t.Context(), appwire.ThreadReadParams{Ref: "host-b:manager"}, false); err != nil {
		t.Fatalf("unwatched remote Manager read: %v", err)
	}
	if got := daemonB.SubscriberCount("manager"); got != 0 {
		t.Fatalf("target daemon subscribers after unwatched read = %d, want 0", got)
	}
	if got := serverB.SubscriberCount("local:manager"); got != 0 {
		t.Fatalf("target hub subscribers after unwatched read = %d, want 0", got)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, flag := range asked["local:manager"] {
		if flag {
			t.Fatal("unwatched remote Manager read asked the target daemon for a subscription")
		}
	}
}

// A remote hub that loses its daemon answers a non-subscribing thread/read from
// saved history. LiveOnly is hub-internal and never crosses the bridge, so the
// controller must classify that daemonless answer as a retryable outage itself;
// otherwise the voice bridge accepts it as a replaced manager and closes.
func TestHubRPCThreadReadLiveOnlyRejectsRemoteDaemonlessHistory(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-remote-live-only-0000000001")
	sessionID := buildRPCParentSession(t, stateDir)
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))
	lease := &scriptedRelaySessionLease{
		deliveries: make(chan appsource.RelayDelivery),
		readFunc: func(appwire.ThreadReadParams) (appsource.RelayReadResult, error) {
			return appsource.RelayReadResult{}, appwire.RetryableSessionUnavailable("relay connection ended before route publication")
		},
	}
	sourcesB := appsource.NewRegistry()
	sourcesB.Add(&relaySessionTestSource{thread: liveRetryingThread(sessionID), lease: lease})
	serverB := newHubAppServer(hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: past}, sourcesB)
	hubB := httptest.NewServer(http.HandlerFunc(serverB.ServeWebSocket))
	t.Cleanup(hubB.Close)
	remote := dialHubRPCInitialized(t, hubB)

	sourcesA := appsource.NewRegistry()
	sourcesA.Add(appsource.NewRemoteHubSource("host-b", nil, func(context.Context, string) (*appwire.Client, error) {
		return remote, nil
	}))
	cfgA := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex("")}
	serverA := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", Version: "test", SourceID: "local"})
	operationsA := &sessionOperations{cfg: cfgA, sources: sourcesA, relays: newHubRelay(serverA, cfgA, sourcesA), server: serverA}
	params := appwire.ThreadReadParams{Ref: "host-b:" + sessionID, IncludeTurns: true, ItemLimit: appwire.TranscriptItemPageLimit}

	saved, err := operationsA.read(t.Context(), params, false)
	if err != nil || saved.BootGeneration != appwire.DaemonlessBootGeneration {
		t.Fatalf("plain remote read = boot %q err %v, want the saved daemonless page", saved.BootGeneration, err)
	}
	params.LiveOnly = true
	if _, err := operationsA.read(t.Context(), params, false); err == nil || !voiceRelayOutage(err) {
		t.Fatalf("live-only remote read = %v, want a retryable relay outage", err)
	}
}

// The daemonless marker is the primary predicate; the authority fallback covers
// an older peer that serves saved history without the marker.
func TestLiveReadDaemonlessPredicate(t *testing.T) {
	live := appwire.ThreadReadResponse{Thread: appwire.Thread{Evener: appwire.EvenerThread{MutationStateAuthoritative: true}}}
	if liveReadDaemonless(live) {
		t.Fatal("an authoritative live read was classified as daemonless")
	}
	descendant := appwire.ThreadReadResponse{Thread: appwire.Thread{Evener: appwire.EvenerThread{MutationStateAuthoritative: false}}}
	if liveReadDaemonless(descendant) {
		t.Fatal("a live descendant read was classified as daemonless")
	}
	marked := live
	marked.BootGeneration = appwire.DaemonlessBootGeneration
	if !liveReadDaemonless(marked) {
		t.Fatal("a daemonless read with the marker was not classified")
	}
	unmarkedSavedManager := live
	unmarkedSavedManager.Thread.Evener.MutationStateAuthoritative = false
	unmarkedSavedManager.Thread.Evener.Manager = true
	if !liveReadDaemonless(unmarkedSavedManager) {
		t.Fatal("an unmarked saved manager page was not classified as daemonless")
	}
}

// A bridge-origin snapshot read (capture=false) that loses its daemon between
// the read and handoff Prepare must hand the controller a retryable outage.
// Otherwise the controller's retry budget preserves an unmarked
// SessionUnavailable, which the voice bridge treats as terminal.
func TestHubRPCThreadReadBridgeSnapshotPrepareFailureIsRetryable(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-remote-snapshot-0000000001")
	sessionID := buildRPCParentSession(t, stateDir)
	lease := &scriptedRelaySessionLease{
		deliveries: make(chan appsource.RelayDelivery),
		readFunc: func(appwire.ThreadReadParams) (appsource.RelayReadResult, error) {
			return appsource.RelayReadResult{
				Response: appwire.ThreadReadResponse{Thread: liveRetryingThread(sessionID), BootGeneration: "7"},
				Handoff:  &guardedRelayHandoff{prepareAllowed: false},
			}, nil
		},
	}
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))
	sourcesB := appsource.NewRegistry()
	sourcesB.Add(&relaySessionTestSource{thread: liveRetryingThread(sessionID), lease: lease})
	serverB := newHubAppServer(hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: past}, sourcesB)
	hubB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverB.ServeWebSocket(w, r.WithContext(withHostRoutingOrigin(r.Context(), hostRoutingOriginBridge)))
	}))
	t.Cleanup(hubB.Close)
	remote := dialHubRPCInitialized(t, hubB)

	var reads atomic.Int32
	sourcesA := appsource.NewRegistry()
	sourcesA.Add(appsource.NewRemoteHubSource("host-b", nil, func(context.Context, string) (*appwire.Client, error) {
		return remote, nil
	}))
	cfgA := hubcore.WebConfig{
		HubStateRoot: t.TempDir(),
		Past:         hubcore.NewPastIndex(""),
		RelayHooks: hubcore.RelayLifecycleHooks{ReadThreadFailure: func(ctx context.Context, _ appwire.ThreadReadParams) error {
			if reads.Add(1) == 1 {
				return nil
			}
			<-ctx.Done()
			return ctx.Err()
		}},
	}
	serverA := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", Version: "test", SourceID: "local"})
	operationsA := &sessionOperations{cfg: cfgA, sources: sourcesA, relays: newHubRelay(serverA, cfgA, sourcesA), server: serverA}
	_, err := operationsA.read(t.Context(), appwire.ThreadReadParams{
		Ref: "host-b:" + sessionID, IncludeTurns: true,
		ItemLimit: appwire.TranscriptItemPageLimit, LiveOnly: true,
	}, false)
	if err == nil || !voiceRelayOutage(err) {
		t.Fatalf("bridge snapshot prepare failure = %v, want a retryable relay outage", err)
	}
}

// A first live-only read of a remote session starts the controller relay with
// sanitized params. An older remote hub strictly decodes ThreadReadParams and
// rejects the hub-internal liveOnly field, so forwarding the original params
// through startRelay would fail the read after its initial live read succeeded.
func TestHubRPCThreadReadLiveOnlyRemoteSubscriptionSanitizesParams(t *testing.T) {
	const sessionID = "02wMz5Txv9yYdSRJat13MZ"
	var subscribeRequests, liveOnlyRejected atomic.Int32
	olderPeer := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", Version: "test", SourceID: "local"})
	olderPeer.Router().Handle(appwire.MethodThreadRead, func(_ context.Context, raw json.RawMessage) (any, error) {
		var fields map[string]json.RawMessage
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &fields); err != nil {
				return nil, appwire.InvalidParams(err.Error())
			}
		}
		if _, ok := fields["liveOnly"]; ok {
			liveOnlyRejected.Add(1)
			return nil, appwire.InvalidParams(`json: unknown field "liveOnly"`)
		}
		var params appwire.ThreadReadParams
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &params); err != nil {
				return nil, appwire.InvalidParams(err.Error())
			}
		}
		if params.Subscribe {
			subscribeRequests.Add(1)
		}
		return appwire.ThreadReadResponse{Thread: liveRetryingThread(sessionID), BootGeneration: "7"}, nil
	})
	hubB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		olderPeer.ServeWebSocket(w, r.WithContext(withHostRoutingOrigin(r.Context(), hostRoutingOriginBridge)))
	}))
	t.Cleanup(hubB.Close)
	remote := dialHubRPCInitialized(t, hubB)

	sourcesA := appsource.NewRegistry()
	sourcesA.Add(appsource.NewRemoteHubSource("host-b", nil, func(context.Context, string) (*appwire.Client, error) {
		return remote, nil
	}))
	cfgA := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex("")}
	serverA := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", Version: "test", SourceID: "local"})
	operationsA := &sessionOperations{cfg: cfgA, sources: sourcesA, relays: newHubRelay(serverA, cfgA, sourcesA), server: serverA}
	_, err := operationsA.read(t.Context(), appwire.ThreadReadParams{
		Ref: "host-b:" + sessionID, IncludeTurns: true,
		ItemLimit: appwire.TranscriptItemPageLimit, LiveOnly: true,
	}, true)
	if err != nil {
		t.Fatalf("first live-only remote read: %v", err)
	}
	if got := liveOnlyRejected.Load(); got != 0 {
		t.Fatalf("subscription requests forwarded liveOnly to the older peer %d times", got)
	}
	if got := subscribeRequests.Load(); got == 0 {
		t.Fatal("the first live-only remote read did not start the controller relay")
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
