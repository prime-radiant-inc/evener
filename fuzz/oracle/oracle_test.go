package oracle_test

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"primeradiant.com/serf/fuzz/oracle"
)

// fakeReporter captures Fatalf instead of aborting, so a single Test can assert
// that an oracle PASSES on a correct function and FAILS on a broken one. A real
// *testing.T.Fatalf calls runtime.Goexit; the fake records and returns, which is
// exactly why the combinators return after every Fatalf. It satisfies
// oracle.Reporter (Fatalf + Helper) — the minimal interface that lets a fake
// stand in for *testing.T here.
type fakeReporter struct {
	failed bool
	msg    string
}

func (r *fakeReporter) Helper() {}

func (r *fakeReporter) Fatalf(format string, args ...any) {
	r.failed = true
	// Record the first failure only; the combinators report at most once, but a
	// misbehaving one that reported twice would be caught by this test's
	// single-failure expectation regardless.
	if r.msg == "" {
		r.msg = format
	}
}

// assertPasses runs check against a fresh reporter and fails the enclosing test
// if the oracle reported — i.e. the oracle wrongly reddened on a correct f.
func assertPasses(t *testing.T, name string, check func(oracle.Reporter)) {
	t.Helper()
	r := &fakeReporter{}
	check(r)
	if r.failed {
		t.Fatalf("%s: oracle failed on a correct function: %s", name, r.msg)
	}
}

// assertFails runs check against a fresh reporter and fails the enclosing test
// if the oracle stayed silent — i.e. the oracle has no teeth against a bug it
// claims to catch.
func assertFails(t *testing.T, name string, check func(oracle.Reporter)) {
	t.Helper()
	r := &fakeReporter{}
	check(r)
	if !r.failed {
		t.Fatalf("%s: oracle did NOT fail on a broken function (no teeth)", name)
	}
}

func TestRoundTrip(t *testing.T) {
	// A correct JSON codec round-trips a map.
	enc := func(m map[string]int) ([]byte, error) { return json.Marshal(m) }
	dec := func(b []byte) (map[string]int, error) {
		var m map[string]int
		err := json.Unmarshal(b, &m)
		return m, err
	}
	in := map[string]int{"a": 1, "b": 2}
	assertPasses(t, "correct codec", func(r oracle.Reporter) {
		oracle.RoundTrip(r, enc, dec, in, oracle.DeepEqual[map[string]int])
	})

	// A decoder that drops a key breaks identity — the oracle must catch it.
	lossyDec := func(b []byte) (map[string]int, error) {
		m, err := dec(b)
		delete(m, "b")
		return m, err
	}
	assertFails(t, "lossy decode", func(r oracle.Reporter) {
		oracle.RoundTrip(r, enc, lossyDec, in, oracle.DeepEqual[map[string]int])
	})

	// An encoder that errors is itself a round-trip failure.
	failEnc := func(map[string]int) ([]byte, error) { return nil, errBoom }
	assertFails(t, "encode error", func(r oracle.Reporter) {
		oracle.RoundTrip(r, failEnc, dec, in, oracle.DeepEqual[map[string]int])
	})

	// A decoder that errors is a round-trip failure too.
	failDec := func([]byte) (map[string]int, error) { return nil, errBoom }
	assertFails(t, "decode error", func(r oracle.Reporter) {
		oracle.RoundTrip(r, enc, failDec, in, oracle.DeepEqual[map[string]int])
	})
}

func TestDeterministic(t *testing.T) {
	pure := func(n int) int { return n * n }
	assertPasses(t, "pure square", func(r oracle.Reporter) {
		oracle.Deterministic(r, pure, 7, eqInt)
	})

	// A closure over mutable state returns a different value each call.
	counter := 0
	impure := func(n int) int { counter++; return n + counter }
	assertFails(t, "stateful counter", func(r oracle.Reporter) {
		oracle.Deterministic(r, impure, 7, eqInt)
	})
}

func TestIdempotent(t *testing.T) {
	// sort-and-dedup is idempotent: the second pass changes nothing.
	normalize := func(s []int) []int {
		out := append([]int(nil), s...)
		sort.Ints(out)
		return dedup(out)
	}
	in := []int{3, 1, 2, 3, 1}
	assertPasses(t, "sort+dedup", func(r oracle.Reporter) {
		oracle.Idempotent(r, normalize, in, oracle.DeepEqual[[]int])
	})

	// Appending a sentinel grows on every application — not idempotent.
	grow := func(s []int) []int { return append(append([]int(nil), s...), 0) }
	assertFails(t, "append sentinel", func(r oracle.Reporter) {
		oracle.Idempotent(r, grow, in, oracle.DeepEqual[[]int])
	})
}

func TestPreserves(t *testing.T) {
	// Reversing preserves length.
	reverse := func(s []int) []int {
		out := append([]int(nil), s...)
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
		return out
	}
	in := []int{1, 2, 3, 4}
	assertPasses(t, "reverse keeps len", func(r oracle.Reporter) {
		oracle.Preserves(r, reverse, in, func(s []int) int { return len(s) })
	})

	// Dropping the first element changes the count — the oracle must catch it.
	drop := func(s []int) []int {
		if len(s) == 0 {
			return s
		}
		return s[1:]
	}
	assertFails(t, "drop changes len", func(r oracle.Reporter) {
		oracle.Preserves(r, drop, in, func(s []int) int { return len(s) })
	})
}

func TestAgreesWith(t *testing.T) {
	// Two independent ways to lowercase agree on all input.
	fast := strings.ToLower
	slow := func(s string) string {
		var b strings.Builder
		for _, ch := range s {
			if ch >= 'A' && ch <= 'Z' {
				ch += 'a' - 'A'
			}
			b.WriteRune(ch)
		}
		return b.String()
	}
	assertPasses(t, "two lowercasers", func(r oracle.Reporter) {
		oracle.AgreesWith(r, fast, slow, "Hello World", eqString)
	})

	// A reference that only handles ASCII 'A' diverges from strings.ToLower on
	// any other uppercase letter — the differential catches the disagreement.
	buggy := func(s string) string { return strings.ReplaceAll(s, "A", "a") }
	assertFails(t, "buggy reference", func(r oracle.Reporter) {
		oracle.AgreesWith(r, fast, buggy, "ABC", eqString)
	})
}

// TestReporterIsSatisfiedByTestingT documents (and compiles-checks) that a real
// *testing.T is a valid Reporter — the property that lets the same combinators
// run from a *testing.T unit test and a *testing.F fuzz body unchanged.
func TestReporterIsSatisfiedByTestingT(t *testing.T) {
	var r oracle.Reporter = t
	oracle.Deterministic(r, func(n int) int { return n + 1 }, 41, eqInt)
}

func eqInt(a, b int) bool       { return a == b }
func eqString(a, b string) bool { return a == b }

var errBoom = boomError("boom")

type boomError string

func (e boomError) Error() string { return string(e) }

func dedup(sorted []int) []int {
	if len(sorted) == 0 {
		return sorted
	}
	out := sorted[:1]
	for _, v := range sorted[1:] {
		if v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	return out
}
