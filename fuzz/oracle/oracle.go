// Package oracle is a small library of reusable fuzz oracles — the "these two
// things must agree" assertions a fuzz target checks after driving its input.
// Each combinator captures one recurring correctness property (round-trip,
// determinism, idempotence, count preservation, differential agreement) so a new
// fuzz target expresses its invariant in ONE line instead of hand-writing the
// compare-and-Fatalf dance every time.
//
// This is the serf-agnostic core of the fuzzing toolkit: it imports only the
// standard library and NOTHING from primeradiant.com/serf, so the boundary that
// keeps fuzz/ portable holds (see fuzz/go.mod). A serf-side fuzz target imports
// this package; this package never imports back.
//
// # Reporter, not *testing.T
//
// Every combinator takes a Reporter — the two-method subset of *testing.T /
// *testing.F / *testing.B that a fuzz body actually uses. Depending on the
// concrete testing.TB would forbid a fake (testing.TB is sealed), and the whole
// point of the combinators is to be unit-tested with a fake that captures
// Fatalf instead of aborting the process. Both a real *testing.T and the tests'
// fake satisfy Reporter.
//
// A real *testing.T.Fatalf aborts the goroutine (runtime.Goexit); a fake does
// not. Every combinator therefore returns immediately after reporting, so it is
// safe against a non-aborting Reporter and never chains a second failure off a
// value it already rejected.
package oracle

import "reflect"

// Reporter is the slice of the testing API the oracles use. *testing.T,
// *testing.F, and *testing.B all satisfy it, as does a capturing fake.
type Reporter interface {
	// Fatalf reports a failed oracle. On a real *testing.T it aborts the test.
	Fatalf(format string, args ...any)
	// Helper marks the caller as a test helper so failures point at the fuzz
	// body, not at this library.
	Helper()
}

// DeepEqual adapts reflect.DeepEqual to the eq func the combinators take, so a
// caller with no cheaper comparison can write oracle.DeepEqual[MyType] instead
// of a one-line closure. Prefer a type-specific comparison when the value has
// fields (e.g. floats, unexported state) that DeepEqual handles wrongly.
func DeepEqual[T any](a, b T) bool { return reflect.DeepEqual(a, b) }

// RoundTrip asserts that decoding an encoded value reproduces the original:
// dec(enc(in)) == in. Use it for any codec — JSON marshal/unmarshal, wire
// encode/decode, serialize/parse, a store's write/read pair. If enc∘dec is a
// documented fixed point rather than strict identity (e.g. a normalizing
// encoder), pass an already-normalized in and an eq that reflects the fixed
// point. Either encode or decode failing is itself a round-trip failure.
func RoundTrip[T, E any](t Reporter, enc func(T) (E, error), dec func(E) (T, error), in T, eq func(a, b T) bool) {
	t.Helper()
	encoded, err := enc(in)
	if err != nil {
		t.Fatalf("RoundTrip: enc(%#v) failed: %v", in, err)
		return
	}
	out, err := dec(encoded)
	if err != nil {
		t.Fatalf("RoundTrip: dec(enc(%#v)) failed: %v", in, err)
		return
	}
	if !eq(in, out) {
		t.Fatalf("RoundTrip: dec(enc(x)) != x\n in  = %#v\n out = %#v", in, out)
	}
}

// Deterministic asserts that f is a pure function of its input: two calls on the
// same value produce equal results, f(in) == f(in). Use it to pin down any
// function a fuzz target expects to be reproducible — a classifier, a parser, a
// hash, a formatter — so a hidden map iteration, clock read, or global-state
// dependency reddens the target.
func Deterministic[T, R any](t Reporter, f func(T) R, in T, eq func(a, b R) bool) {
	t.Helper()
	first := f(in)
	second := f(in)
	if !eq(first, second) {
		t.Fatalf("Deterministic: f(x) varied across calls\n once  = %#v\n twice = %#v\n in    = %#v", first, second, in)
	}
}

// Idempotent asserts that applying f twice equals applying it once:
// f(f(x)) == f(x). Use it for normalizers, canonicalizers, cleanup passes, and
// closure/saturation operations — anything whose second application must be a
// no-op. It is the fixed-point property, distinct from RoundTrip (same type, one
// function) and from Deterministic (which only fixes the FIRST application).
func Idempotent[T any](t Reporter, f func(T) T, in T, eq func(a, b T) bool) {
	t.Helper()
	once := f(in)
	twice := f(once)
	if !eq(once, twice) {
		t.Fatalf("Idempotent: f(f(x)) != f(x)\n once  = %#v\n twice = %#v\n in    = %#v", once, twice, in)
	}
}

// Preserves asserts that f leaves a measured quantity unchanged:
// measure(f(in)) == measure(in). Use it for transforms that must conserve a
// count or size — a filter that reorders but drops nothing, an encoder that
// keeps element count, a rewrite that preserves length. It is a cheap
// partial-correctness oracle when a full RoundTrip reference is too costly.
func Preserves[T any](t Reporter, f func(T) T, in T, measure func(T) int) {
	t.Helper()
	before := measure(in)
	after := measure(f(in))
	if before != after {
		t.Fatalf("Preserves: measure changed under f\n before = %d\n after  = %d\n in     = %#v", before, after, in)
	}
}

// AgreesWith asserts that two implementations agree on every input:
// f(in) == g(in). This is the differential oracle — the highest-value fuzz lever,
// because "the two agree" needs no hand-written expected value. Use it to pit an
// optimized path against a naive reference, an old version against a new one, or
// two independent code paths that must compute the same logical result.
func AgreesWith[T, R any](t Reporter, f, g func(T) R, in T, eq func(a, b R) bool) {
	t.Helper()
	got := f(in)
	want := g(in)
	if !eq(got, want) {
		t.Fatalf("AgreesWith: implementations disagree\n f(x) = %#v\n g(x) = %#v\n in   = %#v", got, want, in)
	}
}
