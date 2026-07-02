//go:build serffuzz

package agent

import (
	"strings"
	"testing"
)

// FuzzWvSelfInfluenceNotice drives selfInfluenceNotice — the breaker's pure
// worker-facing wording seam that replaced the deleted validateWatchDeliveryLoop
// create-time forbid (the runtime breaker now bounds self-delivery loops).
//
// Oracles (beyond never-panic):
//   - determinism: the same inputs yield the same line;
//   - empty exactly when not self-influenced;
//   - every non-empty line is a single <system-reminder>-wrapped line;
//   - truncated always wins the depth-less wording (no "~N" leaks);
//   - a depth >= 2 line names the depth, shallower lines never do.
func FuzzWvSelfInfluenceNotice(f *testing.F) {
	f.Add(false, 0, false)
	f.Add(true, 0, false)
	f.Add(true, 1, false)
	f.Add(true, 3, false)
	f.Add(true, 5, true)
	f.Add(true, -2, false)

	f.Fuzz(func(t *testing.T, self bool, gradientDepth int, truncated bool) {
		got := selfInfluenceNotice(self, gradientDepth, truncated)
		if got2 := selfInfluenceNotice(self, gradientDepth, truncated); got != got2 {
			t.Fatalf("non-deterministic: %q vs %q", got, got2)
		}
		if !self {
			if got != "" {
				t.Fatalf("not self-influenced must be empty, got %q", got)
			}
			return
		}
		if got == "" {
			t.Fatal("self-influenced notice must be non-empty")
		}
		if !strings.HasPrefix(got, "<system-reminder>") || !strings.HasSuffix(got, "</system-reminder>") {
			t.Fatalf("notice must be system-reminder wrapped: %q", got)
		}
		if strings.Contains(got, "\n") {
			t.Fatalf("notice must be a single line: %q", got)
		}
		hasDepth := strings.Contains(got, "~")
		if truncated && hasDepth {
			t.Fatalf("truncated notice must not name a depth: %q", got)
		}
		if !truncated && gradientDepth >= 2 && !hasDepth {
			t.Fatalf("deep notice must name the depth: %q", got)
		}
		if !truncated && gradientDepth < 2 && hasDepth {
			t.Fatalf("shallow notice must not name a depth: %q", got)
		}
	})
}
