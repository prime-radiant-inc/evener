package gatesurface

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
)

// This file is the gate-surface drift tripwire the dev-tooling spec requires
// (docs/superpowers/specs/2026-08-17-dev-tooling-in-go-design.md): the gate's
// test-selection surface lives in two homes — scripts/lib/gate-surface-lib.sh
// for the shell consumers (run-module-tests.sh, coverage-floor.sh) and this
// package for the Go consumers. Two homes that drift are worse than no ratchet: PR
// #222 shipped a Makefile copy whose BRE-style escaping made `go test -run`
// match ZERO tests, and the whole gate exited green without running anything.
// These tests fail the moment either home changes without the other.

const shellGateSurfaceLib = "../../../scripts/lib/gate-surface-lib.sh"

// shellSingleQuotedVar extracts VAR='value' from the shell lib, failing the
// test when the assignment is missing or not single-quoted.
func shellSingleQuotedVar(t *testing.T, name string) string {
	t.Helper()
	content, err := os.ReadFile(shellGateSurfaceLib)
	if err != nil {
		t.Fatalf("reading %s: %v", shellGateSurfaceLib, err)
	}
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `='([^']*)'$`)
	m := re.FindSubmatch(content)
	if m == nil {
		t.Fatalf("%s: no single-quoted assignment of %s found", shellGateSurfaceLib, name)
	}
	return string(m[1])
}

// TestShellTestRunMatchesGoConstant pins the shell lib's GATE_TEST_RUN
// byte-equal to gatesurface.TestRun.
func TestShellTestRunMatchesGoConstant(t *testing.T) {
	if shell := shellSingleQuotedVar(t, "GATE_TEST_RUN"); shell != TestRun {
		t.Errorf("gate surface drift: shell GATE_TEST_RUN=%q, Go gatesurface.TestRun=%q — update both homes together", shell, TestRun)
	}
}

// TestShellFuzzTestSkipMatchesGoConstant pins the shell lib's
// GATE_FUZZ_TEST_SKIP byte-equal to gatesurface.FuzzTestSkip.
func TestShellFuzzTestSkipMatchesGoConstant(t *testing.T) {
	if shell := shellSingleQuotedVar(t, "GATE_FUZZ_TEST_SKIP"); shell != FuzzTestSkip {
		t.Errorf("gate surface drift: shell GATE_FUZZ_TEST_SKIP=%q, Go gatesurface.FuzzTestSkip=%q — update both homes together", shell, FuzzTestSkip)
	}
}

// TestShellHubIntegrationRunMatchesGoConstant pins the shell lib's
// GATE_HUB_INTEGRATION_RUN byte-equal to gatesurface.HubIntegrationRun.
func TestShellHubIntegrationRunMatchesGoConstant(t *testing.T) {
	if shell := shellSingleQuotedVar(t, "GATE_HUB_INTEGRATION_RUN"); shell != HubIntegrationRun {
		t.Errorf("gate surface drift: shell GATE_HUB_INTEGRATION_RUN=%q, Go gatesurface.HubIntegrationRun=%q — update both homes together", shell, HubIntegrationRun)
	}
}

// TestHubIntegrationRunMatchesBudget names every test HubIntegrationRun selects in
// testing-budget.json's perTestCeiling. The gate's list is a literal, and the
// budget is the record of which tests the timing gate watches; a test dropped
// from one and not the other is a test that silently stops being timed and
// stops being exercised by the hub-integration lane.
//
// The three groups it covers used to be named ceiling classes. The map is flat
// now, so the members are listed here with the ceiling they must carry: that
// keeps the coupling checkable without a class key to look up.
func TestHubIntegrationRunMatchesBudget(t *testing.T) {
	raw, err := os.ReadFile("../../../testing-budget.json")
	if err != nil {
		t.Fatalf("reading testing-budget.json: %v", err)
	}
	var budget struct {
		Ceilings map[string]float64 `json:"perTestCeiling"` //nolint:tagliatelle // testing-budget.json's key, camelCase beside perTestCeilingSeconds
	}
	if err := json.Unmarshal(raw, &budget); err != nil {
		t.Fatalf("parsing testing-budget.json: %v", err)
	}
	// group -> its ceiling in the budget. The ceiling is part of the pin: a
	// member that changes number without this test changing is a rebaseline that
	// landed in one place only.
	want := map[string]float64{
		"TestHostDeployFilesystemSafety":                                     30,
		"TestHubRPCThreadListServesRemoteHostWhoseAttachOutlastsLocalBudget": 30,
		"TestValidateDeployBinaryAcceptsAGenuineEvenerArtifact":              30,
		"TestDaemonRetirementProcessAdmittedWorkResetsInterval":              30,
		"TestDaemonRetirementProcessDirectPositiveExits":                     30,
		"TestDaemonRetirementProcessDirectZeroStaysResident":                 30,
		"TestDaemonRetirementProcessHelperExitsWithItsFixture":               30,
		"TestDaemonRetirementProcessHubZeroStaysResident":                    30,
		"TestDaemonRetirementProcessReadsDoNotResetInterval":                 30,
		"TestDaemonRetirementProcessRestartedHubRediscoversOrResumes":        30,
		"TestE2E_StopIsOfferedWheneverTheWireSaysActive":                     15,
		"TestE2E_SendPromptToARetiredSession":                                15,
		"TestE2E_SendPromptAfterDaemonDiedMidTurn":                           15,
		"TestE2E_ControlInvariantDuringPreTurnWorkOnTheFirstTurn":            15,
		"TestE2E_ControlInvariantDuringPreTurnWorkAtATurnBoundary":           15,
	}
	for name, ceiling := range want {
		got, ok := budget.Ceilings[name]
		if !ok {
			t.Errorf("testing-budget.json's perTestCeiling no longer names %q, which HubIntegrationRun selects", name)
			continue
		}
		if got != ceiling {
			t.Errorf("testing-budget.json's perTestCeiling gives %q ceiling %g, want %g", name, got, ceiling)
		}
	}
	body := strings.TrimSuffix(strings.TrimPrefix(HubIntegrationRun, "^("), ")$")
	got := map[string]bool{}
	for name := range strings.SplitSeq(body, "|") {
		got[name] = true
	}
	for name := range want {
		if !got[name] {
			t.Errorf("HubIntegrationRun is missing ceiling member %q", name)
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("HubIntegrationRun has %q, which testing-budget.json does not name", name)
		}
	}
}

// TestTestRunSelectsRealTestNames proves TestRun works under Go's RE2 `go
// test -run` semantics, not just that the constant equals itself. PR #222's
// Makefile carried `^\(Test\|Example\)` — valid BRE, valid RE2, but matching
// only literal parens, so every package reported "no tests to run" and the
// gate was a silent no-op. RE2-compile the constant and require it to select
// ordinary Test/Example names and reject Fuzz/Benchmark names.
func TestTestRunSelectsRealTestNames(t *testing.T) {
	re, err := regexp.Compile(TestRun)
	if err != nil {
		t.Fatalf("TestRun %q does not compile as RE2: %v", TestRun, err)
	}
	for _, name := range []string{"TestShellTestRunMatchesGoConstant", "TestAnything", "ExampleAnything"} {
		if !re.MatchString(name) {
			t.Errorf("TestRun %q fails to match ordinary gate test name %q — the gate would run zero tests", TestRun, name)
		}
	}
	for _, name := range []string{"FuzzParseSSE", "BenchmarkAnything"} {
		if re.MatchString(name) {
			t.Errorf("TestRun %q matches %q, which belongs to make fuzz, not the deterministic gate", TestRun, name)
		}
	}
}

// TestFuzzTestSkipCompilesAndSkipsOnlyFuzzFamilies RE2-compiles FuzzTestSkip
// and requires it to match the fuzz-designated names while leaving ordinary
// test names alone.
func TestFuzzTestSkipCompilesAndSkipsOnlyFuzzFamilies(t *testing.T) {
	re, err := regexp.Compile(FuzzTestSkip)
	if err != nil {
		t.Fatalf("FuzzTestSkip %q does not compile as RE2: %v", FuzzTestSkip, err)
	}
	for _, name := range []string{"TestDelegateSeqFuzz", "TestToolArgsSchemaFuzz", "TestFuzzBuildEnforcesInvariants"} {
		if !re.MatchString(name) {
			t.Errorf("FuzzTestSkip %q fails to match fuzz-designated name %q", FuzzTestSkip, name)
		}
	}
	if re.MatchString("TestOrdinaryBehavior") {
		t.Errorf("FuzzTestSkip %q matches an ordinary test name — the gate would silently skip real tests", FuzzTestSkip)
	}
}

// Example of the failure mode this file exists for, kept executable so the
// tripwire's premise stays true: the BRE-escaped pattern compiles fine under
// RE2 but matches no Go test name.
func ExampleTestRun_breEscapingMatchesNothing() {
	bre := regexp.MustCompile(`^\(Test\|Example\)`)
	fmt.Println(bre.MatchString("TestAnything"))
	// Output: false
}
