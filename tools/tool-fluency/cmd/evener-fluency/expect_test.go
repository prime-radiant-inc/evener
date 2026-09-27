package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEvaluateExpectationsReportsFailedChecks(t *testing.T) {
	t.Parallel()
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "ok.txt"), []byte("ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	probe := probeFile{Expect: expectSpec{Checks: []checkSpec{
		{Name: "ok exists", Run: "test -f ok.txt"},
		{Name: "missing exists", Run: "test -f missing.txt"},
	}}}
	got := evaluateExpectations(work, probe, probeResult{})
	if len(got) != 1 || got[0].Category != "outcome" || got[0].Title != "check failed: missing exists" {
		t.Fatalf("findings = %+v, want one failed check named missing exists", got)
	}
}

// TestRunCheckStopsAHungCommand: a check that never exits fails as a timeout
// soon after its deadline, so one bad check cannot stall a run.
func TestRunCheckStopsAHungCommand(t *testing.T) {
	t.Parallel()
	start := time.Now()
	ok, detail := runCheck(t.TempDir(), checkSpec{Name: "hang", Run: "sleep 30"}, 200*time.Millisecond)
	if ok || !strings.Contains(detail, "timed out") {
		t.Fatalf("runCheck = %v, %q; want a timeout failure", ok, detail)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("runCheck returned after %s for a 200ms timeout", elapsed)
	}
}

func TestEvaluateExpectationsCapsToolCalls(t *testing.T) {
	t.Parallel()
	probe := probeFile{Expect: expectSpec{MaxCalls: map[string]int{"job_status": 1}}}
	within := evaluateExpectations(t.TempDir(), probe, probeResult{CanonicalToolCounts: map[string]int{"job_status": 1}})
	over := evaluateExpectations(t.TempDir(), probe, probeResult{CanonicalToolCounts: map[string]int{"job_status": 3}})
	if len(within) != 0 {
		t.Fatalf("at the cap: findings = %+v, want none", within)
	}
	if len(over) != 1 || over[0].Category != "churn" {
		t.Fatalf("over the cap: findings = %+v, want one churn finding", over)
	}
}

func TestEvaluateExpectationsAllowToolErrors(t *testing.T) {
	t.Parallel()
	res := probeResult{ToolErrors: map[string]int{"read_file": 2}}
	strict := evaluateExpectations(t.TempDir(), probeFile{}, res)
	lenient := evaluateExpectations(t.TempDir(), probeFile{Expect: expectSpec{AllowToolErrors: true}}, res)
	if len(strict) != 1 || strict[0].Category != "arguments" {
		t.Fatalf("strict: findings = %+v, want one arguments finding", strict)
	}
	if len(lenient) != 0 {
		t.Fatalf("lenient: findings = %+v, want none", lenient)
	}
}
