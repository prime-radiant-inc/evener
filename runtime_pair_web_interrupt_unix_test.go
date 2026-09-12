//go:build unix

package evener_test

import (
	"bytes"
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

func TestMakeTestWebInterruptDoesNotSignalReapedCheck(t *testing.T) {
	fixture := newBuildWebFixture(t)
	frontendDir := filepath.Join(fixture.root, "cmd", "evener-hub", "frontend")
	writeTestFile(t, filepath.Join(frontendDir, "package-lock.json"), []byte("{}\n"), 0o644)
	writeTestFile(t, filepath.Join(frontendDir, "package.json"), []byte("{}\n"), 0o644)
	heldReady := filepath.Join(fixture.root, "held-npm.ready")
	heldPID := filepath.Join(fixture.root, "held-npm.pid")
	reapedPID := filepath.Join(fixture.root, "reaped-npm.pid")
	waitedReaped := filepath.Join(fixture.root, "waited-reaped.ready")
	waitRelease := filepath.Join(fixture.root, "wait-release")
	killedReaped := filepath.Join(fixture.root, "killed-reaped")
	recordingShell := filepath.Join(filepath.Dir(fixture.root), "recording-shell")
	if err := syscall.Mkfifo(waitRelease, 0o600); err != nil {
		t.Fatalf("make wait release FIFO: %v", err)
	}
	writeTestFile(t, recordingShell, []byte(`wait() {
  command wait "$@"
  wait_status=$?
  tracked_pid=$(cat "$EVENER_TEST_NPM_TRACK_PID")
  if [ "${1:-}" = "$tracked_pid" ] && [ "${reaped_gate:-0}" -eq 0 ]; then
    reaped_gate=1
	    exec 9<> "$EVENER_TEST_SHELL_WAIT_RELEASE"
    : > "$EVENER_TEST_SHELL_WAITED_REAPED"
	    read -r _ <&9
  fi
  return "$wait_status"
}
kill() {
  tracked_pid=$(cat "$EVENER_TEST_NPM_TRACK_PID")
  for kill_arg in "$@"; do
    [ "$kill_arg" != "$tracked_pid" ] || : > "$EVENER_TEST_SHELL_KILLED_REAPED"
  done
  command kill "$@"
}
`), 0o644)

	// The wait/kill lifecycle lives inside scripts/web/test-web.sh, so the
	// seam moved with it: BASH_ENV lands the recording functions in the
	// script's own bash, where they shadow the builtins it calls.
	command := exec.Command("make", "test-web")
	command.Dir = fixture.root
	command.Env = append(fixture.environment(""),
		"BASH_ENV="+recordingShell,
		"EVENER_TEST_NPM_HOLD_COMMAND=run test",
		"EVENER_TEST_NPM_READY="+heldReady,
		"EVENER_TEST_NPM_PID="+heldPID,
		"EVENER_TEST_NPM_TRACK_COMMAND=run typecheck",
		"EVENER_TEST_NPM_TRACK_PID="+reapedPID,
		"EVENER_TEST_SHELL_WAITED_REAPED="+waitedReaped,
		"EVENER_TEST_SHELL_WAIT_RELEASE="+waitRelease,
		"EVENER_TEST_SHELL_KILLED_REAPED="+killedReaped,
	)
	var output syncBuffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatalf("start make test-web: %v", err)
	}
	// One waiter, started with the child: both readiness waits race against it,
	// and the interrupt assertion below reads the same result.
	run := startChild(command)
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			<-run.done
		}
	})
	if err := waitForPathOrExit(heldReady, run, readinessTripwire); err != nil {
		t.Fatalf("held npm check did not become ready: %v; output = %s", err, output.String())
	}
	if err := waitForPathOrExit(waitedReaped, run, readinessTripwire); err != nil {
		t.Fatalf("Make did not reap the completed typecheck before waiting on the held check: %v; output = %s", err, output.String())
	}
	release, err := os.OpenFile(waitRelease, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open wait release FIFO: %v", err)
	}
	if err := exec.Command("kill", "-TERM", strconv.Itoa(command.Process.Pid)).Run(); err != nil {
		release.Close()
		t.Fatalf("signal make test-web: %v", err)
	}
	if _, err := release.WriteString("release\n"); err != nil {
		release.Close()
		t.Fatalf("write wait release FIFO: %v", err)
	}
	if err := release.Close(); err != nil {
		t.Fatalf("close wait release FIFO: %v", err)
	}
	if err := run.wait(); err == nil {
		t.Fatalf("interrupted make test-web exited zero; output = %s", output.String())
	}
	if _, err := os.Stat(killedReaped); !os.IsNotExist(err) {
		t.Fatalf("interrupt signaled a PID after its npm check was reaped: stat err = %v; output = %s", err, output.String())
	}
}

// webWaitShell can reach the first wait before the scheduled fake npm child
// publishes its PID. Synchronize that test-only identity handoff before
// intercepting the exact wait; otherwise the fixture can miss its only seam.
const webWaitShell = `wait() {
  tracked_pid=
  while [ -z "$tracked_pid" ]; do
    tracked_pid=$(cat "$EVENER_TEST_NPM_PID" 2>/dev/null) || tracked_pid=
    [ -n "$tracked_pid" ] || sleep 0.01
  done
  if [ "${1:-}" = "$tracked_pid" ] && [ "${EVENER_TEST_WEB_WAIT_USED:-0}" -eq 0 ]; then
    EVENER_TEST_WEB_WAIT_USED=1
    printf '%s\n' "$$" > "$EVENER_TEST_WEB_WAIT_READY"
    exec 9<> "$EVENER_TEST_WEB_WAIT_RELEASE"
    read -r _ <&9
  fi
  command wait "$@"
  wait_status=$?
  : > "$EVENER_TEST_WEB_WAIT_REAPED"
  if [ "${1:-}" = "$tracked_pid" ] && [ -n "${EVENER_TEST_WEB_STALE_JOB:-}" ] && [ -e "$EVENER_TEST_WEB_STALE_JOB" ]; then
    return 127
  fi
  return "$wait_status"
}
jobs() {
  if [ -n "${EVENER_TEST_WEB_STALE_JOB:-}" ] && [ -e "$EVENER_TEST_WEB_STALE_JOB" ]; then
    cat "$EVENER_TEST_NPM_PID"
    return
  fi
  command jobs "$@"
}
`

func TestWebWaitSeamRetriesTrackedPIDPublication(t *testing.T) {
	root := t.TempDir()
	bashEnv := filepath.Join(root, "wait-shell")
	fakeBin := filepath.Join(root, "bin")
	if err := os.Mkdir(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, bashEnv, []byte(webWaitShell), 0o644)

	publishedPID := filepath.Join(root, "held-typecheck.pid")
	expectedPID := filepath.Join(root, "expected.pid")
	catTripped := filepath.Join(root, "cat.tripped")
	waitReady := filepath.Join(root, "wait.ready")
	waitRelease := filepath.Join(root, "wait.release")
	waitReaped := filepath.Join(root, "wait.reaped")
	writeTestFile(t, waitRelease, []byte("release\n"), 0o600)
	writeTestFile(t, filepath.Join(fakeBin, "cat"), []byte(`#!/bin/sh
if [ "$1" = "$EVENER_TEST_NPM_PID" ] && [ ! -e "$EVENER_TEST_CAT_TRIPPED" ]; then
  : > "$EVENER_TEST_CAT_TRIPPED"
  IFS= read -r pid < "$EVENER_TEST_EXPECTED_PID"
  printf '%s\n' "$pid" > "$EVENER_TEST_NPM_PID"
  exit 1
fi
while IFS= read -r line; do printf '%s\n' "$line"; done < "$1"
`), 0o755)

	command := exec.Command("bash", "-c", `bash -c 'exit 0' &
printf '%s\n' "$!" > "$EVENER_TEST_EXPECTED_PID"
wait "$!"
`)
	command.Env = []string{
		"BASH_ENV=" + bashEnv,
		"EVENER_TEST_CAT_TRIPPED=" + catTripped,
		"EVENER_TEST_EXPECTED_PID=" + expectedPID,
		"EVENER_TEST_NPM_PID=" + publishedPID,
		"EVENER_TEST_WEB_WAIT_READY=" + waitReady,
		"EVENER_TEST_WEB_WAIT_RELEASE=" + waitRelease,
		"EVENER_TEST_WEB_WAIT_REAPED=" + waitReaped,
		"PATH=" + fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("exercise wait seam: %v; output = %s", err, output)
	}
	if _, err := os.Stat(waitReady); err != nil {
		t.Fatalf("wait seam missed the tracked PID after its first read raced publication: %v; output = %s", err, output)
	}
	if _, err := os.Stat(waitReaped); err != nil {
		t.Fatalf("wait seam did not reap the tracked child: %v; output = %s", err, output)
	}
}

func TestMakeTestWebInterruptAtWaitHandoff(t *testing.T) {
	for _, signal := range []string{"TERM", "INT"} {
		t.Run(signal, func(t *testing.T) {
			runWebWaitHandoff(t, signal, false, false)
		})
	}
}

func TestMakeTestWebInterruptAtWaitHandoffRejectsLostSignalMutation(t *testing.T) {
	for _, signal := range []string{"TERM", "INT"} {
		t.Run(signal, func(t *testing.T) {
			runWebWaitHandoff(t, signal, true, false)
		})
	}
}

func TestMakeTestWebInterruptHandlesStaleRunningJobAfterWaitLosesOwnership(t *testing.T) {
	runWebWaitHandoff(t, "TERM", false, true)
}

// runWebWaitHandoff drives the actual test-web shell at the boundary immediately
// before its exact owned-child wait. The mutation restores the old
// defer-signals-before-wait ordering; it must remain blocked after the parent
// signal until the held child is independently released, proving this test is
// mechanism RED rather than a missing production hook.
func runWebWaitHandoff(t *testing.T, signal string, mutate, simulateStaleJob bool) {
	t.Helper()
	fixture := newBuildWebFixture(t)
	frontendDir := filepath.Join(fixture.root, "cmd", "evener-hub", "frontend")
	writeTestFile(t, filepath.Join(frontendDir, "package-lock.json"), []byte("{}\n"), 0o644)
	writeTestFile(t, filepath.Join(frontendDir, "package.json"), []byte("{}\n"), 0o644)
	if mutate {
		mutateWebWaitDeferral(t, fixture)
	}
	npmPIDPath := filepath.Join(fixture.root, "held-typecheck.pid")
	npmReadyPath := filepath.Join(fixture.root, "held-typecheck.ready")
	npmReleasePath := filepath.Join(fixture.root, "held-typecheck.release")
	npmTermPath := filepath.Join(fixture.root, "held-typecheck.term")
	waitReadyPath := filepath.Join(fixture.root, "wait.ready")
	waitReleasePath := filepath.Join(fixture.root, "wait.release")
	waitReapedPath := filepath.Join(fixture.root, "wait.reaped")
	staleJobPath := filepath.Join(fixture.root, "stale-job")
	staleJobControl := ""
	if simulateStaleJob {
		writeTestFile(t, staleJobPath, nil, 0o600)
		staleJobControl = staleJobPath
	}
	for _, path := range []string{npmReadyPath, npmReleasePath, waitReadyPath, waitReleasePath} {
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			t.Fatalf("make FIFO %s: %v", path, err)
		}
	}
	bashEnv := filepath.Join(fixture.root, "wait-shell")
	writeTestFile(t, bashEnv, []byte(webWaitShell), 0o644)

	childRelease, err := os.OpenFile(npmReleasePath, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open held-child release FIFO: %v", err)
	}
	npmReady, err := os.OpenFile(npmReadyPath, os.O_RDWR, 0)
	if err != nil {
		childRelease.Close()
		t.Fatalf("open held-child readiness FIFO: %v", err)
	}
	waitReady, err := os.OpenFile(waitReadyPath, os.O_RDWR, 0)
	if err != nil {
		childRelease.Close()
		npmReady.Close()
		t.Fatalf("open wait readiness FIFO: %v", err)
	}
	waitRelease, err := os.OpenFile(waitReleasePath, os.O_RDWR, 0)
	if err != nil {
		childRelease.Close()
		npmReady.Close()
		waitReady.Close()
		t.Fatalf("open wait release FIFO: %v", err)
	}

	command := exec.Command("make", "test-web")
	command.Dir = fixture.root
	command.Env = append(fixture.environment(""),
		"BASH_ENV="+bashEnv,
		"EVENER_TEST_NPM_HOLD_COMMAND=run typecheck",
		"EVENER_TEST_NPM_PID="+npmPIDPath,
		"EVENER_TEST_NPM_READY="+npmReadyPath,
		"EVENER_TEST_NPM_HOLD_RELEASE="+npmReleasePath,
		"EVENER_TEST_NPM_HOLD_TERM="+npmTermPath,
		"EVENER_TEST_WEB_WAIT_READY="+waitReadyPath,
		"EVENER_TEST_WEB_WAIT_RELEASE="+waitReleasePath,
		"EVENER_TEST_WEB_WAIT_REAPED="+waitReapedPath,
		"EVENER_TEST_WEB_STALE_JOB="+staleJobControl,
	)
	var output syncBuffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		childRelease.Close()
		npmReady.Close()
		waitReady.Close()
		waitRelease.Close()
		t.Fatalf("start make test-web: %v", err)
	}
	run := startChild(command)
	t.Cleanup(func() {
		_ = os.Remove(staleJobPath)
		_, _ = childRelease.WriteString("cleanup\n")
		_ = childRelease.Close()
		_ = npmReady.Close()
		_, _ = waitRelease.WriteString("cleanup\n")
		_ = waitRelease.Close()
		_ = waitReady.Close()
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			<-run.done
		}
	})

	if _, err := readFIFORecord(npmReady, run, readinessTripwire); err != nil {
		t.Fatalf("held child did not install its signal trap: %v; output = %s", err, output.String())
	}
	shellPID, err := readFIFORecord(waitReady, run, readinessTripwire)
	if err != nil {
		t.Fatalf("wait seam did not publish readiness: %v; output = %s", err, output.String())
	}
	if err := exec.Command("kill", "-"+signal, shellPID).Run(); err != nil {
		t.Fatalf("signal test-web shell %s: %v", shellPID, err)
	}
	if _, err := waitRelease.WriteString("release\n"); err != nil {
		t.Fatalf("release pre-wait seam: %v", err)
	}

	if mutate {
		select {
		case <-run.done:
			t.Fatalf("lost-signal mutation exited before held child release; output = %s", output.String())
		case <-time.After(2 * time.Second):
		}
		if _, err := os.Stat(npmTermPath); !os.IsNotExist(err) {
			t.Fatalf("lost-signal mutation terminated held child; stat err = %v; output = %s", err, output.String())
		}
		if _, err := childRelease.WriteString("release\n"); err != nil {
			t.Fatalf("release mutated held child: %v", err)
		}
		if err := waitForChildExit(run, 5*time.Second); err == nil {
			t.Fatalf("lost-signal mutation eventually exited zero; output = %s", output.String())
		} else if errors.Is(err, errChildExitTimeout) {
			t.Fatalf("lost-signal mutation did not exit after held child release: %v; output = %s", err, output.String())
		}
		return
	}

	if err := waitForChildExit(run, 5*time.Second); err == nil {
		t.Fatalf("external %s did not interrupt test-web; output = %s", signal, output.String())
	} else if errors.Is(err, errChildExitTimeout) {
		t.Fatalf("external %s did not interrupt test-web: %v; output = %s", signal, err, output.String())
	}
	if want := map[string]string{"TERM": "Error 143", "INT": "Error 130"}[signal]; !strings.Contains(output.String(), want) {
		t.Fatalf("external %s status was not retained (%q); output = %s", signal, want, output.String())
	}
	if _, err := os.Stat(npmTermPath); err != nil {
		t.Fatalf("held child termination evidence missing: %v; output = %s", err, output.String())
	}
	if _, err := os.Stat(waitReapedPath); err != nil {
		t.Fatalf("exact wait/reap evidence missing: %v; output = %s", err, output.String())
	}
	if retained := fullLogsPath([]byte(output.String())); retained == "" {
		t.Fatalf("interrupted test-web did not retain evidence; output = %s", output.String())
	}
}

func mutateWebWaitDeferral(t *testing.T, fixture runtimeBuildFixture) {
	t.Helper()
	path := filepath.Join(fixture.root, "scripts", "web", "test-web.sh")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read test-web mutation target: %v", err)
	}
	old := `	if wait "$pid"; then check_status=0; else check_status=$?; fi
	# A completed wait removes the job from Bash's job table, which is the
	# completion/ownership handoff and not a PID liveness guess vulnerable to
	# reuse. Signals are not deferred here: Bash must wake this exact wait.
	# Defer cleanup only while that result is committed to the owned PID list.
	defer_signals=1
	if ! owned_job_is_running "$pid"; then
		forget_pid "$pid"
	fi
	defer_signals=0
	printf '%s\n' "$check_status" >"$dir/$c.status"
	consume_interrupt`
	deferredWaitBlock := `	defer_signals=1
	if wait "$pid"; then check_status=0; else check_status=$?; fi
	if ! owned_job_is_running "$pid"; then
		forget_pid "$pid"
	fi
	printf '%s\n' "$check_status" >"$dir/$c.status"
	defer_signals=0
	consume_interrupt`
	if !bytes.Contains(data, []byte(old)) {
		t.Fatal("test-web wait mutation target changed; update the mechanism RED test")
	}
	data = bytes.Replace(data, []byte(old), []byte(deferredWaitBlock), 1)
	writeTestFile(t, path, data, 0o755)
}

func TestMakeTestWebInterruptDuringExitCleanupPreservesStatus(t *testing.T) {
	for _, signal := range []string{"TERM", "INT"} {
		t.Run(signal, func(t *testing.T) {
			fixture := newBuildWebFixture(t)
			frontendDir := filepath.Join(fixture.root, "cmd", "evener-hub", "frontend")
			writeTestFile(t, filepath.Join(frontendDir, "package-lock.json"), []byte("{}\n"), 0o644)
			writeTestFile(t, filepath.Join(frontendDir, "package.json"), []byte("{}\n"), 0o644)
			readyPath := filepath.Join(fixture.root, "cleanup.ready")
			releasePath := filepath.Join(fixture.root, "cleanup.release")
			pidPath := filepath.Join(fixture.root, "cleanup.pid")
			bashEnv := filepath.Join(fixture.root, "cleanup-shell")
			if err := syscall.Mkfifo(releasePath, 0o600); err != nil {
				t.Fatalf("make cleanup release FIFO: %v", err)
			}
			writeTestFile(t, bashEnv, []byte(`rm() {
	case "$0" in *test-web.sh) ;; *) command rm "$@"; return ;; esac
	printf '%s\n' "$$" > "$EVENER_TEST_WEB_CLEANUP_PID"
	: > "$EVENER_TEST_WEB_CLEANUP_READY"
	exec 9<> "$EVENER_TEST_WEB_CLEANUP_RELEASE"
	while :; do read -r _ <&9 && break; done
	command rm "$@"
}
`), 0o644)

			command := exec.Command("make", "test-web")
			command.Dir = fixture.root
			command.Env = append(fixture.environment(""),
				"BASH_ENV="+bashEnv,
				"EVENER_TEST_WEB_CLEANUP_READY="+readyPath,
				"EVENER_TEST_WEB_CLEANUP_RELEASE="+releasePath,
				"EVENER_TEST_WEB_CLEANUP_PID="+pidPath,
			)
			var output syncBuffer
			command.Stdout = &output
			command.Stderr = &output
			if err := command.Start(); err != nil {
				t.Fatalf("start make test-web: %v", err)
			}
			run := startChild(command)
			if err := waitForPathOrExit(readyPath, run, readinessTripwire); err != nil {
				t.Fatalf("cleanup %s did not reach the signal point: %v; output = %s", signal, err, output.String())
			}
			release, err := os.OpenFile(releasePath, os.O_RDWR, 0)
			if err != nil {
				t.Fatalf("open cleanup release FIFO: %v", err)
			}
			pidData, err := os.ReadFile(pidPath)
			if err != nil {
				release.Close()
				t.Fatalf("read cleanup shell pid: %v", err)
			}
			webPID := strings.TrimSpace(string(pidData))
			t.Cleanup(func() {
				_, _ = release.WriteString("cleanup\n")
				_ = release.Close()
				if command.ProcessState == nil {
					_ = command.Process.Kill()
					<-run.done
				}
			})
			if err := exec.Command("kill", "-"+signal, webPID).Run(); err != nil {
				t.Fatalf("signal cleanup shell: %v", err)
			}
			_, _ = release.WriteString("release\n")
			_ = release.Close()
			if err := run.wait(); err == nil {
				t.Fatalf("cleanup %s exited zero; output = %s", signal, output.String())
			} else if !strings.Contains(output.String(), "Error "+map[string]string{"TERM": "143", "INT": "130"}[signal]) {
				t.Fatalf("cleanup %s did not preserve signal status; output = %s", signal, output.String())
			}
			if !strings.Contains(output.String(), "full logs: ") {
				t.Fatalf("cleanup %s did not retain evidence after the signal: output = %s", signal, output.String())
			}
		})
	}
}
