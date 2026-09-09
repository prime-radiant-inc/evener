package agent

import (
	"errors"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
)

// TestSetHumanNoteInterleavedRetryKeepsLatestNote verifies the outer journal:
// save A, save B, then retry A with A's original input replays A's recorded
// stored value without mutating, so B (the latest save) still stands.
func TestSetHumanNoteInterleavedRetryKeepsLatestNote(t *testing.T) {
	s := newNotesToolSession(t)
	defer s.Close()
	first, err := s.SetHumanNote("outer-a", "note A")
	if err != nil {
		t.Fatalf("save A: %v", err)
	}
	if first != "note A" {
		t.Fatalf("save A stored = %q, want %q", first, "note A")
	}
	second, err := s.SetHumanNote("outer-b", "note B")
	if err != nil {
		t.Fatalf("save B: %v", err)
	}
	if second != "note B" {
		t.Fatalf("save B stored = %q, want %q", second, "note B")
	}
	// Drain the two change events so the retry below must emit nothing.
	nextNotesEvent(t, s, events.EventNotesUpdated)
	nextNotesEvent(t, s, events.EventNotesUpdated)

	replayed, err := s.SetHumanNote("outer-a", "note A")
	if err != nil {
		t.Fatalf("retry A: %v", err)
	}
	if replayed != "note A" {
		t.Fatalf("retry A replayed = %q, want recorded %q", replayed, "note A")
	}
	s.mu.Lock()
	current := s.humanNote
	n := len(s.steeringQueue)
	s.mu.Unlock()
	if current != "note B" {
		t.Fatalf("stored note after retry A = %q, want B to stand", current)
	}
	if n != 2 {
		t.Fatalf("steering queue length = %d, want 2 (retry A must not steer again)", n)
	}
	// No event on replay: the stream must stay quiet.
	select {
	case ev := <-s.Events():
		t.Fatalf("retry A emitted %s, want silence", ev.Kind)
	default:
	}
}

// TestSetHumanNoteReusedIDWithDifferentInputConflicts verifies a reused outer
// ID with different input is rejected without mutating the stored note.
func TestSetHumanNoteReusedIDWithDifferentInputConflicts(t *testing.T) {
	s := newNotesToolSession(t)
	defer s.Close()
	if _, err := s.SetHumanNote("outer-1", "first"); err != nil {
		t.Fatalf("save: %v", err)
	}
	nextNotesEvent(t, s, events.EventNotesUpdated)
	if _, err := s.SetHumanNote("outer-1", "different"); !errors.Is(err, errClientMutationMismatch) {
		t.Fatalf("reused ID with different input err = %v, want mismatch", err)
	}
	s.mu.Lock()
	current := s.humanNote
	n := len(s.steeringQueue)
	s.mu.Unlock()
	if current != "first" {
		t.Fatalf("stored note = %q, want %q (conflict must not mutate)", current, "first")
	}
	if n != 1 {
		t.Fatalf("steering queue length = %d, want 1 (conflict must not steer)", n)
	}
}

// TestRemoveSessionURLRetryReplaysSuccess verifies a repeated urls/remove
// with the same outer ID and entry id returns success (replay) instead of an
// unknown-ID error, even though the entry is gone.
func TestRemoveSessionURLRetryReplaysSuccess(t *testing.T) {
	s := newNotesToolSession(t)
	defer s.Close()
	entry, err := s.addSessionURL("https://x.test/y", "")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	removed, err := s.RemoveSessionURL("outer-rm-1", entry.ID)
	if err != nil || !removed {
		t.Fatalf("remove = %v, %v; want true, nil", removed, err)
	}
	if _, ok := nextNotesEvent(t, s, events.EventUrlsUpdated).(events.UrlsUpdatedData); !ok {
		t.Fatal("URLS_UPDATED payload has wrong type")
	}
	replayed, err := s.RemoveSessionURL("outer-rm-1", entry.ID)
	if err != nil || !replayed {
		t.Fatalf("retry remove = %v, %v; want true, nil (success replay)", replayed, err)
	}
	if got := s.sessionURLsForTest(); len(got) != 0 {
		t.Fatalf("url list after replay = %+v, want empty", got)
	}
}

// TestRemoveSessionURLReusedIDWithDifferentInputConflicts verifies a reused
// outer ID naming a different entry id conflicts without removing anything.
func TestRemoveSessionURLReusedIDWithDifferentInputConflicts(t *testing.T) {
	s := newNotesToolSession(t)
	defer s.Close()
	a, err := s.addSessionURL("https://x.test/a", "")
	if err != nil {
		t.Fatalf("add a: %v", err)
	}
	b, err := s.addSessionURL("https://x.test/b", "")
	if err != nil {
		t.Fatalf("add b: %v", err)
	}
	if removed, err := s.RemoveSessionURL("outer-rm-1", a.ID); err != nil || !removed {
		t.Fatalf("remove a = %v, %v; want true, nil", removed, err)
	}
	nextNotesEvent(t, s, events.EventUrlsUpdated)
	if _, err := s.RemoveSessionURL("outer-rm-1", b.ID); !errors.Is(err, errClientMutationMismatch) {
		t.Fatalf("reused ID with different entry err = %v, want mismatch", err)
	}
	got := s.sessionURLsForTest()
	if len(got) != 1 || got[0].ID != b.ID {
		t.Fatalf("url list after conflict = %+v, want only b", got)
	}
}

// TestRemoveSessionURLUnknownIDRejects verifies an unknown entry id on a
// fresh outer ID stays an InvalidParams error (not a journaled success).
func TestRemoveSessionURLUnknownIDRejects(t *testing.T) {
	s := newNotesToolSession(t)
	defer s.Close()
	if removed, err := s.RemoveSessionURL("outer-fresh", "nonexistent"); removed || err == nil {
		t.Fatalf("unknown remove = %v, %v; want false, error", removed, err)
	} else if !strings.Contains(err.Error(), "no URL entry with id") {
		t.Fatalf("unknown remove err = %v, want unknown-ID error", err)
	}
}
