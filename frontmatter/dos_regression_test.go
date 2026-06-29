package frontmatter

import (
	"strings"
	"testing"

	"primeradiant.com/serf/fuzz/edgeseeds"
)

// TestParseInheritsYAMLDoSLimits proves the frontmatter decoder inherits
// gopkg.in/yaml.v3's denial-of-service limits. Each documented bomb from the
// library's own resistance suite (limit_test.go) — an alias-expansion attack
// and past-max-depth nestings — wrapped as frontmatter, must come back as a
// bounded error carrying the limit's message: never a panic, a hang, or a
// memory blowup, and never a silent success (which would mean the protection
// was bypassed). Gated behind -short like the upstream suite because the
// payloads are ~1 MB each.
func TestParseInheritsYAMLDoSLimits(t *testing.T) {
	if testing.Short() {
		t.Skip("DoS payloads are ~1 MB each; skipped under -short")
	}
	for _, tc := range edgeseeds.YAMLDoS() {
		t.Run(tc.Name, func(t *testing.T) {
			raw := "---\n" + string(tc.YAML) + "\n---\nbody\n"
			doc, err := Parse(raw)
			if err == nil {
				t.Fatalf("expected the yaml.v3 %q limit to fire, got nil error", tc.ErrSubstr)
			}
			if !strings.Contains(err.Error(), tc.ErrSubstr) {
				t.Fatalf("error %q does not contain %q (DoS limit may have been bypassed)", err, tc.ErrSubstr)
			}
			if doc.Meta != nil || doc.Body != "" {
				t.Fatalf("rejected input did not return the zero Document: %#v", doc)
			}
		})
	}
}
