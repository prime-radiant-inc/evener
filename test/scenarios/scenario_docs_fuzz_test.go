package scenarios

import (
	"strings"
	"testing"
)

func FuzzScenarioStateRule(f *testing.F) {
	f.Add("state=processing")
	f.Add("status: active")
	f.Add("<state status='processing'>")

	f.Fuzz(func(t *testing.T, document string) {
		if len(document) > 8192 {
			document = document[:8192]
		}
		match := staleStateRE.FindString(document)
		if (match != "") != staleStateRE.MatchString(document) {
			t.Fatalf("FindString and MatchString disagree for %q", document)
		}
		if match != "" && !strings.Contains(strings.ToLower(match), "processing") {
			t.Fatalf("stale-state match %q does not identify processing", match)
		}
	})
}
