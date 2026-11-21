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

// launcherProc is a launcher started for a signal case, with its output kept for
// the assertions that follow.
type launcherProc struct {
	cmd    *exec.Cmd
	stdout strings.Builder
	stderr strings.Builder
	exited bool
}

// startLauncher starts the launcher and registers the kill cleanup before the
// caller waits for readiness, so a test that fails that wait cannot leave the
// launcher, the helper, or a gh call behind.
func startLauncher(t *testing.T, env []string, args ...string) *launcherProc {
	t.Helper()
	p := &launcherProc{}
	argv := append([]string{mergeEnqueueScript}, args...)
	p.cmd = exec.Command("bash", argv...)
	p.cmd.Env = append(os.Environ(), env...)
	p.cmd.Stdout = &p.stdout
	p.cmd.Stderr = &p.stderr
	if err := p.cmd.Start(); err != nil {
		t.Fatalf("start launcher: %v", err)
	}
	t.Cleanup(func() {
		// Killing is all this may do: waitExit owns Wait when it ran, and a
		// second Wait would race it. A launcher this test never waited for is
		// left for the runner to reap.
		if !p.exited {
			_ = p.cmd.Process.Kill()
		}
	})
	return p
}

// waitExit waits for the launcher and returns its exit status.
func (p *launcherProc) waitExit(t *testing.T) int {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case err := <-done:
		p.exited = true
		if err == nil {
			return 0
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("wait launcher: %v", err)
		}
		return exitErr.ExitCode()
	case <-time.After(20 * time.Second):
		_ = p.cmd.Process.Kill()
		t.Fatal("the launcher did not exit")
		return -1
	}
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
	// The trap goes in before the pid is published: the launcher is signalled the
	// moment the pid is visible, and a TERM delivered before the trap would kill
	// the shell with 143 instead of being recorded and answered with 130. The pid
	// lands by rename so the reader never sees a torn write.
	bin := writeHelper(t, dir, `#!/usr/bin/env bash
trap 'printf stopped > "$FAKE_STOPPED_FILE"; exit 130' TERM
printf '%s\n' "$$" > "$FAKE_PID_FILE.tmp"
mv -- "$FAKE_PID_FILE.tmp" "$FAKE_PID_FILE"
while :; do sleep 0.05; done
`)

	p := startLauncher(t, []string{
		"EVENER_DEV_BIN=" + bin,
		"FAKE_PID_FILE=" + pidFile,
		"FAKE_STOPPED_FILE=" + stopped,
	}, "--pr", "42")
	helperPID := waitForHelperPID(t, pidFile)
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal launcher: %v", err)
	}

	if status := p.waitExit(t); status != 130 {
		t.Fatalf("launcher exit = %d, want 130", status)
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
trap 'printf x >> "$FAKE_TERMS_FILE"' TERM
printf '%s\n' "$$" > "$FAKE_PID_FILE.tmp"
mv -- "$FAKE_PID_FILE.tmp" "$FAKE_PID_FILE"
while :; do sleep 0.05; done
`)

	p := startLauncher(t, []string{
		"EVENER_DEV_BIN=" + bin,
		"FAKE_PID_FILE=" + pidFile,
		"FAKE_TERMS_FILE=" + terms,
	}, "--pr", "42")
	helperPID := waitForHelperPID(t, pidFile)
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal launcher: %v", err)
	}
	// The second signal only counts once the helper has seen the first, which
	// the helper records itself: a fixed sleep would be a guess about scheduling.
	waitForFile(t, terms)
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("second signal launcher: %v", err)
	}

	if status := p.waitExit(t); status != 130 {
		t.Fatalf("launcher exit = %d, want 130: an interrupted run reports the interruption", status)
	}
	waitForProcessGone(t, helperPID)
}

// TestMergeEnqueueForcesThePublishedGitHubGroupOnASecondSignal covers the other
// half of a forced stop. A `gh` call runs in a process group of its own, so
// killing the helper's group does not reach it; the helper publishes that group
// while the call is in flight, and the second signal must kill it too, or a
// forced stop leaves a submit running behind the operator's back.
func TestMergeEnqueueForcesThePublishedGitHubGroupOnASecondSignal(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	terms := filepath.Join(dir, "terms")
	// TMPDIR is named here so the published file's location is known: the
	// launcher removes the directory it lives in when it exits.
	scratchRoot := t.TempDir()
	// The stand-in `gh` call leads its own group, as procgroup's helper does, and
	// its group id is published the way internal/devtool/mergeq publishes it.
	bin := writeHelper(t, dir, `#!/usr/bin/env bash
trap 'printf x >> "$FAKE_TERMS_FILE"' TERM
set -m
sleep 300 &
printf '%s\n' "$!" > "$EVENER_MERGEQ_GH_GROUP_FILE.tmp"
mv -- "$EVENER_MERGEQ_GH_GROUP_FILE.tmp" "$EVENER_MERGEQ_GH_GROUP_FILE"
set +m
printf '%s\n' "$$" > "$FAKE_PID_FILE.tmp"
mv -- "$FAKE_PID_FILE.tmp" "$FAKE_PID_FILE"
while :; do sleep 0.05; done
`)

	p := startLauncher(t, []string{
		"EVENER_DEV_BIN=" + bin,
		"FAKE_PID_FILE=" + pidFile,
		"FAKE_TERMS_FILE=" + terms,
		"TMPDIR=" + scratchRoot,
	}, "--pr", "42")
	helperPID := waitForHelperPID(t, pidFile)
	ghPID := readPublishedGHGroup(t, scratchRoot)
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal launcher: %v", err)
	}
	waitForFile(t, terms)
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("second signal launcher: %v", err)
	}

	if status := p.waitExit(t); status != 130 {
		t.Fatalf("launcher exit = %d, want 130: an interrupted run reports the interruption", status)
	}
	waitForProcessGone(t, helperPID)
	waitForProcessGone(t, ghPID)
	assertDirEmpty(t, scratchRoot)
}

// readPublishedGHGroup returns the group id the helper published. The file lives
// in the launcher's scratch, which is removed when it exits, so it is read while
// the helper still runs.
func readPublishedGHGroup(t *testing.T, scratchRoot string) int {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		// scratch_dir mints "$TMPDIR/merge-enqueue.XXXXXX" (scripts/lib/scratch-lib.sh).
		matches, err := filepath.Glob(filepath.Join(scratchRoot, "merge-enqueue.*", "gh-group"))
		if err == nil {
			for _, match := range matches {
				data, readErr := os.ReadFile(match)
				if readErr != nil {
					continue
				}
				pid, convErr := strconv.Atoi(strings.TrimSpace(string(data)))
				if convErr == nil && pid > 1 {
					return pid
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the helper never published a gh group under %s", scratchRoot)
		}
		time.Sleep(20 * time.Millisecond)
	}
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

// waitForNonEmptyDir waits until the build has written to its cache, which is
// what says the `go build` this case interrupts had actually started.
func waitForNonEmptyDir(t *testing.T, dir string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		entries, err := os.ReadDir(dir)
		if err == nil && len(entries) > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("nothing was written to %s; the build never started", dir)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func assertDirEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	if len(entries) == 0 {
		return
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	t.Errorf("the run left these behind in %s: %v", dir, names)
}

// goToolPath is a PATH that supplies the go toolchain and nothing else: a PATH
// that could also supply an `evener-dev` would skip the build these cases are
// here to exercise.
func goToolPath(t *testing.T) string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("find go: %v", err)
	}
	return filepath.Dir(goBin) + ":/usr/bin:/bin"
}

// TestMergeEnqueueRemovesTheScratchItBuildsIn takes the build path: no
// EVENER_DEV_BIN, and a PATH with no `evener-dev` this checkout built (a binary
// outside the checkout is refused by the launcher's provenance check). The
// helper is invoked without --pr, so it refuses with its usage text and exit 2
// before any GitHub call, and the only thing this case measures is that the
// directory the build ran in is gone afterwards.
func TestMergeEnqueueRemovesTheScratchItBuildsIn(t *testing.T) {
	tmp := t.TempDir()
	_, stderr, status := runMergeEnqueue(t,
		[]string{
			"EVENER_DEV_BIN=",
			"TMPDIR=" + tmp,
			"PATH=" + goToolPath(t),
		})

	if status != 2 {
		t.Fatalf("exit = %d, want 2 (stderr: %s)", status, stderr)
	}
	if want := "usage: evener-dev dev merge-enqueue"; !strings.Contains(stderr, want) {
		t.Fatalf("stderr = %q, want the built helper's usage naming %q", stderr, want)
	}
	assertDirEmpty(t, tmp)
}

// TestMergeEnqueueBuildsAgainstThisCheckoutsWorkspace passes a foreign GOWORK,
// the way a caller's shell or a `go env -w GOWORK=...` would. The build must
// still resolve ./cmd/evener-dev/bin in this checkout's own workspace instead of
// failing because the package is not in the one it was handed.
func TestMergeEnqueueBuildsAgainstThisCheckoutsWorkspace(t *testing.T) {
	foreign := t.TempDir()
	if err := os.WriteFile(filepath.Join(foreign, "go.mod"),
		[]byte("module example.invalid/foreign\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatalf("write foreign go.mod: %v", err)
	}
	gowork := filepath.Join(foreign, "go.work")
	if err := os.WriteFile(gowork, []byte("go 1.21\n\nuse .\n"), 0o644); err != nil {
		t.Fatalf("write foreign go.work: %v", err)
	}
	tmp := t.TempDir()
	_, stderr, status := runMergeEnqueue(t,
		[]string{
			"EVENER_DEV_BIN=",
			"TMPDIR=" + tmp,
			"GOWORK=" + gowork,
			"PATH=" + goToolPath(t),
		})

	if strings.Contains(stderr, "building the evener-dev helper failed") {
		t.Fatalf("the build ran against the foreign GOWORK %s and failed: %s", gowork, stderr)
	}
	if want := "usage: evener-dev dev merge-enqueue"; status != 2 || !strings.Contains(stderr, want) {
		t.Fatalf("exit = %d, stderr = %q, want 2 with the built helper's usage naming %q",
			status, stderr, want)
	}
	assertDirEmpty(t, tmp)
}

// TestMergeEnqueueStopsTheBuildOnTermination interrupts the run while the helper
// is still being built. The build is the tracked child there too, so it is
// stopped and reaped, the scratch is removed, and the run ends 130 without ever
// starting a helper.
func TestMergeEnqueueStopsTheBuildOnTermination(t *testing.T) {
	tmp := t.TempDir()
	// An empty build cache makes the build take seconds, so the signal reliably
	// arrives while it runs, and the cache then testifies that it did run.
	cache := t.TempDir()
	p := startLauncher(t, []string{
		"EVENER_DEV_BIN=",
		"TMPDIR=" + tmp,
		"GOCACHE=" + cache,
		"PATH=" + goToolPath(t),
	})
	waitForNonEmptyDir(t, cache)
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal launcher: %v", err)
	}

	if status := p.waitExit(t); status != 130 {
		t.Fatalf("exit = %d, want 130 (stderr: %q)", status, p.stderr.String())
	}
	if out := p.stdout.String(); out != "" {
		t.Errorf("stdout = %q, want empty: no helper may run", out)
	}
	if errOut := p.stderr.String(); strings.Contains(errOut, "usage: evener-dev") {
		t.Errorf("stderr = %q, want no helper usage: a helper ran", errOut)
	}
	assertDirEmpty(t, tmp)
}

// TestMergeEnqueueFallsBackToAWritableScratchRoot hands the launcher a TMPDIR and
// an XDG_RUNTIME_DIR that do not exist. The build path would report that as exit
// 1, which the contract reads as a dropped PR, so the launcher must pick the next
// writable directory instead and carry on.
func TestMergeEnqueueFallsBackToAWritableScratchRoot(t *testing.T) {
	dir := t.TempDir()
	seen := filepath.Join(dir, "tmpdir")
	bin := writeHelper(t, dir, `#!/usr/bin/env bash
printf '%s\n' "$TMPDIR" > "$FAKE_SEEN"
`)
	cache := t.TempDir()
	_, stderr, status := runMergeEnqueue(t,
		[]string{
			"EVENER_DEV_BIN=" + bin,
			"FAKE_SEEN=" + seen,
			"TMPDIR=" + filepath.Join(dir, "absent-tmp"),
			"XDG_RUNTIME_DIR=" + filepath.Join(dir, "absent-runtime"),
			"XDG_CACHE_HOME=" + cache,
			"HOME=" + filepath.Join(dir, "absent-home"),
		},
		"--pr", "42")

	if status != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", status, stderr)
	}
	data, err := os.ReadFile(seen)
	if err != nil {
		t.Fatalf("read the helper's TMPDIR: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != cache {
		t.Errorf("the helper ran with TMPDIR = %q, want the first writable candidate %q", got, cache)
	}
	assertDirEmpty(t, cache)
}
