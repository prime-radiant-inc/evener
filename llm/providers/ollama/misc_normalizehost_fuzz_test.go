package ollama

import (
	"strings"
	"testing"
)

// FuzzMiscOllamaNormalizeHost drives normalizeHost — the OLLAMA_HOST resolver
// that turns a bare host, host:port, bracketed/bare IPv6 literal, or full URL
// into a base URL ending in /v1 — over arbitrary fuzzed input.
//
// Oracles:
//   - never panics (floor);
//   - suffix invariant: the result always ends in "/v1" (every documented
//     resolution path appends it, and the default constant carries it), so the
//     openai-compatible client always receives a versioned endpoint;
//   - determinism: normalizeHost is pure — two calls agree;
//   - idempotence (metamorphic): normalizeHost already emits a scheme-bearing
//     value ending in /v1, and that form is preserved verbatim by the function,
//     so normalizeHost(normalizeHost(h)) == normalizeHost(h). A second pass that
//     re-mangles a value it just produced would be a real bug.
//
// The result is deliberately NOT asserted to be a parseable URL: a legitimate
// OLLAMA_HOST is a host, host:port, or full URL, but arbitrary fuzzed garbage
// with two-plus colons (e.g. "host:a:b") is treated as a bare IPv6 literal and
// bracketed, yielding an unparseable authority. That is out-of-contract input,
// not a guarantee the function makes.
func FuzzMiscOllamaNormalizeHost(f *testing.F) {
	seeds := []string{
		"",
		"   ",
		"localhost",
		"localhost:11434",
		"http://localhost:11434/v1",
		"https://proxy.example/ollama/v1",
		"https://proxy.example/ollama",
		"127.0.0.1",
		"127.0.0.1:1234",
		"::1",
		"fe80::1",
		"[::1]",
		"[::1]:8080",
		"host:a:b",
		"http://x",
		"://",
		"/v1",
		"trailing///",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, h string) {
		got := normalizeHost(h)

		if !strings.HasSuffix(got, "/v1") {
			t.Fatalf("normalizeHost(%q) = %q, which does not end in /v1", h, got)
		}

		if got2 := normalizeHost(h); got2 != got {
			t.Fatalf("normalizeHost nondeterministic: %q then %q for input %q", got, got2, h)
		}

		if again := normalizeHost(got); again != got {
			t.Fatalf("normalizeHost not idempotent on its own output:\n input=%q\n once=%q\n twice=%q", h, got, again)
		}
	})
}
