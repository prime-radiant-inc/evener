package goal_test

import (
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"primeradiant.com/evener/agent/internal/goal"
)

// Round-17 fix wave, finding 6 (derived-label caps bypass): an over-long
// target with control characters registers (the target is predicate identity,
// byte-capped at MaxURLBytes), but the DERIVED chip label sanitizes to the
// explicit-label contract — at most MaxLabelRunes printable runes — while the
// predicate target stays intact.
func TestFixWave17_DerivedLabelCapped(t *testing.T) {
	s := goal.NewStore()
	s.Set("x", waveBClock())
	// 2048 bytes incl. control chars and a wide rune: byte-capped target,
	// rune-measured label.
	target := strings.Repeat("a\x00\x07é", 400) + strings.Repeat("b", 48)
	if len(target) != goal.MaxURLBytes {
		t.Fatalf("precondition: target bytes = %d, want exactly %d", len(target), goal.MaxURLBytes)
	}
	s.SetSubstrate(&fakeSubstrate{files: map[string]string{target: "base-1"}})
	w, ok := s.RegisterWait(goal.WaitKind{
		Kind:         goal.WaitUntilEvent,
		EventSubtype: goal.EventFileModified,
		Target:       target,
		Timeout:      time.Minute,
	}, waveBClock())
	if !ok {
		t.Fatalf("stat-able file must park: %q", s.LastRejectReason())
	}
	if got := w.Lease.Predicate.Target; got != target {
		t.Fatal("predicate target must stay intact for identity")
	}
	label := w.Lease.Label
	if n := utf8.RuneCountInString(label); n > goal.MaxLabelRunes {
		t.Fatalf("derived label runes = %d, want ≤ %d", n, goal.MaxLabelRunes)
	}
	for _, r := range label {
		if !unicode.IsPrint(r) {
			t.Fatalf("derived label carries non-printable %U", r)
		}
	}
	if !strings.HasPrefix(label, string(goal.WaitUntilEvent)+":") {
		t.Fatalf("derived label %q must keep the kind: prefix", label)
	}
}

// TestFixWave17_DerivedLabelLongTarget pins the truncation boundary through
// the store: a MaxURLBytes plain target derives a chip label within
// MaxLabelRunes runes, with the full target preserved for identity.
func TestFixWave17_DerivedLabelLongTarget(t *testing.T) {
	s := goal.NewStore()
	s.Set("x", waveBClock())
	big := strings.Repeat("f", goal.MaxURLBytes)
	s.SetSubstrate(&fakeSubstrate{files: map[string]string{big: "base-1"}})
	w, ok := s.RegisterWait(goal.WaitKind{
		Kind:         goal.WaitUntilEvent,
		EventSubtype: goal.EventFileModified,
		Target:       big,
		Timeout:      time.Minute,
	}, waveBClock())
	if !ok {
		t.Fatalf("stat-able file must park: %q", s.LastRejectReason())
	}
	if got := w.Lease.Predicate.Target; got != big {
		t.Fatal("predicate target must stay intact for identity")
	}
	label := w.Lease.Label
	if n := utf8.RuneCountInString(label); n > goal.MaxLabelRunes {
		t.Fatalf("derived label runes = %d, want ≤ %d", n, goal.MaxLabelRunes)
	}
	for _, r := range label {
		if !unicode.IsPrint(r) {
			t.Fatalf("derived label carries non-printable %U", r)
		}
	}
	if !strings.HasPrefix(label, string(goal.WaitUntilEvent)+":") {
		t.Fatalf("derived label %q must keep the kind: prefix", label)
	}
}
