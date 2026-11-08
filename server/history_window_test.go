package server

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
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
				ThreadWindowParams: appwire.ThreadWindowParams{
					TurnWindow: window,
				},
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
		ThreadWindowParams: appwire.ThreadWindowParams{
			TurnWindow: &appwire.ThreadTurnWindow{Since: "missing", Limit: 2},
		},
	})
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) || wireErr.Code != appwire.CodeInvalidParams {
		t.Fatalf("missing turn error = %v, want typed invalid params", err)
	}
	var data struct {
		EvenerErrorInfo appwire.ErrorInfo `json:"evenerErrorInfo"`
		TurnID          string            `json:"turnId"`
		OldestTurnID    string            `json:"oldestTurnId"`
		NewestTurnID    string            `json:"newestTurnId"`
		TurnCount       int               `json:"turnCount"`
	}
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
