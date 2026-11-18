//go:build linux

package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
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

// probeStart classifies how a bwrap invocation ended before any exit status
// existed. probeRan means the command ran, so its exit status is the whole
// story; every other value means it never started, and no exit status exists.
type probeStart string

const (
	// probeRan means the command ran: it was launched and reached an exit.
	probeRan probeStart = "ran"
	// probeNoProgram means the launcher resolved no such program: a path whose
	// file is gone (ENOENT), or a bare name os/exec could not resolve on PATH
	// (exec.ErrNotFound). Either way the command never started.
	probeNoProgram probeStart = "no-program"
	// probeRefused means the program exists and the kernel declined to execute
	// it (EACCES, EPERM).
	probeRefused probeStart = "refused"
	// probeNoStart is a launch failure with neither a missing program nor a
	// refusal to name: a fork failure, a host at its process limit, and the
	// like.
	probeNoStart probeStart = "no-start"
)

// classifyProbeStart reports how err ended an invocation before it could reach
// an exit status. os/exec reports a command that ran and exited nonzero as
// *exec.ExitError, so anything else is the launch's own failure; separating
// them here is what keeps "never started" from sharing a shape with "ran".
func classifyProbeStart(err error) probeStart {
	switch {
	case err == nil:
		return probeRan
	case isExitStatus(err):
		return probeRan
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, exec.ErrNotFound):
		return probeNoProgram
	case errors.Is(err, fs.ErrPermission):
		return probeRefused
	default:
		return probeNoStart
	}
}

// isExitStatus reports whether err is a command's own nonzero exit rather than
// a failure to launch it.
func isExitStatus(err error) bool {
	_, ok := errors.AsType[*exec.ExitError](err)
	return ok
}

// probeOutcome is how one bwrap invocation ended, in the terms a failure
// message and a test can both use: an exit status when the command ran, and the
// launch failure's class and text when it never started. It is what the helper
// process writes into its report, so a launch failure survives the process
// boundary that a bare -1 exit status cannot describe.
type probeOutcome struct {
	// Start is probeRan when the command ran, so Exit is its status. Any other
	// value means it never started and Exit is -1.
	Start probeStart `json:"start"`
	// Exit is the exit status, or -1 when no status exists.
	Exit int `json:"exit"`
	// StartErr is the launch failure's text, empty when the command ran. A
	// message may show it; no test asserts on it.
	StartErr string `json:"start_err"`
	// Stderr is what the program wrote to stderr.
	Stderr string `json:"stderr"`
}

// ok reports whether the invocation ran and exited 0. An outcome with no class
// is not a pass: nothing recorded that the command ran.
func (o probeOutcome) ok() bool { return o.Start == probeRan && o.Exit == 0 }

// outcomeFromRun turns one command's result into a probeOutcome, so every
// invocation this file records — the control's, and production's own through
// the recorder below — classifies a launch failure the same way.
func outcomeFromRun(err error, stderr string) probeOutcome {
	out := probeOutcome{Start: probeRan, Exit: -1, Stderr: stderr}
	if err == nil {
		out.Exit = 0
		return out
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		out.Exit = exitErr.ExitCode()
		return out
	}
	// The command never started, so there is no exit status to report and no
	// stderr to explain it: the cause is the launch error itself, and dropping
	// it is what would make this failure look like a silent exit.
	out.Start = classifyProbeStart(err)
	out.StartErr = err.Error()
	return out
}

// checkOutcomeName names a capability verdict for a failure message. It is a
// message, never an oracle: the test asserts the CheckOutcome itself.
func checkOutcomeName(outcome CheckOutcome) string {
	switch outcome {
	case CheckPassed:
		return "passed"
	case CheckFailed:
		return "failed"
	default:
		return "inconclusive"
	}
}

// fate is the outcome as one clause for a failure message: the exit status and
// the program's first diagnostic line, or the fact that it never started and
// why. A diagnostic must never read the same for "ran and was silent" and
// "never started": those outcomes mean opposite things about the probe, one
// measured it and the other measured nothing. Exit alone cannot carry that
// difference — a launch failure and a process a signal killed both reach -1 and
// write no stderr — so the clause is built from Start and StartErr as well.
//
// An outcome with no class at all — a report that never recorded one — reads as
// never started rather than as a silent success, so a missing field cannot make
// a measurement that was never taken look like a probe that passed.
func (o probeOutcome) fate() string {
	switch o.Start {
	case probeRan:
		return fmt.Sprintf("exit %d: %s", o.Exit, firstLine(o.Stderr))
	case probeNoProgram, probeRefused, probeNoStart:
		return fmt.Sprintf("never started (%s): %s", o.Start, o.StartErr)
	default:
		return "never started (no outcome recorded)"
	}
}

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
	// Control is the plain namespaced run — its own argv, not the probe's — so
	// capability is answered without whatever the probe does to avoid a reaper.
	Control probeOutcome `json:"control"`
	// ControlLeft counts the processes the control abandoned. The control does
	// not reap what bwrap abandons, so it should leave a reaper behind: a zero
	// here means this harness cannot see the leak it is looking for.
	ControlLeft int `json:"control_left"`
	// ProbeVerdict is what production's own bwrapUsernsCheck concluded, run
	// through the real hostProbeSystem: its argv, its launch, its cleanup. This
	// is the verdict the test asserts, because it is the one the product acts on.
	ProbeVerdict CheckOutcome `json:"probe_verdict"`
	// Probe is a diagnostic-only re-run of the probe's argv, used to explain a
	// verdict that did not pass. It is never the oracle: it is a second executor,
	// so asserting on it would measure this file's launcher rather than
	// production's.
	Probe probeOutcome `json:"probe"`
	// ProbeLeft lists the processes the probe abandoned, which is the leak.
	ProbeLeft []int `json:"probe_left"`
}

// bwrapProbeFloorFlags are the flags the probe is documented to need and this
// test owns, independent of what bwrapProbeArgs returns: a host bwrap that does
// not list one of them cannot run any version of the probe, so the adoption
// question is not measurable there and the test skips. --as-pid-1 is
// deliberately absent, because the probe does not need it to be correct: a fix
// that reaps the reaper instead of avoiding it leaves this test measurable. A
// flag added to the probe later is also absent, so a bwrap that rejects it
// fails the probe — and this test with it — instead of passing by way of a
// skip.
var bwrapProbeFloorFlags = []string{
	"--unshare-user", "--unshare-pid", "--die-with-parent", "--new-session",
	"--ro-bind", "--proc", "--dev", "--argv0",
}

// TestBwrapProbeLeavesNoAdoptedChild holds the capability probe to both halves
// of its contract: it must succeed, and it must leave no process behind that it
// never waited for. The probe asks bwrap for a PID namespace whose command
// (`true`) creates no children, so the reaper bwrap forks for --unshare-pid has
// nothing to reap, and the probe must not leave that fork behind. How it avoids
// leaving one is not this test's business: asking bwrap not to fork it and
// reaping it afterwards are both accepted.
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
	// The pair is the evidence: the same harness sees the plain run leak and the
	// probe run not, so a clean result cannot be a harness that sees nothing.
	t.Logf("plain namespaced run (not the probe's argv): %s, %d adopted; production's probe check: %s, adopted %v",
		report.Control.fate(), report.ControlLeft, checkOutcomeName(report.ProbeVerdict), report.ProbeLeft)

	if len(report.ProbeLeft) > 0 {
		t.Fatalf("bwrap's capability probe left processes %v adopted and unreaped: the probe asked for a PID-namespace reaper whose exit no owner waits for", report.ProbeLeft)
	}
	if !report.Control.ok() {
		unmeasurable(t, fmt.Sprintf("this host's bwrap cannot create a namespaced sandbox (plain run %s)", report.Control.fate()))
		return
	}
	// Production's own verdict is the oracle: it is what the product acts on, and
	// it was reached through the real hostProbeSystem, so its launch and cleanup
	// are the shipped ones.
	if report.ProbeVerdict != CheckPassed {
		// What follows explains a verdict that did not pass. It attributes the
		// failure only to what it observed, because a launch that never reached
		// bwrap, or a probe the deadline killed, is not evidence about the probe's
		// arguments.
		switch {
		case report.Probe.Start != probeRan:
			t.Fatalf("the capability probe never started (%s) on a host whose bwrap ran a plain namespaced sandbox (%s): production's check could not launch the probe, so the failure says nothing about its arguments", report.Probe.fate(), report.Control.fate())
		default:
			t.Fatalf("production's capability check concluded %s while a plain namespaced run %s on this host; the probe's own invocation %s", checkOutcomeName(report.ProbeVerdict), report.Control.fate(), report.Probe.fate())
		}
	}
	if report.ControlLeft == 0 {
		t.Fatalf("the plain namespaced run left no adopted child either, so this harness cannot see the leak it is measuring for: if this bwrap now reaps its own reaper, the regression is obsolete rather than broken")
	}
}

// TestProbeOutcomeSeparatesFailedLaunchFromSilentExit drives the helper that
// produces the adoption report through the conditions that must not read alike:
// a launch that found no program, a launch the kernel refused, a program that
// ran and wrote nothing, and a program a signal killed. The failed launches and
// the killed program all reach exit -1 with empty stderr, so an outcome made
// only of an exit status and stderr would report every one of them as
// "exit -1: no diagnostic" — the same shape a probe that ran and said nothing
// produces, which is the ambiguity the adoption test exists to remove.
//
// The outcome is checked after a JSON round trip because that is how it reaches
// the parent test: the helper process writes the report and the parent reads it,
// so a classification lost in the encoding would be lost exactly where it is
// needed.
func TestProbeOutcomeSeparatesFailedLaunchFromSilentExit(t *testing.T) {
	// A bare name is resolved through PATH, so the lookup failure this test
	// needs would be a success on a developer machine that happens to have an
	// executable of that name. Pin PATH to a directory this test owns and
	// nothing in it, so the name cannot resolve wherever the suite runs.
	t.Setenv("PATH", t.TempDir())

	dir := t.TempDir()
	missing := filepath.Join(dir, "no-such-bwrap")
	notExecutable := filepath.Join(dir, "not-executable")
	if err := os.WriteFile(notExecutable, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatalf("write the file the kernel should refuse to execute: %v", err)
	}

	const silentExit = "ran and was silent"
	const killedExit = "ran and was killed"
	cases := []struct {
		name  string
		argv  []string
		start probeStart
		exit  int
	}{
		// The first and third name a path, so PATH above does not decide them.
		{name: "program missing", argv: []string{missing}, start: probeNoProgram, exit: -1},
		{name: "program not on PATH", argv: []string{"evener-nonexistent-bwrap-probe"}, start: probeNoProgram, exit: -1},
		{name: "program not executable", argv: []string{notExecutable}, start: probeRefused, exit: -1},
		{name: silentExit, argv: []string{"/bin/sh", "-c", "exit 7"}, start: probeRan, exit: 7},
		{name: killedExit, argv: []string{"/bin/sh", "-c", "kill -9 $$"}, start: probeRan, exit: -1},
	}
	outcomes := map[string]probeOutcome{}
	for _, tc := range cases {
		out := roundTripOutcome(t, runProbeArgv(t, tc.argv))
		outcomes[tc.name] = out
		t.Logf("%s: %s", tc.name, out.fate())
		if out.Start != tc.start || out.Exit != tc.exit {
			t.Errorf("%s: got start %q exit %d, want start %q exit %d", tc.name, out.Start, out.Exit, tc.start, tc.exit)
		}
		if out.Start != probeRan && out.StartErr == "" {
			t.Errorf("%s: the launch failed and the outcome carries no cause: %s", tc.name, out.fate())
		}
	}
	// The invariant, stated as one comparison the per-case table cannot make: a
	// process killed by a signal DID run, and it also reports exit -1 with empty
	// stderr. That is the outcome a failed launch is most likely to be confused
	// with, so the two must not report the same thing; if they do, the parent
	// test cannot say which one it measured.
	//
	// The never-started cases are selected by what each case is meant to be, not
	// by what it reported: selecting on the report would skip a launch failure
	// that reports itself as a run, which is the one case this check exists for.
	killed := outcomes[killedExit]
	for _, tc := range cases {
		if tc.start == probeRan {
			continue
		}
		out := outcomes[tc.name]
		if out.Start == killed.Start && out.Exit == killed.Exit {
			t.Errorf("%s and %s both report start %q exit %d, so a launch that never happened is indistinguishable from a run that was cut short", tc.name, killedExit, out.Start, out.Exit)
		}
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

	report := bwrapProbeOrphanReport{
		MissingFlags: missingHelpFlags(t, path, bwrapProbeFloorFlags),
	}
	if len(report.MissingFlags) == 0 {
		// The control is its own argv, written here rather than derived from the
		// probe, so a bad probe flag shows up as a probe failure and not as a
		// host that cannot run sandboxes. It omits the probe's reaper handling
		// by construction.
		control := []string{
			path,
			"--unshare-user", "--unshare-pid", "--die-with-parent", "--new-session",
			"--ro-bind", "/", "/", "--proc", "/proc", "--dev", "/dev",
			"--", "true",
		}
		report.Control = runProbeArgv(t, control)
		report.ControlLeft = len(reapAdopted(t))

		// The probe is production's own function driven through the real
		// hostProbeSystem: its argv, its launch, and whatever it does about an
		// abandoned reaper. Substituting a boundary here — even one that only
		// records — would put this file's launcher in place of production's, so a
		// fix that reaps inside run would show up as a leak production does not
		// have.
		report.ProbeVerdict = bwrapUsernsCheck(hostProbeSystem{}, path)
		report.ProbeLeft = reapAdopted(t)

		// Explain a verdict that did not pass with a second invocation of the
		// probe's argv. It runs after the leak has been measured, because it is
		// diagnostic only: it must not add processes to the count the leak is
		// judged on, and its outcome is never the oracle.
		if report.ProbeVerdict != CheckPassed {
			report.Probe = runProbeArgv(t, bwrapProbeArgs(path))
			reapAdopted(t)
		}
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

// runProbeArgv runs one bwrap invocation and reports how it ended: its exit
// status with what bwrap wrote to stderr, or the launch failure that kept it
// from reaching one. Run (not Start) reaps the bwrap process we spawned
// ourselves; anything still left is a process bwrap forked and abandoned.
func runProbeArgv(t *testing.T, argv []string) probeOutcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), probeCommandTimeout*4)
	defer cancel()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stderr = &stderr
	// outcomeFromRun is the same classifier the helper's launcher uses, so the
	// regression that pins the classification cannot pass while the report's own
	// path drops it.
	return outcomeFromRun(cmd.Run(), stderr.String())
}

// roundTripOutcome encodes and decodes an outcome the way the helper hands it
// to the parent test, so a classification the report cannot carry fails here
// rather than reading as "no diagnostic" in the parent.
func roundTripOutcome(t *testing.T, out probeOutcome) probeOutcome {
	t.Helper()
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("encode the outcome: %v", err)
	}
	var decoded probeOutcome
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode the outcome: %v", err)
	}
	return decoded
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
