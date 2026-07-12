package hubdiagnostics

import (
	"strings"
	"testing"

	"primeradiant.com/serf/appwire"
)

func FuzzFormatHubDiagnostic(f *testing.F) {
	seeds := []struct{ title, source, message, fallback, kind string }{
		{"", "provider", "boom", "fallback", ""},
		{"", "serf", "", "fallback", ""},
		{"", "hub", "boom", "fallback", ""},
		{"", "ui", "boom", "fallback", ""},
		{"", "other", "boom", "", ""},
		{"Hub warning", "hub", " boom ", "fallback", "PrOvIdEr"},
	}
	for _, seed := range seeds {
		f.Add(seed.title, seed.source, seed.message, seed.fallback, seed.kind)
	}
	f.Fuzz(func(t *testing.T, title, source, message, fallback, kind string) {
		cause := &appwire.DiagnosticCause{Kind: kind}
		got := FormatHubDiagnosticWithCause(title, source, message, fallback, cause)
		if got != strings.TrimSpace(got) || got == "" {
			t.Fatalf("diagnostic must be non-empty and trimmed: %q", got)
		}
	})
}
