package evener_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// timingBudgetScript is the runner under test. It derives its repository root
// from its own path, so these tests copy it (and the two libraries it sources)
// into a synthetic tree and drive it there rather than against the real
// checkout.
const timingBudgetScript = "scripts/gate/test-timing-budget.sh"

// timingBudgetRun drives the runner against a synthetic one-package tree whose
// `go` is a stub: `go list ./...` answers with example.com/mod and
// `go test -json` replays stream verbatim, so the test controls the measurement
// with no real test run and no timing. extraArgs are appended after the flags
// the harness sets. It returns the runner's combined output and exit status.
func timingBudgetRun(t *testing.T, stream string, extraArgs ...string) (string, error) {
	t.Helper()
	root := t.TempDir()

	// The runner sources these two libraries relative to its own path and
	// derives its repo root two levels up, so the copy has to keep the shape.
	for _, rel := range []string{
		timingBudgetScript,
		"scripts/lib/gate-surface-lib.sh",
		"scripts/lib/scratch-lib.sh",
	} {
		body, err := os.ReadFile(rel)
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		writeAuditScriptFixture(t, filepath.Join(root, rel), string(body))
	}

	writeAuditScriptFixture(t, filepath.Join(root, "mod/go.mod"), "module example.com/mod\n")
	streamPath := filepath.Join(root, "stream.jsonl")
	writeAuditScriptFixture(t, streamPath, stream)

	stubDir := filepath.Join(root, "stubbin")
	stub := "#!/bin/sh\ncase \"$1\" in\n" +
		" list) printf 'example.com/mod\\n' ;;\n" +
		" test) cat \"$TIMING_FAKE_GO_JSON\" ;;\n" +
		" *) exit 1 ;;\nesac\n"
	stubPath := filepath.Join(stubDir, "go")
	writeAuditScriptFixture(t, stubPath, stub)
	if err := os.Chmod(stubPath, 0o755); err != nil {
		t.Fatalf("chmod stub go: %v", err)
	}

	budget := filepath.Join(root, "budget.json")
	writeAuditScriptFixture(t, budget, `{"perTestCeilingSeconds":3,"packages":{"example.com/mod":100}}`)

	// scratch_dir requires a usable TMPDIR, so the runner's scratch root is a
	// real directory under the tree rather than the test process's own.
	tmpDir := filepath.Join(root, "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", tmpDir, err)
	}

	args := append([]string{
		"--modules", "mod", "--no-web", "--budget", budget,
	}, extraArgs...)
	cmd := exec.Command("bash", append([]string{filepath.Join(root, timingBudgetScript)}, args...)...)
	cmd.Env = []string{
		"PATH=" + stubDir + ":" + os.Getenv("PATH"),
		"LC_ALL=C",
		"TMPDIR=" + tmpDir,
		"TIMING_FAKE_GO_JSON=" + streamPath,
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestTimingBudgetMeasuresPackageWallTimeNotSummedTests pins issue #172's last
// live defect: the per-package number must be the package's own wall time, read
// from the package-level terminal event, not the sum of top-level test Elapsed.
// Under t.Parallel those tests overlap, so summing their Elapsed counts the same
// wall-clock second more than once and tracks contention rather than work. Two
// 0.3s parallel tests plus a 0.05s test sum to 0.65s while the package terminal
// event reports 0.45s; the runner must report 0.45s.
func TestTimingBudgetMeasuresPackageWallTimeNotSummedTests(t *testing.T) {
	stream := strings.Join([]string{
		`{"Action":"pass","Package":"example.com/mod","Test":"TestParallelA","Elapsed":0.3}`,
		`{"Action":"pass","Package":"example.com/mod","Test":"TestParallelB","Elapsed":0.3}`,
		`{"Action":"pass","Package":"example.com/mod","Test":"TestChild","Elapsed":0.05}`,
		`{"Action":"pass","Package":"example.com/mod","Test":"TestChild/child","Elapsed":0.05}`,
		`{"Action":"pass","Package":"example.com/mod","Elapsed":0.45}`,
	}, "\n") + "\n"

	out, err := timingBudgetRun(t, stream)
	if err != nil {
		t.Fatalf("runner failed on a complete measurement: %v\n%s", err, out)
	}
	if !strings.Contains(out, "example.com/mod: 0.45s") {
		t.Fatalf("runner did not report the package's own wall time (0.45s); it summed top-level test Elapsed instead:\n%s", out)
	}
	if strings.Contains(out, "0.65s") {
		t.Fatalf("runner counted overlapping parallel tests additively (0.65s); the per-package number must come from the package terminal event:\n%s", out)
	}
}

// TestTimingBudgetRefusesAStreamMissingAPackageTerminalEvent pins the
// completeness oracle the parser already carries, so a later change cannot
// quietly drop it: a package `go list` reported that never produces a terminal
// event is a silent drop, and the run must fail rather than bless around it.
func TestTimingBudgetRefusesAStreamMissingAPackageTerminalEvent(t *testing.T) {
	stream := `{"Action":"pass","Package":"example.com/mod","Test":"TestParallelA","Elapsed":0.3}` + "\n"

	out, err := timingBudgetRun(t, stream)
	if err == nil {
		t.Fatalf("a stream missing the package's terminal event must fail the run:\n%s", out)
	}
	if !strings.Contains(out, "produced no terminal event") {
		t.Fatalf("refusal does not name the missing package terminal event:\n%s", out)
	}
}
