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
		killOwnedProcess(report.ShellPID, report.ShellIdentity)
		killOwnedProcess(report.DescendantPID, report.DescendantIdentity)
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

// killOwnedProcess kills only a process whose recorded identity is non-empty and
// still matches the current one, so an empty or mismatched identity never
// authorizes a kill. It reports whether it signaled.
func killOwnedProcess(pid int, identity string) bool {
	if pid <= 0 || identity == "" {
		return false
	}
	got, _, ok := processSnapshot(pid)
	if !ok || got != identity {
		return false
	}
	return syscall.Kill(pid, syscall.SIGKILL) == nil
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
