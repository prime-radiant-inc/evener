package main

import (
	"strings"
	"testing"

	"primeradiant.com/serf/internal/appwire"
)

func TestFormatHubDiagnosticProviderCauseOverridesLegacySource(t *testing.T) {
	cause := &appwire.DiagnosticCause{Kind: "provider", Provider: "openai", Status: 503}

	got := formatHubDiagnosticWithCause("Serf warning", "serf", "upstream failed", "Session warning", cause)
	if !strings.Contains(got, "Provider error: upstream failed") {
		t.Fatalf("formatHubDiagnosticWithCause = %q, want provider title", got)
	}
}

func TestFormatHubTurnErrorProviderCauseOverridesLegacyFields(t *testing.T) {
	err := &appwire.TurnError{
		Message: "rate limited",
		Source:  "serf",
		Title:   "Serf error",
		Cause:   &appwire.DiagnosticCause{Kind: "provider", Provider: "openai", Status: 429},
	}

	got := formatHubTurnError(err, "Session error")
	if !strings.Contains(got, "Provider error: rate limited") {
		t.Fatalf("formatHubTurnError = %q, want provider title", got)
	}
}
