package effort

import (
	"reflect"
	"testing"
)

func TestTiersIsTheRankedLadder(t *testing.T) {
	want := []string{"minimal", "low", "medium", "high", "xhigh", "max"}
	got := Tiers()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Tiers() = %v, want %v", got, want)
	}
	// Callers cannot mutate the package's one copy.
	got[0] = "mutated"
	if Tiers()[0] != "minimal" {
		t.Fatal("Tiers() returned the package's own slice; a caller could re-rank the shared ladder")
	}
}

func TestRankOrdersKnownLevelsAndPassesUnknown(t *testing.T) {
	for i, level := range Tiers() {
		if got := Rank(level); got != i+1 {
			t.Errorf("Rank(%q) = %d, want %d", level, got, i+1)
		}
		if got := Rank("  " + level + "  "); got != i+1 {
			t.Errorf("Rank(%q) = %d, want trimmed rank %d", "  "+level+"  ", got, i+1)
		}
	}
	for _, unknown := range []string{"", "none", "ultra", "off"} {
		if got := Rank(unknown); got != 0 {
			t.Errorf("Rank(%q) = %d, want 0 (outside the lattice)", unknown, got)
		}
	}
	// Rank normalizes like the request side always did.
	if got := Rank(" High "); got != 4 {
		t.Errorf("Rank(\" High \") = %d, want 4", got)
	}
}

func TestValidAcceptsUnsetNoneAndTiers(t *testing.T) {
	for _, valid := range append([]string{"", None}, Tiers()...) {
		if !Valid(valid) {
			t.Errorf("Valid(%q) = false, want true", valid)
		}
	}
	for _, invalid := range []string{"ultra", "off", "null", "0"} {
		if Valid(invalid) {
			t.Errorf("Valid(%q) = true, want false", invalid)
		}
	}
	// Case and surrounding space are normalized, as the request side always did.
	if !Valid(" High ") {
		t.Error("Valid(\" High \") = false, want true")
	}
}
