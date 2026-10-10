package hub

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

// TestUniqueStringsEmpty covers the empty input path.
func TestUniqueStringsEmpty(t *testing.T) {
	if got := uniqueStrings(nil); len(got) != 0 {
		t.Fatalf("nil input should return empty, got %v", got)
	}
	if got := uniqueStrings([]string{}); len(got) != 0 {
		t.Fatalf("empty input should return empty, got %v", got)
	}
}

// TestUniqueStringsWithEmptyValues covers the empty-value filtering path.
func TestUniqueStringsWithEmptyValues(t *testing.T) {
	got := uniqueStrings([]string{"", "a", "", "b", ""})
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("expected ['a','b'], got %v", got)
	}
}

// TestUniqueStringsDuplicates covers the duplicate removal path.
func TestUniqueStringsDuplicates(t *testing.T) {
	got := uniqueStrings([]string{"a", "b", "a", "c", "b"})
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("expected ['a','b','c'], got %v", got)
	}
}

// TestUniqueStringsAllUnique covers the all-unique path.
func TestUniqueStringsAllUnique(t *testing.T) {
	got := uniqueStrings([]string{"x", "y", "z"})
	if len(got) != 3 {
		t.Fatalf("expected 3 items, got %d", len(got))
	}
}

// TestFavoriteRemoteThreadRefWithEvenerRef covers the path where the thread has
// an Evener.Ref.
func TestFavoriteRemoteThreadRefWithEvenerRef(t *testing.T) {
	thread := appwire.Thread{
		Evener: appwire.EvenerThread{Ref: "remote:session1"},
	}
	ref, ok := favoriteRemoteThreadRef(thread)
	if !ok {
		t.Fatal("should return ok=true")
	}
	if ref.SourceID != "remote" || ref.ThreadID != "session1" {
		t.Fatalf("expected remote:session1, got %v", ref)
	}
}

// TestFavoriteRemoteThreadRefWithInvalidEvenerRef covers the path where
// Evener.Ref is invalid.
func TestFavoriteRemoteThreadRefWithInvalidEvenerRef(t *testing.T) {
	thread := appwire.Thread{
		Evener: appwire.EvenerThread{Ref: "not-a-valid-ref-format"},
	}
	_, ok := favoriteRemoteThreadRef(thread)
	if ok {
		t.Fatal("expected ok=false for invalid Evener.Ref without ':' separator")
	}
}

// TestFavoriteRemoteThreadRefFallsBackToAppThreadTreeRef covers the fallback
// path where Evener.Ref is empty.
func TestFavoriteRemoteThreadRefFallsBackToAppThreadTreeRef(t *testing.T) {
	thread := appwire.Thread{
		ID:     "thread-1",
		Source: "remote",
	}
	ref, ok := favoriteRemoteThreadRef(thread)
	if !ok {
		t.Fatal("expected ok=true for fallback to appThreadTreeRef with valid Source and ID")
	}
	if ref.SourceID != "remote" || ref.ThreadID != "thread-1" {
		t.Fatalf("expected remote:thread-1, got %v", ref)
	}
}

// TestFavoriteRemoteOwnershipsLocalExcluded covers the path where local threads
// are excluded.
func TestFavoriteRemoteOwnershipsLocalExcluded(t *testing.T) {
	threads := []appwire.Thread{
		{ID: "t1", Source: "local", Evener: appwire.EvenerThread{Ref: "local:s1"}},
	}
	ownerships := favoriteRemoteOwnerships(threads)
	if len(ownerships) != 0 {
		t.Fatalf("local threads should be excluded, got %v", ownerships)
	}
}

// TestFavoriteRemoteOwnershipsRemoteThread covers a single remote thread.
func TestFavoriteRemoteOwnershipsRemoteThread(t *testing.T) {
	threads := []appwire.Thread{
		{ID: "t1", Source: "remote1", Evener: appwire.EvenerThread{Ref: "remote1:s1"}},
	}
	ownerships := favoriteRemoteOwnerships(threads)
	if len(ownerships) != 1 {
		t.Fatalf("expected 1 ownership, got %d", len(ownerships))
	}
}

// TestFavoriteRemoteOwnershipsMultipleSources covers the path where the same
// ref appears from different sources (conflict → empty ownership).
func TestFavoriteRemoteOwnershipsMultipleSources(t *testing.T) {
	threads := []appwire.Thread{
		{ID: "t1", Source: "remote1", Evener: appwire.EvenerThread{Ref: "remote1:s1"}},
		{ID: "t2", Source: "remote2", Evener: appwire.EvenerThread{Ref: "remote2:s1"}},
	}
	ownerships := favoriteRemoteOwnerships(threads)
	// Different refs → different entries
	if len(ownerships) != 2 {
		t.Fatalf("expected 2 ownerships, got %d", len(ownerships))
	}
}

// TestFavoriteRemoteOwnershipsSameSourceConflict covers the path where the same
// ref appears from the same source (incomplete ownership).
func TestFavoriteRemoteOwnershipsSameSourceConflict(t *testing.T) {
	threads := []appwire.Thread{
		{ID: "t1", Source: "remote1", Evener: appwire.EvenerThread{Ref: "remote1:s1"}},
		{ID: "t2", Source: "remote1", Evener: appwire.EvenerThread{Ref: "remote1:s1"}},
	}
	ownerships := favoriteRemoteOwnerships(threads)
	if len(ownerships) != 1 {
		t.Fatalf("expected 1 ownership, got %d", len(ownerships))
	}
	// Same sourceID → incomplete
	for _, o := range ownerships {
		if o.complete {
			t.Fatal("same-source duplicate should be incomplete")
		}
	}
}

// TestFavoriteRemoteOwnershipsRefMismatch covers the path where the thread's
// Evener.Ref doesn't match its Source (incomplete).
func TestFavoriteRemoteOwnershipsRefMismatch(t *testing.T) {
	threads := []appwire.Thread{
		{ID: "t1", Source: "remote1", Evener: appwire.EvenerThread{Ref: "remote2:s1"}},
	}
	ownerships := favoriteRemoteOwnerships(threads)
	if len(ownerships) != 1 {
		t.Fatalf("expected 1 ownership, got %d", len(ownerships))
	}
	for _, o := range ownerships {
		if o.complete {
			t.Fatal("ref mismatch should be incomplete")
		}
	}
}

// TestFavoriteRemoteOwnershipsEmptySource covers the path where Source is
// empty (falls back to ref SourceID).
func TestFavoriteRemoteOwnershipsEmptySource(t *testing.T) {
	threads := []appwire.Thread{
		{ID: "t1", Source: "", Evener: appwire.EvenerThread{Ref: "remote1:s1"}},
	}
	ownerships := favoriteRemoteOwnerships(threads)
	if len(ownerships) != 1 {
		t.Fatalf("expected 1 ownership, got %d", len(ownerships))
	}
}
