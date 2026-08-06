package globpattern

import (
	"strings"
	"testing"
)

// FuzzExpand drives Expand, the package's only exported entry point, with
// arbitrary patterns. Expand's brace-alternative expander is deliberately
// permissive (review-ledger entry #24): it accepts nested groups, an empty
// alternative ("{,}" broadens rather than narrows), and does not understand
// "[...]" character classes, so a literal brace pair meant as a character
// class (e.g. "*.[{a,b}]") is expanded instead of passed through. That last
// one is a real, confirmed behavior gap — Expand("*.[{a,b}]") returns
// []string{"*.[a]", "*.[b]"} instead of leaving the class alone — but it is
// a design question about the package's contract (should "[...]" be
// brace-opaque?), not something this target's invariants assert around: the
// task is to fuzz the properties Expand actually upholds, not to bake in the
// wished-for character-class behavior it currently lacks.
//
// What Expand does guarantee, and what this target asserts:
//   - never panics;
//   - always terminates (MaxExpansions bounds both recursion depth and the
//     accumulated result size, so no crafted pattern makes it loop or
//     allocate unboundedly — coverage-guided fuzzing over minutes confirms
//     this empirically; no single run took longer than a few milliseconds);
//   - on success, returns between 1 and MaxExpansions patterns, with no
//     duplicates;
//   - on success, every returned pattern is a fixed point of Expand: it has
//     no remaining top-level-comma brace group, so expanding it again is a
//     no-op. This is the round-trip property a brace expander must have:
//     expansion is a one-shot operation, not a state that can still change.
//   - on failure (malformed braces or the expansion cap), Expand returns a
//     nil slice.
//
// Scope note found via mutation testing: the invariants above are
// structural (termination, count, fixed-point), not a full semantic oracle.
// A mutation that makes splitAlternatives split on an escaped comma it
// should treat as literal (breaking `{a\,b,c}`-style patterns) passes every
// assertion above — the wrong split still terminates, stays under the cap,
// dedupes, and each piece is still a fixed point, just the wrong one. A
// content-preserving/differential oracle would catch that class, at the
// cost of substantially reimplementing the expander's semantics inside the
// test; out of scope here. What IS proven caught by mutation (see the task
// record) is a removed MaxExpansions enforcement, via the count-bound
// assertion above.
func FuzzExpand(f *testing.F) {
	seeds := []string{
		"src/**/*.go",
		"*.{ts,tsx,css}",
		"src/{a,{b,c}}/*.go",
		"report{,.md}",
		`literal\{name\}.go`,
		"a/{b,c",
		"a/b}",
		"a/{b,{c,d}",
		strings.Repeat("{a,b}", 9),
		"{,}",
		"a{,}b",
		"*.[{a,b}]",
		"{}",
		"",
		`{a\,b,c}`,
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, pattern string) {
		// Coverage-guided fuzzing can otherwise hand Expand a pathological
		// multi-megabyte pattern; MaxExpansions bounds the RESULT, not the
		// cost of getting an error on the way there for some shapes (e.g. a
		// single huge comma-free brace group), so cap the input like the
		// other size-sensitive fuzz targets in this repo do.
		if len(pattern) > 4096 {
			return
		}

		result, err := Expand(pattern)

		if err != nil {
			if result != nil {
				t.Fatalf("Expand(%q) returned err=%v but non-nil result %#v", pattern, err, result)
			}
			return
		}

		if len(result) == 0 {
			t.Fatalf("Expand(%q) returned no error but zero patterns", pattern)
		}
		if len(result) > MaxExpansions {
			t.Fatalf("Expand(%q) returned %d patterns, want <= %d (MaxExpansions)", pattern, len(result), MaxExpansions)
		}
		seen := make(map[string]struct{}, len(result))
		for _, s := range result {
			if _, dup := seen[s]; dup {
				t.Fatalf("Expand(%q) result %#v contains duplicate %q", pattern, result, s)
			}
			seen[s] = struct{}{}
		}

		for _, s := range result {
			again, err := Expand(s)
			if err != nil {
				t.Fatalf("Expand(%q) (an output of Expand(%q)) errored: %v", s, pattern, err)
			}
			if len(again) != 1 || again[0] != s {
				t.Fatalf("Expand not idempotent on its own output: Expand(%q) = %#v, want [%q] (from Expand(%q) = %#v)", s, again, s, pattern, result)
			}
		}
	})
}
