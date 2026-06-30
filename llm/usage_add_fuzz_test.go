package llm

import "testing"

// FuzzUsageAdd drives Usage.Add (and addOptInt) over two arbitrary usage values
// with independently present/absent optional pointer fields. Add is the field-wise
// token-summation used wherever step usages are folded into a turn total; only
// unit tests with fixed values touched it.
//
// Oracles:
//   - the scalar fields are the exact arithmetic sum (count preservation).
//   - an optional field is non-nil in the result iff it was non-nil in at least
//     one operand, and its value is the sum of the present operands.
//   - Add is commutative (a+b == b+a) for every field, and its Raw is reset to a
//     non-nil empty map per the documented contract.
func FuzzUsageAdd(f *testing.F) {
	f.Add(1, 2, 3, 4, 5, 6, true, false, true)
	f.Add(0, 0, 0, 0, 0, 0, false, false, false)
	f.Add(-3, 7, 100, -100, 9, 0, true, true, true)

	f.Fuzz(func(t *testing.T, ai, ao, bi, bo, ar, br int, aHasR, bHasR, swapEmpty bool) {
		a := Usage{InputTokens: ai, OutputTokens: ao, TotalTokens: ai + ao}
		b := Usage{InputTokens: bi, OutputTokens: bo, TotalTokens: bi + bo}
		if aHasR {
			r := ar
			a.ReasoningTokens = &r
		}
		if bHasR {
			r := br
			b.CacheReadTokens = &r
		}
		if swapEmpty {
			// Exercise the both-nil branch of addOptInt for the same field.
			a.CacheWriteTokens = nil
			b.CacheWriteTokens = nil
		}

		out := a.Add(b)
		if out.InputTokens != ai+bi || out.OutputTokens != ao+bo || out.TotalTokens != a.TotalTokens+b.TotalTokens {
			t.Fatalf("scalar sum wrong: %+v from %+v + %+v", out, a, b)
		}
		if out.Raw == nil || len(out.Raw) != 0 {
			t.Fatalf("Raw not reset to empty map: %v", out.Raw)
		}

		assertOptSum(t, "ReasoningTokens", a.ReasoningTokens, b.ReasoningTokens, out.ReasoningTokens)
		assertOptSum(t, "CacheReadTokens", a.CacheReadTokens, b.CacheReadTokens, out.CacheReadTokens)
		assertOptSum(t, "CacheWriteTokens", a.CacheWriteTokens, b.CacheWriteTokens, out.CacheWriteTokens)

		// Commutativity across every numeric field.
		rev := b.Add(a)
		if rev.InputTokens != out.InputTokens || rev.OutputTokens != out.OutputTokens ||
			rev.TotalTokens != out.TotalTokens || !sameOpt(rev.ReasoningTokens, out.ReasoningTokens) ||
			!sameOpt(rev.CacheReadTokens, out.CacheReadTokens) {
			t.Fatalf("Add not commutative: %+v vs %+v", out, rev)
		}
	})
}

func assertOptSum(t *testing.T, name string, a, b, got *int) {
	t.Helper()
	if a == nil && b == nil {
		if got != nil {
			t.Fatalf("%s: both nil but result %d", name, *got)
		}
		return
	}
	want := 0
	if a != nil {
		want += *a
	}
	if b != nil {
		want += *b
	}
	if got == nil || *got != want {
		t.Fatalf("%s: got %v want %d", name, got, want)
	}
}

func sameOpt(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
