package server

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

// A session's thread snapshots carry the first question of its pending ask
// beside AskPending (S1b). Both come from one read of the pending set, so the
// flag is the question's presence, and the answer clears both.
func TestThreadSnapshotsCarryThePendingQuestion(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	question := &appwire.PendingQuestion{Question: "Which datastore for the ingest path?", Options: []string{"Postgres", "SQLite"}, Count: 2}
	src := publishEnvelope(srv, &stubThreadEnvelopeSource{question: question})
	listed := func() appwire.EvenerThread {
		t.Helper()
		list, err := srv.handleAppThreadList(context.Background(), appwire.ThreadListParams{StatusOnly: true})
		if err != nil {
			t.Fatalf("thread/list: %v", err)
		}
		return list.Data[0].Evener
	}
	if got := listed(); !got.AskPending || !reflect.DeepEqual(got.PendingQuestion, question) {
		t.Fatalf("listed ask = %v with question %+v, want the pending question %+v", got.AskPending, got.PendingQuestion, question)
	}
	if got := readThreadOverWire(t, srv, "local:root").Evener; !got.AskPending || !reflect.DeepEqual(got.PendingQuestion, question) {
		t.Fatalf("read ask = %v with question %+v, want the pending question %+v", got.AskPending, got.PendingQuestion, question)
	}

	// The answer empties the pending set, and the user input that carries it
	// re-samples the ask facet.
	src.question = nil
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventUserInput, SessionID: "root", Data: events.UserInputData{Text: "Postgres"}}, nil)
	got := listed()
	if got.AskPending || got.PendingQuestion != nil {
		t.Fatalf("after the answer ask = %v with question %+v, want neither", got.AskPending, got.PendingQuestion)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "pendingQuestion") {
		t.Fatalf("a session with no pending ask carries pendingQuestion: %s", raw)
	}
}
