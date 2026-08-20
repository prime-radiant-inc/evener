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
