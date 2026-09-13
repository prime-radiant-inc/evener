package events

import "testing"

func TestNotesEventKinds(t *testing.T) {
	if EventNotesUpdated == EventUrlsUpdated || EventNotesUpdated == EventGoalUpdated {
		t.Fatalf("note event kinds collide: %q %q", EventNotesUpdated, EventUrlsUpdated)
	}
	var _ EventData = NotesUpdatedData{}
	var _ EventData = UrlsUpdatedData{}
	if (NotesUpdatedData{}).eventKind() != EventNotesUpdated {
		t.Fatalf("NotesUpdatedData kind mismatch")
	}
	if (UrlsUpdatedData{}).eventKind() != EventUrlsUpdated {
		t.Fatalf("UrlsUpdatedData kind mismatch")
	}
}
