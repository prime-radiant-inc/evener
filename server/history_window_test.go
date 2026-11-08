package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
	"primeradiant.com/evener/llm"
)

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
		Position: *item.Position, ItemVersion: item.Version, Origin: appitempaging.FetchCursorOriginServingDaemon,
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
