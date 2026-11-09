package main

import (
	"strings"
	"testing"

	"primeradiant.com/evener/llm/effort"
)

// TestRenderMirrorsTheSharedLadder pins the generator's output to llm/effort:
// every tier appears, in rank order, and the fallback is its four-tier prefix.
// The committed file's freshness against this output is checked by
// make lint-generated.
func TestRenderMirrorsTheSharedLadder(t *testing.T) {
	out := render()
	tiers := effort.Tiers()
	at := 0
	for _, level := range tiers {
		next := strings.Index(out[at:], `"`+level+`"`)
		if next < 0 {
			t.Fatalf("generated output is missing tier %q:\n%s", level, out)
		}
		at += next
	}
	fallback := tiers
	if len(fallback) > 4 {
		fallback = fallback[:4]
	}
	want := `FALLBACK_EFFORT_LEVELS: string[] = ["` + strings.Join(fallback, `", "`) + `"]`
	if !strings.Contains(out, want) {
		t.Fatalf("generated fallback does not match the four-tier prefix %q:\n%s", want, out)
	}
}
