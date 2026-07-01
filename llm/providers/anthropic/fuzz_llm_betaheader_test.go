package anthropic

import (
	"strings"
	"testing"
)

// FuzzLmAnthropicBetaHeader drives betaHeaderFromProviderOptions, which distills
// the anthropic-beta request header out of a request's free-form ProviderOptions
// (string, []string, or []any of strings). No fuzzer drove it; a malformed
// options map must never panic and the []any arm must never emit an empty or
// untrimmed segment.
//
// Oracles (beyond never-panic):
//   - determinism: the same options map yields the same header.
//   - []any hygiene: the result of a []any of tokens splits back to exactly the
//     trimmed, non-empty tokens in order (round-trip through the join).
//   - a missing/mistyped "anthropic".beta_headers yields the empty string.
func FuzzLmAnthropicBetaHeader(f *testing.F) {
	f.Add("beta-a", "beta-b", "  beta-c  ", "", 0)
	f.Add("computer-use-2025-01-24", "", "", "", 1)
	f.Add(" ", "x", "", "y", 2)
	f.Add("only", "", "", "", 3)
	f.Add("a", "b", "c", "d", 4)

	f.Fuzz(func(t *testing.T, t0, t1, t2, t3 string, shape int) {
		tokens := []string{t0, t1, t2, t3}

		var betaVal any
		switch shape % 5 {
		case 0:
			// []any of strings — the trim+filter arm.
			anyTokens := make([]any, len(tokens))
			for i, s := range tokens {
				anyTokens[i] = s
			}
			betaVal = anyTokens
		case 1:
			betaVal = tokens // []string — join verbatim (no trim/filter)
		case 2:
			betaVal = strings.Join(tokens, ",") // string — trimmed whole
		case 3:
			betaVal = 12345 // wrong type -> ""
		default:
			betaVal = nil
		}

		opts := map[string]any{
			"anthropic": map[string]any{"beta_headers": betaVal},
			"other":     "ignored",
		}

		got := betaHeaderFromProviderOptions(opts)
		if again := betaHeaderFromProviderOptions(opts); again != got {
			t.Fatalf("betaHeaderFromProviderOptions nondeterministic: %q vs %q", got, again)
		}

		switch shape % 5 {
		case 0:
			// The header is exactly the comma-join of the trimmed, non-empty
			// tokens in order. (A token may itself contain a comma; the function
			// does not escape those, so this join equality — not a naive
			// re-split — is the true contract.)
			var want []string
			for _, s := range tokens {
				if ts := strings.TrimSpace(s); ts != "" {
					want = append(want, ts)
				}
			}
			if wantJoined := strings.Join(want, ","); got != wantJoined {
				t.Fatalf("[]any header = %q, want %q", got, wantJoined)
			}
		case 3, 4:
			if got != "" {
				t.Fatalf("wrong-typed beta_headers should yield empty, got %q", got)
			}
		}

		// A map without the anthropic sub-map yields empty.
		if betaHeaderFromProviderOptions(map[string]any{"x": 1}) != "" {
			t.Fatalf("missing anthropic sub-map should yield empty header")
		}
		if betaHeaderFromProviderOptions(nil) != "" {
			t.Fatalf("nil options should yield empty header")
		}
	})
}
