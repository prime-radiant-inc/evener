//go:build serffuzz

package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// This file fuzzes marshalBoundedDelegateResult (session_tools_jobs.go), the
// delegate/job result serializer that must fit its JSON inside a caller-supplied
// character budget by progressively shrinking the output field, then dropping it,
// then dropping the structured result. It is a data→bounded-text transform whose
// central promise is the budget: a huge job output must never round-trip into an
// over-budget tool result.

// FuzzArMarshalBoundedDelegateResult drives the serializer over a fuzzed result
// (large output, optional structured result) and a fuzzed budget. Oracles:
//
//   - never panics;
//   - BUDGET (the load-bearing property): whenever it succeeds and maxChars > 0,
//     the returned JSON is at most maxChars characters (measured the same way the
//     limiter measures — rune count of the JSON bytes);
//   - VALID JSON: a nil-error result always parses back as a delegate result;
//   - DETERMINISM.
func FuzzArMarshalBoundedDelegateResult(f *testing.F) {
	type seed struct {
		output     string
		hasStruct  bool
		structText string
		maxChars   uint16
	}
	seeds := []seed{
		{"", false, "", 800},
		{"small output", false, "", 800},
		{strings.Repeat("x", 50000), false, "", 1000},
		{strings.Repeat("line\n", 10000), true, "big", 900},
		{"tiny", true, strings.Repeat("s", 40000), 800},
		{strings.Repeat("é世🌍", 20000), true, "nested", 2000},
		{"output", false, "", 1}, // pathologically small budget
	}
	for _, s := range seeds {
		f.Add(s.output, s.hasStruct, s.structText, s.maxChars)
	}

	f.Fuzz(func(t *testing.T, output string, hasStruct bool, structText string, maxChars16 uint16) {
		maxChars := int(maxChars16)

		out := delegateToolResult{
			JobID:         "job-1",
			Type:          "delegate",
			Status:        "completed",
			TranscriptRef: "local:child",
			Output:        &output,
		}
		truncated := false
		out.Truncated = &truncated
		if hasStruct {
			// A structured result that itself can be large, exercising the final
			// drop-structured-result fallback.
			out.StructuredResult = map[string]any{"note": structText, "n": len(structText)}
			valid := true
			out.StructuredResultValid = &valid
		}

		got, err := marshalBoundedDelegateResult(out, maxChars)
		if err != nil {
			return // over-budget-even-when-stripped is a legitimate error outcome
		}

		if maxChars > 0 && jsonCharLen([]byte(got)) > maxChars {
			t.Fatalf("marshalBoundedDelegateResult blew its budget: %d chars > %d", jsonCharLen([]byte(got)), maxChars)
		}

		var back delegateToolResult
		if uerr := json.Unmarshal([]byte(got), &back); uerr != nil {
			t.Fatalf("marshalBoundedDelegateResult produced invalid JSON: %v\n%s", uerr, got)
		}

		got2, err2 := marshalBoundedDelegateResult(out, maxChars)
		if err2 != nil || got != got2 {
			t.Fatalf("marshalBoundedDelegateResult non-deterministic (err2=%v)", err2)
		}
	})
}