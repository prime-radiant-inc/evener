//go:build unix

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// The foreground fixture cleanup regression. The outer test re-executes this
// binary and selects a checkpoint plus an outer-owned report path through two
// test-only variables. Neither is a product EVENER_* name, so TestMain's
// product-env scrub leaves them for the child — the same property
// EVENER_LIVE_TESTS relies on.
const (
	foregroundCleanupStageEnv  = "EVENER_TEST_FOREGROUND_CLEANUP_STAGE"
	foregroundCleanupReportEnv = "EVENER_TEST_FOREGROUND_CLEANUP_REPORT"

	// The three checkpoint stages whose arrival is a fatal child outcome.
	foregroundCleanupServeStarted    = "serve-started"
	foregroundCleanupShellStarted    = "shell-started"
	foregroundCleanupProviderWaiting = "provider-waiting"
	// The join observation the fixture emits after serve returns.
	foregroundCleanupJoined = "joined"
	// The normal-completion selector: enables the helper without selecting a
	// failure stage, so the child drives the fixture to its own clean exit.
	foregroundCleanupNormalStage = "normal"
)

// foregroundCleanupReport is the child's structured protocol, decoded by the
// outer test. It is deliberately not parsed from human-facing output.
type foregroundCleanupReport struct {
	Stage              string `json:"stage"`
	Failed             bool   `json:"failed"`
	Joined             bool   `json:"joined"`
	ShellPID           int    `json:"shell_pid"`
	ShellIdentity      string `json:"shell_identity"`
	DescendantPID      int    `json:"descendant_pid"`
	DescendantIdentity string `json:"descendant_identity"`
}

func isForegroundCleanupFailureStage(stage string) bool {
	switch stage {
	case foregroundCleanupServeStarted, foregroundCleanupShellStarted, foregroundCleanupProviderWaiting:
		return true
	}
	return false
}

// TestServeForegroundCleanupHelperProcess is the child side of the regression.
// It skips unless the outer test selected a stage, so an ordinary package run
// reports an explicit skip rather than running the fixture.
func TestServeForegroundCleanupHelperProcess(t *testing.T) {
	stage := os.Getenv(foregroundCleanupStageEnv)
	if stage == "" {
		t.Skip("helper process; driven by TestRunServeForegroundShellCleanup")
	}
	reportPath := os.Getenv(foregroundCleanupReportEnv)
	if reportPath == "" {
		t.Fatalf("%s is not set", foregroundCleanupReportEnv)
	}
	report := &foregroundCleanupReport{}
	// Registered first, so it runs last: the fixture's own cleanup (registered
	// during the fixture) joins serve before this records t.Failed().
	t.Cleanup(func() {
		report.Failed = t.Failed()
		data, err := json.Marshal(report)
		if err != nil {
			t.Errorf("marshal cleanup report: %v", err)
			return
		}
		if err := os.WriteFile(reportPath, data, 0o600); err != nil {
			t.Errorf("write cleanup report: %v", err)
		}
	})

	failureSelected := isForegroundCleanupFailureStage(stage)
	// Stage is what the fixture OBSERVED, never the requested selector: the last
	// observation wins, and a checkpoint that fails freezes it so the join the
	// fixture still performs in cleanup does not erase the failure point.
	failed := false
	checkpoint := func(observed, shellPIDPath string) {
		// Record the shell and its deterministic child ONCE, at the first
		// checkpoint where they are observable (shell-started, while the shell is
		// still gated). Re-reading at a later checkpoint could pick up a PID
		// reused after the process died and authorize touching an unrelated one.
		if shellPIDPath != "" {
			if report.ShellPID == 0 {
				if pid, ok := readForegroundShellPID(shellPIDPath); ok {
					report.ShellPID = pid
					report.ShellIdentity, _ = ownedProcessIdentity(pid)
				}
			}
			if report.DescendantPID == 0 {
				childPath := filepath.Join(filepath.Dir(shellPIDPath), foregroundShellChildPIDFilename)
				if pid, ok := readForegroundShellPID(childPath); ok {
					report.DescendantPID = pid
					report.DescendantIdentity, _ = ownedProcessIdentity(pid)
				}
			}
		}
		if observed == foregroundCleanupJoined {
			report.Joined = true
		}
		if !failed {
			report.Stage = observed
		}
		if failureSelected && observed == stage && !failed {
			failed = true
			t.Fatalf("foreground fixture reached the %s checkpoint", stage)
		}
	}
	runServeForegroundShellPersistenceCaseWithCheckpoint(t, foregroundShellSubscriber, checkpoint)
}

// TestRunServeForegroundShellCleanup drives the fixture to each early failure
// checkpoint and to normal completion, and requires the fixture to join its
// serve and leave no owned process behind in every case.
func TestRunServeForegroundShellCleanup(t *testing.T) {
	for _, stage := range []string{
		foregroundCleanupServeStarted,
		foregroundCleanupShellStarted,
		foregroundCleanupProviderWaiting,
		foregroundCleanupNormalStage,
	} {
		t.Run(stage, func(t *testing.T) {
			runForegroundCleanupCase(t, stage)
		})
	}
}

func runForegroundCleanupCase(t *testing.T, stage string) {
	t.Helper()
	wantFailure := isForegroundCleanupFailureStage(stage)
	// A sentinel the fixture must never touch: killing it would mean cleanup
	// used a name-wide or group-wide kill instead of the recorded fixture PID.
	sentinel := startForegroundCleanupSentinel(t)

	reportPath := filepath.Join(t.TempDir(), "foreground-cleanup-report.json")
	cmd := exec.Command(os.Args[0], "-test.run=^TestServeForegroundCleanupHelperProcess$")
	cmd.Env = append(os.Environ(),
		foregroundCleanupStageEnv+"="+stage,
		foregroundCleanupReportEnv+"="+reportPath,
	)
	var childOut, childErr bytes.Buffer
	// The child's intentional failure is captured, never forwarded into the
	// outer test's own output.
	cmd.Stdout, cmd.Stderr = &childOut, &childErr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start foreground cleanup helper: %v", err)
	}
	reaped := false
	t.Cleanup(func() {
		if !reaped && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	output := func() string { return childOut.String() + childErr.String() }

	exitCode := waitForForegroundCleanupChild(t, cmd, output)
	reaped = true

	report, ok := readForegroundCleanupReport(reportPath)
	if !ok {
		t.Fatalf("child wrote no protocol report at %s\noutput:\n%s", reportPath, output())
	}
	// shell-started, provider-waiting and normal completion must carry the
	// recorded shell and descendant identity: missing evidence must not pass
	// silently. serve-started fails before the shell exists, so PID 0 is right
	// there.
	if stage != foregroundCleanupServeStarted {
		if report.ShellPID == 0 || report.ShellIdentity == "" {
			t.Fatalf("missing shell evidence at %s: pid=%d identity=%q\noutput:\n%s",
				stage, report.ShellPID, report.ShellIdentity, output())
		}
		if report.DescendantPID == 0 || report.DescendantIdentity == "" {
			t.Fatalf("missing descendant evidence at %s: pid=%d identity=%q\noutput:\n%s",
				stage, report.DescendantPID, report.DescendantIdentity, output())
		}
	}
	// Read the survivors before any assertion, and register their removal, so a
	// failing red run still cleans up the processes it proved leaked. Only
	// identity-verified owned PIDs are touched; the sentinel never is.
	shellSurvived := ownedProcessSurvives(report.ShellPID, report.ShellIdentity, 2*time.Second)
	descendantSurvived := ownedProcessSurvives(report.DescendantPID, report.DescendantIdentity, 2*time.Second)
	t.Cleanup(func() {
		terminateOwnedProcessTree(report.ShellPID, report.ShellIdentity,
			report.DescendantPID, report.DescendantIdentity, 2*time.Second)
	})

	if wantFailure {
		// The child's OBSERVED stage must equal the requested checkpoint: this is
		// non-vacuous because report.Stage is recorded from observations, not
		// seeded from the selector.
		if exitCode != 1 || !report.Failed || report.Stage != stage {
			t.Fatalf("wrong child outcome for %s: exit=%d failed=%v stage=%q\noutput:\n%s",
				stage, exitCode, report.Failed, report.Stage, output())
		}
	} else {
		// Normal completion never fails a checkpoint, so the last stage observed
		// is the join.
		if exitCode != 0 || report.Failed || report.Stage != foregroundCleanupJoined {
			t.Fatalf("normal child outcome: exit=%d failed=%v stage=%q\noutput:\n%s",
				exitCode, report.Failed, report.Stage, output())
		}
	}
	if !report.Joined {
		t.Fatalf("fixture did not join serve at the %s checkpoint\noutput:\n%s", stage, output())
	}
	if shellSurvived {
		t.Fatalf("fixture shell %d (identity %q) survived the %s checkpoint cleanup\noutput:\n%s",
			report.ShellPID, report.ShellIdentity, stage, output())
	}
	if descendantSurvived {
		t.Fatalf("fixture shell descendant %d (identity %q) survived the %s checkpoint cleanup\noutput:\n%s",
			report.DescendantPID, report.DescendantIdentity, stage, output())
	}
	if !sentinel.alive() {
		t.Fatal("cleanup escaped fixture ownership: the outer sentinel process was killed")
	}
}

func waitForForegroundCleanupChild(t *testing.T, cmd *exec.Cmd, output func() string) int {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timeout := time.NewTimer(120 * time.Second)
	defer timeout.Stop()
	select {
	case err := <-done:
		if err == nil {
			return 0
		}
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			return exitErr.ExitCode()
		}
		t.Fatalf("helper wait: %v\noutput:\n%s", err, output())
		return -1
	case <-timeout.C:
		// Kill and join the helper before reading the buffers: os/exec's copy
		// goroutines write childOut/childErr until Wait returns, so reading them
		// earlier races an unsynchronized bytes.Buffer.
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-done
		t.Fatalf("helper never exited\noutput:\n%s", output())
		return -1
	}
}

func readForegroundCleanupReport(path string) (foregroundCleanupReport, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return foregroundCleanupReport{}, false
	}
	var report foregroundCleanupReport
	if err := json.Unmarshal(data, &report); err != nil {
		return foregroundCleanupReport{}, false
	}
	return report, true
}

func readForegroundShellPID(path string) (int, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// processSnapshot returns a stable identity for the process and whether it is a
// live, non-zombie process. ok is false when no identity can be read, which is
// also the case once the process has been reaped.
func processStat(pid int) (state byte, identity string, ok bool) {
	if pid <= 0 {
		return 0, "", false
	}
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		if i := strings.LastIndex(string(data), ")"); i >= 0 {
			fields := strings.Fields(string(data)[i+1:])
			if len(fields) >= 20 && len(fields[0]) > 0 {
				return fields[0][0], "proc:" + fields[19], true
			}
		}
	}
	out, err := exec.Command("ps", "-o", "lstart=", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, "", false
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return 0, "", false
	}
	fields := strings.Fields(line)
	stat := fields[len(fields)-1]
	if len(stat) == 0 {
		return 0, "", false
	}
	return stat[0], "ps:" + strings.Join(fields[:len(fields)-1], " "), true
}

func processSnapshot(pid int) (identity string, alive bool, ok bool) {
	state, identity, ok := processStat(pid)
	if !ok {
		return "", false, false
	}
	return identity, state != 'Z', true
}

// processState reports the single-letter process state, or ok=false when the
// process cannot be observed.
func processState(pid int) (byte, bool) {
	state, _, ok := processStat(pid)
	return state, ok
}

func ownedProcessIdentity(pid int) (string, bool) {
	identity, _, ok := processSnapshot(pid)
	return identity, ok
}

// ownedProcessExistsWithIdentity reports whether the process currently exists
// (running OR an unreaped zombie) with the recorded identity. Existence, not
// liveness, is what a leaked fixture process is measured by: a zombie has not
// been reaped, so it must count as a survivor rather than as clean. An empty
// identity never matches, so an unrecorded pair is never treated as gone.
func ownedProcessExistsWithIdentity(pid int, identity string) bool {
	if pid <= 0 || identity == "" {
		return false
	}
	got, _, ok := processSnapshot(pid)
	return ok && got == identity
}

// ownedProcessSurvives reports whether the process still exists after a grace
// period, so a just-reaped fixture shell is not mistaken for a survivor.
func ownedProcessSurvives(pid int, identity string, grace time.Duration) bool {
	if pid <= 0 || identity == "" {
		return false
	}
	deadline := time.Now().Add(grace)
	for {
		if !ownedProcessExistsWithIdentity(pid, identity) {
			return false
		}
		if time.Now().After(deadline) {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// signalOwnedProcess signals a process only when the recorded identity is
// non-empty and still matches the current one, so an empty or mismatched
// identity never authorizes a signal. It reports whether it signaled.
func signalOwnedProcess(pid int, identity string, sig syscall.Signal) bool {
	if pid <= 0 || identity == "" {
		return false
	}
	got, _, ok := processSnapshot(pid)
	if !ok || got != identity {
		return false
	}
	return syscall.Kill(pid, sig) == nil
}

// killOwnedProcess kills only a process whose recorded identity is non-empty and
// still matches the current one, so an empty or mismatched identity never
// authorizes a kill. It reports whether it signaled.
func killOwnedProcess(pid int, identity string) bool {
	return signalOwnedProcess(pid, identity, syscall.SIGKILL)
}

// waitForOwnedProcessesGone waits bounded for both the shell and its descendant
// to disappear (reaped, not merely non-running). An empty or mismatched identity
// counts as gone because it is never signaled.
func waitForOwnedProcessesGone(shellPID int, shellIdentity string, descendantPID int, descendantIdentity string, grace time.Duration) bool {
	deadline := time.Now().Add(grace)
	for {
		if !ownedProcessExistsWithIdentity(shellPID, shellIdentity) &&
			!ownedProcessExistsWithIdentity(descendantPID, descendantIdentity) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// terminateOwnedProcessTree stops the fixture's shell and its descendant without
// removing the descendant's reaper first. The shell is asked to TERM first so its
// trap can kill and wait the child on the graceful path. If that does not settle
// within the grace, escalation SIGKILLs the descendant first so it cannot outlive
// its reaper, then the shell; any zombie that escalation leaves depends on the
// host reaper. Only a non-empty, matching recorded identity is ever signaled;
// nothing is killed by name or process group. It reports whether it signaled.
func terminateOwnedProcessTree(shellPID int, shellIdentity string, descendantPID int, descendantIdentity string, grace time.Duration) bool {
	signaled := signalOwnedProcess(shellPID, shellIdentity, syscall.SIGTERM)
	if waitForOwnedProcessesGone(shellPID, shellIdentity, descendantPID, descendantIdentity, grace) {
		return signaled
	}
	if signalOwnedProcess(descendantPID, descendantIdentity, syscall.SIGKILL) {
		signaled = true
	}
	if signalOwnedProcess(shellPID, shellIdentity, syscall.SIGKILL) {
		signaled = true
	}
	waitForOwnedProcessesGone(shellPID, shellIdentity, descendantPID, descendantIdentity, grace)
	return signaled
}

// foregroundShellCleanupGrace bounds every step of the registered fixture
// cleanup: the shell's own graceful path after its gate is released, and each
// escalation wait. No step in the cleanup is allowed to block indefinitely.
const foregroundShellCleanupGrace = 2 * time.Second

// foregroundShellCleanup returns an idempotent cleanup for a fixture shell
// started as cmd. It is safe to register immediately after cmd.Start, before
// any PID or identity has been captured: it first releases the shell's gate so
// the shell's own path kills and waits its child, then waits bounded for the
// shell to exit (waited closes when cmd.Wait returns). If the graceful path
// stalls it escalates with terminateOwnedProcessTree, which signals only a
// non-empty, matching identity: the shell is this process's direct cmd.Process
// child, and the descendant is read from childPIDPath and verified by reading
// its current identity. Nothing is ever signaled by name or process group, and
// cleanup never fails the test.
func foregroundShellCleanup(cmd *exec.Cmd, waited <-chan struct{}, gatePath, childPIDPath string) func() {
	shellPID := 0
	shellIdentity := ""
	if cmd.Process != nil {
		shellPID = cmd.Process.Pid
		shellIdentity, _ = ownedProcessIdentity(shellPID)
	}
	return sync.OnceFunc(func() {
		// Best-effort and independent of PID/identity capture: releasing the gate
		// is what lets the shell take its normal kill-and-wait path.
		_ = os.WriteFile(gatePath, []byte("release"), 0o600)
		if waitForClosedChannel(waited, foregroundShellCleanupGrace) {
			return
		}
		descendantPID := 0
		descendantIdentity := ""
		if pid, ok := readForegroundShellPID(childPIDPath); ok {
			if identity, ok := ownedProcessIdentity(pid); ok {
				descendantPID, descendantIdentity = pid, identity
			}
		}
		terminateOwnedProcessTree(shellPID, shellIdentity, descendantPID, descendantIdentity, foregroundShellCleanupGrace)
		waitForClosedChannel(waited, foregroundShellCleanupGrace)
	})
}

// waitForClosedChannel waits bounded for ch to close and reports whether it did.
func waitForClosedChannel(ch <-chan struct{}, timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ch:
		return true
	case <-timer.C:
		return false
	}
}

type foregroundCleanupSentinel struct{ process *os.Process }

func startForegroundCleanupSentinel(t *testing.T) *foregroundCleanupSentinel {
	t.Helper()
	cmd := exec.Command("sleep", "600")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start outer sentinel: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})
	return &foregroundCleanupSentinel{process: cmd.Process}
}

// alive reports whether the sentinel is a live, non-zombie process. A bare
// Signal(0) succeeds on an unreaped zombie, which would make the ownership guard
// a false negative if cleanup had killed the sentinel.
func (s *foregroundCleanupSentinel) alive() bool {
	if s.process == nil {
		return false
	}
	_, alive, ok := processSnapshot(s.process.Pid)
	return ok && alive
}

// TestForegroundCleanupSentinelLivenessRejectsZombie pins the zombie-aware
// liveness the outer guard depends on. A killed-but-unreaped child is the exact
// state a name-wide or group-wide kill would leave the sentinel in, so a guard
// that accepts a zombie cannot fail when cleanup escapes fixture ownership.
func TestForegroundCleanupSentinelLivenessRejectsZombie(t *testing.T) {
	cmd := exec.Command("sleep", "600")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start zombie fixture: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})
	sentinel := &foregroundCleanupSentinel{process: cmd.Process}
	if !sentinel.alive() {
		t.Fatal("sentinel liveness rejected a running process")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill zombie fixture: %v", err)
	}
	waitForZombie(t, cmd.Process.Pid)
	if sentinel.alive() {
		t.Fatal("sentinel liveness accepted an unreaped zombie; the ownership guard is a false negative")
	}
}

func waitForZombie(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if state, ok := processState(pid); ok && state == 'Z' {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d never became an unreaped zombie", pid)
}

// TestKillOwnedProcessRequiresRecordedIdentity pins the fail-closed kill: an
// empty or mismatched recorded identity must never authorize touching a live
// process, while a matching identity does clean its owned target.
func TestKillOwnedProcessRequiresRecordedIdentity(t *testing.T) {
	sentinel := startForegroundCleanupSentinel(t)

	// (i) An empty identity must never authorize a kill.
	if killOwnedProcess(sentinel.process.Pid, "") {
		t.Fatal("empty recorded identity authorized a kill")
	}
	if !sentinel.alive() {
		t.Fatal("empty recorded identity killed a live sentinel")
	}

	// (ii) A mismatched identity must never authorize a kill.
	identity, ok := ownedProcessIdentity(sentinel.process.Pid)
	if !ok {
		t.Fatal("could not read the sentinel identity")
	}
	if killOwnedProcess(sentinel.process.Pid, identity+"-not-the-sentinel") {
		t.Fatal("mismatched recorded identity authorized a kill")
	}
	if !sentinel.alive() {
		t.Fatal("mismatched recorded identity killed a live sentinel")
	}

	// (iii) A matching identity does clean its owned target.
	if !killOwnedProcess(sentinel.process.Pid, identity) {
		t.Fatal("matching recorded identity did not kill the owned target")
	}
	_, _ = sentinel.process.Wait()
	if _, _, ok := processSnapshot(sentinel.process.Pid); ok {
		t.Fatal("killed owned target was not reaped")
	}
}

// TestOwnedProcessSurvivesCountsAnUnreapedZombie pins that a leaked fixture
// process is measured by existence, not liveness: a killed-but-unreaped child is
// a zombie that survives for the guard's purpose, so it must be reported as a
// survivor rather than as clean.
func TestOwnedProcessSurvivesCountsAnUnreapedZombie(t *testing.T) {
	cmd := exec.Command("sleep", "600")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start zombie fixture: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})
	identity, ok := ownedProcessIdentity(cmd.Process.Pid)
	if !ok {
		t.Fatal("could not read the zombie fixture identity")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill zombie fixture: %v", err)
	}
	waitForZombie(t, cmd.Process.Pid)
	if !ownedProcessSurvives(cmd.Process.Pid, identity, 200*time.Millisecond) {
		t.Fatal("an unreaped zombie was reported clean; a lingering zombie must count as a survivor")
	}
}

// TestTerminateOwnedProcessTreeReapsDescendant is the real fallback-teardown
// reproducer: a shell with the fixture's TERM trap and a background child are
// both alive when termination starts. The trap writes a marker before it kills
// the child, so the marker proves the shell received SIGTERM first and ran its
// trap; the old parent-first SIGKILL bypasses the trap and leaves the marker
// absent on any host. Both processes must also be gone, not zombies.
func TestTerminateOwnedProcessTreeReapsDescendant(t *testing.T) {
	dir := t.TempDir()
	shellPIDPath := filepath.Join(dir, "shell.pid")
	childPIDPath := filepath.Join(dir, "child.pid")
	gatePath := filepath.Join(dir, "gate")
	trapMarkerPath := filepath.Join(dir, "trap-ran")
	script := fmt.Sprintf(
		"sleep 600 & child=$!; "+
			"trap 'printf ran > %s; kill \"$child\" 2>/dev/null; wait \"$child\" 2>/dev/null; exit 0' TERM; "+
			"printf '%%s' \"$child\" > %s; printf '%%s' $$ > %s; "+
			"while [ ! -f %s ]; do sleep 0.01; done; "+
			"kill \"$child\" 2>/dev/null; wait \"$child\" 2>/dev/null",
		strconv.Quote(trapMarkerPath), strconv.Quote(childPIDPath), strconv.Quote(shellPIDPath), strconv.Quote(gatePath))
	cmd := exec.Command("sh", "-c", script)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start terminating shell: %v", err)
	}
	waited := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(waited)
	}()
	t.Cleanup(foregroundShellCleanup(cmd, waited, gatePath, childPIDPath))

	shellPIDData, ok := waitForFileContent(shellPIDPath, 10*time.Second)
	if !ok {
		t.Fatalf("terminating shell never wrote its PID file %s", shellPIDPath)
	}
	shellPID, err := strconv.Atoi(strings.TrimSpace(shellPIDData))
	if err != nil || shellPID <= 0 {
		t.Fatalf("parse terminating shell PID %q: %v", shellPIDData, err)
	}
	childPIDData, ok := waitForFileContent(childPIDPath, 10*time.Second)
	if !ok {
		t.Fatalf("terminating shell never wrote its child PID file %s", childPIDPath)
	}
	childPID, err := strconv.Atoi(strings.TrimSpace(childPIDData))
	if err != nil || childPID <= 0 {
		t.Fatalf("parse terminating shell child PID %q: %v", childPIDData, err)
	}
	shellIdentity, ok := ownedProcessIdentity(shellPID)
	if !ok {
		t.Fatalf("could not read the terminating shell identity for %d", shellPID)
	}
	childIdentity, ok := ownedProcessIdentity(childPID)
	if !ok {
		t.Fatalf("could not read the terminating shell child identity for %d", childPID)
	}
	if !ownedProcessExistsWithIdentity(shellPID, shellIdentity) {
		t.Fatalf("terminating shell %d was not alive before termination", shellPID)
	}
	if !ownedProcessExistsWithIdentity(childPID, childIdentity) {
		t.Fatalf("terminating shell child %d was not alive before termination", childPID)
	}

	if !terminateOwnedProcessTree(shellPID, shellIdentity, childPID, childIdentity, 2*time.Second) {
		t.Fatal("terminateOwnedProcessTree signaled nothing while both processes were alive")
	}
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("terminating shell was not reaped after termination")
	}
	if _, err := os.Stat(trapMarkerPath); err != nil {
		t.Fatalf("the shell's TERM trap never ran (marker %s missing), so SIGTERM did not reach the shell first: %v", trapMarkerPath, err)
	}
	if _, _, ok := processSnapshot(shellPID); ok {
		t.Fatalf("terminating shell %d still exists after termination", shellPID)
	}
	if _, _, ok := processSnapshot(childPID); ok {
		t.Fatalf("terminating shell child %d still exists after termination; a reaper was removed first", childPID)
	}
}

// TestForegroundShellCleanupReapsDescendantBeforeCapture drives the cleanup
// the regression test registers on an early failure that happens BEFORE any PID
// or identity capture and WITHOUT calling terminateOwnedProcessTree. The shell
// and its child are real, the gate is still held when cleanup is invoked, and
// cleanup must release the gate so the shell's own path kills and waits the
// child. Both must be gone (not zombies) and the shell's wait must have joined
// within the bound. This fails against the old parent-only SIGKILL cleanup,
// which orphans the child.
func TestForegroundShellCleanupReapsDescendantBeforeCapture(t *testing.T) {
	dir := t.TempDir()
	shellPIDPath := filepath.Join(dir, "shell.pid")
	childPIDPath := filepath.Join(dir, "child.pid")
	gatePath := filepath.Join(dir, "gate")
	trapMarkerPath := filepath.Join(dir, "trap-ran")
	script := fmt.Sprintf(
		"sleep 600 & child=$!; "+
			"trap 'printf ran > %s; kill \"$child\" 2>/dev/null; wait \"$child\" 2>/dev/null; exit 0' TERM; "+
			"printf '%%s' \"$child\" > %s; printf '%%s' $$ > %s; "+
			"while [ ! -f %s ]; do sleep 0.01; done; "+
			"kill \"$child\" 2>/dev/null; wait \"$child\" 2>/dev/null",
		strconv.Quote(trapMarkerPath), strconv.Quote(childPIDPath), strconv.Quote(shellPIDPath), strconv.Quote(gatePath))
	cmd := exec.Command("sh", "-c", script)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start fixture shell: %v", err)
	}
	waited := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(waited)
	}()

	// The exact cleanup the regression test registers, with only the direct
	// child handle and paths: no PID or identity from the fixture's files.
	cleanup := foregroundShellCleanup(cmd, waited, gatePath, childPIDPath)
	t.Cleanup(cleanup)

	// Wait for the fixtures to publish their PIDs, then capture only what the
	// assertions need. The cleanup above was already registered without them.
	shellPIDData, ok := waitForFileContent(shellPIDPath, 10*time.Second)
	if !ok {
		t.Fatalf("fixture shell never wrote its PID file %s", shellPIDPath)
	}
	if pid, err := strconv.Atoi(strings.TrimSpace(shellPIDData)); err != nil || pid != cmd.Process.Pid {
		t.Fatalf("shell PID file %q does not match the started shell %d", shellPIDData, cmd.Process.Pid)
	}
	childPIDData, ok := waitForFileContent(childPIDPath, 10*time.Second)
	if !ok {
		t.Fatalf("fixture shell never wrote its child PID file %s", childPIDPath)
	}
	childPID, err := strconv.Atoi(strings.TrimSpace(childPIDData))
	if err != nil || childPID <= 0 {
		t.Fatalf("parse fixture shell child PID %q: %v", childPIDData, err)
	}
	shellIdentity, ok := ownedProcessIdentity(cmd.Process.Pid)
	if !ok {
		t.Fatalf("could not read the fixture shell identity for %d", cmd.Process.Pid)
	}
	childIdentity, ok := ownedProcessIdentity(childPID)
	if !ok {
		t.Fatalf("could not read the fixture shell child identity for %d", childPID)
	}
	// A red run against the old cleanup leaks the child; remove it on the way
	// out, by verified identity only.
	t.Cleanup(func() {
		if !killOwnedProcess(childPID, childIdentity) {
			return
		}
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) && ownedProcessExistsWithIdentity(childPID, childIdentity) {
			time.Sleep(25 * time.Millisecond)
		}
	})
	if !ownedProcessExistsWithIdentity(cmd.Process.Pid, shellIdentity) ||
		!ownedProcessExistsWithIdentity(childPID, childIdentity) {
		t.Fatal("fixture shell and child were not both alive before cleanup")
	}

	cleanup()
	cleanup() // idempotent: a second invocation must be a no-op

	select {
	case <-waited:
	case <-time.After(10 * time.Second):
		t.Fatal("registered cleanup did not join the fixture shell within the bound")
	}
	if ownedProcessExistsWithIdentity(cmd.Process.Pid, shellIdentity) {
		t.Fatalf("fixture shell %d still exists after the registered cleanup (zombie or live)", cmd.Process.Pid)
	}
	if ownedProcessExistsWithIdentity(childPID, childIdentity) {
		t.Fatalf("fixture shell child %d still exists after the registered cleanup (zombie or live)", childPID)
	}
	if _, err := os.Stat(trapMarkerPath); err == nil {
		t.Fatal("cleanup used SIGTERM on the shell instead of the shell's normal gate path")
	}
}
