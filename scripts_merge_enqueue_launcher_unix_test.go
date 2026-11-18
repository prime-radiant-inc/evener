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

	"primeradiant.com/evener/internal/testwait"
)

// mergeEnqueueLauncher is the script under test. These cases drive it as an
// operator does -- a real process, a real SIGTERM at its PID, its real exit
// status -- because the defect they pin is that a signal sent to the launcher
// never reached the helper: the launcher died on the shell's default TERM
// action instead of forwarding it, and the helper kept polling.
//
// The helper is supplied through EVENER_DEV_BIN, the script's own documented
// seam for "run this binary instead of building one". Nothing is put on PATH:
// the fixture stands in for the helper binary the launcher would have built, and
// what these cases pin is the launcher's signal ordering, not the helper's
// decisions (internal/devtool/mergeq pins those, and cmd/evener-dev/mergeq.go
// pins that the helper's own NotifyContext turns a TERM into 130).
const mergeEnqueueLauncher = "scripts/ops/merge-enqueue.sh"

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
// Its output goes to files, not pipes. A pipe is the thing the defect leaves
// open: an orphaned helper inherits the launcher's stdout, so waiting on a pipe
// blocks until the leaked helper exits and the case that should fail on exit 143
// hangs instead of reporting. Files hold the same bytes and let Wait return at
// the launcher's own exit.
func (f launcherFixture) launcherCmd(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("bash", append([]string{mergeEnqueueLauncher}, args...)...)
	cmd.Env = envOverride(os.Environ(),
		"EVENER_DEV_BIN="+f.path,
		"EVENER_FIXTURE_LOG="+f.log,
		"PATH="+os.Getenv("PATH"),
	)
	return cmd
}

// runLauncher starts cmd with its output already redirected.
func runLauncher(t *testing.T, cmd *exec.Cmd, f launcherFixture) {
	t.Helper()
	out, err := os.Create(f.stdout)
	if err != nil {
		t.Fatalf("create launcher stdout: %v", err)
	}
	defer func() { _ = out.Close() }()
	errOut, err := os.Create(f.stderr)
	if err != nil {
		t.Fatalf("create launcher stderr: %v", err)
	}
	defer func() { _ = errOut.Close() }()
	cmd.Stdout, cmd.Stderr = out, errOut
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", mergeEnqueueLauncher, err)
	}
}

// exitStatus waits for cmd and returns its exit code.
func exitStatus(t *testing.T, cmd *exec.Cmd) int {
	t.Helper()
	err := cmd.Wait()
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("wait for the launcher: %v", err)
	}
	return exitErr.ExitCode()
}

// signalLauncher sends TERM to the launcher's own PID -- the signal the script
// documents for cancelling a background run.
func signalLauncher(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	if err := syscall.Kill(cmd.Process.Pid, syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM the launcher: %v", err)
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

// containsLine reports whether lines holds want.
func containsLine(lines []string, want string) bool {
	return slices.Contains(lines, want)
}

// TestMergeEnqueueLauncherForwardsSigtermToAPrebuiltHelper is the medium the
// review at a20021a01b found: with EVENER_DEV_BIN set, the launcher started and
// waited for the helper before installing its traps, so a SIGTERM at the
// launcher PID killed it with the shell's default action (143) and left the
// helper polling until the wait expired, holding the caller's output open for
// the rest of the wait.
//
// Both assertions fail on that ordering: the launcher exited 143 rather than
// 130, and nothing signalled the helper.
func TestMergeEnqueueLauncherForwardsSigtermToAPrebuiltHelper(t *testing.T) {
	t.Parallel()
	f := newLauncherFixture(t, `#!/usr/bin/env bash
set -uo pipefail
printf 'helper-pid=%s\n' "$$" >> "$EVENER_FIXTURE_LOG"
trap 'printf "helper-termed\n" >> "$EVENER_FIXTURE_LOG"; exit 130' TERM
while :; do sleep 0.05; done
`)
	cmd := f.launcherCmd(t, "--pr", "6499", "--wait", "570")
	runLauncher(t, cmd, f)
	helperPID := logPID(t, f, "helper-pid=")
	signalLauncher(t, cmd)
	if code := exitStatus(t, cmd); code != 130 {
		t.Fatalf("launcher exit %d, want 130 (interrupted); %s", code, f.output(t))
	}
	// The helper ran its own TERM handler: the launcher forwarded the signal
	// rather than dying on the shell's default action.
	if got := f.lines(t); !containsLine(got, "helper-termed") {
		t.Fatalf("helper log %v, want it to record the forwarded TERM", got)
	}
	// And it is gone by the time the launcher exits: a forwarded signal that
	// left the poller running would still hold the caller's output open.
	testwait.Until(t, fmt.Sprintf("helper pid %d to terminate", helperPID), func() bool {
		return pidGone(helperPID)
	})
}

// TestMergeEnqueueLauncherSignalsTheHelperBeforeItsProcessTree is the other
// medium: the helper shares the launcher's process group, so stopping the tree
// TERMs the helper's own `gh` before the helper. The helper then sees a failed
// removal-event read while its context is still live, and reports a drop
// verdict and re-enqueue advice for a wait the operator cancelled.
//
// The fixture makes that ordering observable: it forks a long-lived child (the
// stand-in for `gh`) that records a TERM, and stops that child itself when it
// gets its own TERM. The fix signals the helper's PID alone, so the child never
// sees a TERM from the launcher -- the helper's own cancellation is what stops
// it. The tree stopper signals descendants first, so the old path records one.
func TestMergeEnqueueLauncherSignalsTheHelperBeforeItsProcessTree(t *testing.T) {
	t.Parallel()
	child := filepath.Join(t.TempDir(), "gh.sh")
	if err := os.WriteFile(child, []byte(`#!/usr/bin/env bash
trap 'printf "child-termed\n" >> "$EVENER_FIXTURE_LOG"; exit 0' TERM
printf 'child-pid=%s\n' "$$" >> "$EVENER_FIXTURE_LOG"
while :; do sleep 0.05; done
`), 0o755); err != nil {
		t.Fatalf("write fixture child: %v", err)
	}
	f := newLauncherFixture(t, `#!/usr/bin/env bash
set -uo pipefail
printf 'helper-pid=%s\n' "$$" >> "$EVENER_FIXTURE_LOG"
"$EVENER_FIXTURE_CHILD" &
gh=$!
trap 'printf "helper-termed\n" >> "$EVENER_FIXTURE_LOG"; kill -KILL "$gh" 2>/dev/null; exit 130' TERM
while :; do sleep 0.05; done
`)
	cmd := f.launcherCmd(t, "--pr", "6499", "--wait", "570")
	cmd.Env = append(cmd.Env, "EVENER_FIXTURE_CHILD="+child)
	runLauncher(t, cmd, f)
	childPID := logPID(t, f, "child-pid=")
	signalLauncher(t, cmd)
	if code := exitStatus(t, cmd); code != 130 {
		t.Fatalf("launcher exit %d, want 130 (interrupted); %s", code, f.output(t))
	}
	events := f.lines(t)
	if !containsLine(events, "helper-termed") {
		t.Fatalf("helper log %v, want the helper to receive the signal", events)
	}
	// The claim: the launcher did not TERM the helper's child behind its back.
	if containsLine(events, "child-termed") {
		t.Fatalf("helper log %v: the launcher's tree stopper TERMed the helper's own child, which is what lets a cancelled wait report a drop", events)
	}
	testwait.Until(t, fmt.Sprintf("helper child pid %d to terminate", childPID), func() bool {
		return pidGone(childPID)
	})
}

// TestMergeEnqueueLauncherResolvesItsRootUnderCDPATH is the low the review at
// a20021a01b found: `cd` prints the directory it moved to when a CDPATH entry
// matches, so the root captured two lines. Everything that path is used for
// then failed -- the scratch library would not load from a path with a newline
// in it, and `cd "$root"` before the helper fails -- after the exit trap exists
// in the first case and before it in the second.
//
// CDPATH is the checkout itself, which is what makes the relative
// `scripts/ops/...` invocation match. The claim is that the run still works: the
// root resolves to one path, the helper starts from it, and the launcher exits
// 0. On the old form the root was two lines, `cd` failed, and the launcher
// returned 2 without ever running the helper.
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
	if code := exitStatus(t, cmd); code != 0 {
		t.Fatalf("launcher exit %d under CDPATH, want 0; %s", code, f.output(t))
	}
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
