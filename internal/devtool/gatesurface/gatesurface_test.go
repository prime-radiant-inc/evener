package gatesurface

import (
	"strings"
	"testing"
)

// TestFuzzTestSkipNotEmpty asserts that FuzzTestSkip, the regex matching
// fuzz-designated Test* functions excluded from the regular deterministic gate,
// is non-empty. It is a long alternation ported verbatim from
// scripts/lib/gate-surface-lib.sh's GATE_FUZZ_TEST_SKIP.
func TestFuzzTestSkipNotEmpty(t *testing.T) {
	if FuzzTestSkip == "" {
		t.Fatal("FuzzTestSkip must be non-empty; it is the fuzz-designated skip regex")
	}
}

// TestFuzzTestSkipContainsKeyPatterns asserts that FuzzTestSkip names the
// canonical fuzz-designated test families the shell lib enumerates: rapid
// sequence fuzz, schema fuzz, and the build-enforcement reachability proof.
func TestFuzzTestSkipContainsKeyPatterns(t *testing.T) {
	want := []string{"SeqFuzz", "SchemaFuzz", "FuzzBuildEnforces"}
	for _, w := range want {
		if !strings.Contains(FuzzTestSkip, w) {
			t.Errorf("FuzzTestSkip=%q missing key pattern %q", FuzzTestSkip, w)
		}
	}
}

// TestTestRunIsTestExample asserts the gate runs only ordinary Test/Example
// functions; this filter excludes native Fuzz targets' committed seed corpora
// from the default gate.
func TestTestRunIsTestExample(t *testing.T) {
	const want = "^(Test|Example)"
	if TestRun != want {
		t.Errorf("TestRun=%q, want %q", TestRun, want)
	}
}

// TestCapabilitySkipPatternLoopbackBind asserts the loopback-bind capability
// maps to the root-module E2E skip pattern shared by cmd/evener-hub's TestE2E_*
// family and cmd/evener-tui's TestTUITmuxE2E_* family.
func TestCapabilitySkipPatternLoopbackBind(t *testing.T) {
	const want = "^(TestE2E_|TestTUITmuxE2E_)"
	if got := CapabilitySkipPattern("loopback-bind"); got != want {
		t.Errorf("CapabilitySkipPattern(loopback-bind)=%q, want %q", got, want)
	}
}

// TestCapabilitySkipPatternProcessInspect asserts process-inspect maps to the
// same E2E skip pattern as loopback-bind.
func TestCapabilitySkipPatternProcessInspect(t *testing.T) {
	const want = "^(TestE2E_|TestTUITmuxE2E_)"
	if got := CapabilitySkipPattern("process-inspect"); got != want {
		t.Errorf("CapabilitySkipPattern(process-inspect)=%q, want %q", got, want)
	}
}

// TestCapabilitySkipPatternUnknown asserts an unrecognized capability id
// yields an empty skip pattern (nothing is skipped).
func TestCapabilitySkipPatternUnknown(t *testing.T) {
	if got := CapabilitySkipPattern("does-not-exist"); got != "" {
		t.Errorf("CapabilitySkipPattern(unknown)=%q, want empty", got)
	}
}

// TestCapabilitySkipPatternChromeCDP asserts chrome-cdp, a known probed
// capability id, yields an empty skip pattern: no test file anywhere in the
// tree consumes it today. The dead entry returns empty for honesty and is
// reported rather than skipped.
func TestCapabilitySkipPatternChromeCDP(t *testing.T) {
	if got := CapabilitySkipPattern("chrome-cdp"); got != "" {
		t.Errorf("CapabilitySkipPattern(chrome-cdp)=%q, want empty", got)
	}
}

// TestCapabilitySkipPatternGitCache asserts git-cache, the other known-but-dead
// probed capability id, yields an empty skip pattern.
func TestCapabilitySkipPatternGitCache(t *testing.T) {
	if got := CapabilitySkipPattern("git-cache"); got != "" {
		t.Errorf("CapabilitySkipPattern(git-cache)=%q, want empty", got)
	}
}
