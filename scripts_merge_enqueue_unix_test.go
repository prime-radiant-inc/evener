//go:build unix

package evener_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The launcher is a thin wrapper: it forwards its arguments to
// `evener-dev dev merge-enqueue`, passes that helper's output and status
// through, owns the temp directory it builds a helper in, and stops the helper
// on interruption. The queue decisions themselves are internal/devtool/mergeq's
// and are tested there under `go test`; faking `gh` here would restate them
// (docs/developing-evener/testing.md). EVENER_DEV_BIN is the launcher's own
// documented seam for a helper someone else built, so these cases use it to
// test the forwarding the launcher owns.
const mergeEnqueueScript = "scripts/ops/merge-enqueue.sh"

// writeHelper writes an executable helper script for the launcher to run.
func writeHelper(t *testing.T, dir, body string) string {
	t.Helper()
	bin := filepath.Join(dir, "evener-dev")
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	return bin
}

// fakeHelper writes an `evener-dev` substitute that records its argv and exits
// with $FAKE_STATUS after printing $FAKE_OUTPUT.
func fakeHelper(t *testing.T) (bin, record string) {
	t.Helper()
	dir := t.TempDir()
	record = filepath.Join(dir, "argv")
	bin = writeHelper(t, dir, `#!/usr/bin/env bash
printf '%s\n' "$@" > "$FAKE_RECORD"
printf '%s\n' "$FAKE_OUTPUT"
exit "$FAKE_STATUS"
`)
	return bin, record
}

// runMergeEnqueue runs the launcher with env appended to this process's
// environment and returns its stdout, stderr and exit status.
func runMergeEnqueue(t *testing.T, env []string, args ...string) (stdout, stderr string, status int) {
	t.Helper()
	argv := append([]string{mergeEnqueueScript}, args...)
	cmd := exec.Command("bash", argv...)
	cmd.Env = append(os.Environ(), env...)
	var out, errOut strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("run %s: %v", mergeEnqueueScript, err)
		}
		status = exitErr.ExitCode()
	}
	return out.String(), errOut.String(), status
}

func TestMergeEnqueueForwardsEveryArgumentToTheHelper(t *testing.T) {
	bin, record := fakeHelper(t)
	stdout, stderr, status := runMergeEnqueue(t,
		[]string{
			"EVENER_DEV_BIN=" + bin,
			"FAKE_RECORD=" + record,
			"FAKE_OUTPUT=MERGEQ queued position=3",
			"FAKE_STATUS=0",
		},
		"--pr", "42", "--wait", "08", "--jump", "--repo", "o/r")

	if status != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", status, stderr)
	}
	if got, want := stdout, "MERGEQ queued position=3\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	// --wait stays a string the whole way: the helper's parser is the one that
	// reads leading zeros as decimal (#5304), so "08" must arrive unchanged.
	want := "dev\nmerge-enqueue\n--pr\n42\n--wait\n08\n--jump\n--repo\no/r\n"
	argv, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("read recorded argv: %v", err)
	}
	if string(argv) != want {
		t.Errorf("helper argv = %q, want %q", argv, want)
	}
}

func TestMergeEnqueuePassesThroughTheHelperOutcome(t *testing.T) {
	for _, outcome := range []struct {
		output string
		status string
		want   int
	}{
		{"MERGEQ merged sha=abc123", "0", 0},
		{"MERGEQ dropped: PR left the queue without merging", "1", 1},
		{"MERGEQ timeout position=2 state=QUEUED", "1", 1},
		{"evener-dev dev merge-enqueue: --wait takes whole seconds", "2", 2},
		{"MERGEQ interrupted: the wait was cancelled before the queue finished", "130", 130},
	} {
		t.Run("exit "+outcome.status, func(t *testing.T) {
			bin, record := fakeHelper(t)
			stdout, _, status := runMergeEnqueue(t,
				[]string{
					"EVENER_DEV_BIN=" + bin,
					"FAKE_RECORD=" + record,
					"FAKE_OUTPUT=" + outcome.output,
					"FAKE_STATUS=" + outcome.status,
				},
				"--pr", "42")
			if status != outcome.want {
				t.Errorf("exit = %d, want %d", status, outcome.want)
			}
			if got, want := stdout, outcome.output+"\n"; got != want {
				t.Errorf("stdout = %q, want %q", got, want)
			}
		})
	}
}

func TestMergeEnqueueHelpPrintsTheContractAndRunsNoHelper(t *testing.T) {
	bin, record := fakeHelper(t)
	stdout, stderr, status := runMergeEnqueue(t,
		[]string{"EVENER_DEV_BIN=" + bin, "FAKE_RECORD=" + record},
		"--help")

	if status != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %q)", status, stderr)
	}
	for _, want := range []string{
		"--pr N [--wait SECONDS]",
		"MERGEQ merged sha=SHA",
		"MERGEQ dropped: <reason>",
		"Exit 130:",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("--help does not contain %q", want)
		}
	}
	// The contract ends at `set -euo pipefail`; rationale below it is for
	// readers of the file, not for the operator reading --help.
	if strings.Contains(stdout, "set -euo pipefail") {
		t.Error("--help leaked the implementation past the contract block")
	}
	if _, err := os.Stat(record); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("--help ran the helper (record %v)", err)
	}
}

func TestMergeEnqueueRefusesAMissingHelper(t *testing.T) {
	_, stderr, status := runMergeEnqueue(t,
		[]string{"EVENER_DEV_BIN=" + filepath.Join(t.TempDir(), "absent")},
		"--pr", "42")

	if status != 2 {
		t.Errorf("exit = %d, want 2", status)
	}
	if want := "EVENER_DEV_BIN is not an executable file"; !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q, want it to contain %q", stderr, want)
	}
}

// TestMergeEnqueueStopsTheHelperOnTermination sends SIGTERM to the launcher the
// way a background job manager would: to its PID only, so the helper never sees
// the terminal's signal and only the launcher's forwarding can stop it.
func TestMergeEnqueueStopsTheHelperOnTermination(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	stopped := filepath.Join(dir, "stopped")
	// The helper publishes its pid, then handles TERM by recording that it was
	// asked to stop and reporting 130, the status the launcher must pass on.
	bin := writeHelper(t, dir, `#!/usr/bin/env bash
printf '%s\n' "$$" > "$FAKE_PID_FILE"
trap 'printf stopped > "$FAKE_STOPPED_FILE"; exit 130' TERM
while :; do sleep 0.05; done
`)

	cmd := exec.Command("bash", mergeEnqueueScript, "--pr", "42")
	cmd.Env = append(os.Environ(),
		"EVENER_DEV_BIN="+bin,
		"FAKE_PID_FILE="+pidFile,
		"FAKE_STOPPED_FILE="+stopped,
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start launcher: %v", err)
	}
	helperPID := waitForHelperPID(t, pidFile)
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal launcher: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 130 {
			t.Fatalf("launcher exit = %v, want 130", err)
		}
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the launcher did not exit after SIGTERM")
	}

	if _, err := os.Stat(stopped); err != nil {
		t.Errorf("the helper was never asked to stop: %v", err)
	}
	// An orphaned helper is the failure this forwarding exists to prevent: the
	// helper is gone once the launcher reaps it, and still signalable if not.
	waitForProcessGone(t, helperPID)
}

// TestMergeEnqueueForcesTheGroupOnASecondSignal covers the deliberate
// escalation: a helper that catches TERM and keeps running is still stopped,
// because the second signal goes to the process group it leads.
func TestMergeEnqueueForcesTheGroupOnASecondSignal(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	terms := filepath.Join(dir, "terms")
	bin := writeHelper(t, dir, `#!/usr/bin/env bash
printf '%s\n' "$$" > "$FAKE_PID_FILE"
trap 'printf x >> "$FAKE_TERMS_FILE"' TERM
while :; do sleep 0.05; done
`)

	cmd := exec.Command("bash", mergeEnqueueScript, "--pr", "42")
	cmd.Env = append(os.Environ(),
		"EVENER_DEV_BIN="+bin,
		"FAKE_PID_FILE="+pidFile,
		"FAKE_TERMS_FILE="+terms,
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start launcher: %v", err)
	}
	helperPID := waitForHelperPID(t, pidFile)
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal launcher: %v", err)
	}
	// The second signal only counts once the helper has seen the first, which
	// the helper records itself: a fixed sleep would be a guess about scheduling.
	waitForFile(t, terms)
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("second signal launcher: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 137 {
			t.Fatalf("launcher exit = %v, want 137 (the group was killed)", err)
		}
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the launcher did not exit after two SIGTERMs")
	}
	waitForProcessGone(t, helperPID)
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never appeared", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitForProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("process %d is still running", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitForHelperPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, convErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if convErr != nil {
				t.Fatalf("helper pid %q: %v", data, convErr)
			}
			return pid
		}
		if time.Now().After(deadline) {
			t.Fatalf("helper never started (%v)", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestMergeEnqueueRemovesTheScratchItBuildsIn takes the build path: no
// EVENER_DEV_BIN, and a PATH with no `evener-dev` this checkout built (a binary
// outside the checkout is refused by the launcher's provenance check). The
// helper is invoked without --pr, so it refuses with its usage text and exit 2
// before any GitHub call, and the only thing this case measures is that the
// directory the build ran in is gone afterwards.
func TestMergeEnqueueRemovesTheScratchItBuildsIn(t *testing.T) {
	// The build needs the go toolchain and nothing else from this process's
	// PATH: a PATH that could also supply an `evener-dev` would skip the build
	// this case is here to exercise.
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("find go: %v", err)
	}
	tmp := t.TempDir()
	_, stderr, status := runMergeEnqueue(t,
		[]string{
			"EVENER_DEV_BIN=",
			"TMPDIR=" + tmp,
			"PATH=" + filepath.Dir(goBin) + ":/usr/bin:/bin",
		})

	if status != 2 {
		t.Fatalf("exit = %d, want 2 (stderr: %s)", status, stderr)
	}
	if want := "usage: evener-dev dev merge-enqueue"; !strings.Contains(stderr, want) {
		t.Fatalf("stderr = %q, want the built helper's usage naming %q", stderr, want)
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatalf("read TMPDIR: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Errorf("the run left %s in its TMPDIR: %v", mergeEnqueueScript, names)
	}
}
