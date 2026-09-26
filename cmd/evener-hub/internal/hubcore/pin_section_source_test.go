package hubcore

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestPinSectionStoreRetryErrorsNameTheSourceQualifiedPin pins the identity the
// retry-limit diagnostics render: the controller's bare session ID, or the
// host-qualified ref a remote pin is addressed by — never an ArchiveKey struct
// dump.
func TestPinSectionStoreRetryErrorsNameTheSourceQualifiedPin(t *testing.T) {
	store := setupLockedStore(t)
	section, _, err := store.CreateOrReuseAndAssign("Research", "", "seed-a", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}

	resetLockedCounters()
	lockedBeginFailures.Store(100)
	_, _, err = store.Assign(section.ID, "host-a", "th_1", time.Unix(2, 0))
	if err == nil || err.Error() != "assign host-a:th_1: retry limit reached" {
		t.Fatalf("Assign retry limit err = %v, want the host-qualified pin identity", err)
	}
	if strings.Contains(err.Error(), "{") {
		t.Fatalf("Assign retry limit err = %v, want no ArchiveKey struct dump", err)
	}

	resetLockedCounters()
	lockedBeginFailures.Store(100)
	_, err = store.DeleteSession("", "th_2")
	if err == nil || err.Error() != "delete session pin th_2: retry limit reached" {
		t.Fatalf("DeleteSession retry limit err = %v, want the controller pin identity", err)
	}
	if strings.Contains(err.Error(), "{") {
		t.Fatalf("DeleteSession retry limit err = %v, want no ArchiveKey struct dump", err)
	}
}

// TestPinSectionStoreKeepsSameBareIDOnTwoSourcesIndependent pins the
// source-qualified storage key: the same bare session ID on the controller and
// on two remote hosts holds three independent pins, and clearing one host's
// assignment leaves the others untouched.
func TestPinSectionStoreKeepsSameBareIDOnTwoSourcesIndependent(t *testing.T) {
	store := NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
	now := time.Unix(1_700_000_000, 0).UTC()
	local, _, err := store.CreateOrReuseAndAssign("Local", "", "th_1", now)
	if err != nil {
		t.Fatal(err)
	}
	hostA, _, err := store.CreateOrReuseAndAssign("Host A", "host-a", "th_1", now)
	if err != nil {
		t.Fatal(err)
	}
	hostB, _, err := store.CreateOrReuseAndAssign("Host B", "host-b", "th_1", now)
	if err != nil {
		t.Fatal(err)
	}

	assignments, err := store.Assignments()
	if err != nil {
		t.Fatal(err)
	}
	want := map[ArchiveKey]string{
		{Kind: "session", ID: "th_1"}:                   local.ID,
		{Kind: "session", ID: "th_1", Source: "host-a"}: hostA.ID,
		{Kind: "session", ID: "th_1", Source: "host-b"}: hostB.ID,
	}
	if len(assignments) != len(want) {
		t.Fatalf("assignments = %+v, want one pin per source", assignments)
	}
	for key, sectionID := range want {
		assignment, ok := assignments[key]
		if !ok {
			t.Fatalf("assignments = %+v, want key %+v", assignments, key)
		}
		if assignment.SectionID != sectionID || assignment.SessionID != "th_1" || assignment.Source != key.Source {
			t.Fatalf("assignment[%+v] = %+v, want section %s", key, assignment, sectionID)
		}
	}

	if changed, err := store.Unpin("host-a", "th_1"); err != nil || !changed {
		t.Fatalf("Unpin(host-a, th_1) = %v, %v, want changed", changed, err)
	}
	after, err := store.Assignments()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 2 {
		t.Fatalf("assignments after unpinning host-a = %+v, want the other two pins", after)
	}
	for _, key := range []ArchiveKey{
		{Kind: "session", ID: "th_1"},
		{Kind: "session", ID: "th_1", Source: "host-b"},
	} {
		if _, ok := after[key]; !ok {
			t.Fatalf("assignments after unpinning host-a = %+v, want %+v preserved", after, key)
		}
	}
	sections, err := store.Sections()
	if err != nil {
		t.Fatal(err)
	}
	for _, section := range sections {
		if section.ID == hostA.ID && section.MemberCount != 0 {
			t.Fatalf("host-a section member count = %d after unpin, want 0", section.MemberCount)
		}
		if (section.ID == local.ID || section.ID == hostB.ID) && section.MemberCount != 1 {
			t.Fatalf("section %s member count = %d, want 1", section.Name, section.MemberCount)
		}
	}
}

// TestPinSectionStoreNormalizesLocalSpelling pins the canonical local source:
// the wire spelling "local" and an absent source address the same controller
// pin, so a store caller cannot create a second row for one local session.
func TestPinSectionStoreNormalizesLocalSpelling(t *testing.T) {
	store := NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
	now := time.Unix(1_700_000_000, 0).UTC()
	first, _, err := store.CreateOrReuseAndAssign("First", "local", "th_1", now)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := store.CreateOrReuseAndAssign("Second", "", "other", now)
	if err != nil {
		t.Fatal(err)
	}
	moved, changed, err := store.Assign(second.ID, "", "th_1", now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal(`re-assigning the "local"-spelled pin to another section reported no change`)
	}
	if moved.MemberCount != 2 {
		t.Fatalf("moved section member count = %d, want both sessions", moved.MemberCount)
	}
	assignments, err := store.Assignments()
	if err != nil {
		t.Fatal(err)
	}
	if len(assignments) != 2 {
		t.Fatalf("assignments = %+v, want one pin per session", assignments)
	}
	assignment, ok := assignments[ArchiveKey{Kind: "session", ID: "th_1"}]
	if !ok {
		t.Fatalf("assignments = %+v, want the controller key", assignments)
	}
	if assignment.Source != "" || assignment.SectionID != second.ID || assignment.SessionID != "th_1" {
		t.Fatalf(`"local" and the absent source must address one pin: got %+v, want section %s`, assignment, second.ID)
	}
	if first.ID == second.ID {
		t.Fatal("test setup reused one section")
	}
}
