//go:build linux

package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"primeradiant.com/evener/internal/testwait"
)

// bwrapProbeOrphanHelperEnv selects the child side of the adoption regression.
// The parent sets it to the resolved bwrap path; an ordinary package run leaves
// it empty and the helper reports an explicit skip.
const bwrapProbeOrphanHelperEnv = "EVENER_TEST_BWRAP_PROBE_ORPHAN_HELPER"

// bwrapProbeOrphanReportEnv names the file the helper writes its measurements
// to. Exit status alone cannot carry them: "this host cannot create the
// namespaces" must reach the parent as a skip, while "the probe failed where a
// plain namespaced run passed" must reach it as a failure.
const bwrapProbeOrphanReportEnv = "EVENER_TEST_BWRAP_PROBE_ORPHAN_REPORT"

// bwrapProbeRequiredEnv turns a prerequisite skip into a failure. CI sets it in
// the lane that provisions bubblewrap, where this regression is the point of
// the step: a skip there would report success for a measurement never taken.
const bwrapProbeRequiredEnv = "EVENER_TEST_BWRAP_PROBE_REQUIRED"

// bwrapProbeOrphanReport is what the helper measured.
type bwrapProbeOrphanReport struct {
	// Prereq names a host prerequisite the helper could not satisfy, such as a
	// seccomp profile that denies PR_SET_CHILD_SUBREAPER. It is not a failure:
	// the measurement was never started.
	Prereq string `json:"prereq"`
	// MissingFlags are the probe flags this bwrap's --help does not list. A
	// non-empty list means the host's bwrap predates the probe's floor, so
	// there is no probe here to measure.
	MissingFlags []string `json:"missing_flags"`
	// ControlOK is whether bwrap ran a plain namespaced sandbox — its own argv,
	// not the probe's — so capability is answered without the flag under test.
	ControlOK bool `json:"control_ok"`
	// ControlExit is the control's exit status, or -1 when it never ran.
	ControlExit int `json:"control_exit"`
	// ControlStderr is what the control's bwrap wrote to stderr.
	ControlStderr string `json:"control_stderr"`
	// ControlLeft counts the processes the control abandoned. The control does
	// not ask for --as-pid-1, so it should leave a reaper behind: a zero here
	// means this harness cannot see the leak it is looking for.
	ControlLeft int `json:"control_left"`
	// ProbeHasFlag is whether the probe's argv still carries --as-pid-1.
	ProbeHasFlag bool `json:"probe_has_flag"`
	// ProbeOK is whether the probe under test exited 0.
	ProbeOK bool `json:"probe_ok"`
	// ProbeExit is the probe's exit status, or -1 when it never ran.
	ProbeExit int `json:"probe_exit"`
	// ProbeStderr is what the probe's bwrap wrote to stderr.
	ProbeStderr string `json:"probe_stderr"`
	// ProbeLeft lists the processes the probe abandoned, which is the leak.
	ProbeLeft []int `json:"probe_left"`
}

// bwrapProbeFloorFlags are the flags the probe is documented to need and this
// test owns, independent of what bwrapProbeArgs returns: a host bwrap that does
// not list one of them cannot run any version of the probe, so the adoption
// question is not measurable there and the test skips. --as-pid-1 is
// deliberately absent: it is the measurement. A flag added to the probe later
// is also absent, so a bwrap that rejects it fails the probe — and this test
// with it — instead of passing by way of a skip.
var bwrapProbeFloorFlags = []string{
	"--unshare-user", "--unshare-pid", "--die-with-parent", "--new-session",
	"--ro-bind", "--proc", "--dev", "--argv0",
}

// TestBwrapProbeLeavesNoAdoptedChild holds the capability probe to both halves
// of its contract: it must succeed, and it must leave no process behind that it
// never waited for. The probe asks bwrap for a PID namespace whose command
// (`true`) creates no children, so the reaper bwrap forks for --unshare-pid has
// nothing to reap; the flag under test removes that fork.
//
// The test needs BOTH answers to pass: the probe must exit 0, and it must leave
// no adopted child. Capability is decided by a plain namespaced run that does
// not use the probe's argv, and by the host bwrap's --help, so "no sandbox here"
// and "bwrap rejected the probe's arguments" cannot look alike.
//
// The test runs in a child process because identifying the orphan requires
// becoming a child subreaper and then asking wait4 about children this process
// never spawned. Doing that in the test binary itself could consume another
// test's child exit status.
func TestBwrapProbeLeavesNoAdoptedChild(t *testing.T) {
	if os.Getenv(bwrapProbeOrphanHelperEnv) != "" {
		t.Skip("helper process; driven by the parent test")
	}
	if testing.Short() {
		// The fast gate never shells out to bubblewrap, as requireRealBwrap
		// keeps it that way for the rest of the package.
		unmeasurable(t, "-short skips the real-bwrap regression")
		return
	}
	path, err := exec.LookPath("bwrap")
	if err != nil {
		unmeasurable(t, fmt.Sprintf("bwrap is not installed on this host, so its probe cannot be exercised: %v", err))
		return
	}
	reportPath := filepath.Join(t.TempDir(), "adoption.json")
	cmd := exec.Command(os.Args[0], "-test.run=^TestBwrapProbeOrphanHelperProcess$", "-test.v")
	cmd.Env = append(os.Environ(),
		bwrapProbeOrphanHelperEnv+"="+path,
		bwrapProbeOrphanReportEnv+"="+reportPath,
	)
	out, runErr := cmd.CombinedOutput()
	// A report is only evidence of a run that finished: the helper can write one
	// and still fail, for instance in the cleanup that restores the subreaper
	// flag. Accepting the report then would pass over that failure.
	if runErr != nil {
		t.Fatalf("the adoption helper failed (exit: %v)\noutput:\n%s", runErr, out)
	}
	data, readErr := os.ReadFile(reportPath)
	if readErr != nil {
		t.Fatalf("the adoption helper measured nothing (helper exit: %v, report: %v)\noutput:\n%s", runErr, readErr, out)
	}
	var report bwrapProbeOrphanReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("decode the adoption helper's report: %v\noutput:\n%s", err, out)
	}
	// The pair is the evidence: the same harness sees the plain run leak and the
	// probe run not, so a clean result cannot be a harness that sees nothing.
	t.Logf("plain namespaced run (no --as-pid-1): exit %d, %d adopted; probe: exit %d, adopted %v",
		report.ControlExit, report.ControlLeft, report.ProbeExit, report.ProbeLeft)

	if report.Prereq != "" {
		unmeasurable(t, report.Prereq)
		return
	}
	if len(report.MissingFlags) > 0 {
		// This bwrap lacks flags the probe needs, so the probe's own verdict
		// is "not capable" by design and says nothing about adoption.
		unmeasurable(t, fmt.Sprintf("this host's bwrap predates the probe (no %s)", strings.Join(report.MissingFlags, ", ")))
		return
	}
	if len(report.ProbeLeft) > 0 {
		t.Fatalf("bwrap's capability probe left processes %v adopted and unreaped: the probe asked for a PID-namespace reaper whose exit no owner waits for", report.ProbeLeft)
	}
	if !report.ControlOK {
		unmeasurable(t, fmt.Sprintf("this host's bwrap cannot create a namespaced sandbox (plain run exit %d: %s)", report.ControlExit, firstLine(report.ControlStderr)))
		return
	}
	if !report.ProbeHasFlag {
		t.Fatalf("the probe's argv no longer carries --as-pid-1, so this test would prove nothing: the reaper it exists to catch is back")
	}
	if !report.ProbeOK {
		t.Fatalf("the capability probe failed (exit %d: %s) on a host whose bwrap ran a plain namespaced sandbox (exit %d), so an argument the probe passes and the control does not made it fail", report.ProbeExit, firstLine(report.ProbeStderr), report.ControlExit)
	}
	if report.ControlLeft == 0 {
		t.Fatalf("the plain namespaced run left no adopted child either, so this harness cannot see the leak it is measuring for: if this bwrap now reaps its own reaper, the regression is obsolete rather than broken")
	}
}

// unmeasurable reports a prerequisite this host lacks. It fails instead of
// skipping where bwrapProbeRequiredEnv is set, because a lane that provisions
// the sandbox has no legitimate reason to reach here.
func unmeasurable(t *testing.T, reason string) {
	t.Helper()
	if os.Getenv(bwrapProbeRequiredEnv) != "" {
		t.Fatalf("this lane must exercise the probe regression, but it is unmeasurable: %s", reason)
	}
	t.Skipf("the probe regression is unmeasurable here: %s", reason)
}

// TestBwrapProbeOrphanHelperProcess is the child side: it becomes a subreaper,
// runs a plain namespaced sandbox and then the probe under test, and writes
// what each exited with and what each left behind.
func TestBwrapProbeOrphanHelperProcess(t *testing.T) {
	path := os.Getenv(bwrapProbeOrphanHelperEnv)
	reportPath := os.Getenv(bwrapProbeOrphanReportEnv)
	if path == "" || reportPath == "" {
		t.Skip("helper process; driven by TestBwrapProbeLeavesNoAdoptedChild")
	}

	// Not parallel: PR_SET_CHILD_SUBREAPER is process-wide state, and the wait4
	// below must only ever see this helper's own children. The helper exits at
	// the end of this test, so there is no state to restore.
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		writeAdoptionReport(t, reportPath, bwrapProbeOrphanReport{Prereq: fmt.Sprintf("this host denies PR_SET_CHILD_SUBREAPER, which the test needs to see adopted children: %v", err)})
		return
	}

	probe := bwrapProbeArgs(path)
	report := bwrapProbeOrphanReport{
		MissingFlags: missingHelpFlags(t, path, bwrapProbeFloorFlags),
		ProbeHasFlag: slices.Contains(probe, "--as-pid-1"),
		ControlExit:  -1,
		ProbeExit:    -1,
	}
	if len(report.MissingFlags) == 0 {
		// The control is its own argv, written here rather than derived from the
		// probe, so a bad probe flag shows up as a probe failure and not as a
		// host that cannot run sandboxes. It omits --as-pid-1 by construction.
		control := []string{
			path,
			"--unshare-user", "--unshare-pid", "--die-with-parent", "--new-session",
			"--ro-bind", "/", "/", "--proc", "/proc", "--dev", "/dev",
			"--", "true",
		}
		report.ControlExit, report.ControlStderr = runProbeArgv(t, control)
		report.ControlOK = report.ControlExit == 0
		report.ControlLeft = len(reapAdopted(t))

		report.ProbeExit, report.ProbeStderr = runProbeArgv(t, probe)
		report.ProbeOK = report.ProbeExit == 0
		report.ProbeLeft = reapAdopted(t)
	}

	writeAdoptionReport(t, reportPath, report)
}

// writeAdoptionReport hands the helper's measurements back to the parent. A
// write failure is fatal here: the parent turns a missing report into a failed
// test, since nothing measurable happened.
func writeAdoptionReport(t *testing.T, reportPath string, report bwrapProbeOrphanReport) {
	t.Helper()
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("encode the adoption report: %v", err)
	}
	if err := os.WriteFile(reportPath, data, 0o600); err != nil {
		t.Fatalf("write the adoption report: %v", err)
	}
}

// missingHelpFlags returns the flags this bwrap's --help does not list. Each
// help line names one option in its first field, so the option is compared
// whole rather than by substring, which would also match a flag it prefixes.
func missingHelpFlags(t *testing.T, path string, flags []string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), probeCommandTimeout*4)
	defer cancel()
	out, _ := exec.CommandContext(ctx, path, "--help").CombinedOutput()
	listed := map[string]bool{}
	for line := range strings.SplitSeq(string(out), "\n") {
		if name := strings.Fields(line); len(name) > 0 {
			listed[name[0]] = true
		}
	}
	var missing []string
	for _, flag := range flags {
		if !listed[flag] {
			missing = append(missing, flag)
		}
	}
	return missing
}

// firstLine is what bwrap wrote to stderr reduced to its first non-blank line,
// which is where it names the refusal; the rest is usually the usage dump.
func firstLine(diagnostic string) string {
	for line := range strings.SplitSeq(diagnostic, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return "no diagnostic"
}

// runProbeArgv runs one bwrap invocation and reports its exit status, or -1
// when it never reached one, with what bwrap wrote to stderr so a failure names
// an unsupported option or a refused namespace instead of only a status. Run
// (not Start) reaps the bwrap process we spawned ourselves; anything still left
// is a process bwrap forked and abandoned.
func runProbeArgv(t *testing.T, argv []string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), probeCommandTimeout*4)
	defer cancel()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return 0, stderr.String()
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		return exitErr.ExitCode(), stderr.String()
	}
	return -1, stderr.String()
}

// reapAdopted drains every child this subreaper adopted and returns their pids.
// It waits for a process still on its way out rather than sampling once: the
// orphan is gone from bwrap's side before the kernel finishes reaping it.
func reapAdopted(t *testing.T) []int {
	t.Helper()
	var pids []int
	testwait.UntilSeen(t, "every process bwrap abandoned to exit", func() (bool, string) {
		var status unix.WaitStatus
		pid, err := unix.Wait4(-1, &status, unix.WNOHANG, nil)
		switch {
		case err == nil && pid > 0:
			pids = append(pids, pid)
			return false, fmt.Sprintf("process %d has exited and is reaped", pid)
		case err == nil && pid == 0:
			// A child exists and has not exited: WNOHANG names none, so the
			// message cannot name it either.
			return false, "at least one adopted child has not exited"
		case errors.Is(err, unix.ECHILD):
			return true, ""
		default:
			return false, "wait4 reported an error"
		}
	})
	return pids
}
