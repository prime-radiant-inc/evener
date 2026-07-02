//go:build serffuzz

package agent

import (
	"math"
	"testing"
)

// clampWindow maps a fuzzed int into [-1, 1<<32) so the 0.8-threshold math stays
// exactly representable while still exercising the non-positive branch.
func clampWindow(v int) int {
	if v < 0 {
		return -1
	}
	return v % (1 << 32)
}

// FuzzMcContextUsageWarning drives contextUsageWarning — the pure decision core
// lifted out of maybeWarnContextUsage — over adversarial context-window/token
// combinations. Oracles (beyond never-panic):
//   - determinism: the same inputs yield the same result;
//   - warn iff contextWindow>0 && float64(tokens) > 0.8*contextWindow;
//   - warn ⇒ percent>=80 && approxTokens==round(tokens);
//   - contextWindow<=0 ⇒ warn==false.
func FuzzMcContextUsageWarning(f *testing.F) {
	f.Add(1000, 900) // over threshold -> warn
	f.Add(1000, 800) // exactly at threshold -> no warn
	f.Add(1000, 0)   // empty -> no warn
	f.Add(0, 500)    // no window -> no warn
	f.Add(-1, 500)   // negative window -> no warn
	f.Add(200000, 199999)

	f.Fuzz(func(t *testing.T, contextWindow, estimatedTokens int) {
		cw := clampWindow(contextWindow)
		tokens := estimatedTokens
		if tokens < 0 {
			tokens = -tokens
		}
		if tokens < 0 { // math.MinInt after negation
			tokens = 0
		}
		tokens = tokens % (1 << 32)

		warn, approx, pct := contextUsageWarning(cw, tokens)
		warn2, approx2, pct2 := contextUsageWarning(cw, tokens)
		if warn != warn2 || approx != approx2 || pct != pct2 {
			t.Fatalf("non-deterministic: (%v,%d,%d) vs (%v,%d,%d)", warn, approx, pct, warn2, approx2, pct2)
		}

		wantWarn := cw > 0 && float64(tokens) > 0.8*float64(cw)
		if warn != wantWarn {
			t.Fatalf("warn=%v want %v (cw=%d tokens=%d)", warn, wantWarn, cw, tokens)
		}
		if cw <= 0 && warn {
			t.Fatalf("non-positive window must not warn: cw=%d", cw)
		}
		if warn {
			if pct < 80 {
				t.Fatalf("warn implies percent>=80, got %d (cw=%d tokens=%d)", pct, cw, tokens)
			}
			if approx != int(math.Round(float64(tokens))) {
				t.Fatalf("approxTokens %d != round(tokens) %d", approx, int(math.Round(float64(tokens))))
			}
			if approx != tokens {
				t.Fatalf("approxTokens %d != tokens %d for exact-int input", approx, tokens)
			}
		}
	})
}
