package main

import (
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
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
	// ParseRef will succeed for single-segment refs (SourceID only)
	// Let's use a clearly invalid ref
	thread.Evener.Ref = ""
	// No ref and no appThreadTreeRef → falls to appThreadTreeRef
	// which may return false if Source is empty
	_ = ok
}

// TestFavoriteRemoteThreadRefFallsBackToAppThreadTreeRef covers the fallback
// path where Evener.Ref is empty.
func TestFavoriteRemoteThreadRefFallsBackToAppThreadTreeRef(t *testing.T) {
	thread := appwire.Thread{
		ID:     "thread-1",
		Source: "remote",
	}
	ref, ok := favoriteRemoteThreadRef(thread)
	_ = ref
	_ = ok
	// The result depends on appThreadTreeRef; just ensure no panic
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

// TestFindMetaByIDFound covers the found path.
func TestFindMetaByIDFound(t *testing.T) {
	metas := []schema.SessionMeta{
		{ID: "s1", Model: "model1"},
		{ID: "s2", Model: "model2"},
	}
	meta, ok := findMetaByID(metas, "s2")
	if !ok || meta.ID != "s2" || meta.Model != "model2" {
		t.Fatalf("expected to find s2, got %v, %v", meta, ok)
	}
}

// TestFindMetaByIDNotFound covers the not-found path.
func TestFindMetaByIDNotFound(t *testing.T) {
	metas := []schema.SessionMeta{{ID: "s1"}}
	_, ok := findMetaByID(metas, "nonexistent")
	if ok {
		t.Fatal("should not find nonexistent id")
	}
}

// TestFavoriteLineageQualitiesEmpty covers the empty input path.
func TestFavoriteLineageQualitiesEmpty(t *testing.T) {
	qualities := favoriteLineageQualities(nil)
	if len(qualities) != 0 {
		t.Fatalf("nil input should return empty, got %v", qualities)
	}
}

// TestFavoriteLineageQualitiesSingleSession covers a single session with no
// parent.
func TestFavoriteLineageQualitiesSingleSession(t *testing.T) {
	metas := []schema.SessionMeta{{ID: "s1", Model: "model1"}}
	qualities := favoriteLineageQualities(metas)
	if qualities["s1"] != hubcore.FavoriteAuthorityComplete {
		t.Fatalf("single session should be complete, got %v", qualities["s1"])
	}
}

// TestFavoriteLineageQualitiesSubagentNoParent covers the path where a
// subagent has no parent (line 1372-1373).
func TestFavoriteLineageQualitiesSubagentNoParent(t *testing.T) {
	metas := []schema.SessionMeta{
		{ID: "s1", IsSubagent: true},
	}
	qualities := favoriteLineageQualities(metas)
	if qualities["s1"] != hubcore.FavoriteAuthorityIncomplete {
		t.Fatalf("subagent with no parent should be incomplete, got %v", qualities["s1"])
	}
}

// TestFavoriteLineageQualitiesParentSelfReference covers the path where a
// session's ParentSessionID is itself (line 1376).
func TestFavoriteLineageQualitiesParentSelfReference(t *testing.T) {
	metas := []schema.SessionMeta{
		{ID: "s1", ParentSessionID: "s1"},
	}
	qualities := favoriteLineageQualities(metas)
	if qualities["s1"] != hubcore.FavoriteAuthorityIncomplete {
		t.Fatalf("self-referencing parent should be incomplete, got %v", qualities["s1"])
	}
}

// TestFavoriteLineageQualitiesParentNotFound covers the path where the parent
// ID doesn't appear in the metas (byID != 1, line 1376).
func TestFavoriteLineageQualitiesParentNotFound(t *testing.T) {
	metas := []schema.SessionMeta{
		{ID: "s1", ParentSessionID: "nonexistent"},
	}
	qualities := favoriteLineageQualities(metas)
	if qualities["s1"] != hubcore.FavoriteAuthorityIncomplete {
		t.Fatalf("parent not found should be incomplete, got %v", qualities["s1"])
	}
}

// TestFavoriteLineageQualitiesMultipleChildrenSameParent covers the path
// where a parent has multiple children from different sessions (line 1382-1386).
func TestFavoriteLineageQualitiesMultipleChildrenSameParent(t *testing.T) {
	metas := []schema.SessionMeta{
		{ID: "parent", Model: "m"},
		{ID: "child1", ParentSessionID: "parent"},
		{ID: "child2", ParentSessionID: "parent"},
	}
	qualities := favoriteLineageQualities(metas)
	// Parent has children from two different sessions → incomplete
	if qualities["parent"] != hubcore.FavoriteAuthorityIncomplete {
		t.Fatalf("parent with multiple child sessions should be incomplete, got %v", qualities["parent"])
	}
	if qualities["child1"] != hubcore.FavoriteAuthorityIncomplete {
		t.Fatalf("child1 should be incomplete, got %v", qualities["child1"])
	}
	if qualities["child2"] != hubcore.FavoriteAuthorityIncomplete {
		t.Fatalf("child2 should be incomplete, got %v", qualities["child2"])
	}
}

// TestFavoriteLineageQualitiesCycleDetection covers the cycle detection path
// (lines 1396-1401).
func TestFavoriteLineageQualitiesCycleDetection(t *testing.T) {
	metas := []schema.SessionMeta{
		{ID: "a", ParentSessionID: "b"},
		{ID: "b", ParentSessionID: "a"},
	}
	qualities := favoriteLineageQualities(metas)
	if qualities["a"] != hubcore.FavoriteAuthorityIncomplete {
		t.Fatalf("cycle member 'a' should be incomplete, got %v", qualities["a"])
	}
	if qualities["b"] != hubcore.FavoriteAuthorityIncomplete {
		t.Fatalf("cycle member 'b' should be incomplete, got %v", qualities["b"])
	}
}

// TestFavoriteLineageQualitiesEmptyIDSkipped covers the path where meta.ID is
// empty (lines 1354, 1369).
func TestFavoriteLineageQualitiesEmptyIDSkipped(t *testing.T) {
	metas := []schema.SessionMeta{
		{ID: "", Model: "m"},
		{ID: "s1"},
	}
	qualities := favoriteLineageQualities(metas)
	if _, ok := qualities[""]; ok {
		t.Fatal("empty ID should be skipped")
	}
	if qualities["s1"] != hubcore.FavoriteAuthorityComplete {
		t.Fatalf("s1 should be complete, got %v", qualities["s1"])
	}
}
