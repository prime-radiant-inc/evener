package appsource

import (
	"context"
	"net/http"
	"net/http/httptest"
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
