package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
	"primeradiant.com/evener/llm"
)

func TestLiveThreadReadPagesWindowTransferUnderCap(t *testing.T) {
	previous := historyWindowTransferPageBytes
	historyWindowTransferPageBytes = 96 << 10
	t.Cleanup(func() { historyWindowTransferPageBytes = previous })

	const textBytes = 24 << 10
	turns := make([]schema.Turn, 0, 8)
	for i := range 8 {
		turns = append(turns, inExecution(
			fmt.Sprintf("turn_m%d", i+1), i == 0,
			schema.NewTurn(schema.TurnUserInput, llm.User(strings.Repeat("x", textBytes))),
		))
	}
	st := newServedTranscript(t, NewServer(ServerConfig{}), "th_transfer", turns...)

	response, err := st.srv.appThreadReadSnapshotChecked(appwire.ThreadReadParams{
		ThreadID:     st.threadID,
		IncludeTurns: true,
		TurnWindow:   &appwire.ThreadTurnWindow{Latest: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	encodedTurns, err := json.Marshal(response.Thread.Turns)
	if err != nil {
		t.Fatal(err)
	}
	if len(encodedTurns) > historyWindowTransferPageBytes {
		t.Fatalf("windowed read carried %d turn bytes, transfer cap %d", len(encodedTurns), historyWindowTransferPageBytes)
	}
	encodedResponse, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if len(encodedResponse) > historyWindowTransferPageBytes {
		t.Fatalf("windowed read message is %d bytes, transfer cap %d", len(encodedResponse), historyWindowTransferPageBytes)
	}
	if response.Window == nil {
		t.Fatal("windowed read carries no transfer page state")
	}
	if response.Window.ServedTurns == 0 || response.Window.ServedTurns >= 8 || response.Window.WithheldTurns == 0 {
		t.Fatalf("transfer page = %+v, want a proper subset of the eight selected turns", response.Window)
	}
	if got, want := len(response.Thread.Turns), response.Window.ServedTurns; got != want {
		t.Fatalf("served turns = %d, transfer state says %d", got, want)
	}
	if response.Window.FirstWithheldTurnID == "" || response.Window.LastWithheldTurnID == "" {
		t.Fatalf("transfer page names no withheld turns: %+v", response.Window)
	}
	for i, turn := range response.Thread.Turns {
		if want := fmt.Sprintf("turn_m%d", 8-len(response.Thread.Turns)+i+1); turn.ID != want {
			t.Fatalf("served turn %d = %q, want newest chronological order starting at %q", i, turn.ID, want)
		}
	}
}

func TestLiveThreadReadSmallWindowCarriesNoTransferPage(t *testing.T) {
	st := newServedTranscript(t, NewServer(ServerConfig{}), "th_transfer_small",
		inExecution("turn_m1", true, schema.NewTurn(schema.TurnUserInput, llm.User("small"))),
	)

	response, err := st.srv.appThreadReadSnapshotChecked(appwire.ThreadReadParams{
		ThreadID:     st.threadID,
		IncludeTurns: true,
		TurnWindow:   &appwire.ThreadTurnWindow{Latest: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Window != nil {
		t.Fatalf("small window carried transfer state: %+v", response.Window)
	}
	if len(response.Thread.Turns) != 1 || response.Thread.Turns[0].ID != "turn_m1" {
		t.Fatalf("small window turns = %+v, want the one selected turn", response.Thread.Turns)
	}
}

// One execution turn bigger than the transfer cap must not travel whole: the
// page cuts between its items, says the turn continues, and every follow-up
// page stays under the cap while recovering the rest of the same turn.
func TestLiveThreadReadPagesTransferInsideOversizedTurn(t *testing.T) {
	previous := historyWindowTransferPageBytes
	historyWindowTransferPageBytes = 96 << 10
	t.Cleanup(func() { historyWindowTransferPageBytes = previous })

	const textBytes = 24 << 10
	const itemCount = 6
	turns := make([]schema.Turn, 0, itemCount)
	turns = append(turns, inExecution("turn_m1", true, schema.NewTurn(schema.TurnUserInput, llm.User(strings.Repeat("u", textBytes)))))
	for range itemCount - 1 {
		turns = append(turns, inExecution("turn_m1", false, schema.NewTurn(schema.TurnAssistant, llm.Assistant(strings.Repeat("a", textBytes)))))
	}
	st := newServedTranscript(t, NewServer(ServerConfig{}), "th_oversized_transfer", turns...)

	seen := make(map[string]bool, itemCount)
	window := appwire.ThreadTurnWindow{Latest: 1}
	var resume *appwire.ThreadWindowTransfer
	for page := range itemCount {
		response, err := st.srv.appThreadReadSnapshotChecked(appwire.ThreadReadParams{
			ThreadID: st.threadID, IncludeTurns: true,
			TurnWindow: &window, WindowTransfer: resume,
		})
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		if len(encoded) > historyWindowTransferPageBytes {
			t.Fatalf("transfer page %d is %d bytes, cap %d", page, len(encoded), historyWindowTransferPageBytes)
		}
		if len(response.Thread.Turns) != 1 || response.Thread.Turns[0].ID != "turn_m1" {
			t.Fatalf("transfer page %d turns = %+v, want the one selected turn", page, response.Thread.Turns)
		}
		turn := response.Thread.Turns[0]
		for _, item := range turn.Items {
			if item.TranscriptKey == "" || item.Position == nil {
				t.Fatalf("transfer page %d item = %+v, want a positioned transcript key", page, item)
			}
			if seen[item.TranscriptKey] {
				t.Fatalf("transfer page %d repeats item %q", page, item.TranscriptKey)
			}
			seen[item.TranscriptKey] = true
		}
		if !turn.HasEarlierItems {
			break
		}
		first := turn.Items[0]
		if first.Position == nil {
			t.Fatalf("transfer page %d starts with an unpositioned item", page)
		}
		window = appwire.ThreadTurnWindow{Turn: "turn_m1"}
		resume = &appwire.ThreadWindowTransfer{TurnID: "turn_m1", Position: *first.Position, Before: true}
	}
	if len(seen) != itemCount {
		t.Fatalf("transfer pages recovered %d of %d items", len(seen), itemCount)
	}
}

// A reopened turn can have items on both sides of another selected turn:
// A1, B1..B6, A2. A transfer page must serve whole logical turns in turn
// order, so a page never carries a fragment of a turn it reports withheld.
// The reopened turn's items then arrive on the continuation page named by the
// page's withheld turns, and each page's items stay in transcript order.
func TestLiveThreadReadPagesInterleavedTurnAcrossTransferCap(t *testing.T) {
	previous := historyWindowTransferPageBytes
	historyWindowTransferPageBytes = 96 << 10
	t.Cleanup(func() { historyWindowTransferPageBytes = previous })

	const textBytes = 24 << 10
	const bItems = 6
	turns := []schema.Turn{
		inExecution("turn_a", true, schema.NewTurn(schema.TurnUserInput, llm.User(strings.Repeat("a", textBytes)))),
		inExecution("turn_b", true, schema.NewTurn(schema.TurnUserInput, llm.User(strings.Repeat("b", textBytes)))),
	}
	for range bItems - 1 {
		turns = append(turns, inExecution("turn_b", false, schema.NewTurn(schema.TurnAssistant, llm.Assistant(strings.Repeat("b", textBytes)))))
	}
	turns = append(turns, inExecution("turn_a", false, schema.NewTurn(schema.TurnAssistant, llm.Assistant(strings.Repeat("a", textBytes)))))
	st := newServedTranscript(t, NewServer(ServerConfig{}), "th_interleaved_transfer", turns...)

	latest := appwire.ThreadTurnWindow{Latest: 2}
	first, err := st.srv.appThreadReadSnapshotChecked(appwire.ThreadReadParams{
		ThreadID: st.threadID, IncludeTurns: true, TurnWindow: &latest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Thread.Turns) == 0 {
		t.Fatal("the first transfer page is empty")
	}
	for _, turn := range first.Thread.Turns {
		if turn.ID == "turn_a" {
			t.Fatalf("the first transfer page carries a turn_a fragment while reporting turn_a withheld: %+v", first.Thread.Turns)
		}
	}
	if first.Window == nil {
		t.Fatal("the first transfer page carries no transfer state")
	}
	if first.Window.ServedTurns != 1 || first.Window.WithheldTurns != 1 ||
		first.Window.FirstServedTurnID != "turn_b" || first.Window.FirstWithheldTurnID != "turn_a" {
		t.Fatalf("transfer state = %+v, want turn_b served and turn_a withheld", first.Window)
	}
	firstTurn := first.Thread.Turns[0]
	if !firstTurn.HasEarlierItems || len(firstTurn.Items) == 0 || firstTurn.Items[0].Position == nil {
		t.Fatalf("the cut turn fragment = %+v, want an earlier-items continuation", firstTurn)
	}

	resumeWindow := appwire.ThreadTurnWindow{Turn: "turn_b"}
	resume := &appwire.ThreadWindowTransfer{TurnID: "turn_b", Position: *firstTurn.Items[0].Position, Before: true}
	second, err := st.srv.appThreadReadSnapshotChecked(appwire.ThreadReadParams{
		ThreadID: st.threadID, IncludeTurns: true, TurnWindow: &resumeWindow, WindowTransfer: resume,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Thread.Turns) != 1 || second.Thread.Turns[0].ID != "turn_b" {
		t.Fatalf("resume page turns = %+v, want the rest of turn_b", second.Thread.Turns)
	}
	if second.Thread.Turns[0].HasEarlierItems {
		t.Fatalf("resume page still reports earlier items: %+v", second.Thread.Turns[0])
	}

	withheld := appwire.ThreadTurnWindow{Before: &appwire.ThreadTurnBoundary{TurnID: "turn_b", Limit: first.Window.WithheldTurns}}
	third, err := st.srv.appThreadReadSnapshotChecked(appwire.ThreadReadParams{
		ThreadID: st.threadID, IncludeTurns: true, TurnWindow: &withheld,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(third.Thread.Turns) != 1 || third.Thread.Turns[0].ID != "turn_a" {
		t.Fatalf("withheld page turns = %+v, want the reopened turn whole", third.Thread.Turns)
	}
	if got := len(third.Thread.Turns[0].Items); got != 2 {
		t.Fatalf("reopened turn items = %d, want both fragments' items", got)
	}

	positions := make([]appwire.ThreadItemPosition, 0, bItems+2)
	texts := make([]string, 0, bItems+2)
	for _, page := range [][]appwire.Turn{first.Thread.Turns, second.Thread.Turns, third.Thread.Turns} {
		for _, turn := range page {
			for _, item := range turn.Items {
				if item.Position == nil {
					t.Fatalf("transfer page item %q has no position", item.ID)
				}
				positions = append(positions, *item.Position)
				texts = append(texts, item.Text)
			}
		}
	}
	for _, text := range texts {
		if text == "" {
			t.Fatal("a transferred item carried no text")
		}
	}
	slices.SortFunc(positions, appitempaging.ComparePosition)
	for i := 1; i < len(positions); i++ {
		if appitempaging.ComparePosition(positions[i-1], positions[i]) >= 0 {
			t.Fatalf("merged transfer positions repeat or reorder at %d: %v", i, positions)
		}
	}
	if len(positions) != bItems+2 {
		t.Fatalf("transfer pages covered %d items, want %d", len(positions), bItems+2)
	}
}

func TestLiveThreadReadHonorsTurnWindows(t *testing.T) {
	st := newServedTranscript(t, NewServer(ServerConfig{}), "th_window",
		inExecution("turn_m1", true, schema.NewTurn(schema.TurnUserInput, llm.User("one"))),
		inExecution("turn_m2", true, schema.NewTurn(schema.TurnUserInput, llm.User("two"))),
		inExecution("turn_m3", true, schema.NewTurn(schema.TurnUserInput, llm.User("three"))),
	)

	for name, window := range map[string]*appwire.ThreadTurnWindow{
		"latest": {Latest: 2},
		"exact":  {Turn: "turn_m2"},
		"since":  {Since: "turn_m1", Limit: 2},
		"before": {Before: &appwire.ThreadTurnBoundary{TurnID: "turn_m3", Limit: 2}},
	} {
		t.Run(name, func(t *testing.T) {
			response, err := st.srv.appThreadReadSnapshotChecked(appwire.ThreadReadParams{
				ThreadID:     st.threadID,
				IncludeTurns: true,
				TurnWindow:   window,
			})
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, turn := range response.Thread.Turns {
				got = append(got, turn.ID)
			}
			want := map[string][]string{
				"latest": {"turn_m2", "turn_m3"},
				"exact":  {"turn_m2"},
				"since":  {"turn_m2", "turn_m3"},
				"before": {"turn_m1", "turn_m2"},
			}[name]
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("turn IDs = %v, want %v", got, want)
			}
		})
	}

	_, err := st.srv.appThreadReadSnapshotChecked(appwire.ThreadReadParams{
		ThreadID:     st.threadID,
		IncludeTurns: true,
		TurnWindow:   &appwire.ThreadTurnWindow{Since: "missing", Limit: 2},
	})
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) || wireErr.Code != appwire.CodeInvalidParams {
		t.Fatalf("missing turn error = %v, want typed invalid params", err)
	}
	var data appwire.TranscriptTurnNotFoundErrorData
	raw, marshalErr := json.Marshal(wireErr.Data)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if unmarshalErr := json.Unmarshal(raw, &data); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	if data.EvenerErrorInfo != appwire.ErrorInfo("transcriptTurnNotFound") || data.TurnID != "missing" || data.OldestTurnID != "turn_m1" || data.NewestTurnID != "turn_m3" || data.TurnCount != 3 {
		t.Fatalf("missing turn data = %+v", data)
	}
}

func TestLiveThreadReadExactWindowPreservesEmptyCheckpointTurn(t *testing.T) {
	st := newServedTranscript(t, NewServer(ServerConfig{}), "th_empty_checkpoint",
		inExecution("turn_empty", true, schema.Turn{Kind: schema.TurnCheckpoint}),
	)

	response, err := st.srv.appThreadReadSnapshotChecked(appwire.ThreadReadParams{
		ThreadID:     st.threadID,
		IncludeTurns: true,
		TurnWindow:   &appwire.ThreadTurnWindow{Turn: "turn_empty"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Thread.Turns) != 1 {
		t.Fatalf("turns = %+v, want one selected turn shell", response.Thread.Turns)
	}
	if turn := response.Thread.Turns[0]; turn.ID != "turn_empty" || len(turn.Items) != 0 {
		t.Fatalf("selected turn = %+v, want empty turn_empty shell", turn)
	}
}

func TestLiveThreadReadSinceWindowPreservesEmptyCheckpointFrontier(t *testing.T) {
	st := newServedTranscript(t, NewServer(ServerConfig{}), "th_empty_checkpoint_since",
		inExecution("turn_visible", true, schema.NewTurn(schema.TurnUserInput, llm.User("visible"))),
		inExecution("turn_empty", true, schema.Turn{Kind: schema.TurnCheckpoint}),
		inExecution("turn_after", true, schema.NewTurn(schema.TurnUserInput, llm.User("after"))),
	)

	response, err := st.srv.appThreadReadSnapshotChecked(appwire.ThreadReadParams{
		ThreadID:     st.threadID,
		IncludeTurns: true,
		TurnWindow:   &appwire.ThreadTurnWindow{Since: "turn_visible", Limit: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Thread.Turns) != 2 {
		t.Fatalf("turns = %+v, want empty checkpoint and following turn", response.Thread.Turns)
	}
	if empty := response.Thread.Turns[0]; empty.ID != "turn_empty" || len(empty.Items) != 0 {
		t.Fatalf("first unseen turn = %+v, want empty turn_empty shell", empty)
	}
	if response.Thread.Turns[1].ID != "turn_after" {
		t.Fatalf("second unseen turn = %+v, want turn_after", response.Thread.Turns[1])
	}
}

func TestLiveThreadReadAdvertisesWindowReadCapability(t *testing.T) {
	st := newServedTranscript(t, NewServer(ServerConfig{}), "th_window_capability",
		inExecution("turn_m1", true, schema.NewTurn(schema.TurnUserInput, llm.User("one"))),
	)

	response, err := st.srv.appThreadReadSnapshotChecked(appwire.ThreadReadParams{ThreadID: st.threadID})
	if err != nil {
		t.Fatal(err)
	}
	if !response.Thread.Evener.Capabilities.WindowRead {
		t.Fatalf("thread capabilities = %+v, want window reads advertised", response.Thread.Evener.Capabilities)
	}
}

func TestLiveThreadReadFetchesCanonicalItemRange(t *testing.T) {
	st := newServedTranscript(t, NewServer(ServerConfig{}), "th_fetch",
		inExecution("turn_m1", true, schema.NewTurn(schema.TurnUserInput, llm.User("fetch me"))),
	)
	windowParams := appwire.ThreadWindowParams{TurnWindow: &appwire.ThreadTurnWindow{Turn: "turn_m1"}}
	read, err := st.srv.appThreadReadSnapshotChecked(appwire.ThreadReadParams{ThreadID: st.threadID, IncludeTurns: true, ThreadWindowParams: windowParams})
	if err != nil {
		t.Fatal(err)
	}
	item := read.Thread.Turns[0].Items[0]
	want, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	identity := appitempaging.CursorIdentity{
		ThreadRef:         read.Thread.Evener.Ref,
		Incarnation:       windowParams.ViewIncarnation(read.Snapshot.Incarnation, 0),
		ProjectionVersion: appitempaging.TranscriptItemProjectionVersion,
	}
	cursor, err := appitempaging.EncodeFetchCursor(identity, appitempaging.FetchCursor{
		Position: *item.Position, ItemVersion: item.Version, Origin: appitempaging.FetchCursorOriginServingDaemon, Token: "0123456789abcdef-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := st.srv.handleAppThreadRead(context.Background(), appwire.ThreadReadParams{ThreadID: st.threadID, FetchCursor: cursor})
	if err != nil {
		t.Fatal(err)
	}
	if response.Fetch == nil {
		t.Fatal("serving daemon returned no fetch payload")
	}
	got, err := base64.StdEncoding.DecodeString(response.Fetch.Data)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) || response.Fetch.FetchCursor != "" {
		t.Fatalf("fetched canonical bytes = %d, continuation = %q; want %d bytes and no continuation", len(got), response.Fetch.FetchCursor, len(want))
	}
}
