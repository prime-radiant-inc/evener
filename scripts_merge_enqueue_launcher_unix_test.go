//go:build unix

package evener_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/internal/testwait"
)

// mergeEnqueueLauncher is the script under test. These cases drive it as an
// operator does -- a real process, a real signal at its PID or at its process
// group, its real exit status -- because the defects they pin are about process
// lifetime and cancellation, which no in-process seam can show.
//
// The helper is supplied through EVENER_DEV_BIN, the script's own documented
// seam for "run this binary instead of building one". Nothing is put on PATH:
// the fixture stands in for the helper binary the launcher would have built, and
// what these cases pin is the launcher's signal ordering, not the helper's
// decisions (internal/devtool/mergeq pins those, and cmd/evener-dev/mergeq.go
// pins that the helper's own NotifyContext turns a TERM into 130).
//
// Two rules hold in every case here, because a test for a leak must not leak:
//
//   - The launcher starts in its own process group, and t.Cleanup KILLs that
//     group plus every fixture pid the case learned. A failing assertion
//     therefore cannot leave a poller behind on the machine.
//   - Every wait for the launcher is bounded. An unbounded Wait on a
//     launcher that ignored the signal would stall the package until the global
//     timeout, which reads as an unrelated hang instead of the regression.
const mergeEnqueueLauncher = "scripts/ops/merge-enqueue.sh"

// TestMergeEnqueueLauncherLeavesNothingRunningAfterCancellation is the leak the
// review's own reproduction found: three real `evener-dev dev merge-enqueue
// --pr 6499 --wait 570` processes, reparented to PID 1 and still polling more
// than an hour after the tests that started them had finished. Each had a real
// `gh` child. PR 6499 happened to be merged, so the re-enqueue was a no-op; an
// unmerged PR would have been put in the merge queue by a test run.
//
// The property is stated as the operator sees it: after the case ends, no
// process from it survives. Two oracles, both structural rather than textual:
//
//   - the helper the launcher ran is the fixture, proven by the fixture's own
//     marker in its argv. A real helper names `merge-enqueue --repo
//     prime-radiant-inc/evener` and writes no such marker, so this could not
//     pass with the real one.
//   - cleanup leaves nothing behind, checked by scanning for the case's own
//     marker after the group kills rather than by observing that the direct
//     child exited. A `kill` of the direct child is what left the review's
//     orphans alive: the helper runs in a group of its own, so
//     `cmd.Process.Kill()` cannot reach it. The fixture's grandchild stands in
//     for that shape -- it ignores TERM, so only a group signal stops it.
func TestMergeEnqueueLauncherLeavesNothingRunningAfterCancellation(t *testing.T) {
	t.Parallel()
	f := newLauncherFixture(t, `#!/usr/bin/env bash
set -uo pipefail
trap 'printf "helper-termed\n" >> "$EVENER_FIXTURE_LOG"; exit 130' TERM
"$EVENER_FIXTURE_GRANDCHILD" &
grandchild=$!
printf 'helper-pid=%s;pgid=%s\n' "$$" "$(ps -o pgid= -p "$$" | tr -d ' ')" >> "$EVENER_FIXTURE_LOG"
printf 'grandchild-pid=%s;pgid=%s\n' "$grandchild" "$(ps -o pgid= -p "$grandchild" | tr -d ' ')" >> "$EVENER_FIXTURE_LOG"
while :; do sleep 0.05; done
`)
	// A grandchild the fixture does not signal itself: it survives a TERM to
	// the fixture's PID alone, which is exactly the shape that outlived the
	// review's tests.
	grandchild := filepath.Join(f.dir, "grandchild.sh")
	if err := os.WriteFile(grandchild, []byte(`#!/usr/bin/env bash
trap '' TERM
while :; do sleep 0.05; done
`), 0o755); err != nil {
		t.Fatalf("write fixture grandchild: %v", err)
	}
	var helperPID int
	var grandchildPID int
	cmd := f.launcherCmd(t, "--pr", "6499", "--wait", "570", "--repo", "example.invalid/none")
	cmd.Env = append(cmd.Env, "EVENER_FIXTURE_GRANDCHILD="+grandchild)
	runLauncher(t, cmd, f, &helperPID, &grandchildPID)
	helperPID, helperGroup := logPIDAndGroup(t, f, "helper-pid=")
	_, grandchildGroup := logPIDAndGroup(t, f, "grandchild-pid=")
	if grandchildGroup != helperGroup {
		t.Fatalf("the fixture grandchild is in group %d, want the helper's group %d: this case needs it reachable only by a group signal", grandchildGroup, helperGroup)
	}
	// The helper is the fixture, not the real one. EVENER_DEV_BIN is the only
	// way the launcher can have started this pid, and it is a path inside this
	// case's fixture directory.
	helperCmd := commandLineOf(t, helperPID)
	if !strings.Contains(helperCmd, f.path) {
		t.Fatalf("the launcher ran %q, want the fixture helper at %s: a case that starts the real helper can enqueue a real pull request", helperCmd, f.path)
	}
	signalLauncher(t, cmd, syscall.SIGTERM)
	mustExit(t, cmd, f, 130)
	// The helper stopped. That is what the launcher owes: it forwards the
	// cancellation and reaps the helper before reclaiming scratch.
	testwait.Until(t, fmt.Sprintf("helper pid %d to terminate", helperPID), func() bool {
		return pidGone(helperPID)
	})
	// The grandchild is deliberately not asserted here. The launcher does not
	// own a helper's descendants -- the real helper's cancelled context is what
	// stops its `gh` -- so requiring this one to die would be asserting the
	// fixture, not the launcher. What must hold is that nothing from the case
	// outlives the case, which is what cleanup's sweep proves: it runs after
	// the group kills and fails naming every survivor.
}

// commandLineOf returns the full argv of pid, or "" when it is gone.
func commandLineOf(t *testing.T, pid int) string {
	t.Helper()
	for _, proc := range scanProcs(t) {
		if proc.pid == pid {
			return proc.cmd
		}
	}
	return ""
}

// launcherExitDeadline bounds how long a case waits for the launcher to exit.
// It is generous against a loaded host and still far below the package's
// ceiling: the paths under test return in well under a second.
const launcherExitDeadline = 30 * time.Second

// fixtureCheckout is a stand-in checkout the launcher can be pointed at: a copy
// of the script and the libraries it sources, and a Go module whose only
// package under ./cmd/evener-dev/bin writes a sentinel and exits.
//
// The point is that the real helper cannot be built here, let alone started:
// this module has no `dev` subcommand, no merge-queue client and no code that
// could run `gh`. A launcher that reached the build path against *this*
// checkout and then ran the result would run the stub, which writes a file and
// exits.
type fixtureCheckout struct {
	root     string
	script   string
	tmp      string
	sentinel string
}

// newFixtureCheckout copies the launcher and its libraries into a fresh
// directory and writes the fixture module.
//
// tmp is the checkout's TMPDIR: the launcher mints its scratch under it, so the
// scratch path appears in the build's argv and the built helper runs from it.
//
// The build is held open by a FIFO source file rather than by a cold cache. A
// cold build is a race -- how long it takes depends on the host and on what the
// developer's cache already holds -- and the old case lost that race in the one
// direction that matters. A FIFO has no writer, so the go command blocks opening
// it and the build stays in flight until the case signals.
func newFixtureCheckout(t *testing.T, tmp string) fixtureCheckout {
	t.Helper()
	root := filepath.Join(t.TempDir(), "checkout")
	c := fixtureCheckout{
		root:     root,
		script:   filepath.Join(root, "scripts", "ops", "merge-enqueue.sh"),
		tmp:      tmp,
		sentinel: filepath.Join(root, "helper-ran"),
	}
	for _, copy := range []struct{ src, dst string }{
		{mergeEnqueueLauncher, filepath.Join("scripts", "ops", "merge-enqueue.sh")},
		{filepath.Join("scripts", "lib", "scratch-lib.sh"), filepath.Join("scripts", "lib", "scratch-lib.sh")},
		{filepath.Join("scripts", "lib", "gate-scratch-root.sh"), filepath.Join("scripts", "lib", "gate-scratch-root.sh")},
		{filepath.Join("scripts", "lib", "gate-bounded.sh"), filepath.Join("scripts", "lib", "gate-bounded.sh")},
	} {
		body, err := os.ReadFile(copy.src)
		if err != nil {
			t.Fatalf("read %s: %v", copy.src, err)
		}
		path := filepath.Join(root, copy.dst)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", path, err)
		}
		if err := os.WriteFile(path, body, 0o755); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "cmd", "evener-dev", "bin"), 0o755); err != nil {
		t.Fatalf("mkdir fixture package: %v", err)
	}
	// go.mod's directive stays below the toolchain's own version: a newer one
	// makes the go command try to download a toolchain, which would need the
	// network this suite must not touch.
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.invalid/fixture\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatalf("write fixture go.mod: %v", err)
	}
	main := "package main\n\nimport \"os\"\n\nfunc main() { _ = os.WriteFile(" + strconv.Quote(c.sentinel) + ", []byte(\"ran\"), 0o644) }\n"
	if err := os.WriteFile(filepath.Join(root, "cmd", "evener-dev", "bin", "main.go"), []byte(main), 0o644); err != nil {
		t.Fatalf("write fixture main: %v", err)
	}
	// A FIFO named *.go: the build has to read every source in the package, so
	// it blocks in open until a writer appears. Nothing ever opens it.
	if err := syscall.Mkfifo(filepath.Join(root, "cmd", "evener-dev", "bin", "hold.go"), 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		t.Fatalf("mkdir fixture TMPDIR: %v", err)
	}
	return c
}

// requireIsolated states this checkout's two isolation facts, so a case that
// drifts back to the repository's own checkout fails at once with the reason
// instead of timing out looking for a build.
//
//   - The script under test is the fixture copy. It is byte-identical to the
//     repository's script, so what runs is the real launcher; only the checkout
//     around it is a stand-in.
//   - The only package ./cmd/evener-dev/bin can find here is the fixture's
//     stub. The repository's helper lives in the repository's module, and this
//     module's go.mod is the only one on the path, so `go build
//     ./cmd/evener-dev/bin` from here cannot produce it.
func (c fixtureCheckout) requireIsolated(t *testing.T) {
	t.Helper()
	rootPrefix := c.root + string(filepath.Separator)
	if !strings.HasPrefix(c.script, rootPrefix) {
		t.Fatalf("the script under test is %s, want a copy under %s: a case that runs the repository's launcher builds the real helper, and the real helper can enqueue a real pull request", c.script, c.root)
	}
	repo, err := os.ReadFile(mergeEnqueueLauncher)
	if err != nil {
		t.Fatalf("read %s: %v", mergeEnqueueLauncher, err)
	}
	copied, err := os.ReadFile(c.script)
	if err != nil {
		t.Fatalf("read %s: %v", c.script, err)
	}
	if !bytes.Equal(repo, copied) {
		t.Fatalf("%s differs from %s: this case would be testing a script nobody ships", c.script, mergeEnqueueLauncher)
	}
	entries, err := os.ReadDir(filepath.Join(c.root, "cmd", "evener-dev", "bin"))
	if err != nil {
		t.Fatalf("read the fixture package: %v", err)
	}
	for _, entry := range entries {
		switch entry.Name() {
		case "main.go", "hold.go":
		default:
			t.Fatalf("the fixture package holds %s: the fixture must contain only its stub, or the build could produce something other than it", entry.Name())
		}
	}
}

// launcherFixture is a stand-in helper: a script the launcher starts through
// EVENER_DEV_BIN and then signals.
type launcherFixture struct {
	dir    string
	path   string
	log    string
	stdout string
	stderr string
}

// newLauncherFixture writes script as the fake helper and returns its path, the
// file its handlers append to, and the files its output goes to.
func newLauncherFixture(t *testing.T, script string) launcherFixture {
	t.Helper()
	dir := t.TempDir()
	f := launcherFixture{
		dir:    dir,
		path:   filepath.Join(dir, "evener-dev"),
		log:    filepath.Join(dir, "log"),
		stdout: filepath.Join(dir, "stdout"),
		stderr: filepath.Join(dir, "stderr"),
	}
	if err := os.WriteFile(f.path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fixture helper: %v", err)
	}
	return f
}

// lines returns the fixture's handler log, one token per line.
func (f launcherFixture) lines(t *testing.T) []string {
	t.Helper()
	body, err := os.ReadFile(f.log)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		t.Fatalf("read fixture log: %v", err)
	}
	return strings.Fields(string(body))
}

// output returns what the launcher wrote, for a failure message.
func (f launcherFixture) output(t *testing.T) string {
	t.Helper()
	var parts []string
	for _, path := range []string{f.stdout, f.stderr} {
		body, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if trimmed := strings.TrimSpace(string(body)); trimmed != "" {
			parts = append(parts, filepath.Base(path)+": "+trimmed)
		}
	}
	return strings.Join(parts, " | ")
}

// launcherCmd builds the launcher command with the fixture as its helper.
//
// Its output goes to files, not pipes. A pipe is part of what the defect leaves
// open: an orphaned helper inherits the launcher's stdout, so waiting on a pipe
// blocks until the leaked helper exits and the case that should fail on exit 143
// hangs instead of reporting.
//
// The launcher leads its own process group so a case can deliver Ctrl-C's
// signal the way a tty does -- to the group -- without signalling the test
// binary itself, and so cleanup can KILL the group and everything left in it.
func (f launcherFixture) launcherCmd(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("bash", append([]string{mergeEnqueueLauncher}, args...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = envOverride(os.Environ(),
		"EVENER_DEV_BIN="+f.path,
		"EVENER_FIXTURE_LOG="+f.log,
		"PATH="+os.Getenv("PATH"),
		// TMPDIR is the case's own directory, so a helper the launcher builds
		// and runs carries this case's marker in its argv. That is what lets
		// cleanup find a helper whose PID the case never learned -- including
		// the real one, were a case ever to reach it.
		"TMPDIR="+f.tmpDir(),
	)
	return cmd
}

// tmpDir is the TMPDIR this fixture's launcher runs under: inside the case's own
// directory, so the scratch path the launcher mints names the case.
func (f launcherFixture) tmpDir() string { return filepath.Join(f.dir, "tmp") }

// killGroups KILLs each pid's process group, ignoring ones already gone. It is
// the cleanup path: it must never fail the case it is cleaning up after.
func killGroups(pids ...int) {
	for _, pid := range pids {
		if pid <= 1 {
			continue
		}
		if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

// runLauncher starts cmd with its output redirected, and registers cleanup that
// KILLs the launcher's process group, every fixture pid named so far, and every
// process still naming the fixture directory.
//
// That last sweep is what the review's reproduction showed was missing: three
// real helpers from this suite were found orphaned to PID 1 for over an hour,
// each holding a --wait 570 budget the tests believed bounded them. A pid list
// only covers the processes a case managed to learn before its assertion failed;
// a helper that published nothing, or one the launcher started into a group of
// its own, was outside every list. The fixture directory is unique per case and
// appears in the argv of every process a case starts (the fixture helper by
// path, a build or a real helper by the scratch path under it), so it is the one
// handle that does not depend on the case getting that far.
func runLauncher(t *testing.T, cmd *exec.Cmd, f launcherFixture, fixturePIDs ...*int) {
	t.Helper()
	out, err := os.Create(f.stdout)
	if err != nil {
		t.Fatalf("create launcher stdout: %v", err)
	}
	t.Cleanup(func() { _ = out.Close() })
	errOut, err := os.Create(f.stderr)
	if err != nil {
		t.Fatalf("create launcher stderr: %v", err)
	}
	t.Cleanup(func() { _ = errOut.Close() })
	cmd.Stdout, cmd.Stderr = out, errOut
	if err := os.MkdirAll(f.tmpDir(), 0o755); err != nil {
		t.Fatalf("mkdir fixture TMPDIR: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", mergeEnqueueLauncher, err)
	}
	launcherPID := cmd.Process.Pid
	// Cleanups run last-registered-first, so the sweep is registered first and
	// runs after the kills: it reports what the kills missed.
	t.Cleanup(func() { assertNoSurvivors(t, f.dir) })
	t.Cleanup(func() {
		pids := []int{launcherPID}
		for _, p := range fixturePIDs {
			pids = append(pids, *p)
		}
		killGroups(pids...)
	})
}

// launcherCmdAt is launcherCmd for a copy of the script, which is how the
// build-path case runs the launcher without the real checkout's sources in
// reach.
func (f launcherFixture) launcherCmdAt(t *testing.T, script string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("bash", append([]string{script}, args...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = envOverride(os.Environ(),
		"EVENER_DEV_BIN="+f.path,
		"EVENER_FIXTURE_LOG="+f.log,
		"PATH="+os.Getenv("PATH"),
		// TMPDIR is the case's own directory, so a helper the launcher builds
		// and runs carries this case's marker in its argv. That is what lets
		// cleanup find a helper whose PID the case never learned -- including
		// the real one, were a case ever to reach it.
		"TMPDIR="+f.tmpDir(),
	)
	return cmd
}

// scanProcs returns every process on the machine as pid, parent pid, process
// group and command line.
//
// It is `ps -axo`, the form scripts/lib/gate-bounded.sh already uses, because
// `ps --ppid` is a GNU extension that BSD `ps` -- which is what macOS ships, and
// this repo builds Darwin -- rejects with a usage error. The helper that used
// it turned that error into an empty answer, so on a macOS host the case waited
// for a build it could never see and timed out.
func scanProcs(t *testing.T) []procLine {
	t.Helper()
	out, err := exec.Command("ps", "-ww", "-axo", "pid=,ppid=,pgid=,command=").Output()
	if err != nil {
		t.Fatalf("ps -axo: %v", err)
	}
	var procs []procLine
	for line := range strings.SplitSeq(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		ppid, ppidErr := strconv.Atoi(fields[1])
		pgid, pgidErr := strconv.Atoi(fields[2])
		if pidErr != nil || ppidErr != nil || pgidErr != nil {
			continue
		}
		procs = append(procs, procLine{
			pid:  pid,
			ppid: ppid,
			pgid: pgid,
			cmd:  strings.Join(fields[3:], " "),
		})
	}
	return procs
}

// procLine is one row of scanProcs.
type procLine struct {
	pid, ppid, pgid int
	cmd             string
}

// processesNaming returns the processes whose command line contains marker,
// excluding the test binary itself, which is not a process a case started.
//
// The scan's own `ps` child is not excluded and needs no exclusion: it is handed
// no marker, so its argv cannot match.
func processesNaming(t *testing.T, marker string) []procLine {
	t.Helper()
	var found []procLine
	for _, proc := range scanProcs(t) {
		if proc.pid == os.Getpid() || !strings.Contains(proc.cmd, marker) {
			continue
		}
		found = append(found, proc)
	}
	return found
}

// buildIn returns the pid and process group of the process building the fixture
// checkout, or zeros while there is none.
//
// Identification is positive: the launcher builds into scratch under the
// fixture's TMPDIR, so the build's argv names the fixture checkout. "The
// launcher's first child" was the old test, and it identified a scratch
// preflight child as the build on a correct launcher.
func buildIn(t *testing.T, marker string) (pid, group int) {
	t.Helper()
	for _, proc := range processesNaming(t, marker) {
		if strings.Contains(proc.cmd, "go build") {
			return proc.pid, proc.pgid
		}
	}
	return 0, 0
}

// assertNoSurvivors fails when any process still names marker and KILLs it
// first, so a failing case reports a leak without leaving one.
func assertNoSurvivors(t *testing.T, marker string) {
	t.Helper()
	// A zombie is dead and this process cannot reap it: it is another process's
	// exit this one has no business reporting as a leak.
	var survivors []procLine
	for _, proc := range processesNaming(t, marker) {
		if pidGone(proc.pid) {
			continue
		}
		survivors = append(survivors, proc)
	}
	if len(survivors) == 0 {
		return
	}
	var described []string
	for _, proc := range survivors {
		described = append(described, fmt.Sprintf("%d(pgid %d) %s", proc.pid, proc.pgid, proc.cmd))
	}
	for _, proc := range survivors {
		_ = syscall.Kill(-proc.pgid, syscall.SIGKILL)
		_ = syscall.Kill(proc.pid, syscall.SIGKILL)
	}
	t.Fatalf("left behind after cleanup: %s", strings.Join(described, " | "))
}

// waitExit waits for cmd to exit, bounded. It reports the exit code and true,
// or 0 and false when the deadline passed first -- which is a regression, not a
// hang, and the caller says so.
func waitExit(t *testing.T, cmd *exec.Cmd, within time.Duration) (int, bool) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			return 0, true
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("wait for the launcher: %v", err)
		}
		return exitErr.ExitCode(), true
	case <-time.After(within):
		return 0, false
	}
}

// mustExit waits for cmd and fails with the fixture output when it did not exit
// in time or exited with a status other than want.
func mustExit(t *testing.T, cmd *exec.Cmd, f launcherFixture, want int) {
	t.Helper()
	code, exited := waitExit(t, cmd, launcherExitDeadline)
	if !exited {
		t.Fatalf("launcher still running after %s, want exit %d: the signal did not stop it; %s",
			launcherExitDeadline, want, f.output(t))
	}
	if code != want {
		t.Fatalf("launcher exit %d, want %d; %s", code, want, f.output(t))
	}
}

// signalLauncher sends sig to the launcher's own PID -- the signal the script
// documents for cancelling a background run.
func signalLauncher(t *testing.T, cmd *exec.Cmd, sig syscall.Signal) {
	t.Helper()
	if err := syscall.Kill(cmd.Process.Pid, sig); err != nil {
		t.Fatalf("signal the launcher: %v", err)
	}
}

// signalLauncherGroup sends sig to the launcher's process group, which is how a
// terminal delivers Ctrl-C: every process in the foreground group gets it.
func signalLauncherGroup(t *testing.T, cmd *exec.Cmd, sig syscall.Signal) {
	t.Helper()
	if err := syscall.Kill(-cmd.Process.Pid, sig); err != nil {
		t.Fatalf("signal the launcher's process group: %v", err)
	}
}

// pidGone reports whether the pid's process has terminated. A zombie counts:
// the process is dead, and its parent may not have reaped it yet.
func pidGone(pid int) bool {
	state, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return true
	}
	trimmed := strings.TrimSpace(string(state))
	return trimmed == "" || strings.HasPrefix(trimmed, "Z")
}

// logPID reads the pid a fixture wrote under prefix, waiting for it to appear.
func logPID(t *testing.T, f launcherFixture, prefix string) int {
	t.Helper()
	var pid int
	testwait.Until(t, "the fixture to publish "+prefix, func() bool {
		pid = pidFromLines(f.lines(t), prefix)
		return pid != 0
	})
	return pid
}

// waitForLine waits until the fixture has recorded want, and fails naming what
// it saw instead. Failing here rather than proceeding is what stops a case from
// signalling a fixture that has not installed its handler yet.
func waitForLine(t *testing.T, f launcherFixture, want string) {
	t.Helper()
	testwait.UntilSeen(t, "the fixture to record "+want, func() (bool, string) {
		lines := f.lines(t)
		return slices.Contains(lines, want), strings.Join(lines, " ")
	})
}

// pidFromLines returns the pid recorded under prefix, or 0.
func pidFromLines(lines []string, prefix string) int {
	for _, line := range lines {
		rest, ok := strings.CutPrefix(line, prefix)
		if !ok {
			continue
		}
		pid, err := strconv.Atoi(rest)
		if err != nil {
			continue
		}
		return pid
	}
	return 0
}

// logPIDAndGroup reads the "pid=N;pgid=G" line a fixture wrote under prefix,
// waiting for it to appear, and returns both.
func logPIDAndGroup(t *testing.T, f launcherFixture, prefix string) (pid, group int) {
	t.Helper()
	testwait.UntilSeen(t, "the fixture to publish "+prefix, func() (bool, string) {
		lines := f.lines(t)
		for _, line := range lines {
			rest, ok := strings.CutPrefix(line, prefix)
			if !ok {
				continue
			}
			pidText, groupText, ok := strings.Cut(rest, ";pgid=")
			if !ok {
				continue
			}
			parsedPID, err := strconv.Atoi(pidText)
			if err != nil {
				continue
			}
			parsedGroup, err := strconv.Atoi(groupText)
			if err != nil {
				continue
			}
			pid, group = parsedPID, parsedGroup
			return true, ""
		}
		return false, strings.Join(lines, " ")
	})
	return pid, group
}

// TestMergeEnqueueLauncherInterruptsAColdBuildInItsOwnGroup covers two findings
// from the review at 092f60f468.
//
// The build property is the medium from the review at 4adeb894e2: the launcher
// tracked the background subshell while `setsid` ran *inside* it, so `$!` named
// a wrapper whose group did not hold the build, and a compiler forked after
// stop_command's PID snapshot was reparented when `go build` exited. The fix
// runs the build under bash job control, so the tracked child leads the group
// that holds the build's whole tree, and one signal reaches all of it.
//
// The reachability property is the review's next medium: this case used to run
// the *real* launcher from this checkout with no EVENER_DEV_BIN, so it built the
// real helper and -- with an authenticated `gh` on PATH -- the helper could query
// prime-radiant-inc/evener and enqueue PR 6499 from a test run. It did run: the
// review's reproduction found the real `evener-dev dev merge-enqueue --pr 6499
// --wait 570` orphaned to PID 1 for over an hour, each with a fixture `gh.sh`.
//
// So the case runs the script under test from a *fixture checkout*: the script
// and the libraries it sources are copies, and the only package
// ./cmd/evener-dev/bin can build is the fixture's own module. No real helper
// exists here to start, whatever completes the build. The build is held open by
// a FIFO source file instead of a cold cache, so "the build is running" is a
// fact the case can wait for rather than a race it hopes to win.
func TestMergeEnqueueLauncherInterruptsAColdBuildInItsOwnGroup(t *testing.T) {
	t.Parallel()
	c := newFixtureCheckout(t, filepath.Join(t.TempDir(), "tmp"))
	// The marker names the build: the launcher builds into scratch under the
	// fixture's TMPDIR, so the build's argv carries that path and no other
	// process on the machine does. That is positive identification -- the old
	// case asked for "the launcher's first child", which during scratch
	// preflight is whichever probe binary happens to be running.
	marker := c.tmp
	c.requireIsolated(t)
	// The checkout's scratch is outside launcherFixture's directory, so this
	// case sweeps its own markers. Registered before runLauncher, so it runs
	// after the group kills: a build the launcher leaked is reported here
	// rather than left running on the machine.
	for _, m := range []string{c.root, c.tmp} {
		t.Cleanup(func() { assertNoSurvivors(t, m) })
	}
	f := newLauncherFixture(t, "#!/usr/bin/env bash\nexit 0\n")
	cmd := f.launcherCmdAt(t, c.script, "--pr", "6499", "--repo", "example.invalid/none")
	// No EVENER_DEV_BIN: this case has to reach the build path. TMPDIR points at
	// the fixture and GOWORK=off keeps the build inside the fixture module even
	// when the fixture sits under a checkout that has a go.work.
	cmd.Env = envOverride(cmd.Env, "TMPDIR="+c.tmp, "GOWORK=off")
	cmd.Env = slices.DeleteFunc(cmd.Env, func(kv string) bool {
		return strings.HasPrefix(kv, "EVENER_DEV_BIN=")
	})
	runLauncher(t, cmd, f)
	launcherPID := cmd.Process.Pid
	var buildPID, buildGroup int
	testwait.UntilSeen(t, "the launcher's build to appear", func() (bool, string) {
		buildPID, buildGroup = buildIn(t, marker)
		if buildPID == 0 {
			return false, "no process names the fixture scratch yet"
		}
		return true, ""
	})
	// The tracked child leads the group: kill -TERM -$child reaches the build
	// and every compiler it forks, including one forked after this moment.
	if buildGroup != buildPID {
		t.Fatalf("the build child %d is in group %d: stop_command cannot signal the build's tree by group, so a compiler forked after its PID walk escapes", buildPID, buildGroup)
	}
	if buildGroup == launcherPID {
		t.Fatalf("the build shares the launcher's group %d, so a group stop would signal the launcher too", buildGroup)
	}
	signalLauncher(t, cmd, syscall.SIGTERM)
	mustExit(t, cmd, f, 130)
	testwait.Until(t, fmt.Sprintf("build pid %d to terminate", buildPID), func() bool {
		return pidGone(buildPID)
	})
	// The stub never ran: the build was cancelled, so there was nothing to
	// start. This is the sentinel the review's reproduction needed -- proof the
	// helper the launcher would have run is the fixture's own, and that no
	// merge-queue helper exists in this checkout to run at all.
	if _, err := os.Stat(c.sentinel); err == nil {
		t.Fatalf("the fixture helper ran: the build finished after all, so this case did not exercise cancellation")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat fixture sentinel: %v", err)
	}
}

// TestMergeEnqueueLauncherForwardsSigtermToAPrebuiltHelper is the medium from
// the review at a20021a01b: with EVENER_DEV_BIN set, the launcher started and
// waited for the helper before installing its traps, so a SIGTERM at the
// launcher PID killed it with the shell's default action (143) and left the
// helper polling until the wait expired, holding the caller's output open for
// the rest of the wait.
//
// The fixture publishes its pid only after its TERM handler is installed, so a
// correct launcher cannot be failed by signalling a fixture that is still
// setting up.
func TestMergeEnqueueLauncherForwardsSigtermToAPrebuiltHelper(t *testing.T) {
	t.Parallel()
	f := newLauncherFixture(t, `#!/usr/bin/env bash
set -uo pipefail
trap 'printf "helper-termed\n" >> "$EVENER_FIXTURE_LOG"; exit 130' TERM
printf 'helper-pid=%s\n' "$$" >> "$EVENER_FIXTURE_LOG"
while :; do sleep 0.05; done
`)
	var helperPID int
	cmd := f.launcherCmd(t, "--pr", "6499", "--wait", "570", "--repo", "example.invalid/none")
	runLauncher(t, cmd, f, &helperPID)
	helperPID = logPID(t, f, "helper-pid=")
	signalLauncher(t, cmd, syscall.SIGTERM)
	mustExit(t, cmd, f, 130)
	// The helper ran its own TERM handler: the launcher forwarded the signal
	// rather than dying on the shell's default action.
	waitForLine(t, f, "helper-termed")
	// And it is gone by the time the launcher exits: a forwarded signal that
	// left the poller running would still hold the caller's output open.
	testwait.Until(t, fmt.Sprintf("helper pid %d to terminate", helperPID), func() bool {
		return pidGone(helperPID)
	})
}

// TestMergeEnqueueLauncherRunsTheHelperInItsOwnProcessGroup is the third medium
// from the review at 4adeb894e2: the launcher, the helper and the helper's `gh`
// shared one process group, so Ctrl-C's SIGINT reached `gh` before the helper's
// own handler cancelled the context. The helper then read the interrupted call
// as an ordinary GitHub failure and printed a drop verdict with re-enqueue
// advice for a wait the operator cancelled.
//
// The assertion is the process-group structure, not a log line, because a log
// line cannot honestly observe this. A bash fixture forked with `&` from a
// non-interactive shell has SIGINT forced to SIG_IGN before its first command
// runs (SigIgn 6, measured), so `trap ... INT` in the fixture never installs and
// a "did gh receive the SIGINT?" oracle passes whether or not the defect is
// present. What the fix changes, and what this pins, is that the helper leads a
// group of its own: the terminal's foreground group holds the launcher alone, so
// the launcher is the only thing a Ctrl-C touches and the helper's cancelled
// context is what stops `gh`.
//
// It also pins that the helper still ends: the launcher forwards TERM after
// taking the SIGINT, so an operator's Ctrl-C stops the wait.
func TestMergeEnqueueLauncherRunsTheHelperInItsOwnProcessGroup(t *testing.T) {
	t.Parallel()
	gh := filepath.Join(t.TempDir(), "gh.sh")
	if err := os.WriteFile(gh, []byte(`#!/usr/bin/env bash
trap 'printf "gh-termed\n" >> "$EVENER_FIXTURE_LOG"; exit 0' TERM
printf 'gh-pid=%s;pgid=%s\n' "$$" "$(ps -o pgid= -p "$$" | tr -d ' ')" >> "$EVENER_FIXTURE_LOG"
while :; do sleep 0.05; done
`), 0o755); err != nil {
		t.Fatalf("write fixture gh: %v", err)
	}
	f := newLauncherFixture(t, `#!/usr/bin/env bash
set -uo pipefail
trap 'printf "helper-interrupted\n" >> "$EVENER_FIXTURE_LOG"; kill -TERM "$gh" 2>/dev/null; exit 130' INT TERM
"$EVENER_FIXTURE_GH" &
gh=$!
printf 'helper-pid=%s;pgid=%s\n' "$$" "$(ps -o pgid= -p "$$" | tr -d ' ')" >> "$EVENER_FIXTURE_LOG"
while :; do sleep 0.05; done
`)
	var helperPID, ghPID int
	cmd := f.launcherCmd(t, "--pr", "6499", "--wait", "570", "--repo", "example.invalid/none")
	cmd.Env = append(cmd.Env, "EVENER_FIXTURE_GH="+gh)
	runLauncher(t, cmd, f, &helperPID, &ghPID)
	launcherGroup := cmd.Process.Pid
	helperPID, helperGroup := logPIDAndGroup(t, f, "helper-pid=")
	ghPID, ghGroup := logPIDAndGroup(t, f, "gh-pid=")
	if helperGroup == launcherGroup {
		t.Fatalf("the helper runs in the launcher's process group (%d): a terminal Ctrl-C would reach its gh directly, which is what lets an interrupted wait report a drop", launcherGroup)
	}
	// The helper owns its `gh`: one signal from the launcher reaches both, and
	// the helper's cancelled context is what ends the call.
	if ghGroup != helperGroup {
		t.Fatalf("the helper's gh is in group %d, want the helper's own group %d", ghGroup, helperGroup)
	}
	signalLauncherGroup(t, cmd, syscall.SIGINT)
	mustExit(t, cmd, f, 130)
	waitForLine(t, f, "helper-interrupted")
	testwait.Until(t, fmt.Sprintf("fixture gh pid %d to terminate", ghPID), func() bool {
		return pidGone(ghPID)
	})
	testwait.Until(t, fmt.Sprintf("helper pid %d to terminate", helperPID), func() bool {
		return pidGone(helperPID)
	})
}

// TestMergeEnqueueLauncherResolvesItsRootUnderCDPATH is the low from the review
// at a20021a01b: `cd` prints the directory it moved to when a CDPATH entry
// matches, so the root captured two lines. Everything that path is used for then
// failed -- the scratch library would not load from a path with a newline in it,
// and `cd "$root"` before the helper fails.
//
// CDPATH is the checkout itself, which is what makes the relative
// `scripts/ops/...` invocation match. The claim is that the run still works: the
// root resolves to one path, the helper starts from it, and the launcher exits 0.
func TestMergeEnqueueLauncherResolvesItsRootUnderCDPATH(t *testing.T) {
	t.Parallel()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	f := newLauncherFixture(t, `#!/usr/bin/env bash
set -uo pipefail
printf 'helper-ran=%s\n' "$PWD" >> "$EVENER_FIXTURE_LOG"
exit 0
`)
	cmd := f.launcherCmd(t, "--pr", "6499", "--repo", "example.invalid/none")
	cmd.Env = envOverride(cmd.Env, "CDPATH="+wd)
	runLauncher(t, cmd, f)
	mustExit(t, cmd, f, 0)
	// The helper ran from the resolved root, not from a path with a newline in
	// it: start_helper changes directory before launching.
	var ran string
	testwait.Until(t, "the helper to record the directory it ran from", func() bool {
		for _, line := range f.lines(t) {
			if rest, ok := strings.CutPrefix(line, "helper-ran="); ok {
				ran = rest
				return true
			}
		}
		return false
	})
	if ran != wd {
		t.Fatalf("helper ran from %q, want the checkout root %q", ran, wd)
	}
}
