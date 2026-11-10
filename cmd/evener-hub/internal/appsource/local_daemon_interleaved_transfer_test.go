package appsource

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
	"primeradiant.com/evener/internal/appserver"
)

// interleavedTransferSelected names the interleaved selection A1, B1..B6, A2
// in transcript order: turn_a's items surround turn_b's, and turn_a's later
// item is physically newer than every turn_b item. A bounded transfer serves
// whole logical turns from the served edge, so the page that cuts inside
// turn_b carries none of turn_a, and the page named by the transfer's withheld
// turn carries all of it.
func interleavedTransferSelected() []appwire.ThreadItem {
	item := func(turnID, id, text string, entry uint64) appwire.ThreadItem {
		position := appwire.ThreadItemPosition{Entry: entry}
		return appwire.ThreadItem{
			Type: "agentMessage", ID: id, TurnID: turnID,
			TranscriptKey: "key-" + id, Position: &position,
			Text: text, Version: 1,
		}
	}
	return []appwire.ThreadItem{
		item("turn_a", "a1", "a-first", 1),
		item("turn_b", "b1", "b-1", 2),
		item("turn_b", "b2", "b-2", 3),
		item("turn_b", "b3", "b-3", 4),
		item("turn_b", "b4", "b-4", 5),
		item("turn_b", "b5", "b-5", 6),
		item("turn_b", "b6", "b-6", 7),
		item("turn_a", "a2", "a-second", 8),
	}
}

func TestLocalDaemonSourceMergesAnInterleavedWindowTransfer(t *testing.T) {
	const ref = "local:th_1"
	selected := interleavedTransferSelected()

	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleThreadHistory(daemon.Router(),
		func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			thread := appwire.Thread{
				ID: "th_1", SessionID: "sess_1", Source: "local",
				Evener: appwire.EvenerThread{Ref: ref, Capabilities: appwire.ThreadCapabilities{WindowRead: true}},
			}
			response := appwire.ThreadReadResponse{
				Thread:   thread,
				Snapshot: &appwire.SnapshotIdentity{Incarnation: "interleaved-window-source", Length: int64(len(selected))},
			}
			if !params.IncludeTurns || params.TurnWindow == nil {
				return response, nil
			}
			switch {
			case params.WindowTransfer != nil:
				// The rest of turn_b, older than the first page's newest items.
				response.Thread.Turns = []appwire.Turn{{
					ID: "turn_b", Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFragment,
					Items: []appwire.ThreadItem{
						appwire.CloneThreadItem(selected[1]), appwire.CloneThreadItem(selected[2]), appwire.CloneThreadItem(selected[3]),
					},
				}}
			case params.TurnWindow.Before != nil:
				// The withheld reopened turn, whole: nothing of turn_b is newer
				// than it, so the page carries both fragments' items.
				response.Thread.Turns = []appwire.Turn{{
					ID: "turn_a", Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFragment,
					Items: []appwire.ThreadItem{appwire.CloneThreadItem(selected[0]), appwire.CloneThreadItem(selected[7])},
				}}
			default:
				response.Thread.Turns = []appwire.Turn{{
					ID: "turn_b", Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFragment,
					HasEarlierItems: true,
					Items: []appwire.ThreadItem{
						appwire.CloneThreadItem(selected[4]), appwire.CloneThreadItem(selected[5]), appwire.CloneThreadItem(selected[6]),
					},
				}}
				response.Window = &appwire.ThreadWindowResult{
					ServedTurns: 1, WithheldTurns: 1,
					FirstServedTurnID: "turn_b", LastServedTurnID: "turn_b",
					FirstWithheldTurnID: "turn_a", LastWithheldTurnID: "turn_a",
					TrimmedEnd: "oldest",
				}
			}
			return response, nil
		},
		func(_ context.Context, _ appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
			t.Fatal("interleaved window read unexpectedly paged thread/turns")
			return appwire.ThreadTurnsListResponse{}, nil
		},
	)
	httpServer := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer httpServer.Close()

	source := newTestLocalDaemonSource(httpServer)
	response, err := source.ReadThread(context.Background(), appwire.ThreadReadParams{
		Ref: ref, IncludeTurns: true,
		TurnWindow: &appwire.ThreadTurnWindow{Latest: 2},
	})
	if err != nil {
		t.Fatalf("ReadThread: %v", err)
	}
	candidates, err := appitempaging.CandidatesFromTurns(response.Thread.Turns)
	if err != nil {
		t.Fatalf("merged turns: %v", err)
	}
	// This is the validation the hub packer applies before it serves a window
	// page: positions must increase and repeat neither.
	if err := appitempaging.ValidateCandidates(candidates); err != nil {
		t.Fatalf("merged window candidates are not a valid transcript order: %v (turns %+v)", err, turnIDsOf(response.Thread.Turns))
	}
	want := []string{"a1", "b1", "b2", "b3", "b4", "b5", "b6", "a2"}
	got := make([]string, len(candidates))
	for i := range candidates {
		got[i] = candidates[i].Item.ID
	}
	if len(got) != len(want) {
		t.Fatalf("merged items = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("merged items = %v, want %v", got, want)
		}
	}
}

func turnIDsOf(turns []appwire.Turn) []string {
	ids := make([]string, 0, len(turns))
	for _, turn := range turns {
		ids = append(ids, turn.ID)
	}
	return ids
}

// forwardInterleavedSelected names the forward selection over turn_0, then
// turns a, b and c in logical order. Turn a's items surround turn b's, so a
// page that serves a and b whole ends with a fragment of a: its physical last
// fragment is not its logical last served turn.
func forwardInterleavedSelected() []appwire.ThreadItem {
	item := func(turnID, id string, entry uint64) appwire.ThreadItem {
		position := appwire.ThreadItemPosition{Entry: entry}
		return appwire.ThreadItem{
			Type: "agentMessage", ID: id, TurnID: turnID,
			TranscriptKey: "key-" + id, Position: &position, Text: id, Version: 1,
		}
	}
	return []appwire.ThreadItem{
		item("turn_a", "a1", 1),
		item("turn_b", "b1", 2),
		item("turn_c", "c1", 3),
		item("turn_a", "a2", 4),
	}
}

// A forward transfer resumes strictly after the logical last turn it served.
// Naming the page's physical last fragment instead re-selects a turn the page
// already served, and the turn after it disappears: the since selector's limit
// counts the reloaded turn, so the daemon reports nothing withheld.
func TestLocalDaemonSourceResumesForwardFromTheLogicalLastServedTurn(t *testing.T) {
	const ref = "local:th_1"
	selected := forwardInterleavedSelected()
	byId := make(map[string]appwire.ThreadItem, len(selected))
	for _, item := range selected {
		byId[item.ID] = item
	}
	fragment := func(turnID string, items ...appwire.ThreadItem) appwire.Turn {
		return appwire.Turn{ID: turnID, Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFragment, Items: items}
	}
	var boundaries []string
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleThreadHistory(daemon.Router(),
		func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			response := appwire.ThreadReadResponse{
				Thread: appwire.Thread{
					ID: "th_1", SessionID: "sess_1", Source: "local",
					Evener: appwire.EvenerThread{Ref: ref, Capabilities: appwire.ThreadCapabilities{WindowRead: true}},
				},
				Snapshot: &appwire.SnapshotIdentity{Incarnation: "forward-window-source", Length: int64(len(selected))},
			}
			if !params.IncludeTurns || params.TurnWindow == nil {
				return response, nil
			}
			if params.TurnWindow.Since == "" {
				t.Fatalf("unexpected forward selector %+v", params.TurnWindow)
			}
			boundaries = append(boundaries, params.TurnWindow.Since)
			// A since selector selects the next Limit turns after its boundary,
			// so a boundary the page already served reloads a served turn and
			// never reaches the turn behind it.
			switch params.TurnWindow.Since {
			case "turn_0":
				response.Thread.Turns = []appwire.Turn{
					fragment("turn_a", appwire.CloneThreadItem(byId["a1"])),
					fragment("turn_b", appwire.CloneThreadItem(byId["b1"])),
					fragment("turn_a", appwire.CloneThreadItem(byId["a2"])),
				}
				response.Window = &appwire.ThreadWindowResult{
					ServedTurns: 2, WithheldTurns: 1,
					FirstServedTurnID: "turn_a", LastServedTurnID: "turn_b",
					FirstWithheldTurnID: "turn_c", LastWithheldTurnID: "turn_c",
					TrimmedEnd: "newest",
				}
			case "turn_b":
				response.Thread.Turns = []appwire.Turn{fragment("turn_c", appwire.CloneThreadItem(byId["c1"]))}
			case "turn_a":
				response.Thread.Turns = []appwire.Turn{fragment("turn_b", appwire.CloneThreadItem(byId["b1"]))}
			default:
				t.Fatalf("unexpected forward boundary %q", params.TurnWindow.Since)
			}
			return response, nil
		},
		func(_ context.Context, _ appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
			t.Fatal("forward interleaved window read unexpectedly paged thread/turns")
			return appwire.ThreadTurnsListResponse{}, nil
		},
	)
	httpServer := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer httpServer.Close()

	source := newTestLocalDaemonSource(httpServer)
	response, err := source.ReadThread(context.Background(), appwire.ThreadReadParams{
		Ref: ref, IncludeTurns: true,
		TurnWindow: &appwire.ThreadTurnWindow{Since: "turn_0", Limit: 3},
	})
	if err != nil {
		t.Fatalf("ReadThread: %v", err)
	}
	candidates, err := appitempaging.CandidatesFromTurns(response.Thread.Turns)
	if err != nil {
		t.Fatalf("merged turns: %v", err)
	}
	var got []string
	for _, candidate := range candidates {
		got = append(got, candidate.Item.ID)
	}
	want := []string{"a1", "b1", "c1", "a2"}
	if !slices.Equal(got, want) {
		t.Fatalf("forward merged items = %v, want %v (boundaries %v)", got, want, boundaries)
	}
	if !slices.Equal(boundaries, []string{"turn_0", "turn_b"}) {
		t.Fatalf("forward resume boundaries = %v, want the logical last served turn turn_b", boundaries)
	}
}
