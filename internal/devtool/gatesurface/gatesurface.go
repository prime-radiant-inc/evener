// Package gatesurface is the single definition of which tests the
// deterministic non-fuzz gate runs, shared by the gate itself and by the
// coverage ratchet that must measure the same surface. It is a Go port of
// scripts/lib/gate-surface-lib.sh.
//
// This lives in one file because the alternative is two copies that drift. A
// ratchet measuring a different surface than the gate proves is worse than no
// ratchet: the number moves for reasons no one can attribute, and the floor
// ends up blessed against a surface no gate reproduces.
package gatesurface

// FuzzTestSkip is the regex matching fuzz-designated Test* functions excluded
// from the regular deterministic gate. Native Fuzz* targets are already
// excluded by -run; these names cover rapid/sequence fuzz tests and
// structured-generator reachability proofs that remain under make fuzz.
//
// Ported verbatim from scripts/lib/gate-surface-lib.sh's GATE_FUZZ_TEST_SKIP.
var FuzzTestSkip = `(SeqFuzz|SchemaFuzz|Structured.*Reach|LifecycleAdapter|ToolArgsAdapter|SeqAdapter|TurnPagingEquivalenceSanity|WireTypeRegistryCoverage|LineWindowExtractorsSanity|WriteListRoundTrip|LaunchConfigThreeStateRoundTrip|DifferentialSanity|StreamVsNonStreamSanity|FuzzBuildEnforces)`

// TestRun is the regex for which tests the gate runs. The gate runs ordinary
// Test/Example functions only; without this filter `go test` also executes
// every native Fuzz target's committed seed corpus, which is make fuzz's job,
// not the default gate's.
//
// Ported verbatim from scripts/lib/gate-surface-lib.sh's GATE_TEST_RUN.
var TestRun = `^(Test|Example)`
