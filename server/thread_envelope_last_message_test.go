package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

// The thread snapshot carries the opening of the session's last agent message,
// sampled from its meta; TURN_ENDED re-samples it, because it re-samples every
// facet (S1d). A session that has written no message carries no key.
func TestThreadSnapshotsCarryTheLastMessage(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	src := publishEnvelope(srv, &stubThreadEnvelopeSource{meta: schema.SessionMeta{ID: "root"}})
	listed := func() appwire.EvenerThread {
		t.Helper()
		list, err := srv.handleAppThreadList(context.Background(), appwire.ThreadListParams{StatusOnly: true})
		if err != nil {
			t.Fatalf("thread/list: %v", err)
		}
		return list.Data[0].Evener
	}
	raw, err := json.Marshal(listed())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "lastMessage") {
		t.Fatalf("a session that has written nothing carries lastMessage: %s", raw)
	}

	const message = "Three layouts are ready for review. I recommend B."
	src.meta.LastMessage = message
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventTurnEnded, SessionID: "root", Data: events.TurnEndedData{TurnDurationMS: 1_000}}, nil)
	if got := listed().LastMessage; got != message {
		t.Fatalf("listed lastMessage = %q, want %q", got, message)
	}
	if got := readThreadOverWire(t, srv, "local:root").Evener.LastMessage; got != message {
		t.Fatalf("read lastMessage = %q, want %q", got, message)
	}
}
