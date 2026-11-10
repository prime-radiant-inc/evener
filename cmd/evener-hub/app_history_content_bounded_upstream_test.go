package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

// boundedWindowDaemon serves a Manager window from a fake daemon that returns
// one page bounded by a synthetic upstream transfer cap, exactly as a daemon
// bounds its response. It records how many pages the hub asked for and the
// largest response it sent.
type boundedWindowDaemon struct {
	turns            []appwire.Turn
	capBytes         int
	pageCalls        int
	maxResponseBytes int
	servedPageTurns  [][]string
}

func (d *boundedWindowDaemon) handler(sessionID, ref string) http.HandlerFunc {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		if err := appserver.SubscribeWithAdmission(ctx, sessionID, false); err != nil {
			return appwire.ThreadReadResponse{}, err
		}
		thread := appwire.Thread{
			ID: sessionID, SessionID: sessionID, Source: "local",
			Evener: appwire.EvenerThread{Ref: ref, Capabilities: appwire.ThreadCapabilities{WindowRead: true}},
		}
		if !params.IncludeTurns || params.TurnWindow == nil {
			return appwire.ThreadReadResponse{Thread: thread}, nil
		}
		d.pageCalls++
		selected := selectWindowTestTurns(d.turns, *params.TurnWindow)
		page, withheld := pageWindowTestTurns(selected, *params.TurnWindow, d.capBytes)
		thread.Turns = page
		response := appwire.ThreadReadResponse{
			Thread:   thread,
			Snapshot: &appwire.SnapshotIdentity{Incarnation: "bounded-window-fixture", Length: int64(len(d.turns))},
		}
		if len(withheld) > 0 {
			response.Window = &appwire.ThreadWindowResult{
				ServedTurns:         len(page),
				WithheldTurns:       len(withheld),
				FirstWithheldTurnID: withheld[0].ID,
				LastWithheldTurnID:  withheld[len(withheld)-1].ID,
				TrimmedEnd:          "oldest",
			}
			if params.TurnWindow.Since != "" {
				response.Window.TrimmedEnd = "newest"
			}
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			return appwire.ThreadReadResponse{}, err
		}
		d.maxResponseBytes = max(d.maxResponseBytes, len(encoded))
		return response, nil
	})
	return daemon.ServeWebSocket
}

func selectWindowTestTurns(turns []appwire.Turn, window appwire.ThreadTurnWindow) []appwire.Turn {
	if window.Latest > 0 {
		return turns[max(0, len(turns)-window.Latest):]
	}
	boundary := window.Turn
	if window.Since != "" {
		boundary = window.Since
	}
	if window.Before != nil {
		boundary = window.Before.TurnID
	}
	at := -1
	for i := range turns {
		if turns[i].ID == boundary {
			at = i
			break
		}
	}
	if at < 0 {
		return nil
	}
	switch {
	case window.Turn != "":
		return turns[at : at+1]
	case window.Since != "":
		return turns[at+1 : min(len(turns), at+1+window.Limit)]
	default:
		return turns[max(0, at-window.Before.Limit):at]
	}
}

// pageWindowTestTurns keeps whole turns from the side the packer serves first:
// newest for a latest or before window, oldest for a since window.
func pageWindowTestTurns(selected []appwire.Turn, window appwire.ThreadTurnWindow, capBytes int) ([]appwire.Turn, []appwire.Turn) {
	if len(selected) == 0 {
		return nil, nil
	}
	sizes := make([]int, len(selected))
	for i, turn := range selected {
		encoded, err := json.Marshal(turn)
		if err != nil {
			encoded = []byte(turn.ID)
		}
		sizes[i] = len(encoded)
	}
	used := 0
	if window.Since != "" {
		end := 0
		for end < len(selected) {
			if end > 0 && used+sizes[end] > capBytes {
				break
			}
			used += sizes[end]
			end++
		}
		return selected[:end], selected[end:]
	}
	start := len(selected)
	for start > 0 {
		if start < len(selected) && used+sizes[start-1] > capBytes {
			break
		}
		used += sizes[start-1]
		start--
	}
	return selected[start:], selected[:start]
}

func TestManagerWindowReadWalksBoundedUpstreamPages(t *testing.T) {
	const sessionID = "bounded-window-upstream"
	const ref = "local:" + sessionID
	const upstreamCap = 12 << 10
	const budget = 16 << 10

	daemon := &boundedWindowDaemon{capBytes: upstreamCap}
	for i := range 12 {
		id := fmt.Sprintf("turn-%02d", i+1)
		position := appwire.ThreadItemPosition{Entry: uint64(i + 1)}
		daemon.turns = append(daemon.turns, appwire.Turn{
			ID: id, Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFragment,
			Items: []appwire.ThreadItem{{
				ID: id + "-message", Type: "userMessage", TurnID: id, TranscriptKey: id + "-message",
				Text: strings.Repeat(fmt.Sprintf("%02d", i+1), 2_000), Position: &position, Version: 1,
			}},
		})
	}
	httpServer := httptest.NewServer(daemon.handler(sessionID, ref))
	t.Cleanup(httpServer.Close)

	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{
			Entry: rendezvous.Entry{
				Protocol: appwire.ProtocolVersion, Endpoint: "ws" + strings.TrimPrefix(httpServer.URL, "http"),
				SourceID: "local", ThreadID: sessionID, SessionID: sessionID,
				WorkspaceRef: ref, InstanceID: sessionID,
			},
			SessionID:         sessionID,
			CapabilitiesKnown: true,
			Capabilities:      appwire.ThreadCapabilities{WindowRead: true},
		}}
	}, httpServer.Client())
	sources := appsource.NewRegistry()
	sources.Add(source)
	cfg := testWebConfig(t, hubcore.WebConfig{})
	server := appserver.NewServer(appserver.ServerConfig{})
	owner := &sessionOperations{cfg: cfg, sources: sources, server: server, relays: newHubRelay(server, cfg, sources)}
	caller := appwire.ManagerView{Ref: "local:manager", InstanceID: "manager-instance", Manager: true}

	page := managerInvoke(t, owner, caller, appwire.SessionOperationRequest{
		Turns: &appwire.ThreadTurnsListParams{
			Ref:               ref,
			TurnWindow:        &appwire.ThreadTurnWindow{Latest: 12},
			ItemTypes:         []string{"messages"},
			WindowBudgetBytes: budget,
		},
	}).Turns
	if page == nil {
		t.Fatal("Manager windowed read returned no turns page")
	}
	if daemon.pageCalls < 2 {
		t.Fatalf("hub read %d upstream window page(s), want more than one bounded page", daemon.pageCalls)
	}
	if daemon.maxResponseBytes > upstreamCap {
		t.Fatalf("upstream page response is %d bytes, transfer cap %d", daemon.maxResponseBytes, upstreamCap)
	}
	if page.Window == nil || page.Window.EffectiveBudgetBytes != budget {
		t.Fatalf("window result = %+v, want the requested %d-byte budget", page.Window, budget)
	}
	encoded, err := json.Marshal(page.Data)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > page.Window.EffectiveBudgetBytes {
		t.Fatalf("served page is %d bytes, budget %d", len(encoded), page.Window.EffectiveBudgetBytes)
	}
	if len(page.Data) < 3 {
		t.Fatalf("served %d turn(s), want the page filled from the walked pages", len(page.Data))
	}
	if page.Window.WithheldTurns == 0 || page.Window.ContinuationCursor == "" {
		t.Fatalf("window result = %+v, want the unwalked turns withheld with a continuation", page.Window)
	}
	for _, turn := range page.Data {
		for _, item := range turn.Items {
			if item.Type != "userMessage" {
				t.Fatalf("served item type %q, want only messages", item.Type)
			}
		}
	}
}

func TestManagerSinceWindowReadWalksBoundedUpstreamPages(t *testing.T) {
	const sessionID = "bounded-since-upstream"
	const ref = "local:" + sessionID
	const upstreamCap = 12 << 10
	const budget = 16 << 10

	daemon := &boundedWindowDaemon{capBytes: upstreamCap}
	for i := range 13 {
		id := fmt.Sprintf("turn-%02d", i)
		position := appwire.ThreadItemPosition{Entry: uint64(i + 1)}
		daemon.turns = append(daemon.turns, appwire.Turn{
			ID: id, Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFragment,
			Items: []appwire.ThreadItem{{
				ID: id + "-message", Type: "userMessage", TurnID: id, TranscriptKey: id + "-message",
				Text: strings.Repeat(fmt.Sprintf("%02d", i), 2_000), Position: &position, Version: 1,
			}},
		})
	}
	httpServer := httptest.NewServer(daemon.handler(sessionID, ref))
	t.Cleanup(httpServer.Close)

	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{
			Entry: rendezvous.Entry{
				Protocol: appwire.ProtocolVersion, Endpoint: "ws" + strings.TrimPrefix(httpServer.URL, "http"),
				SourceID: "local", ThreadID: sessionID, SessionID: sessionID,
				WorkspaceRef: ref, InstanceID: sessionID,
			},
			SessionID:         sessionID,
			CapabilitiesKnown: true,
			Capabilities:      appwire.ThreadCapabilities{WindowRead: true},
		}}
	}, httpServer.Client())
	sources := appsource.NewRegistry()
	sources.Add(source)
	cfg := testWebConfig(t, hubcore.WebConfig{})
	server := appserver.NewServer(appserver.ServerConfig{})
	owner := &sessionOperations{cfg: cfg, sources: sources, server: server, relays: newHubRelay(server, cfg, sources)}
	caller := appwire.ManagerView{Ref: "local:manager", InstanceID: "manager-instance", Manager: true}

	page := managerInvoke(t, owner, caller, appwire.SessionOperationRequest{
		Turns: &appwire.ThreadTurnsListParams{
			Ref:               ref,
			TurnWindow:        &appwire.ThreadTurnWindow{Since: "turn-00", Limit: 12},
			ItemTypes:         []string{"messages"},
			WindowBudgetBytes: budget,
		},
	}).Turns
	if page == nil {
		t.Fatal("Manager since window returned no turns page")
	}
	if daemon.pageCalls < 2 {
		t.Fatalf("hub read %d upstream window page(s), want more than one bounded page", daemon.pageCalls)
	}
	if daemon.maxResponseBytes > upstreamCap {
		t.Fatalf("upstream page response is %d bytes, transfer cap %d", daemon.maxResponseBytes, upstreamCap)
	}
	if page.Window == nil || page.Window.EffectiveBudgetBytes != budget {
		t.Fatalf("window result = %+v, want the requested %d-byte budget", page.Window, budget)
	}
	encoded, err := json.Marshal(page.Data)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > page.Window.EffectiveBudgetBytes {
		t.Fatalf("served page is %d bytes, budget %d", len(encoded), page.Window.EffectiveBudgetBytes)
	}
	if len(page.Data) < 2 || page.Data[0].ID != "turn-01" {
		t.Fatalf("served turns start at %+v, want the oldest unseen turn first", page.Data)
	}
	if page.Window.WithheldTurns == 0 || page.Window.ContinuationCursor == "" {
		t.Fatalf("window result = %+v, want the unwalked turns withheld with a continuation", page.Window)
	}
	if page.Window.TrimmedEnd != "newest" {
		t.Fatalf("trimmed end = %q, want newest", page.Window.TrimmedEnd)
	}
}

// oversizedTurnWindowDaemon serves one turn larger than its transfer page as
// item pages: the newest page first, then the older remainder wherever the hub
// resumes. It records how many pages the hub asked for and how it resumed.
type oversizedTurnWindowDaemon struct {
	turn             appwire.Turn
	itemsPerPage     int
	refuseTransfer   bool
	pageCalls        int
	resumeCalls      int
	maxResponseBytes int
}

func (d *oversizedTurnWindowDaemon) handler(sessionID, ref string) http.HandlerFunc {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		if err := appserver.SubscribeWithAdmission(ctx, sessionID, false); err != nil {
			return appwire.ThreadReadResponse{}, err
		}
		thread := appwire.Thread{
			ID: sessionID, SessionID: sessionID, Source: "local",
			Evener: appwire.EvenerThread{Ref: ref, Capabilities: appwire.ThreadCapabilities{WindowRead: true}},
		}
		response := appwire.ThreadReadResponse{
			Thread:   thread,
			Snapshot: &appwire.SnapshotIdentity{Incarnation: "oversized-turn-fixture", Length: 1},
		}
		if !params.IncludeTurns || params.TurnWindow == nil {
			return response, nil
		}
		d.pageCalls++
		if params.WindowTransfer != nil {
			d.resumeCalls++
			if d.refuseTransfer {
				// The strict-decode refusal an older daemon returns for a
				// parameter it does not know.
				return appwire.ThreadReadResponse{}, appwire.InvalidParams("windowTransfer is not a known field")
			}
			if params.WindowTransfer.TurnID != d.turn.ID {
				return appwire.ThreadReadResponse{}, appwire.InvalidParams("transfer names no served turn")
			}
		}
		items := d.turn.Items
		start, end := 0, len(items)
		switch {
		case params.WindowTransfer != nil && params.WindowTransfer.Before:
			at := -1
			for index := range items {
				if items[index].Position != nil && *items[index].Position == params.WindowTransfer.Position {
					at = index
					break
				}
			}
			if at < 0 {
				return appwire.ThreadReadResponse{}, appwire.InvalidParams("transfer position is not served")
			}
			end = at
			start = max(0, end-d.itemsPerPage)
		case params.WindowTransfer != nil:
			at := -1
			for index := range items {
				if items[index].Position != nil && *items[index].Position == params.WindowTransfer.Position {
					at = index
					break
				}
			}
			if at < 0 {
				return appwire.ThreadReadResponse{}, appwire.InvalidParams("transfer position is not served")
			}
			start = at + 1
			end = min(len(items), start+d.itemsPerPage)
		default:
			start = max(0, len(items)-d.itemsPerPage)
		}
		fragment := appwire.CloneTurn(d.turn)
		fragment.ItemsView = appwire.TurnItemsViewFragment
		fragment.Items = append([]appwire.ThreadItem(nil), items[start:end]...)
		fragment.HasEarlierItems = start > 0
		fragment.HasLaterItems = end < len(items)
		thread.Turns = []appwire.Turn{fragment}
		response.Thread = thread
		encoded, err := json.Marshal(response)
		if err != nil {
			return appwire.ThreadReadResponse{}, err
		}
		d.maxResponseBytes = max(d.maxResponseBytes, len(encoded))
		return response, nil
	})
	return daemon.ServeWebSocket
}

func TestManagerWindowReadResumesInsideAnOversizedTurn(t *testing.T) {
	const sessionID = "oversized-turn-upstream"
	const ref = "local:" + sessionID
	const upstreamCap = 24 << 10
	const budget = 16 << 10
	const itemsPerPage = 3
	const itemCount = 8

	daemon := &oversizedTurnWindowDaemon{
		itemsPerPage: itemsPerPage,
		turn: appwire.Turn{
			ID: "turn-1", Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFragment,
		},
	}
	for i := range itemCount {
		id := fmt.Sprintf("item-%02d", i)
		position := appwire.ThreadItemPosition{Entry: uint64(i + 1)}
		daemon.turn.Items = append(daemon.turn.Items, appwire.ThreadItem{
			ID: id, Type: "userMessage", TurnID: "turn-1", TranscriptKey: id,
			Text: strings.Repeat(strconv.Itoa(i), 4<<10), Position: &position, Version: 1,
		})
	}
	httpServer := httptest.NewServer(daemon.handler(sessionID, ref))
	t.Cleanup(httpServer.Close)

	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{
			Entry: rendezvous.Entry{
				Protocol: appwire.ProtocolVersion, Endpoint: "ws" + strings.TrimPrefix(httpServer.URL, "http"),
				SourceID: "local", ThreadID: sessionID, SessionID: sessionID,
				WorkspaceRef: ref, InstanceID: sessionID,
			},
			SessionID:         sessionID,
			CapabilitiesKnown: true,
			Capabilities:      appwire.ThreadCapabilities{WindowRead: true},
		}}
	}, httpServer.Client())
	sources := appsource.NewRegistry()
	sources.Add(source)
	cfg := testWebConfig(t, hubcore.WebConfig{})
	server := appserver.NewServer(appserver.ServerConfig{})
	owner := &sessionOperations{cfg: cfg, sources: sources, server: server, relays: newHubRelay(server, cfg, sources)}
	caller := appwire.ManagerView{Ref: "local:manager", InstanceID: "manager-instance", Manager: true}

	page := managerInvoke(t, owner, caller, appwire.SessionOperationRequest{
		Turns: &appwire.ThreadTurnsListParams{
			Ref:               ref,
			TurnWindow:        &appwire.ThreadTurnWindow{Latest: 1},
			ItemTypes:         []string{"messages"},
			WindowBudgetBytes: budget,
		},
	}).Turns
	if page == nil {
		t.Fatal("Manager windowed read returned no turns page")
	}
	if daemon.pageCalls < 2 || daemon.resumeCalls < 1 {
		t.Fatalf("hub read %d page(s) with %d intra-turn resume(s), want the rest of the oversized turn", daemon.pageCalls, daemon.resumeCalls)
	}
	if daemon.maxResponseBytes > upstreamCap {
		t.Fatalf("upstream page response is %d bytes, transfer cap %d", daemon.maxResponseBytes, upstreamCap)
	}
	if page.Window == nil || page.Window.EffectiveBudgetBytes != budget {
		t.Fatalf("window result = %+v, want the requested %d-byte budget", page.Window, budget)
	}
	encoded, err := json.Marshal(page.Data)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > page.Window.EffectiveBudgetBytes {
		t.Fatalf("served page is %d bytes, budget %d", len(encoded), page.Window.EffectiveBudgetBytes)
	}
	if len(page.Data) != 1 || page.Data[0].ID != "turn-1" {
		t.Fatalf("served turns = %v, want the one selected turn", turnIDsFromTurns(page.Data))
	}
	if got := len(page.Data[0].Items); got <= itemsPerPage {
		t.Fatalf("served turn carries %d item(s), want more than the first upstream page's %d", got, itemsPerPage)
	}
	for _, item := range page.Data[0].Items {
		if item.Type != "userMessage" {
			t.Fatalf("served item type %q, want only messages", item.Type)
		}
	}
}

func TestManagerWindowReadKeepsGatheredPageWhenDaemonRefusesResume(t *testing.T) {
	const sessionID = "oversized-turn-refused"
	const ref = "local:" + sessionID
	const budget = 16 << 10
	const itemsPerPage = 3

	daemon := &oversizedTurnWindowDaemon{
		itemsPerPage:   itemsPerPage,
		refuseTransfer: true,
		turn: appwire.Turn{
			ID: "turn-1", Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFragment,
		},
	}
	for i := range 6 {
		id := fmt.Sprintf("item-%02d", i)
		position := appwire.ThreadItemPosition{Entry: uint64(i + 1)}
		daemon.turn.Items = append(daemon.turn.Items, appwire.ThreadItem{
			ID: id, Type: "userMessage", TurnID: "turn-1", TranscriptKey: id,
			Text: strings.Repeat("x", 2<<10), Position: &position, Version: 1,
		})
	}
	httpServer := httptest.NewServer(daemon.handler(sessionID, ref))
	t.Cleanup(httpServer.Close)

	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{
			Entry: rendezvous.Entry{
				Protocol: appwire.ProtocolVersion, Endpoint: "ws" + strings.TrimPrefix(httpServer.URL, "http"),
				SourceID: "local", ThreadID: sessionID, SessionID: sessionID,
				WorkspaceRef: ref, InstanceID: sessionID,
			},
			SessionID:         sessionID,
			CapabilitiesKnown: true,
			Capabilities:      appwire.ThreadCapabilities{WindowRead: true},
		}}
	}, httpServer.Client())
	sources := appsource.NewRegistry()
	sources.Add(source)
	cfg := testWebConfig(t, hubcore.WebConfig{})
	server := appserver.NewServer(appserver.ServerConfig{})
	owner := &sessionOperations{cfg: cfg, sources: sources, server: server, relays: newHubRelay(server, cfg, sources)}
	caller := appwire.ManagerView{Ref: "local:manager", InstanceID: "manager-instance", Manager: true}

	page := managerInvoke(t, owner, caller, appwire.SessionOperationRequest{
		Turns: &appwire.ThreadTurnsListParams{
			Ref:               ref,
			TurnWindow:        &appwire.ThreadTurnWindow{Latest: 1},
			ItemTypes:         []string{"messages"},
			WindowBudgetBytes: budget,
		},
	}).Turns
	if page == nil {
		t.Fatal("Manager windowed read returned no turns page")
	}
	if daemon.resumeCalls < 1 {
		t.Fatalf("hub asked for %d intra-turn resume(s), want the refused request", daemon.resumeCalls)
	}
	if len(page.Data) != 1 || len(page.Data[0].Items) > itemsPerPage {
		t.Fatalf("served turns = %+v, want the gathered first page only", page.Data)
	}
}

// fencedWindowHandler serves bounded transfer pages through the production
// history handler, so every page's Snapshot.Incarnation carries the window
// view fence the handler applies. Two pages of one window therefore carry
// different snapshot identities even though the source transcript is the same.
func (d *boundedWindowDaemon) fencedWindowHandler(t *testing.T, sessionID, ref string) http.HandlerFunc {
	t.Helper()
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleThreadHistory(daemon.Router(),
		func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			thread := appwire.Thread{
				ID: sessionID, SessionID: sessionID, Source: "local",
				Evener: appwire.EvenerThread{Ref: ref, Capabilities: appwire.ThreadCapabilities{WindowRead: true}},
			}
			response := appwire.ThreadReadResponse{
				Thread: thread,
				// One source incarnation for the whole transcript. The handler
				// fences it per requested window.
				Snapshot: &appwire.SnapshotIdentity{Incarnation: "fenced-window-source", Length: int64(len(d.turns))},
			}
			if !params.IncludeTurns || params.TurnWindow == nil {
				return response, nil
			}
			d.pageCalls++
			selected := selectWindowTestTurns(d.turns, *params.TurnWindow)
			page, withheld := pageWindowTestTurns(selected, *params.TurnWindow, d.capBytes)
			thread.Turns = page
			response.Thread = thread
			served := make([]string, len(page))
			for index := range page {
				served[index] = page[index].ID
			}
			d.servedPageTurns = append(d.servedPageTurns, served)
			if len(withheld) > 0 {
				response.Window = &appwire.ThreadWindowResult{
					ServedTurns:         len(page),
					WithheldTurns:       len(withheld),
					FirstWithheldTurnID: withheld[0].ID,
					LastWithheldTurnID:  withheld[len(withheld)-1].ID,
					TrimmedEnd:          "oldest",
				}
				if params.TurnWindow.Since != "" {
					response.Window.TrimmedEnd = "newest"
				}
			}
			return response, nil
		},
		func(_ context.Context, _ appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
			t.Fatal("fenced window read unexpectedly paged thread/turns")
			return appwire.ThreadTurnsListResponse{}, nil
		})
	return daemon.ServeWebSocket
}

func TestManagerWindowReadGathersPagesUnderFencedSnapshots(t *testing.T) {
	const sessionID = "fenced-window-upstream"
	const ref = "local:" + sessionID
	const upstreamCap = 12 << 10
	const budget = 16 << 10

	daemon := &boundedWindowDaemon{capBytes: upstreamCap}
	for i := range 12 {
		id := fmt.Sprintf("turn-%02d", i+1)
		position := appwire.ThreadItemPosition{Entry: uint64(i + 1)}
		daemon.turns = append(daemon.turns, appwire.Turn{
			ID: id, Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFragment,
			Items: []appwire.ThreadItem{{
				ID: id + "-message", Type: "userMessage", TurnID: id, TranscriptKey: id + "-message",
				Text: strings.Repeat(fmt.Sprintf("%02d", i+1), 2_000), Position: &position, Version: 1,
			}},
		})
	}
	httpServer := httptest.NewServer(daemon.fencedWindowHandler(t, sessionID, ref))
	t.Cleanup(httpServer.Close)

	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{
			Entry: rendezvous.Entry{
				Protocol: appwire.ProtocolVersion, Endpoint: "ws" + strings.TrimPrefix(httpServer.URL, "http"),
				SourceID: "local", ThreadID: sessionID, SessionID: sessionID,
				WorkspaceRef: ref, InstanceID: sessionID,
			},
			SessionID:         sessionID,
			CapabilitiesKnown: true,
			Capabilities:      appwire.ThreadCapabilities{WindowRead: true},
		}}
	}, httpServer.Client())
	sources := appsource.NewRegistry()
	sources.Add(source)
	cfg := testWebConfig(t, hubcore.WebConfig{})
	server := appserver.NewServer(appserver.ServerConfig{})
	owner := &sessionOperations{cfg: cfg, sources: sources, server: server, relays: newHubRelay(server, cfg, sources)}
	caller := appwire.ManagerView{Ref: "local:manager", InstanceID: "manager-instance", Manager: true}

	page := managerInvoke(t, owner, caller, appwire.SessionOperationRequest{
		Turns: &appwire.ThreadTurnsListParams{
			Ref:               ref,
			TurnWindow:        &appwire.ThreadTurnWindow{Latest: 12},
			WindowBudgetBytes: budget,
		},
	}).Turns
	if page == nil {
		t.Fatal("Manager windowed read returned no turns page")
	}
	if len(daemon.servedPageTurns) < 2 {
		t.Fatalf("daemon served %d upstream page(s), want more than one bounded page", len(daemon.servedPageTurns))
	}
	firstPage := make(map[string]bool, len(daemon.servedPageTurns[0]))
	for _, turnID := range daemon.servedPageTurns[0] {
		firstPage[turnID] = true
	}
	gatheredLaterPage := false
	for _, turn := range page.Data {
		if !firstPage[turn.ID] {
			gatheredLaterPage = true
			break
		}
	}
	if !gatheredLaterPage {
		t.Fatalf("served turns %v are all from the first upstream page %v; the walk discarded the later page",
			turnIDsFromTurns(page.Data), daemon.servedPageTurns[0])
	}
}
