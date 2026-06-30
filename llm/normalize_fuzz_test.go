package llm

import (
	"strings"
	"testing"
)

// canonicalFinishReasons is the closed set normalizeFinish may return.
var canonicalFinishReasons = map[string]bool{
	FinishReasonStop:          true,
	FinishReasonLength:        true,
	FinishReasonToolCalls:     true,
	FinishReasonContentFilter: true,
	FinishReasonPauseTurn:     true,
	FinishReasonOther:         true,
}

// FuzzNormalizeFinishReason drives NormalizeFinishReason/normalizeFinish over an
// arbitrary provider and raw finish reason. These map provider-specific finish
// codes to serf's canonical vocabulary and feed every api_call log and response;
// only unit tests with a handful of pairs exercised them.
//
// Oracles (not bare no-panic):
//   - empty raw always yields the Stop sentinel with empty Raw (documented).
//   - a non-empty raw is preserved verbatim in .Raw (the contract: "raw value is
//     always preserved").
//   - the canonical Reason is always a member of the closed canonical set.
//   - normalization is deterministic.
func FuzzNormalizeFinishReason(f *testing.F) {
	f.Add("anthropic", "end_turn")
	f.Add("anthropic", "max_tokens")
	f.Add("google", "SAFETY")
	f.Add("openai", "tool_calls")
	f.Add("openai", "weird_value")
	f.Add("", "")

	f.Fuzz(func(t *testing.T, provider, raw string) {
		fr := NormalizeFinishReason(provider, raw)

		if raw == "" {
			if fr.Reason != FinishReasonStop || fr.Raw != "" {
				t.Fatalf("empty raw -> %+v, want {Stop, \"\"}", fr)
			}
			return
		}
		if fr.Raw != raw {
			t.Fatalf("raw not preserved: got %q want %q", fr.Raw, raw)
		}
		if !canonicalFinishReasons[fr.Reason] {
			t.Fatalf("non-canonical reason %q for (%q,%q)", fr.Reason, provider, raw)
		}
		if again := NormalizeFinishReason(provider, raw); again != fr {
			t.Fatalf("not deterministic: %+v vs %+v", fr, again)
		}
	})
}

// FuzzReasoningEffort drives the reasoning-effort vocabulary helpers
// (NormalizeReasoningEffort, ReasoningEffortRank, ClampReasoningEffort,
// ReasoningBudget) over an arbitrary requested level and supported set. These are
// the single source of truth shared by the CLI resolver, runtime setter, and
// loop-detector escalation; a drift here would send a model a level it rejects.
//
// Oracles:
//   - NormalizeReasoningEffort output is lowercased+trimmed and idempotent; the
//     disable aliases collapse to "".
//   - ReasoningEffortRank is >= 0 and agrees for equal normalized values.
//   - ClampReasoningEffort is deterministic, idempotent, and — whenever it
//     actually clamps a known level against a set with known levels — returns a
//     member of the supported set whose rank lies within [minRank, maxRank].
func FuzzReasoningEffort(f *testing.F) {
	f.Add("high", "minimal,low,medium,high")
	f.Add("xhigh", "minimal,low,medium")
	f.Add("  MAX ", "low,high")
	f.Add("none", "low,medium")
	f.Add("bogus", "low,medium")
	f.Add("low", "")

	f.Fuzz(func(t *testing.T, requested, supportedCSV string) {
		supported := splitNonEmpty(supportedCSV)

		norm := NormalizeReasoningEffort(requested)
		if norm != strings.ToLower(norm) || norm != strings.TrimSpace(norm) {
			t.Fatalf("NormalizeReasoningEffort(%q)=%q not lowercased/trimmed", requested, norm)
		}
		if NormalizeReasoningEffort(norm) != norm {
			t.Fatalf("NormalizeReasoningEffort not idempotent on %q", norm)
		}
		switch strings.ToLower(strings.TrimSpace(requested)) {
		case "none", "null", "off", "false", "0":
			if norm != "" {
				t.Fatalf("disable alias %q normalized to %q, want \"\"", requested, norm)
			}
		}

		if r := ReasoningEffortRank(requested); r < 0 {
			t.Fatalf("negative rank %d for %q", r, requested)
		}
		if ReasoningEffortRank(requested) != ReasoningEffortRank(norm) && norm != "" {
			t.Fatalf("rank disagrees across normalization for %q/%q", requested, norm)
		}
		if ReasoningBudget(requested) < 0 {
			t.Fatalf("negative budget for %q", requested)
		}

		got := ClampReasoningEffort(requested, supported)
		if again := ClampReasoningEffort(requested, supported); again != got {
			t.Fatalf("ClampReasoningEffort not deterministic: %q vs %q", got, again)
		}
		if c := ClampReasoningEffort(got, supported); c != got {
			t.Fatalf("ClampReasoningEffort not idempotent: clamp(%q)=%q", got, c)
		}

		// When the result differs from the request, the code chose a supported
		// level: it must be a member of the supported set (case-insensitively) and
		// its rank must lie within the supported set's known rank span.
		minRank, maxRank, anyKnown := supportedRankSpan(supported)
		req := strings.ToLower(strings.TrimSpace(requested))
		if got != requested && req != "" && req != "none" && anyKnown {
			if !containsFold(supported, got) {
				t.Fatalf("clamp(%q,%v)=%q not in supported set", requested, supported, got)
			}
			gr := ReasoningEffortRank(got)
			if gr != 0 && (gr < minRank || gr > maxRank) {
				t.Fatalf("clamp(%q,%v)=%q rank %d outside [%d,%d]", requested, supported, got, gr, minRank, maxRank)
			}
		}
	})
}

func splitNonEmpty(csv string) []string {
	var out []string
	for _, p := range strings.Split(csv, ",") {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return out
}

func containsFold(set []string, want string) bool {
	for _, s := range set {
		if strings.EqualFold(strings.TrimSpace(s), strings.TrimSpace(want)) {
			return true
		}
	}
	return false
}

// supportedRankSpan reports the min and max known rank among supported levels.
func supportedRankSpan(supported []string) (minRank, maxRank int, anyKnown bool) {
	minRank, maxRank = 1<<30, -1
	for _, s := range supported {
		r := ReasoningEffortRank(s)
		if r == 0 {
			continue
		}
		anyKnown = true
		if r < minRank {
			minRank = r
		}
		if r > maxRank {
			maxRank = r
		}
	}
	return minRank, maxRank, anyKnown
}
