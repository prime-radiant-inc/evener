package hub

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent/schema"
)

// TestPastThreadReadProjectsPersistedNotes mirrors
// TestPastThreadReadProjectsPersistedGoal (app_threadread_tasks_test.go):
// stored human/agent notes and the URL list project into the past thread
// snapshot; an empty stored state reads as absent, never invented.
func TestPastThreadReadProjectsPersistedNotes(t *testing.T) {
	cfg, sessionID, _ := seedPastSessionWithTasks(t, nil)
	entry, ok := cfg.Past.Find(sessionID)
	if !ok {
		t.Fatal("past entry not found")
	}
	entry.Meta.HumanNote = "human hello"
	entry.Meta.AgentNote = "agent hello"
	entry.Meta.SessionURLs = []schema.SessionURL{
		{ID: "u1", URL: "https://x.test/y", Label: "x", AddedBy: "agent", AddedAt: 7},
	}

	thread, err := pastEntryThread(context.Background(), cfg, entry, false)
	if err != nil {
		t.Fatalf("pastEntryThread: %v", err)
	}
	if thread.Evener.HumanNote != "human hello" || thread.Evener.AgentNote != "agent hello" {
		t.Fatalf("persisted notes = %q/%q, want human hello/agent hello", thread.Evener.HumanNote, thread.Evener.AgentNote)
	}
	if len(thread.Evener.SessionURLs) != 1 {
		t.Fatalf("persisted urls = %+v, want one entry", thread.Evener.SessionURLs)
	}
	got := thread.Evener.SessionURLs[0]
	if got.ID != "u1" || got.URL != "https://x.test/y" || got.Label != "x" || got.AddedBy != "agent" || got.AddedAt != 7 {
		t.Fatalf("persisted url = %+v, want u1/https://x.test/y/x/agent/7", got)
	}
	if !thread.Evener.Capabilities.SharedNotes {
		t.Fatal("past thread capabilities miss SharedNotes")
	}
	// The projection must not alias the stored meta: mutating the served
	// snapshot must leave the past index's SessionMeta unchanged.
	thread.Evener.SessionURLs[0].URL = "mutated"
	if entry.Meta.SessionURLs[0].URL != "https://x.test/y" {
		t.Fatal("past notes projection aliases stored meta")
	}
}

func TestPastThreadReadNotesAbsentWhenUnset(t *testing.T) {
	cfg, sessionID, _ := seedPastSessionWithTasks(t, nil)
	entry, ok := cfg.Past.Find(sessionID)
	if !ok {
		t.Fatal("past entry not found")
	}
	thread, err := pastEntryThread(context.Background(), cfg, entry, false)
	if err != nil {
		t.Fatalf("pastEntryThread: %v", err)
	}
	if thread.Evener.HumanNote != "" || thread.Evener.AgentNote != "" {
		t.Fatalf("unset notes = %q/%q, want empty", thread.Evener.HumanNote, thread.Evener.AgentNote)
	}
	if thread.Evener.SessionURLs != nil {
		t.Fatalf("unset urls = %+v, want nil", thread.Evener.SessionURLs)
	}
}
