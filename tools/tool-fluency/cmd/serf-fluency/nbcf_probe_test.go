package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNBCFSeededConfigPathProbeLoadsAndValidates is an offline check (no live
// model calls — see docs/testing.md's "keep it real only where model
// behavior is the thing under test" boundary) for kata nbcf's opt-in
// diagnose-then-fix eval scaffold. It proves three things deterministically:
//
//  1. the probe manifest under probes-nbcf/ parses and its prompt describes
//     the SYMPTOM without leaking the seeded bug (the model must diagnose it);
//  2. the fixture materializes with the bug still present, not pre-fixed;
//  3. the probe's expectations actually distinguish a stalled transcript
//     (no test run, no regression test, no completion report) from a
//     completed one — proving the pass/fail gate is not a tautology.
//
// It does not run a live model. See probes-nbcf/README.md for that.
func TestNBCFSeededConfigPathProbeLoadsAndValidates(t *testing.T) {
	probes, err := loadProbes(filepath.Join("..", "..", "probes-nbcf"), "all")
	if err != nil {
		t.Fatalf("loadProbes: %v", err)
	}
	if len(probes) != 1 {
		t.Fatalf("probes = %#v, want exactly 1", probes)
	}
	probe := probes[0]
	if probe.ID != "diagnostic_fix.seeded_config_path" {
		t.Fatalf("id = %q", probe.ID)
	}
	if probe.Tool != "shell" {
		t.Fatalf("tool = %q, want shell", probe.Tool)
	}
	if !strings.Contains(probe.Prompt, "DIAGNOSIS_COMPLETE") {
		t.Fatalf("prompt missing completion token: %q", probe.Prompt)
	}

	// The prompt must name the SYMPTOM, never the seeded bug's mechanism —
	// diagnosing that is the model's job.
	for _, giveaway := range []string{"os.Getenv", "Getenv(\"HOME\")", "ignores homeDir", "ignores the homeDir"} {
		if strings.Contains(probe.Prompt, giveaway) {
			t.Fatalf("prompt leaks the seeded bug via %q", giveaway)
		}
	}

	work := t.TempDir()
	if err := materializeFixture(work, probe.Fixture); err != nil {
		t.Fatalf("materializeFixture: %v", err)
	}
	src, err := os.ReadFile(filepath.Join(work, "configpath", "resolve.go"))
	if err != nil {
		t.Fatalf("reading materialized fixture: %v", err)
	}
	if !strings.Contains(string(src), "homeDir") {
		t.Fatalf("materialized fixture lost the homeDir parameter: %s", src)
	}
	// The fixture ships the bug, not the fix: the fallback branch must not
	// already use homeDir, or there is nothing left to diagnose.
	if strings.Contains(string(src), "filepath.Join(homeDir") {
		t.Fatalf("fixture is pre-fixed; the seeded bug must still be present: %s", src)
	}

	// A stalled transcript (never ran a test, never wrote the regression
	// test, never reported completion) must fail expectations.
	stalled := probeResult{
		FinalOutput:         "still investigating the environment propagation",
		CanonicalToolCounts: map[string]int{"shell": 0},
	}
	if findings := evaluateExpectations(work, probe, stalled); len(findings) == 0 {
		t.Fatal("stalled transcript passed expectations; the gate is not discriminating")
	}

	// A completed transcript — regression test written, fix applied, tests
	// run twice, completion token reported — must pass.
	if err := os.WriteFile(filepath.Join(work, "configpath", "resolve_test.go"), []byte("package configpath\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const buggyReturn = `return filepath.Join(os.Getenv("HOME"), ".evener", "providers.toml")`
	const fixedReturn = `return filepath.Join(homeDir, ".evener", "providers.toml")`
	fixedSrc := strings.Replace(string(src), buggyReturn, fixedReturn, 1)
	if fixedSrc == string(src) {
		t.Fatal("test setup did not actually rewrite the fallback branch — fixture text drifted from this test")
	}
	if err := os.WriteFile(filepath.Join(work, "configpath", "resolve.go"), []byte(fixedSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	completed := probeResult{
		FinalOutput:         "DIAGNOSIS_COMPLETE: the fallback branch read the real HOME env var instead of the homeDir parameter. Fixed by joining homeDir directly.",
		CanonicalToolCounts: map[string]int{"shell": 2},
	}
	if findings := evaluateExpectations(work, probe, completed); len(findings) != 0 {
		t.Fatalf("completed transcript failed expectations: %#v", findings)
	}
}
