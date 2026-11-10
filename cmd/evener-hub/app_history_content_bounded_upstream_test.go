package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
