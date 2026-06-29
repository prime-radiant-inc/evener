package plugin

import (
	"strings"
	"testing"
)

// TestParseManifestSurvivesDeepNesting proves the manifest JSON decode path
// inherits encoding/json's nesting cap: a past-max-depth array and object —
// large enough to overflow a naive recursive descent — must come back as a
// bounded "exceeded max depth" error rather than a panic or stack overflow,
// and leave the zero Manifest. encoding/json caps nesting at 10000 (the Go
// security fix for unbounded recursion); this guards that serf's wrapper does
// not bypass it. Gated behind -short because the inputs are ~100 KB.
func TestParseManifestSurvivesDeepNesting(t *testing.T) {
	if testing.Short() {
		t.Skip("deep-nesting inputs are ~100 KB; skipped under -short")
	}
	const depth = 100000 // comfortably past encoding/json's 10000 cap
	cases := map[string]string{
		"array":  strings.Repeat("[", depth) + strings.Repeat("]", depth),
		"object": strings.Repeat(`{"a":`, depth) + "1" + strings.Repeat("}", depth),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			m, err := ParseManifest([]byte(raw))
			if err == nil {
				t.Fatalf("expected a depth-limit error, got nil")
			}
			if !strings.Contains(err.Error(), "max depth") {
				t.Fatalf("error %q does not mention the depth limit (a deep input slipped past the cap?)", err)
			}
			if !manifestIsZero(m) {
				t.Fatalf("rejected input did not return the zero Manifest: %#v", m)
			}
		})
	}
}
