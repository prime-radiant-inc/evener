package schema

import "testing"

func TestSessionMetaSharedNotesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	id := "notesrt1"
	seed := SessionMeta{ID: id, HumanNote: "human hello", AgentNote: "agent hello",
		SessionURLs: []SessionURL{{ID: "u1", URL: "https://x.test/y", Label: "x", AddedBy: "agent"}}}
	if err := SaveSessionMeta(dir, seed); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}
	got, err := LoadSessionMeta(dir, id)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if got.HumanNote != "human hello" || got.AgentNote != "agent hello" || len(got.SessionURLs) != 1 || got.SessionURLs[0].URL != "https://x.test/y" {
		t.Fatalf("round trip = %+v", got)
	}
}

func TestSessionMetaSharedNotesDefaultsEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := SaveSessionMeta(dir, SessionMeta{ID: "notesempty1"}); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}
	got, err := LoadSessionMeta(dir, "notesempty1")
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if got.HumanNote != "" || got.AgentNote != "" || len(got.SessionURLs) != 0 {
		t.Fatalf("defaults = %+v", got)
	}
}
