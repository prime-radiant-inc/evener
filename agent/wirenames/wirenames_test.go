package wirenames

import (
	"testing"

	"primeradiant.com/evener/llm/registry"
)

func TestCanonicalInvertsAllSurfaceWireNames(t *testing.T) {
	t.Parallel()
	for _, surface := range []string{registry.SurfaceOpenAI, registry.SurfaceGoogle, registry.SurfaceAnthropic} {
		for canonical, wire := range ForSurface(surface) {
			if got := Canonical(wire); got != canonical {
				t.Errorf("surface %s: Canonical(%q) = %q, want %q", surface, wire, got, canonical)
			}
			if got := Canonical(canonical); got != canonical {
				t.Errorf("surface %s: Canonical(%q) = %q, want %q", surface, canonical, got, canonical)
			}
		}
	}
	for _, untouched := range []string{"read_file", "communicate", "mcp__plugin_demo__search", ""} {
		if got := Canonical(untouched); got != untouched {
			t.Errorf("Canonical(%q) = %q, want unchanged", untouched, got)
		}
	}
}

// TestSameAgreesWithTheDispatchTable pins the invariant the transcript's
// settlement guard depends on: Same answers from the SAME table dispatch
// canonicalized with. If a future change adds an alias to the dispatch path
// without touching this package — or adds one here without touching dispatch —
// every renamed tool silently regresses to an unmatched result, and no other
// test would notice.
func TestSameAgreesWithTheDispatchTable(t *testing.T) {
	t.Parallel()
	// The positive direction: every aliased pair is one tool.
	for _, surface := range []string{registry.SurfaceOpenAI, registry.SurfaceGoogle} {
		for canonical, wire := range ForSurface(surface) {
			if !Same(canonical, wire) {
				t.Errorf("surface %s: Same(%q, %q) = false: an alias and its canonical name are one tool",
					surface, canonical, wire)
			}
			if !Same(wire, canonical) {
				t.Errorf("surface %s: Same(%q, %q) = false: the comparison must not depend on which side is the alias",
					surface, wire, canonical)
			}
			// Dispatch trims before executing, so the result records the trimmed
			// name while the announcement may carry the padded spelling.
			if !Same(" "+wire+" ", canonical) {
				t.Errorf("surface %s: Same(%q, %q) = false: whitespace is not part of a tool name",
					surface, " "+wire+" ", canonical)
			}
		}
	}
	// The negative direction, and the one that matters: a Same that returned
	// true unconditionally would pass every aliased-call test in the transcript
	// suite while destroying the phantom-row check that guards against a reused
	// call id settling the wrong tool.
	for _, pair := range [][2]string{
		{"read_file", "communicate"},
		{"read_file", "shell"},
		{"shell", "grep"},
		{"exec_command", "grep_files"},
		{"run_shell_command", "read_file"},
		{"", "shell"},
	} {
		if Same(pair[0], pair[1]) {
			t.Errorf("Same(%q, %q) = true: genuinely different tools must not settle each other's call",
				pair[0], pair[1])
		}
	}
	// The empty case: an unnamed result carries no claim, and Same must not
	// manufacture one by treating "" as equal to a real tool.
	if Same("", "read_file") || Same("read_file", "") {
		t.Error("Same(\"\", tool) = true: an unnamed result has no tool identity to match")
	}
}
