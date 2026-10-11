package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
)

// The thread snapshot carries when the last turn ended, sampled from the
// session's meta. TURN_ENDED re-samples it, because it re-samples every facet
// (S4).
func TestThreadSnapshotsCarryTheLastTurnEndedTime(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	ended := time.Date(2026, 9, 26, 12, 0, 0, 123_000_000, time.UTC)
	src := publishEnvelope(srv, &stubThreadEnvelopeSource{meta: schema.SessionMeta{ID: "root", LastTurnEndedAt: ended}})
	if got := listedRootEvener(t, srv).LastTurnEndedAt; got != ended.UnixMilli() {
		t.Fatalf("listed lastTurnEndedAt = %d, want %d", got, ended.UnixMilli())
	}
	if got := readThreadOverWire(t, srv, "local:root").Evener.LastTurnEndedAt; got != ended.UnixMilli() {
		t.Fatalf("read lastTurnEndedAt = %d, want %d", got, ended.UnixMilli())
	}
	later := ended.Add(time.Minute)
	src.meta.LastTurnEndedAt = later
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventTurnEnded, SessionID: "root", Data: events.TurnEndedData{TurnDurationMS: 60_000}}, nil)
	if got := listedRootEvener(t, srv).LastTurnEndedAt; got != later.UnixMilli() {
		t.Fatalf("after TURN_ENDED lastTurnEndedAt = %d, want %d", got, later.UnixMilli())
	}
}

// A session that has not ended a turn carries no key at all.
func TestThreadSnapshotsOmitTheTurnEndBeforeAnyTurnEnds(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	publishEnvelope(srv, &stubThreadEnvelopeSource{meta: schema.SessionMeta{ID: "root"}})
	raw, err := json.Marshal(readThreadOverWire(t, srv, "local:root").Evener)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "lastTurnEndedAt") {
		t.Fatalf("a session with no turn end carries lastTurnEndedAt: %s", raw)
	}
}
