package server

import (
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
)

// TestThreadEnvelopeSeedUsesStructuredMetaNotes mirrors
// TestThreadEnvelopeSeedUsesTaskAggregateAndStructuredMetaGoal: notes stored
// in session meta seed the envelope and reach the wire on thread/read, so a
// rejoin onto a live session carries the latest notes and URLs in the
// authoritative snapshot.
func TestThreadEnvelopeSeedUsesStructuredMetaNotes(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	source := &stubThreadEnvelopeSource{}
	// Real normalization can end at a space when its 1000-rune clamp cuts
	// between words; SessionMeta already carries that canonical value.
	wantHuman := strings.Repeat("x", 999) + " "
	source.meta.HumanNote = wantHuman
	source.meta.AgentNote = "agent hello"
	source.meta.SessionURLs = []schema.SessionURL{
		{ID: "u1", URL: "https://x.test/y", Label: "x", AddedBy: "agent", AddedAt: 7},
	}
	publishEnvelope(srv, source)

	thread := readThreadOverWire(t, srv, "local:th_1")
	if thread.Evener.HumanNote != wantHuman {
		t.Fatalf("thread.Evener.HumanNote = %q, want canonical %q", thread.Evener.HumanNote, wantHuman)
	}
	if thread.Evener.AgentNote != "agent hello" {
		t.Fatalf("thread.Evener.AgentNote = %q, want agent hello", thread.Evener.AgentNote)
	}
	if len(thread.Evener.SessionURLs) != 1 {
		t.Fatalf("thread.Evener.SessionURLs = %+v, want one entry", thread.Evener.SessionURLs)
	}
	got := thread.Evener.SessionURLs[0]
	if got.ID != "u1" || got.URL != "https://x.test/y" || got.Label != "x" || got.AddedBy != "agent" || got.AddedAt != 7 {
		t.Fatalf("thread.Evener.SessionURLs[0] = %+v, want u1/https://x.test/y/x/agent/7", got)
	}
}

// TestThreadEnvelopeSeedLeavesNotesAbsentWhenUnset mirrors the absent half:
// an unset stored state reads as empty, never invented.
func TestThreadEnvelopeSeedLeavesNotesAbsentWhenUnset(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	publishEnvelope(srv, &stubThreadEnvelopeSource{})

	thread := readThreadOverWire(t, srv, "local:th_1")
	if thread.Evener.HumanNote != "" || thread.Evener.AgentNote != "" {
		t.Fatalf("unset notes = %q/%q, want empty", thread.Evener.HumanNote, thread.Evener.AgentNote)
	}
	if thread.Evener.SessionURLs != nil {
		t.Fatalf("unset urls = %+v, want nil", thread.Evener.SessionURLs)
	}
}

// TestNotesAndUrlsCarriersReplaceSeededRootState mirrors
// TestTaskAndGoalCarriersReplaceSeededRootState: the typed pushes patch the
// envelope directly (no store re-pull), so the wire converges on the carrier
// even when the sampled source is stale.
func TestNotesAndUrlsCarriersReplaceSeededRootState(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	source := publishEnvelope(srv, &stubThreadEnvelopeSource{
		meta: schema.SessionMeta{
			HumanNote:   "stale human",
			AgentNote:   "stale agent",
			SessionURLs: []schema.SessionURL{{ID: "old", URL: "https://old.test/"}},
		},
	})
	metaCalls := source.metaCalls

	feedBridge(srv,
		events.SessionEvent{Kind: events.EventNotesUpdated, SessionID: "th_1", Data: events.NotesUpdatedData{
			HumanNote: "carrier human", AgentNote: "carrier agent",
		}},
		events.SessionEvent{Kind: events.EventUrlsUpdated, SessionID: "th_1", Data: events.UrlsUpdatedData{
			URLs: []events.SessionURLData{{ID: "u1", URL: "https://x.test/y", Label: "x", AddedBy: "agent", AddedAt: 7}},
		}},
	)
	thread := readThreadOverWire(t, srv, "local:th_1")
	if thread.Evener.HumanNote != "carrier human" || thread.Evener.AgentNote != "carrier agent" {
		t.Fatalf("carrier notes = %q/%q, want carrier human/carrier agent", thread.Evener.HumanNote, thread.Evener.AgentNote)
	}
	if len(thread.Evener.SessionURLs) != 1 || thread.Evener.SessionURLs[0].ID != "u1" || thread.Evener.SessionURLs[0].URL != "https://x.test/y" {
		t.Fatalf("carrier urls = %+v, want the pushed entry", thread.Evener.SessionURLs)
	}
	if source.metaCalls != metaCalls {
		t.Fatalf("carrier events re-pulled the meta store: meta calls %d→%d", metaCalls, source.metaCalls)
	}
	// Leave the source deliberately stale: the direct carrier must win.
	if source.meta.HumanNote != "stale human" {
		t.Fatal("fixture source unexpectedly changed; test no longer proves carrier-first behavior")
	}
}

// TestUrlsCarrierClearEmptiesTheWireList mirrors the goal-clear half: an
// empty pushed list reads as no links, not as the previously known list.
func TestUrlsCarrierClearEmptiesTheWireList(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	publishEnvelope(srv, &stubThreadEnvelopeSource{
		meta: schema.SessionMeta{
			SessionURLs: []schema.SessionURL{{ID: "old", URL: "https://old.test/"}},
		},
	})
	if got := readThreadOverWire(t, srv, "local:th_1").Evener.SessionURLs; len(got) != 1 {
		t.Fatalf("fixture did not publish a URL list to clear: %+v", got)
	}
	feedBridge(srv, events.SessionEvent{Kind: events.EventUrlsUpdated, SessionID: "th_1", Data: events.UrlsUpdatedData{}})
	if got := readThreadOverWire(t, srv, "local:th_1").Evener.SessionURLs; got != nil {
		t.Fatalf("thread/read still carries urls %+v after urls/updated cleared it", got)
	}
}
