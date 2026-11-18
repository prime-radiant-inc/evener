//go:build unix

package evener_test

import (
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

// launcherExitDeadline bounds how long a case waits for the launcher to exit.
// It is generous against a loaded host and still far below the package's
// ceiling: the paths under test return in well under a second.
const launcherExitDeadline = 30 * time.Second

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
	)
	return cmd
}

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
// KILLs the launcher's process group and every fixture pid named so far.
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
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", mergeEnqueueLauncher, err)
	}
	launcherPID := cmd.Process.Pid
	t.Cleanup(func() {
		pids := []int{launcherPID}
		for _, p := range fixturePIDs {
			pids = append(pids, *p)
		}
		killGroups(pids...)
	})
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

// TestMergeEnqueueLauncherInterruptsAColdBuildInItsOwnGroup is the medium from
// the review at 4adeb894e2: the launcher tracked the background subshell while
// `setsid` ran *inside* it, so `$!` named a wrapper whose group did not hold the
// build. `stop_command` then walked PIDs from the wrapper, and a compiler
// spawned after that walk's snapshot was reparented when `go build` exited,
// keeping the caller's output open after the launcher returned 130.
//
// This drives a real cold build -- a fresh GOCACHE makes it slow enough to
// catch -- and asserts the property the fix is about: the tracked child leads
// the process group that holds the build, so one signal reaches the whole tree.
// It stops after the child appears, so it costs a second, not a build.
func TestMergeEnqueueLauncherInterruptsAColdBuildInItsOwnGroup(t *testing.T) {
	t.Parallel()
	f := newLauncherFixture(t, "#!/usr/bin/env bash\nexit 0\n")
	cache := filepath.Join(t.TempDir(), "gocache")
	cmd := f.launcherCmd(t, "--pr", "6499")
	// No EVENER_DEV_BIN: this case has to reach the build path.
	cmd.Env = envOverride(cmd.Env, "GOCACHE="+cache)
	cmd.Env = slices.DeleteFunc(cmd.Env, func(kv string) bool {
		return strings.HasPrefix(kv, "EVENER_DEV_BIN=")
	})
	runLauncher(t, cmd, f)
	launcherPID := cmd.Process.Pid
	var buildPID, buildGroup int
	testwait.UntilSeen(t, "the launcher to start its build", func() (bool, string) {
		buildPID, buildGroup = childPIDAndGroup(launcherPID)
		if buildPID == 0 {
			return false, "no child yet"
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
}

// childPIDAndGroup returns the pid and process group of launcherPID's first
// child, or zeros when it has none yet.
func childPIDAndGroup(launcherPID int) (pid, group int) {
	out, err := exec.Command("ps", "--ppid", strconv.Itoa(launcherPID), "-o", "pid=,pgid=").Output()
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		parsedPID, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		parsedGroup, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		return parsedPID, parsedGroup
	}
	return 0, 0
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
	cmd := f.launcherCmd(t, "--pr", "6499", "--wait", "570")
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
	cmd := f.launcherCmd(t, "--pr", "6499", "--wait", "570")
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
	cmd := f.launcherCmd(t, "--pr", "6499")
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
