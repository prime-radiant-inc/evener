package hubdiagnostics

import (
	"testing"

	"primeradiant.com/serf/appwire"
)

func TestFormatHubDiagnosticProviderCauseOverridesLegacySource(t *testing.T) {
	cause := &appwire.DiagnosticCause{Kind: "provider", Provider: "openai", Status: 503}

	got := FormatHubDiagnosticWithCause("Serf warning", "serf", "upstream failed", "Session warning", cause)
	if got != "Provider error: upstream failed" {
		t.Fatalf("FormatHubDiagnosticWithCause = %q, want %q", got, "Provider error: upstream failed")
	}
}

func TestFormatHubTurnErrorProviderCauseOverridesLegacyFields(t *testing.T) {
	err := &appwire.TurnError{
		Message: "rate limited",
		Source:  "serf",
		Title:   "Serf error",
		Cause:   &appwire.DiagnosticCause{Kind: "provider", Provider: "openai", Status: 429},
	}

	got := FormatHubTurnError(err, "Session error")
	if got != "Provider error: rate limited" {
		t.Fatalf("FormatHubTurnError = %q, want %q", got, "Provider error: rate limited")
	}

	// Nil TurnError should fall back to the fallback title — not reachable by Test 1.
	gotNil := FormatHubTurnError(nil, "Session error")
	if gotNil != "Session error" {
		t.Fatalf("FormatHubTurnError(nil) = %q, want %q", gotNil, "Session error")
	}
}

// TestFormatHubDiagnosticCustomTitlePreservedWithProviderCause verifies that a
// non-legacy custom title (not in the serf/hub/ui/session family) is kept even
// when a provider cause is present.  A mutation that makes
// isLegacyNonProviderDiagnosticTitle always return true would wipe the custom
// title and produce "Provider error: upstream failed", causing this test to fail.
func TestFormatHubDiagnosticCustomTitlePreservedWithProviderCause(t *testing.T) {
	cause := &appwire.DiagnosticCause{Kind: "provider", Provider: "stripe", Status: 503}

	got := FormatHubDiagnosticWithCause("Payment Gateway Overload", "serf", "upstream failed", "Session warning", cause)
	if got != "Payment Gateway Overload: upstream failed" {
		t.Fatalf("FormatHubDiagnosticWithCause = %q, want custom title preserved", got)
	}
}
