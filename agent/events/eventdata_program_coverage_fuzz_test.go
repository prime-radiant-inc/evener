//go:build serffuzz

package events

import (
	"os"
	"reflect"
	"regexp"
	"testing"
)

// eventKindMethodRE matches the marker method every sealed payload implements,
// capturing the payload type name. The body may sit on the same line or the
// next, so it deliberately stops at the signature.
var eventKindMethodRE = regexp.MustCompile(`func \((\w+)\) eventKind\(\) EventKind`)

// TestEventDataProgramCasesCoverEverySealedPayload is the completeness guard
// for FuzzEventDataProgram, whose doc claims it "runs every member of the
// sealed event payload set" -- a claim nothing enforced. TurnStartedData and
// ModelRetryData were both absent while that sentence stood.
//
// A literal length assertion cannot catch the next omission: adding a payload
// without adding a row leaves the row count unchanged, so the assertion passes.
// This derives the expected set from the marker methods themselves, so the set
// grows exactly when the sealed set grows.
func TestEventDataProgramCasesCoverEverySealedPayload(t *testing.T) {
	src, err := os.ReadFile("eventdata.go")
	if err != nil {
		t.Fatalf("read the payload marker declarations: %v", err)
	}
	matches := eventKindMethodRE.FindAllSubmatch(src, -1)
	if len(matches) == 0 {
		t.Fatal("found no eventKind() implementations; the regexp no longer matches how payloads are sealed")
	}

	covered := map[string]bool{}
	for _, tc := range eventDataProgramCases("x", 1, true) {
		covered[reflect.TypeOf(tc.data).Name()] = true
	}

	var missing []string
	for _, m := range matches {
		name := string(m[1])
		if !covered[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("%d sealed payload(s) never reach FuzzEventDataProgram: %v\nadd a row to eventDataProgramCases for each", len(missing), missing)
	}
}
