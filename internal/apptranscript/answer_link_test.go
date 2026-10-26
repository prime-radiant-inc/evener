package apptranscript_test

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/internal/transcriptindex"
)

func TestManagerAnswerLinkLiveAndSaved(t *testing.T) {
	t.Parallel()
	for _, kind := range []schema.TurnKind{schema.TurnUserInput, schema.TurnSteering} {
		t.Run(string(kind), func(t *testing.T) { checkAnswerLinkLiveAndSaved(t, kind) })
	}
}

func checkAnswerLinkLiveAndSaved(t *testing.T, kind schema.TurnKind) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	var turn schema.Turn
	if err := json.Unmarshal([]byte(`{"format":1,"turn_id":"answer","kind":"USER_INPUT","answer_to_turn_ids":["question"],"message":{"role":"user","content":[{"kind":"text","text":"yes"}]}}`), &turn); err != nil {
		t.Fatal(err)
	}
	turn.Kind = kind
	if err := writer.Append(turn); err != nil {
		t.Fatal(err)
	}
	check := func(t *testing.T, items []appwire.ThreadItem) {
		t.Helper()
		if len(items) != 1 {
			t.Fatalf("items=%+v", items)
		}
		raw, err := json.Marshal(items[0])
		if err != nil {
			t.Fatal(err)
		}
		var item struct {
			Observation *struct {
				TurnID          string   `json:"turnId"`
				AnswerToTurnIDs []string `json:"answerToTurnIds"`
			} `json:"observation"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			t.Fatal(err)
		}
		if item.Observation == nil || item.Observation.TurnID != "answer" || !reflect.DeepEqual(item.Observation.AnswerToTurnIDs, []string{"question"}) {
			t.Fatalf("lost recorded answer link: %s", raw)
		}
	}
	t.Run("shared live projection", func(t *testing.T) {
		items, _ := apptranscript.ProjectEntryParts("answer", 1, turn, nil, nil, nil)
		check(t, items)
	})
	t.Run("saved indexed projection", func(t *testing.T) {
		index, err := transcriptindex.Open(path, filepath.Join(t.TempDir(), "index"))
		if err != nil {
			t.Fatal(err)
		}
		defer index.Close()
		window, err := index.Latest(40)
		if err != nil {
			t.Fatal(err)
		}
		var items []appwire.ThreadItem
		for _, candidate := range window.Candidates {
			items = append(items, candidate.Item)
		}
		check(t, items)
	})
}
