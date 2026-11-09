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
	"reflect"
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

type movingWindowManagerSource struct {
	*managerReadSource
	turns []appwire.Turn
}

type movingLegacyManagerSource struct {
	*movingWindowManagerSource
}

type compatibilityWindowManagerSource struct {
	*managerReadSource
	t         *testing.T
	read      appwire.ThreadReadResponse
	pages     map[string]appwire.ThreadTurnsListResponse
	readCalls []appwire.ThreadReadParams
	pageCalls []appwire.ThreadTurnsListParams
}

func (*compatibilityWindowManagerSource) CachedThreadCapabilities(string, string) (appwire.ThreadCapabilities, bool) {
	return appwire.ThreadCapabilities{}, true
}

func (s *compatibilityWindowManagerSource) ReadThread(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	s.readCalls = append(s.readCalls, params)
	if params.TurnWindow != nil || len(params.ItemTypes) != 0 || params.PreviewBytes != 0 || params.MessageBytes != 0 || params.WindowBudgetBytes != 0 {
		s.t.Fatalf("legacy ReadThread received window fields: %+v", params)
	}
	response := s.read
	response.Thread = appwire.CloneThread(response.Thread)
	response.Snapshot = appwire.ClonePointer(response.Snapshot)
	return response, nil
}

func (s *compatibilityWindowManagerSource) ListTurns(_ context.Context, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
	s.pageCalls = append(s.pageCalls, params)
	if params.TurnWindow != nil || len(params.ItemTypes) != 0 || params.PreviewBytes != 0 || params.MessageBytes != 0 || params.WindowBudgetBytes != 0 {
		s.t.Fatalf("legacy ListTurns received window fields: %+v", params)
	}
	page, ok := s.pages[params.Cursor]
	if !ok {
		return appwire.ThreadTurnsListResponse{}, appwire.TranscriptItemCursorStale()
	}
	page.Data = appwire.CloneThread(appwire.Thread{Turns: page.Data}).Turns
	page.Snapshot = appwire.ClonePointer(page.Snapshot)
	return page, nil
}

func (*movingLegacyManagerSource) CachedThreadCapabilities(string, string) (appwire.ThreadCapabilities, bool) {
	return appwire.ThreadCapabilities{}, true
}

func (*movingWindowManagerSource) CachedThreadCapabilities(string, string) (appwire.ThreadCapabilities, bool) {
	return appwire.ThreadCapabilities{WindowRead: true}, true
}

func (s *movingWindowManagerSource) ReadThread(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	turns := s.turns
	if params.TurnWindow != nil {
		turns = testResolveTurnWindow(turns, *params.TurnWindow)
	}
	return appwire.ThreadReadResponse{
		Thread:   appwire.Thread{ID: "moving", SessionID: "moving", Evener: appwire.EvenerThread{Ref: params.Ref}, Turns: turns},
		Snapshot: &appwire.SnapshotIdentity{Incarnation: "fixture", Length: int64(len(s.turns))},
	}, nil
}

func movingWindowTestTurns() []appwire.Turn {
	turns := make([]appwire.Turn, 0, 4)
	for i, id := range []string{"turn-a", "turn-b", "turn-c", "turn-d"} {
		position := appwire.ThreadItemPosition{Entry: uint64(i + 1)}
		turns = append(turns, appwire.Turn{
			ID: id, Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFull,
			Items: []appwire.ThreadItem{{
				ID: id + "-reasoning", Type: "reasoning", TurnID: id, TranscriptKey: id + "-reasoning",
				Text: strings.Repeat(id, 2_500), Position: &position, Version: 1,
			}},
		})
	}
	return turns
}

func requireMovingWindowPage(t *testing.T, page appwire.ThreadTurnsListResponse, withheld int, turnID string) {
	t.Helper()
	if page.Window == nil {
		t.Fatal("moving window page has no window metadata")
	}
	if page.Window.WithheldTurns != withheld || page.Window.ContinuationCursor == "" || len(page.Data) != 1 || page.Data[0].ID != turnID {
		gotTurnID := ""
		if len(page.Data) > 0 {
			gotTurnID = page.Data[0].ID
		}
		t.Fatalf("moving window page = withheld %d, continuation %t, turns %d, first turn %q; want %d, true, 1, %q",
			page.Window.WithheldTurns, page.Window.ContinuationCursor != "", len(page.Data), gotTurnID, withheld, turnID)
	}
}

func newMovingWindowOperations(t *testing.T, turns []appwire.Turn) (*sessionOperations, *movingWindowManagerSource) {
	t.Helper()
	base := &managerReadSource{scriptedAppSource: &scriptedAppSource{
		id: "local", thread: appwire.Thread{ID: "moving", SessionID: "moving", Evener: appwire.EvenerThread{Ref: "local:moving"}},
	}}
	source := &movingWindowManagerSource{managerReadSource: base, turns: turns}
	sources := appsource.NewRegistry()
	sources.Add(source)
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), HubID: "hub-test", Past: hubcore.NewPastIndex("")}
	server := appserver.NewServer(appserver.ServerConfig{})
	return &sessionOperations{cfg: cfg, sources: sources, server: server, relays: newHubRelay(server, cfg, sources)}, source
}

func newCompatibilityWindowOperations(t *testing.T, read appwire.ThreadReadResponse, pages map[string]appwire.ThreadTurnsListResponse) (*sessionOperations, *compatibilityWindowManagerSource) {
	t.Helper()
	ref := read.Thread.Evener.Ref
	base := &managerReadSource{scriptedAppSource: &scriptedAppSource{
		id: "local", thread: appwire.Thread{ID: read.Thread.ID, SessionID: read.Thread.SessionID, Evener: appwire.EvenerThread{Ref: ref}},
	}}
	source := &compatibilityWindowManagerSource{managerReadSource: base, t: t, read: read, pages: pages}
	sources := appsource.NewRegistry()
	sources.Add(source)
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), HubID: "hub-test", Past: hubcore.NewPastIndex("")}
	server := appserver.NewServer(appserver.ServerConfig{})
	return &sessionOperations{cfg: cfg, sources: sources, server: server, relays: newHubRelay(server, cfg, sources)}, source
}

func testResolveTurnWindow(turns []appwire.Turn, window appwire.ThreadTurnWindow) []appwire.Turn {
	if window.Latest > 0 {
		return turns[max(0, len(turns)-window.Latest):]
	}
	index := slices.IndexFunc(turns, func(turn appwire.Turn) bool {
		return turn.ID == window.Turn || turn.ID == window.Since || window.Before != nil && turn.ID == window.Before.TurnID
	})
	if index < 0 {
		return nil
	}
	if window.Turn != "" {
		return turns[index : index+1]
	}
	if window.Since != "" {
		return turns[index+1 : min(len(turns), index+1+window.Limit)]
	}
	return turns[max(0, index-window.Before.Limit):index]
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

func TestSessionOperationsExactWindowPreservesEmptyCheckpointTurn(t *testing.T) {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleThreadHistory(daemon.Router(), func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{
			Thread: appwire.Thread{
				ID: "windowed", SessionID: "windowed", Evener: appwire.EvenerThread{Ref: params.Ref},
				Turns: []appwire.Turn{{ID: "turn_empty", ItemsView: appwire.TurnItemsViewFragment}},
			},
			Snapshot: &appwire.SnapshotIdentity{Incarnation: "empty-checkpoint-incarnation", Length: 1},
		}, nil
	}, func(_ context.Context, _ appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
		t.Fatal("exact empty checkpoint window unexpectedly paged daemon history")
		return appwire.ThreadTurnsListResponse{}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()
	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{
			Entry: rendezvous.Entry{
				Protocol: appwire.ProtocolVersion, Endpoint: "ws" + strings.TrimPrefix(daemonHTTP.URL, "http"), SourceID: "local",
				ThreadID: "windowed", SessionID: "windowed", WorkspaceRef: "local:windowed",
			},
			Capabilities: appwire.ThreadCapabilities{WindowRead: true}, CapabilitiesKnown: true,
		}}
	}, http.DefaultClient)
	sources := appsource.NewRegistry()
	sources.Add(source)
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex("")}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", Version: "test", SourceID: "local"})
	operations := &sessionOperations{cfg: cfg, sources: sources, relays: newHubRelay(server, cfg, sources), server: server}

	response, err := operations.Invoke(t.Context(), appwire.ManagerView{Ref: "local:manager", Manager: true}, appwire.SessionOperationRequest{
		Turns: &appwire.ThreadTurnsListParams{
			Ref: "local:windowed", TurnWindow: &appwire.ThreadTurnWindow{Turn: "turn_empty"}, ItemTypes: []string{"messages"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Turns == nil || len(response.Turns.Data) != 1 || response.Turns.Data[0].ID != "turn_empty" || len(response.Turns.Data[0].Items) != 0 {
		t.Fatalf("exact empty checkpoint window = %+v, want one empty turn shell", response.Turns)
	}
}

func TestSessionOperationsCompatibilityWindowPreservesPagedEmptyTurn(t *testing.T) {
	position := appwire.ThreadItemPosition{Entry: 2}
	newest := appwire.Turn{
		ID: "turn-c", Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFull,
		Items: []appwire.ThreadItem{{
			ID: "message-c", Type: "agentMessage", TurnID: "turn-c", TranscriptKey: "message-c",
			Text: "newest", Position: &position, Version: 1,
		}},
	}
	empty := appwire.Turn{ID: "turn-b", Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFragment, Version: 1}
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleThreadHistory(daemon.Router(), func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		if params.TurnWindow != nil {
			t.Fatalf("older daemon received turn window: %+v", params.TurnWindow)
		}
		return appwire.ThreadReadResponse{
			Thread: appwire.Thread{
				ID: "compat-empty", SessionID: "compat-empty", Evener: appwire.EvenerThread{Ref: params.Ref},
				Turns: []appwire.Turn{newest},
			},
			OlderCursor: "older", Snapshot: &appwire.SnapshotIdentity{Incarnation: "compat-empty-incarnation", Length: 2},
		}, nil
	}, func(_ context.Context, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
		if params.Cursor != "older" || params.TurnWindow != nil {
			t.Fatalf("older daemon page params = %+v", params)
		}
		return appwire.ThreadTurnsListResponse{Data: []appwire.Turn{empty}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()
	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{
			Entry: rendezvous.Entry{
				Protocol: appwire.ProtocolVersion, Endpoint: "ws" + strings.TrimPrefix(daemonHTTP.URL, "http"), SourceID: "local",
				ThreadID: "compat-empty", SessionID: "compat-empty", WorkspaceRef: "local:compat-empty",
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
			Ref: "local:compat-empty", TurnWindow: &appwire.ThreadTurnWindow{Latest: 2}, ItemTypes: []string{"messages"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Turns == nil || !slices.Equal(turnIDsFromTurns(response.Turns.Data), []string{"turn-b", "turn-c"}) {
		t.Fatalf("compatibility window = %+v, want empty turn-b then positioned turn-c", response.Turns)
	}
}

func TestManagerTurnsPublishesPerTurnElisionFollowUp(t *testing.T) {
	items := make([]appwire.ThreadItem, 12)
	for i := range items {
		position := appwire.ThreadItemPosition{Entry: uint64(i)}
		items[i] = appwire.ThreadItem{
			ID: fmt.Sprintf("message-%02d", i), Type: "agentMessage", TurnID: "turn-default",
			TranscriptKey: fmt.Sprintf("message-%02d", i), Position: &position, Version: 1, Text: "visible",
		}
	}
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleThreadHistory(daemon.Router(), func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{
			Thread: appwire.Thread{
				ID: "window-default", SessionID: "window-default", Evener: appwire.EvenerThread{Ref: params.Ref},
				Turns: []appwire.Turn{{ID: "turn-default", ItemsView: appwire.TurnItemsViewFull, Items: items}},
			},
			Snapshot: &appwire.SnapshotIdentity{Incarnation: "window-default-incarnation", Length: int64(len(items))},
		}, nil
	}, func(_ context.Context, _ appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
		t.Fatal("exact default-limit window unexpectedly paged daemon history")
		return appwire.ThreadTurnsListResponse{}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()
	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{
			Entry: rendezvous.Entry{
				Protocol: appwire.ProtocolVersion, Endpoint: "ws" + strings.TrimPrefix(daemonHTTP.URL, "http"), SourceID: "local",
				ThreadID: "window-default", SessionID: "window-default", WorkspaceRef: "local:window-default",
			},
			Capabilities: appwire.ThreadCapabilities{WindowRead: true}, CapabilitiesKnown: true,
		}}
	}, http.DefaultClient)
	sources := appsource.NewRegistry()
	sources.Add(source)
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex("")}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", Version: "test", SourceID: "local"})
	operations := &sessionOperations{cfg: cfg, sources: sources, relays: newHubRelay(server, cfg, sources), server: server}

	params := appwire.ThreadTurnsListParams{
		Ref: "local:window-default", ItemLimit: 2,
		TurnWindow: &appwire.ThreadTurnWindow{Turn: "turn-default"}, ItemTypes: []string{"messages"},
		PreviewBytes: 512, MessageBytes: 1024, WindowBudgetBytes: appwire.ThreadWindowBudgetBytesFloor,
	}
	response, err := operations.Invoke(t.Context(), appwire.ManagerView{Ref: "local:manager", Manager: true}, appwire.SessionOperationRequest{Turns: &params})
	if err != nil {
		t.Fatal(err)
	}
	if response.Turns == nil || len(response.Turns.Data) != 1 || len(response.Turns.Data[0].Items) != params.ItemLimit {
		t.Fatalf("exact window = %+v, want %d items", response.Turns, params.ItemLimit)
	}
	elision := response.Turns.Data[0].Elision
	if elision == nil || elision.CapWithheld != len(items)-params.ItemLimit || elision.ContinuationCursor == "" {
		t.Fatalf("exact-window elision = %+v", elision)
	}
	if response.Turns.NextCursor != "" || response.Turns.Window == nil || response.Turns.Window.ContinuationCursor != "" {
		t.Fatalf("exact window unexpectedly has a page-level continuation: %+v", response.Turns)
	}
	wantFollowUps := []appwire.SessionStatusFollowUp{{
		Tool: "read_transcript",
		Arguments: appwire.SessionStatusArguments{Hub: appwire.SessionStatusTarget{
			ThreadWindowParams: params.ThreadWindowParams,
			Ref:                params.Ref, Cursor: elision.ContinuationCursor, ItemLimit: params.ItemLimit,
		}},
	}}
	if !reflect.DeepEqual(response.FollowUps, wantFollowUps) {
		t.Fatalf("per-turn recovery = %+v, want %+v", response.FollowUps, wantFollowUps)
	}
}

func TestManagerTurnsFitsStructuredFollowUpsWithinResponseCap(t *testing.T) {
	const turnCount = 250
	turns := make([]appwire.Turn, turnCount)
	wantTurnIDs := make([]string, turnCount)
	for turnIndex := range turns {
		turnID := fmt.Sprintf("turn-%03d", turnIndex)
		wantTurnIDs[turnIndex] = turnID
		items := make([]appwire.ThreadItem, 2)
		for itemIndex := range items {
			position := appwire.ThreadItemPosition{Entry: uint64(turnIndex*len(items) + itemIndex + 1)}
			items[itemIndex] = appwire.ThreadItem{
				ID: fmt.Sprintf("message-%03d-%d", turnIndex, itemIndex), Type: "agentMessage", TurnID: turnID,
				TranscriptKey: fmt.Sprintf("message-%03d-%d", turnIndex, itemIndex), Text: "visible",
				Position: &position, Version: 1,
			}
		}
		turns[turnIndex] = appwire.Turn{
			ID: turnID, Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFull, Items: items,
		}
	}
	operations, _ := newMovingWindowOperations(t, turns)
	caller := appwire.ManagerView{Ref: "local:manager", Manager: true}
	params := appwire.ThreadTurnsListParams{
		Ref: "local:moving", ItemLimit: 1,
		TurnWindow: &appwire.ThreadTurnWindow{Latest: turnCount}, ItemTypes: []string{"messages"},
	}

	var gotTurnIDs []string
	for pageCount := 0; pageCount <= turnCount; pageCount++ {
		response, err := operations.Invoke(t.Context(), caller, appwire.SessionOperationRequest{Turns: &params})
		if err != nil {
			t.Fatalf("read page %d: %v", pageCount, err)
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		if len(encoded) > managerReadMaxJSONBytes {
			t.Fatalf("page %d encoded to %d bytes, cap %d", pageCount, len(encoded), managerReadMaxJSONBytes)
		}
		if response.Turns == nil || len(response.Turns.Data) == 0 {
			t.Fatalf("page %d returned no turns", pageCount)
		}
		followUps := make(map[string]bool, len(response.FollowUps))
		for _, followUp := range response.FollowUps {
			followUps[followUp.Arguments.Hub.Cursor] = true
		}
		pageTurnIDs := make([]string, len(response.Turns.Data))
		for index, turn := range response.Turns.Data {
			pageTurnIDs[index] = turn.ID
			if turn.Elision == nil || turn.Elision.ContinuationCursor == "" || !followUps[turn.Elision.ContinuationCursor] {
				t.Fatalf("page %d turn %q recovery = %+v, follow-ups %+v", pageCount, turn.ID, turn.Elision, response.FollowUps)
			}
		}
		gotTurnIDs = append(pageTurnIDs, gotTurnIDs...)
		params.Cursor = ""
		if response.Turns.Window != nil {
			params.Cursor = response.Turns.Window.ContinuationCursor
		}
		if params.Cursor == "" {
			if !slices.Equal(gotTurnIDs, wantTurnIDs) {
				t.Fatalf("continued turns = %v, want every selected turn once in order", gotTurnIDs)
			}
			return
		}
	}
	t.Fatal("structured-follow-up continuation did not terminate")
}

func TestSessionOperationsFetchesServingDaemonItem(t *testing.T) {
	cwd := t.TempDir()
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0}
	if err := os.WriteFile(filepath.Join(cwd, "plot.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	position := appwire.ThreadItemPosition{Entry: 7}
	item := appwire.ThreadItem{
		ID: "tool-large", Type: "commandExecution", TurnID: "turn_1", TranscriptKey: "tool-large", ToolName: "write_file",
		ArgumentsJSON: `{"file_path":"plot.png"}`, Output: strings.Repeat("output", 50_000), Status: appwire.TurnStatusCompleted,
		OutputImages: []appwire.OutputImage{{Source: "tool-result", SHA: strings.Repeat("a", 64)}},
		Position:     &position, Version: 4,
	}
	inFlight := appwire.ThreadItem{
		ID: "tool-running", Type: "commandExecution", TurnID: "turn_1", ToolName: "shell",
		Output: strings.Repeat("running", 20_000),
	}
	const nativeIncarnation = "daemon-index-incarnation"

	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleThreadHistory(daemon.Router(), func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		thread := appwire.Thread{ID: "windowed-after-clear", SessionID: "windowed-after-clear", CWD: cwd, Evener: appwire.EvenerThread{Ref: params.Ref}}
		snapshot := &appwire.SnapshotIdentity{Incarnation: nativeIncarnation, Length: 10}
		if params.FetchCursor == "" {
			thread.Turns = []appwire.Turn{{ID: "turn_1", Status: appwire.TurnStatusInProgress, ItemsView: appwire.TurnItemsViewFull, Items: []appwire.ThreadItem{item}}}
			return appwire.ThreadReadResponse{
				Thread: thread, Snapshot: snapshot,
				Overlay: []appwire.OverlayItem{{Key: "tool:tool-running", Kind: appwire.OverlayTool, TurnID: "turn_1", Item: inFlight}},
			}, nil
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
	}, func(_ context.Context, _ appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
		t.Fatal("exact turn window unexpectedly paged daemon history")
		return appwire.ThreadTurnsListResponse{}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()
	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{
			Entry: rendezvous.Entry{
				Protocol: appwire.ProtocolVersion, Endpoint: "ws" + strings.TrimPrefix(daemonHTTP.URL, "http"), SourceID: "local",
				ThreadID: "windowed-after-clear", SessionID: "windowed-after-clear", WorkspaceRef: "local:windowed",
			},
			Capabilities: appwire.ThreadCapabilities{WindowRead: true}, CapabilitiesKnown: true,
		}}
	}, http.DefaultClient)
	sources := appsource.NewRegistry()
	sources.Add(source)
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex("")}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", Version: "test", SourceID: "local"})
	operations := &sessionOperations{cfg: cfg, sources: sources, relays: newHubRelay(server, cfg, sources), server: server}
	caller := appwire.ManagerView{Ref: "local:manager", Manager: true}
	windowParams := appwire.ThreadTurnsListParams{
		Ref:        "local:windowed",
		TurnWindow: &appwire.ThreadTurnWindow{Turn: "turn_1"}, ItemTypes: []string{"commandExecution"}, PreviewBytes: 64,
	}

	window, err := operations.Invoke(t.Context(), caller, appwire.SessionOperationRequest{Turns: &windowParams})
	if err != nil {
		t.Fatal(err)
	}
	if window.Turns == nil || len(window.Turns.Data) != 1 || len(window.Turns.Data[0].Items) != 2 || window.Turns.Data[0].Items[0].FetchCursor == "" {
		t.Fatalf("bounded window = %+v, want fetchable settled and labeled in-flight tool items", window.Turns)
	}
	if got := window.Turns.Data[0].Items[1]; !got.InFlight || got.FetchCursor != "" {
		t.Fatalf("in-flight bounded item = %+v, want label without fetch cursor", got)
	}

	unfiltered, err := operations.read(t.Context(), appwire.ThreadReadParams{Ref: "local:windowed", IncludeTurns: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(unfiltered.Thread.Turns) != 1 {
		t.Fatalf("unfiltered turn count = %d, want 1", len(unfiltered.Thread.Turns))
	}
	if got := testItemIDs(unfiltered.Thread.Turns[0].Items); !slices.Equal(got, []string{item.ID}) {
		t.Fatalf("unfiltered recorded item ids = %v, want settled transcript item only", got)
	}
	if len(unfiltered.Overlay) != 1 || unfiltered.Overlay[0].Item.ID != inFlight.ID {
		t.Fatalf("unfiltered overlay = %+v, want in-flight item only in overlay", unfiltered.Overlay)
	}
	completeIndex := slices.IndexFunc(unfiltered.Thread.Turns[0].Items, func(candidate appwire.ThreadItem) bool {
		return candidate.ID == item.ID
	})
	if completeIndex < 0 {
		t.Fatalf("unfiltered item ids = %v, want %q", testItemIDs(unfiltered.Thread.Turns[0].Items), item.ID)
	}
	complete := unfiltered.Thread.Turns[0].Items[completeIndex]
	if len(complete.OutputImages) != 2 || complete.OutputImages[0].URL == "" || complete.OutputImages[1].URL == "" {
		t.Fatalf("unfiltered output images = %+v, want routed tool-result and file-backed descriptors", complete.OutputImages)
	}
	want, err := json.Marshal(complete)
	if err != nil {
		t.Fatal(err)
	}
	cursor := window.Turns.Data[0].Items[0].FetchCursor
	var got []byte
	firstFetch, err := operations.Invoke(t.Context(), caller, appwire.SessionOperationRequest{Fetch: &appwire.SessionFetchRequest{
		Ref: "local:windowed", FetchCursor: cursor,
	}})
	if err != nil {
		t.Fatal(err)
	}
	firstFragment, err := base64.StdEncoding.DecodeString(firstFetch.Fetch.Data)
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, firstFragment...)
	if firstFetch.Fetch.FetchCursor == "" {
		t.Fatal("first canonical fetch unexpectedly completed in one range")
	}
	cursor = firstFetch.Fetch.FetchCursor

	changedPNG := append(append([]byte(nil), png...), 1)
	if err := os.WriteFile(filepath.Join(cwd, "plot.png"), changedPNG, 0o644); err != nil {
		t.Fatal(err)
	}
	freshRead, err := operations.read(t.Context(), appwire.ThreadReadParams{Ref: "local:windowed", IncludeTurns: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	freshComplete := freshRead.Thread.Turns[0].Items[0]
	freshBytes, err := json.Marshal(freshComplete)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Equal(freshBytes, want) {
		t.Fatal("changed file did not change fresh canonical enrichment")
	}
	reread, err := operations.Invoke(t.Context(), caller, appwire.SessionOperationRequest{Turns: &windowParams})
	if err != nil {
		t.Fatal(err)
	}
	if reread.Turns.Data[0].Items[0].FetchCursor != window.Turns.Data[0].Items[0].FetchCursor {
		t.Fatal("same source identity minted a different fetch cursor")
	}
	if !slices.Equal(reread.Turns.Data[0].Items[0].OutputImages, window.Turns.Data[0].Items[0].OutputImages) {
		t.Fatalf("reread changed canonical images behind existing cursor: got %+v, want %+v", reread.Turns.Data[0].Items[0].OutputImages, window.Turns.Data[0].Items[0].OutputImages)
	}
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

func TestContinueLiveTranscriptWindowBudgetsCanonicalImages(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "plot.png"), []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}, 0o644); err != nil {
		t.Fatal(err)
	}
	position := appwire.ThreadItemPosition{Entry: 1}
	source := &windowReadCapableManagerSource{managerReadSource: &managerReadSource{scriptedAppSource: &scriptedAppSource{id: "local", thread: appwire.Thread{
		ID: "windowed", SessionID: "windowed", CWD: cwd, Evener: appwire.EvenerThread{Ref: "local:windowed"},
		Turns: []appwire.Turn{{
			ID: "turn_1", Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFull,
			Items: []appwire.ThreadItem{{
				ID: "tool-large", Type: "commandExecution", TurnID: "turn_1", TranscriptKey: "tool-large",
				ToolName: "write_file", ArgumentsJSON: `{"file_path":"plot.png"}`, Output: strings.Repeat("x", 15_900),
				OutputImages: []appwire.OutputImage{{Source: "tool-result", SHA: strings.Repeat("a", 64)}},
				Position:     &position, Version: 1,
			}},
		}},
	}}}}

	windowParams := appwire.ThreadWindowParams{
		TurnWindow: &appwire.ThreadTurnWindow{Turn: "turn_1"},
		ItemTypes:  []string{"commandExecution"}, PreviewBytes: 16_000, WindowBudgetBytes: 1,
	}
	readParams := appwire.ThreadReadParams{Ref: "local:windowed", IncludeTurns: true, ThreadWindowParams: windowParams}
	read, err := source.ReadThread(t.Context(), readParams)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := sourceItemCandidateResultForRead(t.Context(), source, readParams, read)
	if err != nil {
		t.Fatal(err)
	}
	identity := transcriptWindowCursorIdentity(candidates.Identity, windowParams, appwire.ThreadWindowItemLimitDefault)
	cursor, err := appitempaging.EncodeWindowCursor(identity, appitempaging.WindowCursorDirectionForward, appitempaging.WindowCursor{
		AnchorTurnID: "turn_1", Position: position, ItemVersion: 1, ResumeWindow: &appwire.ThreadTurnWindow{Turn: "turn_1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	window, err := continueLiveTranscriptWindow(t.Context(), source, appwire.ThreadTurnsListParams{
		Ref: "local:windowed", TurnWindow: &appwire.ThreadTurnWindow{Turn: "turn_1"},
		ItemTypes: []string{"commandExecution"}, PreviewBytes: 16_000, WindowBudgetBytes: 1, Cursor: cursor,
	})
	if err != nil {
		t.Fatalf("read window: %v", err)
	}
	encoded, err := json.Marshal(window.Data)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > window.Window.EffectiveBudgetBytes {
		t.Fatalf("encoded window = %d bytes, effective budget = %d", len(encoded), window.Window.EffectiveBudgetBytes)
	}
}

func TestContinueLiveTranscriptWindowPreservesResolvedLatestRangeAfterAppend(t *testing.T) {
	turns := movingWindowTestTurns()
	base := &managerReadSource{scriptedAppSource: &scriptedAppSource{
		id: "local", thread: appwire.Thread{ID: "moving", SessionID: "moving", Evener: appwire.EvenerThread{Ref: "local:moving"}},
	}}
	source := &movingWindowManagerSource{managerReadSource: base, turns: turns[:3]}
	params := appwire.ThreadWindowParams{
		TurnWindow: &appwire.ThreadTurnWindow{Latest: 3}, WindowBudgetBytes: 1,
	}
	itemLimit, err := normalizeTranscriptWindowItemLimit(params.TurnWindow, 0)
	if err != nil {
		t.Fatal(err)
	}
	readParams := appwire.ThreadReadParams{Ref: "local:moving", IncludeTurns: true, ItemLimit: itemLimit, ThreadWindowParams: params}
	read, err := source.ReadThread(t.Context(), readParams)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := sourceItemCandidateResultForRead(t.Context(), source, readParams, read)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := packThreadTurnsWindowItemCandidates(candidates, func(response appwire.ThreadTurnsListResponse) (appwire.ThreadTurnsListResponse, error) {
		return response, nil
	}, transcriptWindowOptionsFor(candidates, params, itemLimit, transcriptWindowAppliedByServingDaemon, appitempaging.FetchCursorOriginServingDaemon))
	if err != nil {
		t.Fatal(err)
	}
	requireMovingWindowPage(t, initial, 2, "turn-c")

	source.turns = turns
	continued, err := continueLiveTranscriptWindow(t.Context(), source, appwire.ThreadTurnsListParams{
		Ref: "local:moving", Cursor: initial.Window.ContinuationCursor, ThreadWindowParams: params,
	})
	if err != nil {
		t.Fatal(err)
	}
	requireMovingWindowPage(t, continued, 1, "turn-b")
}

func TestSessionOperationsContinuesAcrossEmptyBoundary(t *testing.T) {
	turnA := testResolverTurn("turn-a", "item-a", 1)
	turnA.Items[0].Position = &appwire.ThreadItemPosition{Entry: 1}
	turnA.Items[0].Text = strings.Repeat("a", 9_000)
	turnB := appwire.Turn{ID: "turn-b", Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFragment}
	turnC := testResolverTurn("turn-c", "item-c", 1)
	turnC.Items[0].Position = &appwire.ThreadItemPosition{Entry: 3}
	turnC.Items[0].Text = strings.Repeat("c", 9_000)
	operations, source := newMovingWindowOperations(t, []appwire.Turn{turnA, turnB, turnC})
	params := appwire.ThreadTurnsListParams{
		Ref:               "local:moving",
		TurnWindow:        &appwire.ThreadTurnWindow{Latest: 3},
		WindowBudgetBytes: 1,
	}

	initial, err := operations.turns(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	if initial.Window == nil || initial.Window.ContinuationCursor == "" {
		t.Fatalf("initial empty-boundary window = %+v, want a continuation", initial)
	}
	if got := turnIDsFromTurns(initial.Data); !slices.Equal(got, []string{"turn-b", "turn-c"}) {
		t.Fatalf("initial empty-boundary turns = %v, want [turn-b turn-c]", got)
	}
	cursor, _, direction, err := appitempaging.DecodeWindowCursorForRef(initial.Window.ContinuationCursor, params.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if direction != appitempaging.WindowCursorDirectionBackward || cursor.ResumeWindow == nil || cursor.ResumeWindow.Before == nil || cursor.ResumeWindow.Before.TurnID != "turn-b" {
		t.Fatalf("empty-boundary resume = direction %q window %+v, want backward before turn-b", direction, cursor.ResumeWindow)
	}
	if cursor.AnchorTurnID != "turn-c" || cursor.Position != *turnC.Items[0].Position || cursor.ItemVersion != turnC.Items[0].Version {
		t.Fatalf("empty-boundary anchor = %+v, want positioned turn-c", cursor)
	}

	continuedParams := params
	continuedParams.Cursor = initial.Window.ContinuationCursor
	continued, err := operations.turns(t.Context(), continuedParams)
	if err != nil {
		t.Fatal(err)
	}
	if got := turnIDsFromTurns(continued.Data); !slices.Equal(got, []string{"turn-a"}) {
		t.Fatalf("continued empty-boundary turns = %v, want [turn-a]", got)
	}

	source.turns = cloneTranscriptTurnShells([]appwire.Turn{turnA, turnB, turnC})
	source.turns[2].Items[0].Version++
	changed, err := operations.turns(t.Context(), continuedParams)
	if err == nil {
		t.Fatalf("changed empty-boundary anchor returned %+v, want typed failure", changed)
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("changed empty-boundary anchor error = %T %v, want WireError", err, err)
	}
	data, ok := wire.Data.(appwire.TranscriptItemChangedErrorData)
	if !ok || data.EvenerErrorInfo != appwire.ErrorTranscriptItemChanged {
		t.Fatalf("changed empty-boundary anchor error = %+v, want item changed", wire)
	}
}

func TestSessionOperationsContinuesAllEmptyBudgetWindow(t *testing.T) {
	const shellCount = 200
	original := make([]appwire.Turn, shellCount)
	for index := range original {
		original[index] = appwire.Turn{
			ID:     fmt.Sprintf("turn-empty-%03d", index),
			Status: strings.Repeat("settled-", 16), ItemsView: appwire.TurnItemsViewFragment, Version: uint64(index + 1),
		}
	}
	operations, source := newMovingWindowOperations(t, cloneTranscriptTurnShells(original))
	params := appwire.ThreadTurnsListParams{
		Ref:               "local:moving",
		TurnWindow:        &appwire.ThreadTurnWindow{Latest: shellCount},
		WindowBudgetBytes: 1,
	}
	initial, err := operations.turns(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	if initial.Window == nil || initial.Window.ContinuationCursor == "" {
		t.Fatalf("initial all-empty window = %+v, want digest continuation", initial)
	}
	firstCursor := initial.Window.ContinuationCursor
	decoded, _, _, err := appitempaging.DecodeWindowCursorForRef(firstCursor, params.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.EmptyRangeDigest == "" || decoded.AnchorTurnID != "" {
		t.Fatalf("all-empty cursor proof = %+v, want digest only", decoded)
	}

	got := turnIDsFromTurns(initial.Data)
	cursor := firstCursor
	for range shellCount {
		if cursor == "" {
			break
		}
		continuedParams := params
		continuedParams.Cursor = cursor
		page, err := operations.turns(t.Context(), continuedParams)
		if err != nil {
			t.Fatal(err)
		}
		pageIDs := turnIDsFromTurns(page.Data)
		got = append(pageIDs, got...)
		cursor = ""
		if page.Window != nil {
			cursor = page.Window.ContinuationCursor
		}
	}
	if cursor != "" {
		t.Fatal("all-empty continuation did not terminate")
	}
	want := turnIDsFromTurns(original)
	if !slices.Equal(got, want) {
		t.Fatalf("all-empty continued turns = %v, want every original shell once in order", got)
	}

	resumeTurns := testResolveTurnWindow(original, *decoded.ResumeWindow)
	if len(resumeTurns) < 2 {
		t.Fatalf("all-empty resume range = %d turns, want mutation room", len(resumeTurns))
	}
	resumeStart := slices.IndexFunc(original, func(turn appwire.Turn) bool { return turn.ID == resumeTurns[0].ID })
	resumeEnd := resumeStart + len(resumeTurns)
	mutations := map[string]func([]appwire.Turn) []appwire.Turn{
		"add positioned item": func(turns []appwire.Turn) []appwire.Turn {
			position := appwire.ThreadItemPosition{Entry: 1}
			turns[resumeStart].ItemsView = appwire.TurnItemsViewFull
			turns[resumeStart].Items = []appwire.ThreadItem{{
				ID: "new-item", Type: "agentMessage", TurnID: turns[resumeStart].ID, TranscriptKey: "new-item",
				Text: "new", Position: &position, Version: 1,
			}}
			return turns
		},
		"change shell": func(turns []appwire.Turn) []appwire.Turn {
			turns[resumeStart].Status += "changed"
			return turns
		},
		"reorder shells": func(turns []appwire.Turn) []appwire.Turn {
			turns[resumeStart], turns[resumeStart+1] = turns[resumeStart+1], turns[resumeStart]
			return turns
		},
		"insert shell": func(turns []appwire.Turn) []appwire.Turn {
			inserted := appwire.Turn{ID: "turn-inserted", Status: "settled", ItemsView: appwire.TurnItemsViewFragment}
			return append(turns[:resumeEnd], append([]appwire.Turn{inserted}, turns[resumeEnd:]...)...)
		},
		"remove shell": func(turns []appwire.Turn) []appwire.Turn {
			return append(turns[:resumeStart], turns[resumeStart+1:]...)
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			source.turns = mutate(cloneTranscriptTurnShells(original))
			continuedParams := params
			continuedParams.Cursor = firstCursor
			page, err := operations.turns(t.Context(), continuedParams)
			if !errors.Is(err, appwire.TranscriptItemCursorStale()) {
				t.Fatalf("mutated all-empty continuation error = %v, page %+v; want stale", err, page)
			}
			if len(page.Data) != 0 {
				t.Fatalf("mutated all-empty continuation returned %d turns", len(page.Data))
			}
		})
	}
}

func TestSessionOperationsDigestsEmptyResumedRangeBeforePositionedSuffix(t *testing.T) {
	const emptyCount = 200
	original := make([]appwire.Turn, 0, emptyCount+1)
	for index := range emptyCount {
		original = append(original, appwire.Turn{
			ID: fmt.Sprintf("turn-empty-%03d", index), Status: strings.Repeat("settled-", 16),
			ItemsView: appwire.TurnItemsViewFragment, Version: uint64(index + 1),
		})
	}
	position := appwire.ThreadItemPosition{Entry: 1_000}
	positioned := testResolverTurn("turn-positioned", "item-positioned", 1)
	positioned.Items[0].Position = &position
	positioned.Items[0].Text = strings.Repeat("visible-", 1_000)
	original = append(original, positioned)
	operations, source := newMovingWindowOperations(t, cloneTranscriptTurnShells(original))
	params := appwire.ThreadTurnsListParams{
		Ref: "local:moving", TurnWindow: &appwire.ThreadTurnWindow{Latest: len(original)}, WindowBudgetBytes: 1,
	}

	initial, err := operations.turns(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	if initial.Window == nil || initial.Window.ContinuationCursor == "" {
		t.Fatalf("mixed empty-prefix window = %+v, want continuation", initial)
	}
	cursor, _, _, err := appitempaging.DecodeWindowCursorForRef(initial.Window.ContinuationCursor, params.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if cursor.EmptyRangeDigest == "" || cursor.AnchorTurnID != "" {
		t.Fatalf("mixed empty-prefix proof = %+v, want digest only", cursor)
	}
	continuedParams := params
	continuedParams.Cursor = initial.Window.ContinuationCursor
	continued, err := operations.turns(t.Context(), continuedParams)
	if err != nil || len(continued.Data) == 0 {
		t.Fatalf("unchanged mixed continuation = %+v, error %v", continued, err)
	}

	resumeTurns := testResolveTurnWindow(original, *cursor.ResumeWindow)
	if len(resumeTurns) == 0 {
		t.Fatal("mixed continuation resume range is empty")
	}
	resumeStart := slices.IndexFunc(original, func(turn appwire.Turn) bool { return turn.ID == resumeTurns[0].ID })
	mutations := map[string]func([]appwire.Turn){
		"change shell": func(turns []appwire.Turn) {
			turns[resumeStart].Status += "changed"
		},
		"add positioned item": func(turns []appwire.Turn) {
			insertedPosition := appwire.ThreadItemPosition{Entry: 1}
			turns[resumeStart].ItemsView = appwire.TurnItemsViewFull
			turns[resumeStart].Items = []appwire.ThreadItem{{
				ID: "inserted", Type: "agentMessage", TurnID: turns[resumeStart].ID, TranscriptKey: "inserted",
				Text: "inserted", Position: &insertedPosition, Version: 1,
			}}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			source.turns = cloneTranscriptTurnShells(original)
			mutate(source.turns)
			page, err := operations.turns(t.Context(), continuedParams)
			if !errors.Is(err, appwire.TranscriptItemCursorStale()) || len(page.Data) != 0 {
				t.Fatalf("mutated mixed continuation = %+v, error %v; want stale with no content", page, err)
			}
		})
	}
}

func TestContinueHubCompatibilityWindowPreservesResolvedLatestRangeAfterAppend(t *testing.T) {
	turns := movingWindowTestTurns()
	base := &managerReadSource{scriptedAppSource: &scriptedAppSource{
		id: "local", thread: appwire.Thread{ID: "moving", SessionID: "moving", Evener: appwire.EvenerThread{Ref: "local:moving"}},
	}}
	moving := &movingWindowManagerSource{managerReadSource: base, turns: turns[:3]}
	source := &movingLegacyManagerSource{movingWindowManagerSource: moving}
	params := appwire.ThreadWindowParams{
		TurnWindow: &appwire.ThreadTurnWindow{Latest: 3}, WindowBudgetBytes: 1,
	}
	itemLimit, err := normalizeTranscriptWindowItemLimit(params.TurnWindow, 0)
	if err != nil {
		t.Fatal(err)
	}
	readParams := appwire.ThreadReadParams{Ref: "local:moving", IncludeTurns: true, ItemLimit: itemLimit, ThreadWindowParams: params}
	read, err := source.ReadThread(t.Context(), appwire.ThreadReadParams{Ref: "local:moving", IncludeTurns: true, ItemLimit: appwire.TranscriptItemPageLimit})
	if err != nil {
		t.Fatal(err)
	}
	selected, resolved := resolveCompatibilityTurnWindow(read.Thread.Turns, params.TurnWindow, true)
	if !resolved {
		t.Fatal("initial compatibility range was not resolved")
	}
	read.Thread.Turns = selected
	candidates, err := compatibilityItemCandidateResultForRead(t.Context(), source, readParams, read)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := packThreadTurnsWindowItemCandidates(candidates, func(response appwire.ThreadTurnsListResponse) (appwire.ThreadTurnsListResponse, error) {
		return response, nil
	}, transcriptWindowOptionsFor(candidates, params, itemLimit, transcriptWindowAppliedByHubCompat, appitempaging.FetchCursorOriginHubCompat))
	if err != nil {
		t.Fatal(err)
	}
	requireMovingWindowPage(t, initial, 2, "turn-c")

	moving.turns = turns
	continued, err := continueLiveTranscriptWindow(t.Context(), source, appwire.ThreadTurnsListParams{
		Ref: "local:moving", Cursor: initial.Window.ContinuationCursor, ThreadWindowParams: params,
	})
	if err != nil {
		t.Fatal(err)
	}
	requireMovingWindowPage(t, continued, 1, "turn-b")
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
	liveItem := appwire.ThreadItem{
		ID: "turn_3-live-message", Type: "agentMessage", TurnID: "turn_3", Text: strings.Repeat("still streaming", 20_000),
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
			Overlay:  []appwire.OverlayItem{{Key: "stream:legacy:agentMessage", Kind: appwire.OverlayStream, TurnID: "turn_3", Item: liveItem}},
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
			Ref:        "local:legacy-window",
			TurnWindow: &appwire.ThreadTurnWindow{Latest: 2}, ItemTypes: []string{"messages"}, MessageBytes: 64,
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
	gotLive := response.Turns.Data[1].Items[len(response.Turns.Data[1].Items)-1]
	if gotLive.ID != liveItem.ID || !gotLive.InFlight || gotLive.FetchCursor != "" {
		t.Fatalf("compatibility live item = %+v, want selected in-flight item without fetch cursor", gotLive)
	}
}

func TestSessionOperationsCompatibilityCountsLogicalTurnsAcrossFragments(t *testing.T) {
	item := func(turnID, itemID string, entry uint64, itemIndex uint32) appwire.ThreadItem {
		position := appwire.ThreadItemPosition{Entry: entry, Item: itemIndex}
		return appwire.ThreadItem{
			ID: itemID, Type: "agentMessage", TurnID: turnID, TranscriptKey: itemID,
			Text: itemID, Position: &position, Version: 1,
		}
	}
	const ref = "local:legacy-fragments"
	olderCursor, err := appitempaging.EncodeCursor(appitempaging.CursorIdentity{
		ThreadRef: ref, Incarnation: "fixture", ProjectionVersion: 1,
	}, appwire.ThreadItemPosition{Entry: 2, Item: 2})
	if err != nil {
		t.Fatal(err)
	}
	read := appwire.ThreadReadResponse{
		Thread: appwire.Thread{
			ID: "legacy-fragments", SessionID: "legacy-fragments", Evener: appwire.EvenerThread{Ref: ref},
			Turns: []appwire.Turn{
				{ID: "turn-b", ItemsView: appwire.TurnItemsViewFragment, HasEarlierItems: true, Items: []appwire.ThreadItem{item("turn-b", "b-newer", 2, 2)}},
				{ID: "turn-c", ItemsView: appwire.TurnItemsViewFull, Items: []appwire.ThreadItem{item("turn-c", "c", 3, 0)}},
			},
		},
		OlderCursor: olderCursor,
		Snapshot:    &appwire.SnapshotIdentity{Incarnation: "legacy-fragments-view", Length: 4},
	}
	pages := map[string]appwire.ThreadTurnsListResponse{
		olderCursor: {
			Data: []appwire.Turn{
				{ID: "turn-a", ItemsView: appwire.TurnItemsViewFull, Items: []appwire.ThreadItem{item("turn-a", "a", 1, 0)}},
				{ID: "turn-b", ItemsView: appwire.TurnItemsViewFragment, Items: []appwire.ThreadItem{item("turn-b", "b-older", 2, 1)}},
			},
			Snapshot: read.Snapshot,
		},
	}
	operations, source := newCompatibilityWindowOperations(t, read, pages)
	response, err := operations.Invoke(t.Context(), appwire.ManagerView{Ref: "local:manager", Manager: true}, appwire.SessionOperationRequest{
		Turns: &appwire.ThreadTurnsListParams{
			Ref: ref, TurnWindow: &appwire.ThreadTurnWindow{Latest: 2}, ItemTypes: []string{"messages"}, ItemLimit: 10,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(source.readCalls) != 1 || len(source.pageCalls) != 1 {
		t.Fatalf("legacy calls = reads %d pages %d, want 1 and 1", len(source.readCalls), len(source.pageCalls))
	}
	if response.Turns == nil || response.Turns.Window == nil {
		t.Fatalf("compatibility response = %+v, want window", response.Turns)
	}
	if got := turnIDsFromTurns(response.Turns.Data); !reflect.DeepEqual(got, []string{"turn-b", "turn-c"}) {
		t.Fatalf("logical turns = %v, want [turn-b turn-c]", got)
	}
	var itemIDs []string
	for _, turn := range response.Turns.Data {
		for _, got := range turn.Items {
			itemIDs = append(itemIDs, got.ID)
		}
	}
	if !reflect.DeepEqual(itemIDs, []string{"b-older", "b-newer", "c"}) {
		t.Fatalf("selected item IDs = %v, want all turn-b fragments and turn-c", itemIDs)
	}

	params := appwire.ThreadTurnsListParams{
		Ref: ref, TurnWindow: &appwire.ThreadTurnWindow{Latest: 2}, ItemTypes: []string{"messages"}, ItemLimit: 1,
	}
	caller := appwire.ManagerView{Ref: "local:manager", Manager: true}
	var bItems []string
	seenC := false
	for range 10 {
		page, invokeErr := operations.Invoke(t.Context(), caller, appwire.SessionOperationRequest{Turns: &params})
		if invokeErr != nil {
			t.Fatal(invokeErr)
		}
		next := ""
		for _, turn := range page.Turns.Data {
			for _, got := range turn.Items {
				switch got.TurnID {
				case "turn-a":
					t.Fatalf("item-limit continuation exposed unrelated older item %+v", got)
				case "turn-b":
					bItems = append(bItems, got.ID)
				case "turn-c":
					seenC = true
				}
			}
			if turn.ID == "turn-b" && turn.Elision != nil && turn.Elision.ContinuationCursor != "" {
				next = turn.Elision.ContinuationCursor
			}
		}
		if next == "" && page.Turns.Window != nil {
			next = page.Turns.Window.ContinuationCursor
		}
		if next == "" {
			break
		}
		params.Cursor = next
	}
	if !reflect.DeepEqual(bItems, []string{"b-older", "b-newer"}) || !seenC {
		t.Fatalf("item-limit logical range = B %v, C %v; want every B fragment and C", bItems, seenC)
	}
}

func TestSessionOperationsCompatibilityExhaustsSelectedWindowBeforeNativeHistory(t *testing.T) {
	item := func(turnID, itemID string, entry uint64, itemIndex uint32) appwire.ThreadItem {
		position := appwire.ThreadItemPosition{Entry: entry, Item: itemIndex}
		return appwire.ThreadItem{ID: itemID, Type: "agentMessage", TurnID: turnID, TranscriptKey: itemID, Position: &position, Version: 1}
	}
	const ref = "local:legacy-selection"
	olderCursor, err := appitempaging.EncodeCursor(appitempaging.CursorIdentity{
		ThreadRef: ref, Incarnation: "fixture", ProjectionVersion: 1,
	}, appwire.ThreadItemPosition{Entry: 2, Item: 1})
	if err != nil {
		t.Fatal(err)
	}
	read := appwire.ThreadReadResponse{
		Thread: appwire.Thread{
			ID: "legacy-selection", SessionID: "legacy-selection", Evener: appwire.EvenerThread{Ref: ref},
			Turns: []appwire.Turn{
				{ID: "turn-b", ItemsView: appwire.TurnItemsViewFull, Items: []appwire.ThreadItem{item("turn-b", "b-1", 2, 1), item("turn-b", "b-2", 2, 2)}},
				{ID: "turn-c", ItemsView: appwire.TurnItemsViewFull, Items: []appwire.ThreadItem{item("turn-c", "c", 3, 0)}},
			},
		},
		OlderCursor: olderCursor,
		Snapshot:    &appwire.SnapshotIdentity{Incarnation: "legacy-selection-view", Length: 4},
	}
	operations, source := newCompatibilityWindowOperations(t, read, map[string]appwire.ThreadTurnsListResponse{
		olderCursor: {Data: []appwire.Turn{{ID: "turn-a", ItemsView: appwire.TurnItemsViewFull, Items: []appwire.ThreadItem{item("turn-a", "a", 1, 0)}}}},
	})
	params := appwire.ThreadTurnsListParams{
		Ref: ref, TurnWindow: &appwire.ThreadTurnWindow{Latest: 2}, ItemTypes: []string{"messages"}, ItemLimit: 1,
	}
	loader, err := newCompatibilityTranscriptWindowLoader(t.Context(), source, appwire.ThreadReadParams{
		Ref: ref, IncludeTurns: true, ItemLimit: 1, ThreadWindowParams: params.ThreadWindowParams,
	}, read)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := loader.load(t.Context(), params.TurnWindow)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Result.Exhausted {
		t.Fatal("selected latest range inherited unrelated older-history exhaustion")
	}
	caller := appwire.ManagerView{Ref: "local:manager", Manager: true}
	var got []string
	var final *appwire.ThreadTurnsListResponse
	for range 10 {
		response, invokeErr := operations.Invoke(t.Context(), caller, appwire.SessionOperationRequest{Turns: &params})
		if invokeErr != nil {
			t.Fatal(invokeErr)
		}
		if response.Turns == nil {
			t.Fatal("compatibility response has no turns")
		}
		for _, turn := range response.Turns.Data {
			got = append(got, testItemIDs(turn.Items)...)
		}
		next := ""
		for _, turn := range response.Turns.Data {
			if turn.Elision != nil && turn.Elision.ContinuationCursor != "" {
				next = turn.Elision.ContinuationCursor
				break
			}
		}
		if next == "" && response.Turns.Window != nil {
			next = response.Turns.Window.ContinuationCursor
		}
		if next == "" {
			final = response.Turns
			break
		}
		params.Cursor = next
	}
	if final == nil {
		t.Fatal("compatibility continuation did not exhaust")
	}
	if len(source.pageCalls) != 0 {
		t.Fatalf("selected latest window paged unrelated older history %d times", len(source.pageCalls))
	}
	if !reflect.DeepEqual(got, []string{"b-1", "c", "b-2"}) {
		t.Fatalf("selected item IDs = %v, want only complete latest range", got)
	}
	if final.Window == nil || final.Window.WithheldTurns != 0 || final.Window.ContinuationCursor != "" {
		t.Fatalf("final selected window = %+v, want exhausted", final.Window)
	}
}

func TestSessionOperationsCompatibilityBoundsLegacyPageTraversal(t *testing.T) {
	for _, test := range []struct {
		name      string
		pageCount int
		cycle     bool
		wantCalls int
	}{
		{name: "cycle", cycle: true, wantCalls: 1},
		{name: "ninth page required", pageCount: hubCompatFetchPageLimit, wantCalls: hubCompatFetchPageLimit},
	} {
		t.Run(test.name, func(t *testing.T) {
			const ref = "local:legacy-bounded"
			position := appwire.ThreadItemPosition{Entry: 9}
			firstCursor, err := appitempaging.EncodeCursor(appitempaging.CursorIdentity{
				ThreadRef: ref, Incarnation: "fixture", ProjectionVersion: 1,
			}, position)
			if err != nil {
				t.Fatal(err)
			}
			read := appwire.ThreadReadResponse{
				Thread: appwire.Thread{ID: "legacy-bounded", SessionID: "legacy-bounded", Evener: appwire.EvenerThread{Ref: ref}, Turns: []appwire.Turn{{
					ID: "turn-c", ItemsView: appwire.TurnItemsViewFull, Items: []appwire.ThreadItem{{
						ID: "c", Type: "agentMessage", TurnID: "turn-c", TranscriptKey: "c", Position: &position, Version: 1,
					}},
				}}},
				OlderCursor: firstCursor,
				Snapshot:    &appwire.SnapshotIdentity{Incarnation: "legacy-bounded-view", Length: 9},
			}
			pages := make(map[string]appwire.ThreadTurnsListResponse)
			if test.cycle {
				pages[firstCursor] = appwire.ThreadTurnsListResponse{NextCursor: firstCursor}
			} else {
				cursor := firstCursor
				for i := 0; i < test.pageCount; i++ {
					next := fmt.Sprintf("legacy-page-%d", i+2)
					pages[cursor] = appwire.ThreadTurnsListResponse{NextCursor: next}
					cursor = next
				}
			}
			operations, source := newCompatibilityWindowOperations(t, read, pages)
			response, invokeErr := operations.Invoke(t.Context(), appwire.ManagerView{Ref: "local:manager", Manager: true}, appwire.SessionOperationRequest{
				Turns: &appwire.ThreadTurnsListParams{Ref: ref, TurnWindow: &appwire.ThreadTurnWindow{Latest: 2}, ItemTypes: []string{"messages"}},
			})
			if !errors.Is(invokeErr, appwire.TranscriptItemCursorStale()) {
				t.Fatalf("bounded compatibility error = %v, want stale", invokeErr)
			}
			if response.Turns != nil || response.Fetch != nil {
				t.Fatalf("bounded compatibility returned content = %+v", response)
			}
			if len(source.pageCalls) != test.wantCalls {
				t.Fatalf("legacy page calls = %d, want %d", len(source.pageCalls), test.wantCalls)
			}
		})
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
				Turns: &appwire.ThreadTurnsListParams{Ref: "local:legacy-window", TurnWindow: window},
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

func TestSessionOperationsCompletesOlderDaemonLatestWindow(t *testing.T) {
	position := appwire.ThreadItemPosition{Entry: 2}
	olderPosition := appwire.ThreadItemPosition{Entry: 1}
	var reads, pages atomic.Int64
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "old-daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		reads.Add(1)
		if params.TurnWindow != nil || len(params.ItemTypes) != 0 || params.PreviewBytes != 0 || params.MessageBytes != 0 || params.WindowBudgetBytes != 0 {
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
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadTurnsList, func(_ context.Context, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
		pages.Add(1)
		if params.Cursor != "legacy-older-page" || params.TurnWindow != nil || len(params.ItemTypes) != 0 || params.PreviewBytes != 0 || params.MessageBytes != 0 || params.WindowBudgetBytes != 0 {
			return appwire.ThreadTurnsListResponse{}, appwire.InvalidParams("older daemon rejected continuation shape")
		}
		return appwire.ThreadTurnsListResponse{
			Data: []appwire.Turn{{
				ID: "turn_1", Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFragment,
				Items: []appwire.ThreadItem{{ID: "message_1", Type: "agentMessage", TurnID: "turn_1", TranscriptKey: "message_1", Position: &olderPosition, Version: 1, Text: "older"}},
			}},
			Snapshot: &appwire.SnapshotIdentity{Incarnation: "legacy-partial-snapshot", Length: 2},
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
		TurnWindow: &appwire.ThreadTurnWindow{Latest: 2}, ItemTypes: []string{"messages"},
		PreviewBytes: 321, MessageBytes: 654, WindowBudgetBytes: 16 * 1024,
	}
	response, err := operations.Invoke(t.Context(), appwire.ManagerView{Ref: "local:manager", Manager: true}, appwire.SessionOperationRequest{Turns: &request})
	if err != nil {
		t.Fatal(err)
	}
	window := response.Turns.Window
	if window == nil || window.ServedTurns != 2 || window.WithheldTurns != 0 || window.TrimmedEnd != "" || window.ContinuationCursor != "" {
		t.Fatalf("complete compatibility window = %+v", window)
	}
	if len(response.Turns.Data) != 2 || response.Turns.Data[0].ID != "turn_1" || response.Turns.Data[1].ID != "turn_2" {
		t.Fatalf("complete compatibility turns = %+v, want turn_1 and turn_2", response.Turns.Data)
	}
	if len(response.FollowUps) != 0 {
		t.Fatalf("complete compatibility follow-ups = %+v, want none", response.FollowUps)
	}
	if reads.Load() != 1 || pages.Load() != 1 {
		t.Fatalf("compatibility initial calls = reads %d pages %d, want 1 and 1", reads.Load(), pages.Load())
	}
}

func TestSessionOperationsPassesOlderRemoteWindowFailureThrough(t *testing.T) {
	var forwarded *appwire.ThreadReadParams
	remote, _ := newScriptedRemoteHub(t, func(method string, raw json.RawMessage) any {
		switch method {
		case appwire.MethodInitialize:
			return appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion, SourceID: "local"}
		case appwire.MethodThreadList:
			return appwire.ThreadListResponse{Data: []appwire.Thread{{
				ID: "legacy-remote", Source: "local", Evener: appwire.EvenerThread{Ref: "local:legacy-remote"},
			}}}
		case appwire.MethodThreadRead:
			var params appwire.ThreadReadParams
			if err := json.Unmarshal(raw, &params); err != nil {
				return appwire.InternalError(err.Error())
			}
			forwarded = &params
			return appwire.WindowUnsupportedOnDaemon()
		case appwire.MethodThreadTurnsList:
			return appwire.InternalError("stopped remote window fell back to a compatibility page")
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
	params := appwire.ThreadTurnsListParams{
		Ref: "host-b:legacy-remote", ItemLimit: 7,
		TurnWindow: &appwire.ThreadTurnWindow{Latest: 2}, ItemTypes: []string{"messages"},
		PreviewBytes: 321, MessageBytes: 654, WindowBudgetBytes: transcriptWindowBudgetFloorBytes,
	}

	_, err := operations.Invoke(t.Context(), appwire.ManagerView{Ref: "local:manager", Manager: true}, appwire.SessionOperationRequest{Turns: &params})
	wire, ok := errors.AsType[appwire.WireError](err)
	if !ok {
		t.Fatalf("older remote window error = %T %v, want a typed wire error", err, err)
	}
	if data, _ := appwire.DecodedErrorData(wire.Data); data.EvenerErrorInfo != appwire.ErrorWindowUnsupportedOnDaemon {
		t.Fatalf("older remote window error info = %q, want the owning hub's typed failure", data.EvenerErrorInfo)
	}
	if forwarded == nil || forwarded.TurnWindow == nil || forwarded.TurnWindow.Latest != 2 ||
		!slices.Equal(forwarded.ItemTypes, []string{"messages"}) ||
		forwarded.PreviewBytes != params.PreviewBytes || forwarded.MessageBytes != params.MessageBytes ||
		forwarded.WindowBudgetBytes != params.WindowBudgetBytes {
		t.Fatalf("forwarded window params = %+v, want the complete window shape", forwarded)
	}
}

func TestSessionOperationsContinuesHubCompatibilityIntraTurnCapForward(t *testing.T) {
	items := make([]appwire.ThreadItem, 3)
	for index := range items {
		position := appwire.ThreadItemPosition{Entry: uint64(index + 1)}
		items[index] = appwire.ThreadItem{
			ID: fmt.Sprintf("message-%d", index), TranscriptKey: fmt.Sprintf("message-%d", index),
			TurnID: "turn-cap", Type: "agentMessage", Position: &position, Version: 1,
		}
	}
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "old-daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		if params.TurnWindow != nil {
			t.Fatal("old daemon received window fields")
		}
		return appwire.ThreadReadResponse{
			Thread: appwire.Thread{ID: "compat-cap", SessionID: "compat-cap", Evener: appwire.EvenerThread{Ref: params.Ref}, Turns: []appwire.Turn{{
				ID: "turn-cap", ItemsView: appwire.TurnItemsViewFull, Items: items,
			}}},
			Snapshot: &appwire.SnapshotIdentity{Incarnation: "compat-cap-history", Length: 3},
		}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()
	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{Entry: rendezvous.Entry{
			Protocol: appwire.ProtocolVersion, Endpoint: "ws" + strings.TrimPrefix(daemonHTTP.URL, "http"), SourceID: "local",
			ThreadID: "compat-cap", SessionID: "compat-cap", WorkspaceRef: "local:compat-cap",
		}, CapabilitiesKnown: true}}
	}, http.DefaultClient)
	sources := appsource.NewRegistry()
	sources.Add(source)
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex("")}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", Version: "test", SourceID: "local"})
	operations := &sessionOperations{cfg: cfg, sources: sources, relays: newHubRelay(server, cfg, sources), server: server}
	params := appwire.ThreadTurnsListParams{
		Ref: "local:compat-cap", ItemLimit: 1,
		TurnWindow: &appwire.ThreadTurnWindow{Latest: 1}, ItemTypes: []string{"messages"},
	}
	caller := appwire.ManagerView{Ref: "local:manager", Manager: true}
	var got []string
	for {
		response, err := operations.Invoke(t.Context(), caller, appwire.SessionOperationRequest{Turns: &params})
		if err != nil {
			t.Fatal(err)
		}
		if response.Turns == nil || len(response.Turns.Data) != 1 {
			t.Fatalf("compatibility cap page = %+v, want one turn", response.Turns)
		}
		got = append(got, testItemIDs(response.Turns.Data[0].Items)...)
		params.Cursor = ""
		if response.Turns.Data[0].Elision != nil {
			params.Cursor = response.Turns.Data[0].Elision.ContinuationCursor
		}
		if params.Cursor == "" {
			break
		}
	}
	if want := []string{"message-0", "message-1", "message-2"}; !slices.Equal(got, want) {
		t.Fatalf("compatibility cap continuation ids = %v, want %v", got, want)
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
		Turns: &appwire.ThreadTurnsListParams{Ref: "local:stale-positive", TurnWindow: &appwire.ThreadTurnWindow{Latest: 1}},
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
		Turns: &appwire.ThreadTurnsListParams{Ref: "local:window-failure", TurnWindow: &appwire.ThreadTurnWindow{Latest: 1}},
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
			Ref: "local:compat-fetch", TurnWindow: &appwire.ThreadTurnWindow{Latest: 1},
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
	completeRead, err := operations.read(t.Context(), appwire.ThreadReadParams{
		Ref: "local:compat-fetch", IncludeTurns: true, ItemLimit: appwire.TranscriptItemPageLimit,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	completeItems := flattenTestItems(completeRead.Thread.Turns)
	if len(completeItems) != 1 {
		t.Fatalf("complete compat items = %d, want 1", len(completeItems))
	}
	fetched, err := operations.fetch(t.Context(), appwire.SessionFetchRequest{Ref: "local:compat-fetch", FetchCursor: fetchCursor})
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(completeItems[0])
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
	if got := requests.Load(); got != 3 {
		t.Fatalf("old daemon reads = %d, want window, complete-view, and cursorless fetch reads", got)
	}
}

func TestSessionOperationsRejectsHubCompatibilityCursorWithoutCanonicalBytes(t *testing.T) {
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
		Position: targetPosition, ItemVersion: target.Version, Origin: appitempaging.FetchCursorOriginHubCompat, Token: "0123456789abcdef-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = operations.fetch(t.Context(), appwire.SessionFetchRequest{Ref: identity.ThreadRef, FetchCursor: fetchCursor})
	if !errors.Is(err, appwire.TranscriptItemCursorStale()) {
		t.Fatalf("fetch without retained canonical bytes = %v, want typed stale cursor", err)
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
			Entry:             rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws" + strings.TrimPrefix(daemonHTTP.URL, "http"), SourceID: "local", ThreadID: "compat-pre-before", SessionID: "compat-pre-before", WorkspaceRef: "local:compat-pre-before"},
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
		Position: targetPosition, ItemVersion: 1, Origin: appitempaging.FetchCursorOriginHubCompat, Token: "0123456789abcdef-1",
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
		Position: targetPosition, ItemVersion: target.Version - 1, Origin: appitempaging.FetchCursorOriginHubCompat, Token: "0123456789abcdef-1",
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

func TestSessionOperationsRejectsHubCompatibilityFetchAcrossHistoryIncarnation(t *testing.T) {
	targetPosition := appwire.ThreadItemPosition{Entry: 1}
	target := appwire.ThreadItem{
		ID: "compat-target", Type: "agentMessage", TurnID: "turn-all", TranscriptKey: "compat-target",
		Position: &targetPosition, Version: 3, Text: "target",
	}
	newestPosition := appwire.ThreadItemPosition{Entry: 2}
	newest := appwire.ThreadItem{
		ID: "compat-newest", Type: "agentMessage", TurnID: "turn-all", TranscriptKey: "compat-newest",
		Position: &newestPosition, Version: 1, Text: "newest",
	}
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "old-daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{
			Thread: appwire.Thread{ID: "compat-rewritten", SessionID: "compat-rewritten", Evener: appwire.EvenerThread{Ref: params.Ref}, Turns: []appwire.Turn{{
				ID: "turn-all", Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFragment, Items: []appwire.ThreadItem{newest}, HasEarlierItems: true,
			}}},
			OlderCursor: "native-older", Snapshot: &appwire.SnapshotIdentity{Incarnation: "compat-rewritten-before", Length: 2},
		}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadTurnsList, func(_ context.Context, _ appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
		return appwire.ThreadTurnsListResponse{
			Data:     []appwire.Turn{{ID: "turn-all", Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFragment, Items: []appwire.ThreadItem{target}}},
			Snapshot: &appwire.SnapshotIdentity{Incarnation: "compat-rewritten-after", Length: 2},
		}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()
	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{
			Entry:             rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws" + strings.TrimPrefix(daemonHTTP.URL, "http"), SourceID: "local", ThreadID: "compat-rewritten", SessionID: "compat-rewritten", WorkspaceRef: "local:compat-rewritten"},
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
		ThreadRef: "local:compat-rewritten", Incarnation: window.ViewIncarnation("compat-rewritten-before", appwire.TranscriptItemPageLimit), ProjectionVersion: appitempaging.TranscriptItemProjectionVersion,
	}
	_, err := operations.fetchHubCompatibilityTranscriptItem(t.Context(), identity.ThreadRef, identity, appitempaging.FetchCursor{
		Position: targetPosition, ItemVersion: target.Version, Origin: appitempaging.FetchCursorOriginHubCompat, Token: "0123456789abcdef-1",
	})
	if !errors.Is(err, appwire.TranscriptItemCursorStale()) {
		t.Fatalf("rewritten-history compatibility fetch = %v, want stale", err)
	}
}

func TestSessionOperationsFetchesTranslatedRemoteItem(t *testing.T) {
	position := appwire.ThreadItemPosition{Entry: 11, Item: 2}
	item := appwire.ThreadItem{
		ID: "remote-tool", Type: "commandExecution", TurnID: "turn-remote", TranscriptKey: "remote-tool", ToolName: "shell",
		ArgumentsJSON: `{"emoji":"🌒"}`, Output: strings.Repeat("remote-output", 30_000), Raw: json.RawMessage(`{"nested":"remote"}`),
		Actor: &appwire.SessionActor{Kind: "manager", ManagerRef: "local:manager-on-remote"}, Position: &position, Version: 9,
	}
	inFlight := appwire.ThreadItem{
		ID: "remote-tool-running", Type: "commandExecution", TurnID: "turn-remote", ToolName: "shell",
		Output: strings.Repeat("remote-running", 20_000),
	}
	remote, calls := newScriptedRemoteHub(t, func(method string, params json.RawMessage) any {
		switch method {
		case appwire.MethodInitialize:
			return appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion, SourceID: "local"}
		case appwire.MethodThreadList:
			return appwire.ThreadListResponse{Data: []appwire.Thread{{
				ID: "remote", Source: "local", Evener: appwire.EvenerThread{Ref: "local:remote", Capabilities: appwire.ThreadCapabilities{WindowRead: true}},
			}}}
		case appwire.MethodThreadRead:
			var read appwire.ThreadReadParams
			if err := json.Unmarshal(params, &read); err != nil {
				t.Fatal(err)
			}
			if read.FetchCursor != "" {
				t.Fatalf("controller forwarded fetch cursor to remote hub: %+v", read)
			}
			response := appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID: "remote", Source: "local", Evener: appwire.EvenerThread{Ref: "local:remote", Capabilities: appwire.ThreadCapabilities{WindowRead: true}},
				Turns: []appwire.Turn{{ID: "turn-remote", Status: appwire.TurnStatusInProgress, ItemsView: appwire.TurnItemsViewFull, Items: []appwire.ThreadItem{item}}},
			}, Snapshot: &appwire.SnapshotIdentity{Incarnation: "remote-history", Length: 1}, Overlay: []appwire.OverlayItem{{Key: "tool:remote-tool-running", Kind: appwire.OverlayTool, TurnID: "turn-remote", Item: inFlight}}}
			if read.TurnWindow != nil {
				var err error
				response, err = packCurrentRemoteHubRead(response, read.ThreadWindowParams, read.ItemLimit)
				if err != nil {
					return appwire.InternalError(err.Error())
				}
			}
			return response
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
		Ref:        "host-b:remote",
		TurnWindow: &appwire.ThreadTurnWindow{Turn: "turn-remote"}, ItemTypes: []string{"commandExecution"}, PreviewBytes: 64,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if window.Turns == nil || len(window.Turns.Data) != 1 || len(window.Turns.Data[0].Items) != 2 || window.Turns.Data[0].Items[0].FetchCursor == "" {
		t.Fatalf("bounded remote window = %+v, want fetchable settled and labeled in-flight tool items", window.Turns)
	}
	if got := window.Turns.Data[0].Items[1]; !got.InFlight || got.FetchCursor != "" {
		t.Fatalf("remote in-flight bounded item = %+v, want label without fetch cursor", got)
	}

	completeRead, err := operations.read(t.Context(), appwire.ThreadReadParams{Ref: "host-b:remote", IncludeTurns: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	completeItems := flattenTestItems(completeRead.Thread.Turns)
	completeIndex := slices.IndexFunc(completeItems, func(candidate appwire.ThreadItem) bool {
		return candidate.ID == item.ID
	})
	if completeIndex < 0 {
		t.Fatalf("complete remote item ids = %v, want %q", testItemIDs(completeItems), item.ID)
	}
	want, err := json.Marshal(completeItems[completeIndex])
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

func TestSessionOperationsDefersRemoteFetchUnavailability(t *testing.T) {
	oldPosition := appwire.ThreadItemPosition{Entry: 1}
	newPosition := appwire.ThreadItemPosition{Entry: 2}
	oldItem := appwire.ThreadItem{
		ID: "old-tool", Type: "commandExecution", TurnID: "turn-old", TranscriptKey: "old-tool", ToolName: "shell",
		Output: strings.Repeat("old-remote-output", 1_000), Position: &oldPosition, Version: 1,
	}
	newItem := appwire.ThreadItem{
		ID: "new-message", Type: "agentMessage", TurnID: "turn-new", TranscriptKey: "new-message",
		Text: "newest", Position: &newPosition, Version: 2,
	}
	allTurns := []appwire.Turn{
		{ID: "turn-old", Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFull, Items: []appwire.ThreadItem{oldItem}},
		{ID: "turn-new", Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFull, Items: []appwire.ThreadItem{newItem}},
	}
	remote, _ := newScriptedRemoteHub(t, func(method string, params json.RawMessage) any {
		switch method {
		case appwire.MethodInitialize:
			return appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion, SourceID: "local"}
		case appwire.MethodThreadList:
			return appwire.ThreadListResponse{Data: []appwire.Thread{{
				ID: "remote", Source: "local", Evener: appwire.EvenerThread{Ref: "local:remote", Capabilities: appwire.ThreadCapabilities{WindowRead: true}},
			}}}
		case appwire.MethodThreadRead:
			var read appwire.ThreadReadParams
			if err := json.Unmarshal(params, &read); err != nil {
				t.Fatal(err)
			}
			turns := allTurns[len(allTurns)-1:]
			if read.TurnWindow != nil {
				turns = allTurns
			}
			response := appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID: "remote", Source: "local", Evener: appwire.EvenerThread{Ref: "local:remote", Capabilities: appwire.ThreadCapabilities{WindowRead: true}},
				Turns: turns,
			}, Snapshot: &appwire.SnapshotIdentity{Incarnation: "remote-history", Length: int64(len(allTurns))}}
			if read.TurnWindow != nil {
				var err error
				response, err = packCurrentRemoteHubRead(response, read.ThreadWindowParams, read.ItemLimit)
				if err != nil {
					return appwire.InternalError(err.Error())
				}
			}
			return response
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
		Ref: "host-b:remote", TurnWindow: &appwire.ThreadTurnWindow{Turn: "turn-old"}, PreviewBytes: 8,
	}})
	if err != nil {
		t.Fatalf("remote exact window: %v", err)
	}
	if window.Turns == nil || len(window.Turns.Data) == 0 || len(window.Turns.Data[0].Items) != 1 || window.Turns.Data[0].Items[0].FetchCursor == "" {
		t.Fatalf("remote exact window = %+v, want fetchable old item", window.Turns)
	}

	_, err = operations.Invoke(t.Context(), caller, appwire.SessionOperationRequest{Fetch: &appwire.SessionFetchRequest{
		Ref: "host-b:remote", FetchCursor: window.Turns.Data[0].Items[0].FetchCursor,
	}})
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) {
		t.Fatalf("remote unavailable fetch error = %T %v", err, err)
	}
	data, ok := appwire.DecodedErrorData(wireErr.Data)
	if !ok || data.EvenerErrorInfo != appwire.ErrorFetchUnavailableRemote {
		t.Fatalf("remote unavailable fetch error data = %#v", wireErr.Data)
	}
}

func TestSessionOperationsContinuesCurrentRemoteHubWindow(t *testing.T) {
	const turnCount = 80
	turns := make([]appwire.Turn, 0, turnCount)
	for index := range turnCount {
		position := appwire.ThreadItemPosition{Entry: uint64(index + 1)}
		turnID := fmt.Sprintf("turn-%03d", index)
		turns = append(turns, appwire.Turn{
			ID: turnID, Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFull,
			Items: []appwire.ThreadItem{{
				ID: "message-" + turnID, TranscriptKey: "message-" + turnID, TurnID: turnID, Type: "agentMessage", Text: strings.Repeat("remote-window", 100),
				Position: &position, Version: 1,
			}},
		})
	}
	remote, _ := newScriptedRemoteHub(t, func(method string, params json.RawMessage) any {
		switch method {
		case appwire.MethodInitialize:
			return appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion, SourceID: "local"}
		case appwire.MethodThreadList:
			return appwire.ThreadListResponse{Data: []appwire.Thread{{
				ID: "remote", Source: "local", Evener: appwire.EvenerThread{Ref: "local:remote", Capabilities: appwire.ThreadCapabilities{WindowRead: true}},
			}}}
		case appwire.MethodThreadRead:
			var read appwire.ThreadReadParams
			if err := json.Unmarshal(params, &read); err != nil {
				t.Fatal(err)
			}
			response := appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID: "remote", Source: "local", Evener: appwire.EvenerThread{Ref: "local:remote", Capabilities: appwire.ThreadCapabilities{WindowRead: true}}, Turns: turns,
			}, Snapshot: &appwire.SnapshotIdentity{Incarnation: "remote-history", Length: turnCount}}
			packed, err := packCurrentRemoteHubRead(response, read.ThreadWindowParams, read.ItemLimit)
			if err != nil {
				return appwire.InternalError(err.Error())
			}
			return packed
		case appwire.MethodThreadTurnsList:
			var list appwire.ThreadTurnsListParams
			if err := json.Unmarshal(params, &list); err != nil {
				return appwire.InternalError(err.Error())
			}
			page, err := packCurrentRemoteHubPage(turns, list.ThreadWindowParams, list.ItemLimit, list.Cursor)
			if err != nil {
				if wire, ok := errors.AsType[appwire.WireError](err); ok {
					return wire
				}
				return appwire.InternalError(err.Error())
			}
			return page
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
	params := appwire.ThreadTurnsListParams{
		Ref: "host-b:remote", TurnWindow: &appwire.ThreadTurnWindow{Latest: turnCount}, ItemTypes: []string{"agentMessage"},
		PreviewBytes: 64, MessageBytes: 64, WindowBudgetBytes: transcriptWindowBudgetFloorBytes,
	}

	initial, err := operations.Invoke(t.Context(), caller, appwire.SessionOperationRequest{Turns: &params})
	if err != nil {
		t.Fatal(err)
	}
	if initial.Turns == nil || initial.Turns.Window == nil || initial.Turns.Window.ContinuationCursor == "" {
		t.Fatalf("initial remote window = %+v, want usable continuation metadata", initial.Turns)
	}
	requireRetainedCanonicalItems(t, operations, params.Ref, flattenTestItems(initial.Turns.Data))
	oldestInitial := initial.Turns.Data[0].ID
	params.Cursor = initial.Turns.Window.ContinuationCursor
	continued, err := operations.Invoke(t.Context(), caller, appwire.SessionOperationRequest{Turns: &params})
	if err != nil {
		t.Fatal(err)
	}
	if continued.Turns == nil || len(continued.Turns.Data) == 0 || continued.Turns.Data[len(continued.Turns.Data)-1].ID >= oldestInitial {
		t.Fatalf("continued remote window = %+v, want turns older than %q", continued.Turns, oldestInitial)
	}
	requireRetainedCanonicalItems(t, operations, params.Ref, flattenTestItems(continued.Turns.Data))
}

func TestSessionOperationsServesStoppedCurrentRemoteSavedWindowsWithoutDaemonCapability(t *testing.T) {
	var requested []appwire.ThreadTurnWindow
	remote, _ := newScriptedRemoteHub(t, func(method string, raw json.RawMessage) any {
		switch method {
		case appwire.MethodInitialize:
			return appwire.InitializeResponse{
				ProtocolVersion: appwire.ProtocolVersion, SourceID: "local",
			}
		case appwire.MethodThreadList:
			return appwire.ThreadListResponse{Data: []appwire.Thread{{
				ID: "stopped-remote", Source: "local", Evener: appwire.EvenerThread{Ref: "local:stopped-remote"},
			}}}
		case appwire.MethodThreadRead:
			var params appwire.ThreadReadParams
			if err := json.Unmarshal(raw, &params); err != nil {
				return appwire.InternalError(err.Error())
			}
			if params.TurnWindow == nil {
				return appwire.InvalidParams("saved window was not forwarded")
			}
			requested = append(requested, *params.TurnWindow)
			position := appwire.ThreadItemPosition{Entry: uint64(len(requested))}
			return appwire.ThreadReadResponse{
				Thread: appwire.Thread{ID: "stopped-remote", Source: "local", Evener: appwire.EvenerThread{Ref: "local:stopped-remote"}, Turns: []appwire.Turn{{
					ID: "selected", ItemsView: appwire.TurnItemsViewFragment, Items: []appwire.ThreadItem{{
						ID: "message", TranscriptKey: "message", TurnID: "selected", Type: "agentMessage", Position: &position, Version: 1,
					}},
				}}},
				Snapshot: &appwire.SnapshotIdentity{Incarnation: "saved-remote", Length: 3},
				Window:   &appwire.ThreadWindowResult{AppliedBy: transcriptWindowAppliedBySavedIndex, ServedTurns: 1},
			}
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
	windows := []appwire.ThreadTurnWindow{
		{Latest: 1},
		{Turn: "turn-2"},
		{Since: "turn-1", Limit: 2},
		{Before: &appwire.ThreadTurnBoundary{TurnID: "turn-3", Limit: 2}},
	}
	for _, window := range windows {
		params := appwire.ThreadTurnsListParams{Ref: "host-b:stopped-remote", TurnWindow: &window}
		response, err := operations.Invoke(t.Context(), caller, appwire.SessionOperationRequest{Turns: &params})
		if err != nil {
			t.Fatalf("window %+v: %v", window, err)
		}
		if response.Turns == nil || response.Turns.Window == nil || response.Turns.Window.AppliedBy != transcriptWindowAppliedBySavedIndex {
			t.Fatalf("window %+v response = %+v, want owning saved index", window, response.Turns)
		}
	}
	if !reflect.DeepEqual(requested, windows) {
		t.Fatalf("forwarded windows = %+v, want %+v", requested, windows)
	}
}

func TestSessionOperationsPreservesStoppedRemoteSavedWindowError(t *testing.T) {
	remote, _ := newScriptedRemoteHub(t, func(method string, _ json.RawMessage) any {
		switch method {
		case appwire.MethodInitialize:
			return appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion, SourceID: "local"}
		case appwire.MethodThreadList:
			return appwire.ThreadListResponse{Data: []appwire.Thread{{
				ID: "stopped-remote", Source: "local", Evener: appwire.EvenerThread{Ref: "local:stopped-remote"},
			}}}
		case appwire.MethodThreadRead:
			return appwire.TranscriptTurnNotFound("missing", "oldest", "newest", 2)
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

	_, err := operations.Invoke(t.Context(), appwire.ManagerView{Ref: "local:manager", Manager: true}, appwire.SessionOperationRequest{Turns: &appwire.ThreadTurnsListParams{
		Ref: "host-b:stopped-remote", TurnWindow: &appwire.ThreadTurnWindow{Turn: "missing"},
	}})
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("saved window error = %v, want typed remote error", err)
	}
	data, ok := appwire.DecodedErrorData(wire.Data)
	if !ok || data.EvenerErrorInfo != appwire.ErrorTranscriptTurnNotFound {
		t.Fatalf("saved window error = %+v, want remote turn frontier error", wire)
	}
}

func packCurrentRemoteHubRead(response appwire.ThreadReadResponse, params appwire.ThreadWindowParams, requestedLimit int) (appwire.ThreadReadResponse, error) {
	if params.TurnWindow == nil {
		return response, nil
	}
	itemLimit, err := appwire.NormalizeTranscriptItemLimit(requestedLimit)
	if err != nil {
		return appwire.ThreadReadResponse{}, err
	}
	candidates, err := itemCandidateResultFromReadResponse(response)
	if err != nil {
		return appwire.ThreadReadResponse{}, err
	}
	candidates.Identity = appitempaging.CursorIdentity{ThreadRef: "local:remote", Incarnation: "remote-history", ProjectionVersion: appitempaging.TranscriptItemProjectionVersion}
	candidates.Exhausted = true
	candidates, err = appendReadOverlayCandidates(candidates, response)
	if err != nil {
		return appwire.ThreadReadResponse{}, err
	}
	packed, err := packThreadReadWindowItemCandidates(candidates, func(packed appwire.ThreadReadResponse) (appwire.ThreadReadResponse, error) {
		packed.Thread = threadWithPackedTurns(response.Thread, packed.Thread.Turns)
		return packed, nil
	}, transcriptWindowOptionsFor(candidates, params, itemLimit, transcriptWindowAppliedByServingDaemon, appitempaging.FetchCursorOriginServingDaemon))
	if err != nil {
		return appwire.ThreadReadResponse{}, err
	}
	response.Snapshot.Incarnation = params.ViewIncarnation(response.Snapshot.Incarnation, itemLimit)
	return withReadHistoryIdentity(packed, response), nil
}

func packCurrentRemoteHubPage(turns []appwire.Turn, params appwire.ThreadWindowParams, requestedLimit int, cursor string) (appwire.ThreadTurnsListResponse, error) {
	itemLimit, err := appwire.NormalizeTranscriptItemLimit(requestedLimit)
	if err != nil {
		return appwire.ThreadTurnsListResponse{}, err
	}
	request := appwire.ThreadTurnsListParams{
		Ref: "local:remote", ThreadWindowParams: params, ItemLimit: requestedLimit, Cursor: cursor,
	}
	loaded, _, err := resolveTranscriptWindowContinuation(context.Background(), request, itemLimit, func(_ context.Context, selector *appwire.ThreadTurnWindow) (transcriptWindowLoadedRange, error) {
		selected, resolved := resolveCompatibilityTurnWindow(turns, selector, true)
		if !resolved {
			return transcriptWindowLoadedRange{}, appwire.TranscriptItemCursorStale()
		}
		candidates, candidateErr := itemCandidateResultFromTurns(selected, "")
		if candidateErr != nil {
			return transcriptWindowLoadedRange{}, candidateErr
		}
		candidates.Identity = appitempaging.CursorIdentity{ThreadRef: "local:remote", Incarnation: "remote-history", ProjectionVersion: appitempaging.TranscriptItemProjectionVersion}
		candidates.Exhausted = true
		return transcriptWindowLoadedRange{
			Result: candidates,
			Thread: appwire.Thread{ID: "remote", Source: "local", Evener: appwire.EvenerThread{Ref: "local:remote"}, Turns: selected},
		}, nil
	})
	if err != nil {
		return appwire.ThreadTurnsListResponse{}, err
	}
	page, err := packThreadTurnsWindowItemCandidates(loaded.Result, func(page appwire.ThreadTurnsListResponse) (appwire.ThreadTurnsListResponse, error) {
		return page, nil
	}, transcriptWindowOptionsFor(loaded.Result, params, itemLimit, transcriptWindowAppliedByServingDaemon, appitempaging.FetchCursorOriginServingDaemon))
	if err != nil {
		return appwire.ThreadTurnsListResponse{}, err
	}
	page.Snapshot = &appwire.SnapshotIdentity{Incarnation: params.ViewIncarnation("remote-history", itemLimit), Length: int64(len(turns))}
	return page, nil
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
		case appwire.MethodThreadList:
			return appwire.ThreadListResponse{Data: []appwire.Thread{{
				ID: "remote", Source: "local", Evener: appwire.EvenerThread{Ref: "local:remote", Capabilities: appwire.ThreadCapabilities{WindowRead: true}},
			}}}
		case appwire.MethodThreadRead:
			thread := appwire.Thread{ID: "remote", Source: "local", Evener: appwire.EvenerThread{Ref: "local:remote", Capabilities: appwire.ThreadCapabilities{WindowRead: true}}}
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
		Ref:        "host-b:remote",
		TurnWindow: &appwire.ThreadTurnWindow{Turn: "turn-remote"}, ItemTypes: []string{"commandExecution"}, PreviewBytes: 8,
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
		return []appsource.LocalDaemonEntry{{
			Entry: rendezvous.Entry{
				Protocol: appwire.ProtocolVersion, Endpoint: endpoint, SourceID: "local",
				ThreadID: "windowed", SessionID: "windowed", WorkspaceRef: "local:windowed",
			},
			Capabilities: appwire.ThreadCapabilities{WindowRead: true}, CapabilitiesKnown: true,
		}}
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
		return []appsource.LocalDaemonEntry{{
			Entry: rendezvous.Entry{
				Protocol: appwire.ProtocolVersion, Endpoint: endpoint, SourceID: "local",
				ThreadID: "windowed", SessionID: "windowed", WorkspaceRef: "local:windowed",
			},
			Capabilities: appwire.ThreadCapabilities{WindowRead: true}, CapabilitiesKnown: true,
		}}
	}, http.DefaultClient)
	sources := appsource.NewRegistry()
	sources.Add(source)
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex("")}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", Version: "test", SourceID: "local"})
	operations := &sessionOperations{cfg: cfg, sources: sources, relays: newHubRelay(server, cfg, sources), server: server}

	page, err := operations.turns(t.Context(), appwire.ThreadTurnsListParams{
		Ref:        "local:windowed",
		TurnWindow: &appwire.ThreadTurnWindow{Latest: 1},
		ItemTypes:  []string{"messages"},
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
				TranscriptKey: fmt.Sprintf("message-%02d", i), Position: &position, Text: "answer" + strings.Repeat("x", 100), Version: uint64(i + 1),
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
		return []appsource.LocalDaemonEntry{{
			Entry: rendezvous.Entry{
				Protocol: appwire.ProtocolVersion, Endpoint: endpoint, SourceID: "local",
				ThreadID: "windowed", SessionID: "windowed", WorkspaceRef: "local:windowed",
			},
			Capabilities: appwire.ThreadCapabilities{WindowRead: true}, CapabilitiesKnown: true,
		}}
	}, http.DefaultClient)
	sources := appsource.NewRegistry()
	sources.Add(source)
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex("")}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", Version: "test", SourceID: "local"})
	operations := &sessionOperations{cfg: cfg, sources: sources, relays: newHubRelay(server, cfg, sources), server: server}

	params := appwire.ThreadTurnsListParams{
		Ref: "local:windowed", ItemLimit: 10,
		TurnWindow: &appwire.ThreadTurnWindow{Turn: "turn_1"}, ItemTypes: []string{"messages"}, MessageBytes: len("answer"),
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
		requireRetainedCanonicalItems(t, operations, params.Ref, page.Data[0].Items)
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

func requireRetainedCanonicalItems(t *testing.T, operations *sessionOperations, ref string, items []appwire.ThreadItem) {
	t.Helper()
	retained := 0
	for _, item := range items {
		if item.FetchCursor == "" {
			continue
		}
		cursor, _, err := appitempaging.DecodeFetchCursorForRef(item.FetchCursor, ref)
		if err != nil {
			t.Fatalf("decode fetch cursor for %q: %v", item.ID, err)
		}
		if _, ok := operations.canonicalItemStore().get(cursor.Token); !ok {
			t.Fatalf("fetch cursor for %q has no retained canonical bytes", item.ID)
		}
		retained++
	}
	if retained == 0 {
		t.Fatal("page has no fetch cursor to verify")
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
