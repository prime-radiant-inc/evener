package appoverlay

import "testing"

// A recent set forgets its oldest key past its capacity, and order entries
// left stale by deletes are compacted away once the order outgrows twice
// the capacity.
func TestRecentSetEvictsAndCompacts(t *testing.T) {
	t.Parallel()
	s := newRecentSet[int](1)
	s.put("a", 1)
	s.put("b", 2)
	if _, ok := s.get("a"); ok {
		t.Fatal("the evicted key is still readable")
	}
	if v, ok := s.get("b"); !ok || v != 2 {
		t.Fatalf("newest key = %d, %v", v, ok)
	}

	// Stale order entries pile up as keys are deleted and re-added; the
	// compaction pass must drop them without losing the live tail.
	s.delete("b")
	s.put("c", 3)
	s.delete("c")
	s.put("d", 4)
	if v, ok := s.get("d"); !ok || v != 4 {
		t.Fatalf("live key after compaction = %d, %v", v, ok)
	}
	if s.len() != 1 {
		t.Fatalf("len = %d, want only the live key", s.len())
	}
}
