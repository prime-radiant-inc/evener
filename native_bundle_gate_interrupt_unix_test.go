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

// The native bundle gate (scripts/native/test-native-bundle.sh) runs Metro as
// a background job in its own process group, because Metro is a worker pool
// and signalling only the direct child orphans the workers. These tests pin
// the two halves of that contract: every exit path stops the whole group, and
// the group is the only thing ever signalled — never a pid Bash has already
// reaped, whose number may by then name an unrelated process.
//
// The fixture is the real script, copied next to the real scratch-lib, with a
// fake `npx` on PATH standing in for the bundler. Nothing here matches process
// names or reads a global process list; each test signals only the script it
// started and asks about the one worker pid that script's own fixture
// published.

type nativeBundleFixture struct {
	root      string
	fakeBin   string
	workerPID string
	npxReady  string
}

// newNativeBundleFixture lays out the smallest tree the script resolves
// against: itself under scripts/native, scratch-lib under scripts/lib, and the
// mobile-native/node_modules the preflight requires. npxBody becomes the fake
// bundler.
func newNativeBundleFixture(t *testing.T, npxBody string) nativeBundleFixture {
	t.Helper()
	repoRoot, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve fixture root: %v", err)
	}
	copyRepositoryFile(t, repoRoot, root, "scripts/native/test-native-bundle.sh", 0o755)
	copyRepositoryFile(t, repoRoot, root, "scripts/lib/scratch-lib.sh", 0o644)
	for _, dir := range []string{"mobile-native/node_modules", "fake-bin", "tmp", "home"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	fixture := nativeBundleFixture{
		root:      root,
		fakeBin:   filepath.Join(root, "fake-bin"),
		workerPID: filepath.Join(root, "worker.pid"),
		npxReady:  filepath.Join(root, "npx.ready"),
	}
	writeTestFile(t, filepath.Join(fixture.fakeBin, "npx"), []byte(npxBody), 0o755)
	return fixture
}

// command builds the script invocation. The script gets its own process group
// so a test signals exactly the script it started, and its fixture's children
// cannot reach the test binary's own group.
func (f nativeBundleFixture) command(t *testing.T, environment ...string) (*exec.Cmd, *syncBuffer) {
	t.Helper()
	command := exec.Command(filepath.Join(f.root, "scripts", "native", "test-native-bundle.sh"))
	command.Dir = f.root
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Env = append([]string{
		"PATH=" + f.fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + filepath.Join(f.root, "home"),
		"TMPDIR=" + filepath.Join(f.root, "tmp"),
		"EVENER_TEST_WORKER_PID=" + f.workerPID,
		"EVENER_TEST_NPX_READY=" + f.npxReady,
	}, environment...)
	output := &syncBuffer{}
	command.Stdout = output
	command.Stderr = output
	return command, output
}

// A bundler that publishes one worker pid and then holds. The worker is a
// grandchild, so only a signal aimed at the group reaches it.
const nativeBundleHoldingNPX = `#!/bin/sh
sh -c 'printf "%s\n" "$$" > "$EVENER_TEST_WORKER_PID"; : > "$EVENER_TEST_NPX_READY"; while :; do sleep 0.05; done' &
wait
`

// The same bundler with every TERM declined, top to bottom: the shape that
// turns an unbounded post-TERM wait into a hung job.
const nativeBundleTermProofNPX = `#!/bin/sh
trap '' TERM
sh -c 'trap "" TERM; printf "%s\n" "$$" > "$EVENER_TEST_WORKER_PID"; : > "$EVENER_TEST_NPX_READY"; while :; do sleep 0.05; done' &
wait
`

// readWorkerPID returns the one pid the fake bundler published, and registers
// a cleanup that ends it. The cleanup matters on the failure paths: a test
// that gives up before requireWorkerGone (a script that hangs, say) would
// otherwise leave the fixture's worker spinning past the run. The number comes
// from this fixture's own file, never from a process listing.
func (f nativeBundleFixture) readWorkerPID(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile(f.workerPID)
	if err != nil {
		t.Fatalf("read worker pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parse worker pid %q: %v", data, err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	return pid
}

// requireWorkerGone polls until the published worker pid can no longer be
// signalled. The deadline is a hang tripwire, not the mechanism: a correct
// script has already killed the group before its own exit.
func requireWorkerGone(t *testing.T, pid int, output *syncBuffer) {
	t.Helper()
	deadline := time.Now().Add(readinessTripwire)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Do not leave a runaway behind for the next test in this binary.
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("bundler worker %d survived the script's exit; output = %s", pid, output.String())
}

// TestNativeBundleInterruptStopsBundlerProcessGroup is the regression: the
// script's traps used to clean up its scratch and exit while the bundler's
// group ran on, so a Ctrl-C or a cancelled CI job orphaned Metro's workers.
func TestNativeBundleInterruptStopsBundlerProcessGroup(t *testing.T) {
	for _, signal := range []string{"TERM", "INT", "HUP"} {
		t.Run(signal, func(t *testing.T) {
			fixture := newNativeBundleFixture(t, nativeBundleHoldingNPX)
			command, output := fixture.command(t, "EVENER_NATIVE_BUNDLE_TIMEOUT=600")
			if err := command.Start(); err != nil {
				t.Fatalf("start bundle gate: %v", err)
			}
			run := startChild(command)
			t.Cleanup(func() {
				if command.ProcessState == nil {
					_ = command.Process.Kill()
					<-run.done
				}
			})
			if err := waitForPathOrExit(fixture.npxReady, run, readinessTripwire); err != nil {
				t.Fatalf("fake bundler never started: %v; output = %s", err, output.String())
			}
			worker := fixture.readWorkerPID(t)

			if err := command.Process.Signal(signalByName(t, signal)); err != nil {
				t.Fatalf("signal bundle gate: %v", err)
			}
			if err := waitForChildExit(run, readinessTripwire); err == nil {
				t.Fatalf("interrupted bundle gate exited zero; output = %s", output.String())
			} else if errors.Is(err, errChildExitTimeout) {
				t.Fatalf("interrupted bundle gate never exited: %v; output = %s", err, output.String())
			}
			requireWorkerGone(t, worker, output)
			if !strings.Contains(output.String(), "full log: ") {
				t.Fatalf("interrupted bundle gate discarded its evidence; output = %s", output.String())
			}
		})
	}
}

// TestNativeBundleTimeoutEscalatesPastIgnoredTerm covers the other half: the
// timeout's own stop must be bounded, or a bundler that declines TERM turns
// the tripwire into the hang it exists to prevent.
func TestNativeBundleTimeoutEscalatesPastIgnoredTerm(t *testing.T) {
	fixture := newNativeBundleFixture(t, nativeBundleTermProofNPX)
	// Three seconds, not one: the script measures its bound with Bash's
	// whole-second SECONDS, so a one-second bound can elapse before the fake
	// bundler has published the worker this test needs to ask about.
	command, output := fixture.command(t,
		"EVENER_NATIVE_BUNDLE_TIMEOUT=3",
		"EVENER_NATIVE_BUNDLE_STOP_GRACE=1",
	)
	if err := command.Start(); err != nil {
		t.Fatalf("start bundle gate: %v", err)
	}
	run := startChild(command)
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			<-run.done
		}
	})
	if err := waitForPathOrExit(fixture.npxReady, run, readinessTripwire); err != nil {
		t.Fatalf("fake bundler never started: %v; output = %s", err, output.String())
	}
	worker := fixture.readWorkerPID(t)

	if err := waitForChildExit(run, readinessTripwire); err == nil {
		t.Fatalf("timed-out bundle gate exited zero; output = %s", output.String())
	} else if errors.Is(err, errChildExitTimeout) {
		t.Fatalf("timed-out bundle gate hung on a bundler that declines TERM: %v; output = %s", err, output.String())
	}
	requireWorkerGone(t, worker, output)
	if !strings.Contains(output.String(), "timed out after 3s") {
		t.Fatalf("timed-out bundle gate did not name its bound; output = %s", output.String())
	}
}

// nativeBundleRecordingShell shadows the two builtins whose ordering is the
// contract. wait publishes the pid the script reaped; kill records, from that
// moment on, any call naming that pid or its group. It is installed through
// BASH_ENV, so it lands in the script's own bash rather than anywhere else.
const nativeBundleRecordingShell = `wait() {
  case "${1:-}" in
    [0-9]*) [ -s "$EVENER_TEST_BUNDLE_PID" ] || printf '%s\n' "$1" > "$EVENER_TEST_BUNDLE_PID" ;;
  esac
  command wait "$@"
  wait_status=$?
  reaped_pid=$(cat "$EVENER_TEST_BUNDLE_PID" 2>/dev/null) || reaped_pid=
  if [ -n "$reaped_pid" ] && [ "${1:-}" = "$reaped_pid" ]; then
    : > "$EVENER_TEST_REAPED"
  fi
  return "$wait_status"
}
kill() {
  if [ -e "$EVENER_TEST_REAPED" ]; then
    reaped_pid=$(cat "$EVENER_TEST_BUNDLE_PID" 2>/dev/null) || reaped_pid=
    for kill_arg in "$@"; do
      if [ -n "$reaped_pid" ] && { [ "$kill_arg" = "$reaped_pid" ] || [ "$kill_arg" = "-$reaped_pid" ]; }; then
        : > "$EVENER_TEST_SIGNALLED_REAPED"
      fi
    done
  fi
  command kill "$@"
}
`

// A bundler that succeeds immediately, so the script takes its normal reap
// path. The module count line is the shape the PASS line parses.
const nativeBundleSucceedingNPX = `#!/bin/sh
: > "$EVENER_TEST_NPX_READY"
printf 'iOS Bundled 10ms index.ts (3 modules)\n'
`

// TestNativeBundleDoesNotSignalReapedBundler pins the ownership handoff: once
// Bash has reaped the bundler, its pid is no longer this script's to signal,
// because the number can be recycled into an unrelated process group. Deleting
// the clearing assignment after the normal wait fails this.
func TestNativeBundleDoesNotSignalReapedBundler(t *testing.T) {
	fixture := newNativeBundleFixture(t, nativeBundleSucceedingNPX)
	bashEnv := filepath.Join(fixture.root, "recording-shell")
	writeTestFile(t, bashEnv, []byte(nativeBundleRecordingShell), 0o644)
	bundlePID := filepath.Join(fixture.root, "bundle.pid")
	reaped := filepath.Join(fixture.root, "reaped")
	signalledReaped := filepath.Join(fixture.root, "signalled-reaped")
	writeTestFile(t, bundlePID, nil, 0o600)

	command, output := fixture.command(t,
		"BASH_ENV="+bashEnv,
		"EVENER_TEST_BUNDLE_PID="+bundlePID,
		"EVENER_TEST_REAPED="+reaped,
		"EVENER_TEST_SIGNALLED_REAPED="+signalledReaped,
	)
	if err := command.Run(); err != nil {
		t.Fatalf("bundle gate failed on a succeeding bundler: %v; output = %s", err, output.String())
	}
	if !strings.Contains(output.String(), "PASS  native-bundle") {
		t.Fatalf("bundle gate did not report a pass; output = %s", output.String())
	}
	if _, err := os.Stat(reaped); err != nil {
		t.Fatalf("recording shell never saw the bundler reaped, so this test proved nothing: %v; output = %s", err, output.String())
	}
	if _, err := os.Stat(signalledReaped); !os.IsNotExist(err) {
		t.Fatalf("bundle gate signalled the bundler pid after reaping it: stat err = %v; output = %s", err, output.String())
	}
}

func signalByName(t *testing.T, name string) os.Signal {
	t.Helper()
	switch name {
	case "TERM":
		return syscall.SIGTERM
	case "INT":
		return syscall.SIGINT
	case "HUP":
		return syscall.SIGHUP
	}
	t.Fatalf("unhandled signal name %q", name)
	return nil
}
