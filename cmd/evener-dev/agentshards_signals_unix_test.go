//go:build unix

package dev

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// startHeldRun starts the evener-dev binary against the fixture with one shard
// held open, and returns the running command, the held test-binary pid, the
// scratch TMPDIR, and the hold directory. extraEnv arms fixture behaviour in
// the shard processes.
func startHeldRun(t *testing.T, extraEnv ...string) (*exec.Cmd, int, string, string) {
	t.Helper()
	bin := buildEvenerDev(t)
	tmp := t.TempDir()
	resolvedTmp, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		t.Fatalf("resolving TMPDIR: %v", err)
	}
	holdDir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(holdDir, "hold.fifo"), 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	workRoot := t.TempDir()
	if err := os.Symlink(fixtureModule(t), filepath.Join(workRoot, "agent")); err != nil {
		t.Fatalf("linking fixture as ./agent: %v", err)
	}

	cmd := exec.Command(bin, "dev", "agent-shards")
	cmd.Dir = workRoot
	cmd.Env = append(os.Environ(),
		"TMPDIR="+tmp,
		"GOWORK=off", // the fixture module lives outside the repo workspace
		"SHARD_FIXTURE_HOLD="+holdDir,
		"AGENT_SHARD_COUNT=2",
		"AGENT_SHARD_PARALLEL=1",
		"AGENT_SHARD_NO_SURVEY=1",
		"AGENT_SHARD_CACHE_DIR="+filepath.Join(t.TempDir(), "cache"),
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting held run: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	// Await the hold announcement; the ceiling is a tripwire sized for a
	// cold `go test -c` under load, not a mechanism.
	deadline := time.Now().Add(120 * time.Second)
	for {
		matches, _ := filepath.Glob(filepath.Join(holdDir, "held.*"))
		if len(matches) == 1 {
			var heldPid int
			if _, err := fmt.Sscanf(filepath.Base(matches[0]), "held.%d", &heldPid); err != nil {
				t.Fatalf("parsing held pid from %s: %v", matches[0], err)
			}
			return cmd, heldPid, resolvedTmp, holdDir
		}
		if time.Now().After(deadline) {
			t.Fatalf("held shard never announced; stderr:\n%s", &stderr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// awaitFile waits for a path to appear, failing with label if it never does.
func awaitFile(t *testing.T, path, label string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: %s never appeared", label, path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// releaseHold unblocks a held shard by opening the write end of its FIFO.
// Only safe while the shard is still reading: an open with no reader blocks.
func releaseHold(t *testing.T, holdDir string) {
	t.Helper()
	fifo, err := os.OpenFile(filepath.Join(holdDir, "hold.fifo"), os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("opening hold fifo for release: %v", err)
	}
	_, _ = fifo.WriteString("x")
	_ = fifo.Close()
}

func awaitPidGone(t *testing.T, pid int, label string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: pid %d still alive", label, pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestAgentShardsSIGTERMExits143RetainsLogsReapsChildren(t *testing.T) {
	cmd, heldPid, tmp, _ := startHeldRun(t)

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signaling runner: %v", err)
	}
	err := cmd.Wait()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 143 {
		t.Fatalf("interrupted runner exit = %v, want exit status 143", err)
	}
	stderr := cmd.Stderr.(*bytes.Buffer).String()
	if !strings.Contains(stderr, "interrupted by SIGTERM") {
		t.Fatalf("interruption not explained on stderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, "full logs: ") {
		t.Fatalf("interrupted run did not point at retained logs:\n%s", stderr)
	}
	want := filepath.Join(tmp, fmt.Sprintf("agent-test-shards.%d", cmd.Process.Pid))
	if _, statErr := os.Stat(want); statErr != nil {
		t.Fatalf("interrupted run's scratch %s not retained: %v", want, statErr)
	}
	awaitPidGone(t, heldPid, "held shard child after SIGTERM")
}

// TestAgentShardsSecondSignalEndsAWedgedRun pins the second Ctrl-C. A shard
// that ignores TERM leaves the runner waiting on it forever; the script's
// first handler cleared its traps, so the next signal took its default action
// and ended the run. Relaying only one signal makes the runner deaf to every
// signal after the first, and SIGKILL becomes the only way out.
func TestAgentShardsSecondSignalEndsAWedgedRun(t *testing.T) {
	cmd, heldPid, tmp, holdDir := startHeldRun(t, "SHARD_FIXTURE_IGNORE_TERM=1")

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("first SIGTERM: %v", err)
	}
	// The shard announcing the TERM it swallowed proves the runner handled
	// the first signal and forwarded it, so the second is never mistaken for
	// the first.
	awaitFile(t, filepath.Join(holdDir, fmt.Sprintf("termed.%d", heldPid)),
		"held shard never saw the runner's forwarded TERM")

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("second SIGTERM: %v", err)
	}
	// A deaf runner hangs here; the killer bounds the wait and its firing IS
	// the failure.
	killed := make(chan struct{})
	killer := time.AfterFunc(20*time.Second, func() {
		_ = cmd.Process.Signal(syscall.SIGKILL)
		close(killed)
	})
	err := cmd.Wait()
	timely := killer.Stop()
	if !timely {
		<-killed
	}
	// Whatever happened to the runner, the held shard is orphaned now: it
	// ignores TERM, so KILL is what ends it.
	_ = syscall.Kill(heldPid, syscall.SIGKILL)
	awaitPidGone(t, heldPid, "held shard child after the run ended")
	if !timely {
		t.Fatalf("second SIGTERM did not end the runner; it had to be SIGKILLed after 20s (wait: %v)", err)
	}

	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 143 {
		t.Fatalf("second SIGTERM: runner exit = %v, want exit status 143", err)
	}
	stderr := cmd.Stderr.(*bytes.Buffer).String()
	if !strings.Contains(stderr, "interrupted by SIGTERM") {
		t.Fatalf("first interruption not explained on stderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, "SIGTERM again") {
		t.Fatalf("second signal not explained on stderr:\n%s", stderr)
	}
	// The logs the first signal retained are still on disk for diagnosis.
	want := filepath.Join(tmp, fmt.Sprintf("agent-test-shards.%d", cmd.Process.Pid))
	if _, statErr := os.Stat(want); statErr != nil {
		t.Fatalf("second-signal run's scratch %s not retained: %v", want, statErr)
	}
}

func TestAgentShardsSIGKILLLeftoverIsReclaimedByNextRun(t *testing.T) {
	cmd, heldPid, tmp, holdDir := startHeldRun(t)

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("SIGKILLing runner: %v", err)
	}
	_ = cmd.Wait()

	leftover := filepath.Join(tmp, fmt.Sprintf("agent-test-shards.%d", cmd.Process.Pid))
	if _, err := os.Stat(leftover); err != nil {
		t.Fatalf("SIGKILL should leave scratch behind: %v", err)
	}

	// Release the orphaned held child (nothing reparents it once the runner
	// is KILLed), and wait for it to be truly gone before reclaiming.
	releaseHold(t, holdDir)
	awaitPidGone(t, heldPid, "held shard child after release")

	// The next run of the same tool — same TMPDIR, hold disarmed — reclaims
	// the dead runner's scratch and completes green.
	t.Setenv("TMPDIR", tmp)
	t.Setenv("GOWORK", "off")
	var stdout, stderr bytes.Buffer
	cfg := shardsConfig{
		agentDir: fixtureModule(t),
		count:    2,
		parallel: 1,
		noSurvey: true,
		cacheDir: filepath.Join(t.TempDir(), "cache"),
		stdout:   &stdout,
		stderr:   &stderr,
	}
	if rc := runShards(cfg); rc != 0 {
		t.Fatalf("next run rc = %d\nstdout:\n%s\nstderr:\n%s", rc, &stdout, &stderr)
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Fatalf("next run did not reclaim the SIGKILL leftover %s: %v", leftover, err)
	}
}
