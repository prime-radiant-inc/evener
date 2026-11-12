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
