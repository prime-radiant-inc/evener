//go:build serffuzz

package agent

import (
	"testing"

	"primeradiant.com/serf/llm"
)

// clampTokens maps a fuzzed int to [0, 1<<40) so the three-way sum inside the core
// cannot overflow and stays exactly representable as a float64 integer.
func clampTokens(v int) int {
	if v < 0 {
		v = -v
	}
	if v < 0 { // math.MinInt after negation
		v = 0
	}
	return v % (1 << 40)
}

// FuzzMcEffectiveRecordedInputTokens drives effectiveRecordedInputTokens and its
// companion responseHasServerWebSearch — the pure cores lifted out of
// recordResponseUsage — over adversarial usage snapshots. Oracles (beyond
// never-panic):
//   - determinism: the same inputs yield the same result;
//   - hasServerWebSearch ⇒ record==false;
//   - record iff !hasServerWebSearch && tokens>0;
//   - tokens >= InputTokens+cacheRead+cacheWrite and tokens >= fullHistoryEstimate
//     (on the non-web-search path);
//   - no negative tokens for non-negative inputs;
//   - responseHasServerWebSearch matches the constructed content.
func FuzzMcEffectiveRecordedInputTokens(f *testing.F) {
	f.Add(100, false, 0, false, 0, 0, false)  // plain input, record
	f.Add(0, false, 0, false, 0, 0, false)    // zero -> no record
	f.Add(50, true, 20, true, 30, 200, false) // estimate floors, record
	f.Add(100, true, 10, false, 0, 0, true)   // web search -> skip
	f.Add(0, false, 0, false, 0, 500, false)  // estimate drives record

	f.Fuzz(func(t *testing.T, inputTokens int, hasCacheRead bool, cacheRead int,
		hasCacheWrite bool, cacheWrite int, fullHistoryEstimate int, hasServerWebSearch bool) {

		in := clampTokens(inputTokens)
		cr := clampTokens(cacheRead)
		cw := clampTokens(cacheWrite)
		est := clampTokens(fullHistoryEstimate)

		usage := llm.Usage{InputTokens: in}
		if hasCacheRead {
			v := cr
			usage.CacheReadTokens = &v
		}
		if hasCacheWrite {
			v := cw
			usage.CacheWriteTokens = &v
		}

		tokens, record := effectiveRecordedInputTokens(usage, est, hasServerWebSearch)
		tokens2, record2 := effectiveRecordedInputTokens(usage, est, hasServerWebSearch)
		if tokens != tokens2 || record != record2 {
			t.Fatalf("non-deterministic: (%d,%v) vs (%d,%v)", tokens, record, tokens2, record2)
		}

		if hasServerWebSearch {
			if record {
				t.Fatalf("web search must not record")
			}
		} else {
			base := in
			if hasCacheRead {
				base += cr
			}
			if hasCacheWrite {
				base += cw
			}
			if tokens < base {
				t.Fatalf("tokens %d below input+cache %d", tokens, base)
			}
			if tokens < est {
				t.Fatalf("tokens %d below full-history estimate %d", tokens, est)
			}
			if tokens < 0 {
				t.Fatalf("negative tokens %d for non-negative inputs", tokens)
			}
			if record != (tokens > 0) {
				t.Fatalf("record %v must match tokens>0 (%d)", record, tokens)
			}
		}

		// responseHasServerWebSearch companion.
		var content []llm.ContentPart
		content = append(content, llm.ContentPart{Kind: llm.ContentText})
		if hasServerWebSearch {
			content = append(content, llm.ContentPart{Kind: llm.ContentWebSearch})
		}
		if got := responseHasServerWebSearch(content); got != hasServerWebSearch {
			t.Fatalf("responseHasServerWebSearch=%v want %v", got, hasServerWebSearch)
		}
	})
}
