package openai

import (
	"encoding/json"
	"strings"
	"testing"
)

// FuzzReasoningSummaryRoundTrip drives the inverse pair that carries a reasoning
// summary across a Responses continuation: reasoningSummaryInput turns the
// []string summary into the {type:"summary_text", text} wire items sent back to
// the API, and parseReasoningSummary recovers a []string from a Responses output
// item. They must round-trip, modulo the documented whitespace/empty drop.
//
// Oracles beyond no-panic:
//   - reasoningSummaryInput drops exactly the entries that are empty after
//     TrimSpace, and every emitted item is {type:"summary_text", text:<trimmed>};
//   - round-trip identity: parseReasoningSummary(reasoningSummaryInput(s)) equals
//     s with empty/whitespace entries removed AND each surviving entry trimmed —
//     a drift in either direction (lost summary, leaked blank) reddens it;
//   - parseReasoningSummary never panics on an arbitrary decoded JSON value.
func FuzzReasoningSummaryRoundTrip(f *testing.F) {
	f.Add("first thought", "  second  ", "")
	f.Add("", "   ", "\t\n")
	f.Add("only", "only", "only")
	f.Add("unicode ☃", "", "  padded  ")

	f.Fuzz(func(t *testing.T, a, b, c string) {
		summary := []string{a, b, c}

		// Expected survivors: non-blank-after-trim, each trimmed.
		var want []string
		for _, s := range summary {
			trimmed := strings.TrimSpace(s)
			if trimmed != "" {
				want = append(want, trimmed)
			}
		}

		items := reasoningSummaryInput(summary)
		if len(items) != len(want) {
			t.Fatalf("reasoningSummaryInput emitted %d items, want %d (summary=%q)", len(items), len(want), summary)
		}
		for i, itemAny := range items {
			item, ok := itemAny.(map[string]any)
			if !ok {
				t.Fatalf("item[%d] is %T, want map", i, itemAny)
			}
			if item["type"] != "summary_text" {
				t.Fatalf("item[%d] type=%v, want summary_text", i, item["type"])
			}
			if text, _ := item["text"].(string); text != want[i] {
				t.Fatalf("item[%d] text=%q, want %q", i, text, want[i])
			}
		}

		// Round-trip: feed the emitted items (as a generic []any, the shape
		// parseReasoningSummary consumes) back through the parser.
		got := parseReasoningSummary(any(items))
		if len(got) != len(want) {
			t.Fatalf("round-trip length mismatch: got %d, want %d (summary=%q)", len(got), len(want), summary)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("round-trip[%d]=%q, want %q", i, got[i], want[i])
			}
		}

		// parseReasoningSummary must also survive arbitrary JSON-shaped input.
		var arbitrary any
		_ = json.Unmarshal([]byte(a), &arbitrary)
		_ = parseReasoningSummary(arbitrary)
	})
}
