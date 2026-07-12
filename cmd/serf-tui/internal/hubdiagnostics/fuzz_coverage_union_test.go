//go:build serffuzz

package hubdiagnostics

import "testing"

func init() {
	fuzzCoverageUnion = func(t *testing.T) {
		TestFormatHubDiagnosticCustomTitlePreservedWithProviderCause(t)
		TestFormatHubDiagnosticProviderCauseOverridesLegacySource(t)
		TestFormatHubTurnErrorProviderCauseOverridesLegacyFields(t)
	}
}
