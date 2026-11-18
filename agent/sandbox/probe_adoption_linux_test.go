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
// existed. probeRan means the command ran and answered, so its exit status is
// the whole story; every other value means there is no answer to report —
// either the command never started, or it was cut short before it could finish.
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
	// probeNoVerdict means the invocation produced no verdict the check could act
	// on: its time bound ran out, or the host could not start it for want of a
	// resource. Those are exactly the conditions production's checkOutcome calls
	// CheckInconclusive, and naming them from the same call is what keeps this
	// file's diagnostic from disagreeing with production about the run it
	// explains.
	probeNoVerdict probeStart = "no-verdict"
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
// invocation this file records — the control's, and the diagnostic re-run of
// the probe's argv — classifies it the same way.
//
// The "no answer" verdict is production's own checkOutcome rather than a second
// rule, so the diagnostic cannot disagree with production about the run it is
// explaining: a probe the time bound killed, or one the host could not start for
// want of a resource, is reported as cut short rather than as a silent exit, and
// production calls the same outcome CheckInconclusive.
func outcomeFromRun(ctx context.Context, err error, stderr string) probeOutcome {
	out := probeOutcome{Start: probeRan, Exit: -1, Stderr: stderr}
	if err == nil {
		out.Exit = 0
		return out
	}
	if checkOutcome(ctx, err) == CheckInconclusive {
		out.Start = probeNoVerdict
		out.StartErr = err.Error()
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

// probeLaunchObserver is production's own host boundary with the result of each
// run kept. It embeds hostProbeSystem and overrides nothing, so the launch,
// the wait and the cleanup are still production's: it only looks at what the
// run returned. That is what lets the report carry production's launch result
// alongside production's verdict, instead of inferring one from the other or
// borrowing a rerun's outcome for it.
type probeLaunchObserver struct {
	hostProbeSystem
	launch probeOutcome
}

func (o *probeLaunchObserver) run(ctx context.Context, name string, args ...string) error {
	// hostProbeSystem.run is the shipped launch and wait; calling it, rather
	// than re-implementing it, is what keeps this observation from changing the
	// behavior being observed.
	err := o.hostProbeSystem.run(ctx, name, args...)
	o.launch = outcomeFromRun(ctx, err, "")
	return err
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
	case probeNoVerdict:
		return fmt.Sprintf("cut short before it answered (%s), which production calls inconclusive", o.StartErr)
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
	// ProbeLaunch is how THAT invocation ended, observed at the same boundary as
	// the verdict. It is carried alongside the verdict rather than reconstructed
	// later, so a failure to launch is attributed to the invocation production
	// made. A report must never state an outcome that belongs to a different
	// invocation: an inconclusive verdict is not "the probe could not launch"
	// unless this field says the launch itself failed.
	ProbeLaunch probeOutcome `json:"probe_launch"`
	// ProbeRerun is a diagnostic-only second invocation of the probe's argv, and
	// it is labelled a rerun because it is one: it exists to explain a verdict
	// that did not pass, and stating its outcome as the probe's would attribute
	// one invocation's failure to another. It is not filled in when the verdict
	// passed, and it is never the oracle — asserting on it would measure this
	// file's launcher rather than production's.
	ProbeRerun probeOutcome `json:"probe_rerun"`
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
	t.Logf("plain namespaced run (not the probe's argv): %s, %d adopted; production's probe check: %s (%s), adopted %v",
		report.Control.fate(), report.ControlLeft, checkOutcomeName(report.ProbeVerdict), report.ProbeLaunch.fate(), report.ProbeLeft)

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
	//
	// Every sentence below describes ONE invocation. The verdict and the launch
	// come from the same call; the rerun is named as a rerun. That separation is
	// the point: an inconclusive verdict beside a failed rerun is "the probe did
	// not pass, and the explanation could not be obtained", which is a different
	// sentence from "the probe could not launch", and only the report's own
	// launch field can say which one is true.
	if report.ProbeVerdict != CheckPassed {
		t.Fatalf("%s", explainFailedProbe(report).Message)
	}
	if report.ControlLeft == 0 {
		t.Fatalf("the plain namespaced run left no adopted child either, so this harness cannot see the leak it is measuring for: if this bwrap now reaps its own reaper, the regression is obsolete rather than broken")
	}
}

// probeCulprit names which invocation a failed verdict can be attributed to. It
// is the structured form of the report's central rule, so a test can assert the
// attribution without reading copy.
type probeCulprit string

const (
	// culpritLaunch means the production invocation itself never started, so the
	// failure is the launch's.
	culpritLaunch probeCulprit = "production-launch"
	// culpritNoVerdict means the production invocation produced no verdict at
	// all: a bound expired, or the host could not start it for want of a resource.
	culpritNoVerdict probeCulprit = "production-no-verdict"
	// culpritProbeRan means the production invocation ran and answered; the
	// verdict is the probe's, and any rerun failure belongs to the rerun alone.
	culpritProbeRan probeCulprit = "production-ran"
)

// failedProbeExplanation is what a failed production verdict is evidence of: the
// invocation the failure is attributed to, and a sentence saying so.
type failedProbeExplanation struct {
	Culprit probeCulprit
	Message string
}

// explainFailedProbe says what a failed production verdict is evidence of, in
// sentences that each describe one invocation.
//
// The rule: a report never states an outcome that belongs to a different
// invocation. The verdict and ProbeLaunch come from the same call, so a launch
// failure can be attributed to the probe. ProbeRerun is a second invocation, so
// its failure is reported as the rerun's and never as the probe's — an
// inconclusive verdict beside a failed rerun is "the probe did not pass, and the
// explanation could not be obtained", which sends a reader to different work
// than "the probe could not launch" does.
func explainFailedProbe(report bwrapProbeOrphanReport) failedProbeExplanation {
	const controlPart = "while a plain namespaced run %s on this host"
	control := report.Control.fate()
	switch report.ProbeLaunch.Start {
	case probeNoVerdict:
		return failedProbeExplanation{
			Culprit: culpritNoVerdict,
			Message: fmt.Sprintf("production's capability check concluded %s because that invocation produced no verdict (%s), "+controlPart+": nothing was learned about the probe's arguments",
				checkOutcomeName(report.ProbeVerdict), report.ProbeLaunch.fate(), control),
		}
	case probeNoProgram, probeRefused, probeNoStart:
		return failedProbeExplanation{
			Culprit: culpritLaunch,
			Message: fmt.Sprintf("production's capability check could not launch the probe (%s) on a host whose bwrap ran a plain namespaced sandbox (%s), so the failure says nothing about its arguments",
				report.ProbeLaunch.fate(), control),
		}
	}
	// The invocation that produced the verdict ran. Say so, and say separately
	// what a rerun added, if anything.
	if report.ProbeRerun.Start == "" {
		return failedProbeExplanation{
			Culprit: culpritProbeRan,
			Message: fmt.Sprintf("production's capability check concluded %s from an invocation that %s, "+controlPart+"; no separate rerun was obtained",
				checkOutcomeName(report.ProbeVerdict), report.ProbeLaunch.fate(), control),
		}
	}
	return failedProbeExplanation{
		Culprit: culpritProbeRan,
		Message: fmt.Sprintf("production's capability check concluded %s from an invocation that %s, "+controlPart+"; a separate rerun of the probe's argv %s",
			checkOutcomeName(report.ProbeVerdict), report.ProbeLaunch.fate(), control, report.ProbeRerun.fate()),
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
		// A run production would call inconclusive must not read as a silent
		// exit, which is what checkOutcome's deadline branch is for. The context
		// is cancelled rather than given a deadline: a wall-clock bound here would
		// be the mechanism, and the point is only to reach the same verdict.
		{name: "cut short by its bound", argv: []string{"/bin/sh", "-c", "sleep 5"}, start: probeNoVerdict, exit: -1},
	}
	outcomes := map[string]probeOutcome{}
	for _, tc := range cases {
		ctx := t.Context()
		if tc.start == probeNoVerdict {
			var cancel context.CancelFunc
			ctx, cancel = context.WithCancel(ctx)
			cancel()
		}
		out := roundTripOutcome(t, runProbeArgvContext(ctx, tc.argv))
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

		// The probe is production's own function driven through production's own
		// host boundary: its argv, its launch, its wait and whatever it does about
		// an abandoned reaper. The observer delegates every call to
		// hostProbeSystem rather than re-implementing it, so a fix that reaps
		// inside run still runs and the test sees it.
		observer := &probeLaunchObserver{}
		report.ProbeVerdict = bwrapUsernsCheck(observer, path)
		// The launch result is taken from the SAME call as the verdict, so the two
		// never describe different invocations.
		report.ProbeLaunch = observer.launch
		report.ProbeLeft = reapAdopted(t)

		// Explain a verdict that did not pass with a second, separate invocation of
		// the probe's argv. It runs after the leak has been measured, because it is
		// diagnostic only: it must not add processes to the count the leak is
		// judged on. Its outcome is recorded as a rerun, never as the probe's.
		if report.ProbeVerdict != CheckPassed {
			report.ProbeRerun = runProbeArgv(t, bwrapProbeArgs(path))
			reapAdopted(t)
		}
	}

	// A report never states an outcome that belongs to a different invocation.
	// The verdict and the launch above come from one call; the rerun is a second.
	// An inconclusive verdict beside a failed rerun is "the probe did not pass,
	// and separately the explanation could not be obtained" — not "the probe
	// could not launch". Those lead whoever reads CI to different work, so the
	// fields are kept apart all the way to the failure message.
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
	return runProbeArgvContext(ctx, argv)
}

// runProbeArgvContext is runProbeArgv with the caller's context, so a test can
// reach the verdict a cut-short invocation gets without waiting for a bound.
func runProbeArgvContext(ctx context.Context, argv []string) probeOutcome {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stderr = &stderr
	// outcomeFromRun is the same classifier the helper's launcher uses, so the
	// regression that pins the classification cannot pass while the report's own
	// path drops it.
	return outcomeFromRun(ctx, cmd.Run(), stderr.String())
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

// TestProbeReportKeepsInvocationsApart holds the report to one rule: an outcome
// is never stated of an invocation it did not come from. Two lies are possible
// when the verdict and the invocation that produced it are stored apart, and
// both would mislead whoever reads CI:
//
//   - a production verdict of inconclusive, reached because the host refused to
//     start the probe for want of a resource, reported as a bare "inconclusive"
//     with the launch failure dropped;
//   - a failed diagnostic rerun reported as the production invocation's failure,
//     which says the probe could not launch when it did.
//
// The check is structural: which invocation the explanation blames is decided by
// the report's own fields, so the test drives the fields and compares the
// classification, never the message text.
func TestProbeReportKeepsInvocationsApart(t *testing.T) {
	control := probeOutcome{Start: probeRan, Exit: 0, Stderr: ""}
	refused := probeOutcome{Start: probeNoStart, Exit: -1, StartErr: "fork/exec: resource temporarily unavailable"}
	ran := probeOutcome{Start: probeRan, Exit: 0}

	cases := []struct {
		name   string
		report bwrapProbeOrphanReport
		want   probeCulprit
	}{
		{
			name: "production could not launch the probe",
			report: bwrapProbeOrphanReport{
				Control: control, ProbeVerdict: CheckInconclusive, ProbeLaunch: refused,
			},
			want: culpritLaunch,
		},
		{
			name: "production ran the probe, the explanation did not",
			report: bwrapProbeOrphanReport{
				Control: control, ProbeVerdict: CheckInconclusive,
				ProbeLaunch: ran, ProbeRerun: refused,
			},
			want: culpritProbeRan,
		},
		{
			name: "production ran the probe, no explanation was obtained",
			report: bwrapProbeOrphanReport{
				Control: control, ProbeVerdict: CheckFailed, ProbeLaunch: probeOutcome{Start: probeRan, Exit: 1},
			},
			want: culpritProbeRan,
		},
		{
			name: "production produced no verdict",
			report: bwrapProbeOrphanReport{
				Control: control, ProbeVerdict: CheckInconclusive,
				ProbeLaunch: probeOutcome{Start: probeNoVerdict, Exit: -1, StartErr: "signal: killed"},
			},
			want: culpritNoVerdict,
		},
	}
	for _, tc := range cases {
		// The structured attribution, not the prose: whatever the message says, a
		// rerun's failure may not be attributed to the production invocation.
		if got := explainFailedProbe(tc.report).Culprit; got != tc.want {
			t.Errorf("%s: attributed to %q, want %q", tc.name, got, tc.want)
		}
	}
}
