package agent

import (
	"testing"
)

// TestNewJobActivityClock_EmptyRoot covers the empty-rootSessionID path
// (lines 19-21) where newJobActivityClock returns nil.
func TestNewJobActivityClock_EmptyRoot(t *testing.T) {
	t.Parallel()
	if got := newJobActivityClock(""); got != nil {
		t.Fatal("expected nil for empty rootSessionID")
	}
	if got := newJobActivityClock("   "); got != nil {
		t.Fatal("expected nil for whitespace rootSessionID")
	}
}

// TestNewJobActivityClock_Valid covers the valid rootSessionID path.
func TestNewJobActivityClock_Valid(t *testing.T) {
	t.Parallel()
	c := newJobActivityClock("root1")
	if c == nil {
		t.Fatal("expected non-nil for valid rootSessionID")
	}
	if c.rootSessionID != "root1" {
		t.Fatalf("rootSessionID = %q, want root1", c.rootSessionID)
	}
}

// TestJobActivityClock_NextRevision_NilClock covers the nil receiver path
// in nextRevision (lines 26-27).
func TestJobActivityClock_NextRevision_NilClock(t *testing.T) {
	t.Parallel()
	var c *jobActivityClock
	_, _, ok := c.nextRevision()
	if ok {
		t.Fatal("expected ok=false for nil clock")
	}
}

// TestJobActivityClock_EnsureAtLeast_NilClock covers the nil receiver path
// in ensureAtLeast (lines 33-34).
func TestJobActivityClock_EnsureAtLeast_NilClock(t *testing.T) {
	t.Parallel()
	var c *jobActivityClock
	if got := c.ensureAtLeast(100); got != 0 {
		t.Fatalf("expected 0 for nil clock, got %d", got)
	}
}

// TestReserveTreeSlot_NilSession covers the nil-session guard (line 176).
func TestReserveTreeSlot_NilSession(t *testing.T) {
	t.Parallel()
	var s *Session
	slot, ok := s.reserveTreeSlot(slotKindJob)
	if slot != nil || !ok {
		t.Fatal("expected nil slot and ok=true for nil session")
	}
}

// TestReserveTreeSlot_NilCounter covers the nil-counter guard (line 176).
func TestReserveTreeSlot_NilCounter(t *testing.T) {
	t.Parallel()
	s := &Session{}
	slot, ok := s.reserveTreeSlot(slotKindJob)
	if slot != nil || !ok {
		t.Fatal("expected nil slot and ok=true for nil counter")
	}
}

// TestReserveDriveSlot_NilSession covers the nil-session guard (line 216).
func TestReserveDriveSlot_NilSession(t *testing.T) {
	t.Parallel()
	var s *Session
	slot, ok := s.reserveDriveSlot()
	if slot != nil || !ok {
		t.Fatal("expected nil slot and ok=true for nil session")
	}
}

// TestReserveDriveSlot_NilCounter covers the nil-counter guard (line 216).
func TestReserveDriveSlot_NilCounter(t *testing.T) {
	t.Parallel()
	s := &Session{}
	slot, ok := s.reserveDriveSlot()
	if slot != nil || !ok {
		t.Fatal("expected nil slot and ok=true for nil counter")
	}
}

// TestReleasePreparedTreeSlot_Nil covers the nil guard (line 204).
func TestReleasePreparedTreeSlot_Nil(t *testing.T) {
	t.Parallel()
	releasePreparedTreeSlot(nil)
	// Should not panic.
}
