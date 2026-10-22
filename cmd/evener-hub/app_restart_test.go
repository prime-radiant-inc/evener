package hub

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/selfupdate"
)

func TestHubRestartRefusesUnsupportedPlatform(t *testing.T) {
	previous := hubRestartSupported
	hubRestartSupported = func() bool { return false }
	t.Cleanup(func() { hubRestartSupported = previous })

	if _, err := hubRestart(context.Background(), appwire.HubRestartParams{}); !errors.Is(err, errInPlaceRestartUnsupported) {
		t.Fatalf("hubRestart error = %v, want %v", err, errInPlaceRestartUnsupported)
	}
}

func TestHubRestartRefusesMissingLaunchPath(t *testing.T) {
	previousSupported := hubRestartSupported
	hubRestartSupported = func() bool { return true }
	t.Cleanup(func() { hubRestartSupported = previousSupported })

	previousPath := hubLaunchEntrypoint
	hubLaunchEntrypoint = ""
	t.Cleanup(func() { hubLaunchEntrypoint = previousPath })

	if _, err := hubRestart(context.Background(), appwire.HubRestartParams{}); err == nil {
		t.Fatal("hubRestart accepted an empty launch path")
	}
}

func TestHubRestartRefusesNonEvenerTarget(t *testing.T) {
	previousSupported := hubRestartSupported
	hubRestartSupported = func() bool { return true }
	t.Cleanup(func() { hubRestartSupported = previousSupported })

	previousPath := hubLaunchEntrypoint
	hubLaunchEntrypoint = writeProbeScript(t, "echo 'git version 2.43.0'")
	t.Cleanup(func() { hubLaunchEntrypoint = previousPath })

	if _, err := hubRestart(context.Background(), appwire.HubRestartParams{}); err == nil {
		t.Fatal("hubRestart accepted a non-Evener binary")
	}
}

func TestHubRestartRefusesHangingTarget(t *testing.T) {
	previousSupported := hubRestartSupported
	hubRestartSupported = func() bool { return true }
	t.Cleanup(func() { hubRestartSupported = previousSupported })

	previousPath := hubLaunchEntrypoint
	hubLaunchEntrypoint = writeProbeScript(t, "sleep 5")
	t.Cleanup(func() { hubLaunchEntrypoint = previousPath })

	previousTimeout := hubProbeTimeout
	hubProbeTimeout = 50 * time.Millisecond
	t.Cleanup(func() { hubProbeTimeout = previousTimeout })

	if _, err := hubRestart(context.Background(), appwire.HubRestartParams{}); !errors.Is(err, errHubProbeTimeout) {
		t.Fatalf("hubRestart error = %v, want the probe timeout", err)
	}
}

func TestHubRestartSchedulesAtLaunchPath(t *testing.T) {
	setBuild(t, "abc1234", "release")
	t.Cleanup(func() { hubUpdateMu = sync.Mutex{} })

	previousSupported := hubRestartSupported
	hubRestartSupported = func() bool { return true }
	t.Cleanup(func() { hubRestartSupported = previousSupported })

	path := writeProbeScript(t, "echo 'evener abc1234 (2026-10-21T00:00:00Z)'")
	previousPath := hubLaunchEntrypoint
	hubLaunchEntrypoint = path
	t.Cleanup(func() { hubLaunchEntrypoint = previousPath })

	previousArgs := hubProcessArgs
	hubProcessArgs = func() []string { return []string{path, "hub", "-addr", "127.0.0.1:9180"} }
	t.Cleanup(func() { hubProcessArgs = previousArgs })

	startedAt := time.Date(2026, 10, 21, 12, 0, 0, 123, time.UTC)
	previousStarted := hubProcessStartedAt
	hubProcessStartedAt = startedAt
	t.Cleanup(func() { hubProcessStartedAt = previousStarted })

	shareDir := t.TempDir()
	previousShare := hubLaunchShareBinDir
	hubLaunchShareBinDir = shareDir
	t.Cleanup(func() { hubLaunchShareBinDir = previousShare })

	type scheduleCall struct {
		pin    restartPin
		binary string
		args   []string
	}
	var calls []scheduleCall
	previousSchedule := scheduleHubRestartOnly
	scheduleHubRestartOnly = func(_ context.Context, pin restartPin, binary string, args []string) {
		calls = append(calls, scheduleCall{pin: pin, binary: binary, args: args})
	}
	t.Cleanup(func() { scheduleHubRestartOnly = previousSchedule })

	resp, err := hubRestart(context.Background(), appwire.HubRestartParams{})
	if err != nil {
		t.Fatalf("hubRestart: %v", err)
	}
	if !resp.Restarting {
		t.Fatal("response did not report restarting")
	}
	if resp.TargetVersion != "abc1234" {
		t.Fatalf("target version = %q, want abc1234", resp.TargetVersion)
	}
	if resp.CurrentVersion != buildinfo.Version() {
		t.Fatalf("current version = %q, want %q", resp.CurrentVersion, buildinfo.Version())
	}
	if !resp.Incarnation.Equal(startedAt) {
		t.Fatalf("incarnation = %v, want %v", resp.Incarnation, startedAt)
	}
	if len(calls) != 1 {
		t.Fatalf("scheduled %d restarts, want 1", len(calls))
	}
	if calls[0].binary != path || calls[0].pin.path != path || calls[0].pin.sha256Hex == "" || calls[0].pin.shareBinDir != shareDir {
		t.Fatalf("scheduled call = %+v, want the pinned launch path", calls[0])
	}
	if len(calls[0].args) != 3 || calls[0].args[0] != "hub" {
		t.Fatalf("restart args = %v, want the process arguments after argv[0]", calls[0].args)
	}
}

func TestHubRestartRefusesWhenUpdateInProgress(t *testing.T) {
	setBuild(t, "abc1234", "release")
	t.Cleanup(func() { hubUpdateMu = sync.Mutex{} })

	previousSupported := hubRestartSupported
	hubRestartSupported = func() bool { return true }
	t.Cleanup(func() { hubRestartSupported = previousSupported })

	previousPath := hubLaunchEntrypoint
	hubLaunchEntrypoint = writeProbeScript(t, "echo 'evener abc1234 (2026-10-21T00:00:00Z)'")
	t.Cleanup(func() { hubLaunchEntrypoint = previousPath })

	hubUpdateMu.Lock()
	if _, err := hubRestart(context.Background(), appwire.HubRestartParams{}); !errors.Is(err, errHubUpdateInProgress) {
		t.Fatalf("hubRestart error = %v, want %v", err, errHubUpdateInProgress)
	}
}

func TestHubRestartAbortsWhenTargetChangesDuringProbe(t *testing.T) {
	setBuild(t, "abc1234", "release")
	t.Cleanup(func() { hubUpdateMu = sync.Mutex{} })

	previousSupported := hubRestartSupported
	hubRestartSupported = func() bool { return true }
	t.Cleanup(func() { hubRestartSupported = previousSupported })

	path := writeProbeScript(t, "echo 'evener abc1234 (2026-10-21T00:00:00Z)'")
	previousPath := hubLaunchEntrypoint
	hubLaunchEntrypoint = path
	t.Cleanup(func() { hubLaunchEntrypoint = previousPath })
	previousErr := errHubLaunchEntrypoint
	errHubLaunchEntrypoint = nil
	t.Cleanup(func() { errHubLaunchEntrypoint = previousErr })

	previousProbe := probeHubBinaryFn
	probeHubBinaryFn = func(path string) (string, error) {
		if err := os.WriteFile(path, []byte("changed"), 0o755); err != nil {
			t.Fatal(err)
		}
		return "abc1234", nil
	}
	t.Cleanup(func() { probeHubBinaryFn = previousProbe })

	if _, err := hubRestart(context.Background(), appwire.HubRestartParams{}); err == nil {
		t.Fatal("hubRestart accepted a target that changed during its probe")
	}
}

func TestHubRestartOnlyExecFailureReleasesLock(t *testing.T) {
	previousDelay := hubRestartDelay
	hubRestartDelay = 0
	t.Cleanup(func() { hubRestartDelay = previousDelay })

	previousExec := execHubBinary
	execHubBinary = func(string, []string) error { return errors.New("exec failed") }
	t.Cleanup(func() { execHubBinary = previousExec })

	stderr := newSyncWriter("restart failed")
	previousStderr := hubUpdateStderr
	hubUpdateStderr = stderr
	t.Cleanup(func() { hubUpdateStderr = previousStderr })

	hubUpdateMu = sync.Mutex{}
	hubUpdateMu.Lock()
	t.Cleanup(func() { hubUpdateMu = sync.Mutex{} })

	path := writeProbeScript(t, "echo 'evener abc1234 (2026-10-21T00:00:00Z)'")
	pin, err := pinInstalledBinary(path)
	if err != nil {
		t.Fatal(err)
	}
	scheduleHubRestartOnlyAfterResponse(context.Background(), pin, path, []string{"hub"})

	<-stderr.done
	if !hubUpdateMu.TryLock() {
		t.Fatal("failed exec left the update mutex locked")
	}
	hubUpdateMu.Unlock()
}

func TestHubRestartOnlyUsesManagedInstallLock(t *testing.T) {
	previousDelay := hubRestartDelay
	hubRestartDelay = 0
	t.Cleanup(func() { hubRestartDelay = previousDelay })

	previousExec := execHubBinary
	execHubBinary = func(string, []string) error { return errors.New("exec failed") }
	t.Cleanup(func() { execHubBinary = previousExec })

	stderr := newSyncWriter("restart failed")
	previousStderr := hubUpdateStderr
	hubUpdateStderr = stderr
	t.Cleanup(func() { hubUpdateStderr = previousStderr })

	hubUpdateMu = sync.Mutex{}
	hubUpdateMu.Lock()
	t.Cleanup(func() { hubUpdateMu = sync.Mutex{} })

	path := writeProbeScript(t, "echo 'evener abc1234 (2026-10-21T00:00:00Z)'")
	pin, err := pinInstalledBinary(path)
	if err != nil {
		t.Fatal(err)
	}
	shareDir := t.TempDir()
	pin.shareBinDir = shareDir
	scheduleHubRestartOnlyAfterResponse(context.Background(), pin, path, []string{"hub"})

	<-stderr.done
	if _, err := os.Stat(filepath.Join(shareDir, ".install.lock")); err != nil {
		t.Fatalf("managed restart did not use the install lock: %v", err)
	}
}

func TestHubRestartOnlyHoldsManagedInstallLockAcrossExec(t *testing.T) {
	previousDelay := hubRestartDelay
	hubRestartDelay = 0
	t.Cleanup(func() { hubRestartDelay = previousDelay })

	entered := make(chan struct{})
	release := make(chan struct{})
	previousExec := execHubBinary
	execHubBinary = func(string, []string) error {
		close(entered)
		<-release
		return errors.New("exec failed")
	}
	t.Cleanup(func() { execHubBinary = previousExec })

	stderr := newSyncWriter("restart failed")
	previousStderr := hubUpdateStderr
	hubUpdateStderr = stderr
	t.Cleanup(func() { hubUpdateStderr = previousStderr })

	hubUpdateMu = sync.Mutex{}
	hubUpdateMu.Lock()
	t.Cleanup(func() { hubUpdateMu = sync.Mutex{} })

	path := writeProbeScript(t, "echo 'evener abc1234 (2026-10-21T00:00:00Z)'")
	pin, err := pinInstalledBinary(path)
	if err != nil {
		t.Fatal(err)
	}
	pin.shareBinDir = t.TempDir()
	scheduleHubRestartOnlyAfterResponse(context.Background(), pin, path, []string{"hub"})
	<-entered

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	if _, err := selfupdate.AcquireInstallLockForVerify(ctx, pin.shareBinDir); err == nil {
		cancel()
		close(release)
		t.Fatal("installer acquired the lock while the restart was between verify and exec")
	}
	cancel()
	close(release)
	<-stderr.done
}

func TestHubRestartOnlyContinuesWhenInstallDirIsNotWritable(t *testing.T) {
	previousDelay := hubRestartDelay
	hubRestartDelay = 0
	t.Cleanup(func() { hubRestartDelay = previousDelay })

	executed := false
	previousExec := execHubBinary
	execHubBinary = func(string, []string) error {
		executed = true
		return errors.New("exec failed")
	}
	t.Cleanup(func() { execHubBinary = previousExec })

	stderr := newSyncWriter("restart failed")
	previousStderr := hubUpdateStderr
	hubUpdateStderr = stderr
	t.Cleanup(func() { hubUpdateStderr = previousStderr })

	hubUpdateMu = sync.Mutex{}
	hubUpdateMu.Lock()
	t.Cleanup(func() { hubUpdateMu = sync.Mutex{} })

	path := writeProbeScript(t, "echo 'evener abc1234 (2026-10-21T00:00:00Z)'")
	pin, err := pinInstalledBinary(path)
	if err != nil {
		t.Fatal(err)
	}
	shareDir := t.TempDir()
	if err := os.Chmod(shareDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(shareDir, 0o755) })
	pin.shareBinDir = shareDir
	scheduleHubRestartOnlyAfterResponse(context.Background(), pin, path, []string{"hub"})

	select {
	case <-stderr.done:
	case <-time.After(5 * time.Second):
		t.Fatal("restart did not report a failure")
	}
	if !executed {
		t.Fatal("restart did not reach exec without a writable install lock")
	}
}

func TestHubRestartOnlyAbortsWhileAnotherInstallerHoldsTheLock(t *testing.T) {
	previousDelay := hubRestartDelay
	hubRestartDelay = 0
	t.Cleanup(func() { hubRestartDelay = previousDelay })
	previousLockTimeout := hubRestartLockTimeout
	hubRestartLockTimeout = 100 * time.Millisecond
	t.Cleanup(func() { hubRestartLockTimeout = previousLockTimeout })

	executed := false
	previousExec := execHubBinary
	execHubBinary = func(string, []string) error {
		executed = true
		return errors.New("exec failed")
	}
	t.Cleanup(func() { execHubBinary = previousExec })

	stderr := newSyncWriter("restart aborted")
	previousStderr := hubUpdateStderr
	hubUpdateStderr = stderr
	t.Cleanup(func() { hubUpdateStderr = previousStderr })

	hubUpdateMu = sync.Mutex{}
	hubUpdateMu.Lock()
	t.Cleanup(func() { hubUpdateMu = sync.Mutex{} })

	path := writeProbeScript(t, "echo 'evener abc1234 (2026-10-21T00:00:00Z)'")
	pin, err := pinInstalledBinary(path)
	if err != nil {
		t.Fatal(err)
	}
	shareDir := t.TempDir()
	holder, err := selfupdate.AcquireInstallLockForVerify(context.Background(), shareDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(holder)
	pin.shareBinDir = shareDir
	scheduleHubRestartOnlyAfterResponse(context.Background(), pin, path, []string{"hub"})

	select {
	case <-stderr.done:
	case <-time.After(5 * time.Second):
		t.Fatal("restart did not abort while another installer held the lock")
	}
	if executed {
		t.Fatal("restart proceeded while another installer held the lock")
	}
}

// TestLaunchBinaryDigestInvalidatesOnSameSizeReplacement proves the digest
// cache notices a replaced executable even when the new bytes have the same
// size and the old mtime: the path now names a different file.
func TestLaunchBinaryDigestInvalidatesOnSameSizeReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "evener")
	first := []byte("#!/bin/sh\necho first\n")
	second := []byte("#!/bin/sh\necho secnd\n")
	if len(first) != len(second) {
		t.Fatalf("test data lengths differ: %d != %d", len(first), len(second))
	}
	if err := os.WriteFile(path, first, 0o755); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	firstDigest, err := launchBinaryDigest(path)
	if err != nil {
		t.Fatal(err)
	}

	tmp := filepath.Join(dir, "replacement")
	if err := os.WriteFile(tmp, second, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(tmp, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}

	secondDigest, err := launchBinaryDigest(path)
	if err != nil {
		t.Fatal(err)
	}
	if secondDigest == firstDigest {
		t.Fatal("digest cache returned the pre-replacement bytes")
	}
}

func TestHubRestartOnlyAbortsWhenBinarySwapped(t *testing.T) {
	previousDelay := hubRestartDelay
	hubRestartDelay = 0
	t.Cleanup(func() { hubRestartDelay = previousDelay })

	execCalled := false
	previousExec := execHubBinary
	execHubBinary = func(string, []string) error {
		execCalled = true
		return nil
	}
	t.Cleanup(func() { execHubBinary = previousExec })

	stderr := newSyncWriter("restart aborted")
	previousStderr := hubUpdateStderr
	hubUpdateStderr = stderr
	t.Cleanup(func() { hubUpdateStderr = previousStderr })

	hubUpdateMu = sync.Mutex{}
	hubUpdateMu.Lock()
	t.Cleanup(func() { hubUpdateMu = sync.Mutex{} })

	path := writeProbeScript(t, "echo 'evener abc1234 (2026-10-21T00:00:00Z)'")
	pin, err := pinInstalledBinary(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed"), 0o755); err != nil {
		t.Fatal(err)
	}
	scheduleHubRestartOnlyAfterResponse(context.Background(), pin, path, []string{"hub"})

	<-stderr.done
	if execCalled {
		t.Fatal("exec ran after the target changed")
	}
	if !hubUpdateMu.TryLock() {
		t.Fatal("aborted restart left the update mutex locked")
	}
	hubUpdateMu.Unlock()
}

func TestResolveLaunchEntrypoint(t *testing.T) {
	target := writeProbeScript(t, "echo 'evener abc1234 (2026-10-21T00:00:00Z)'")
	other := writeProbeScript(t, "echo 'evener def5678 (2026-10-21T00:00:00Z)'")
	link := filepath.Join(t.TempDir(), "evener-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	t.Run("same file", func(t *testing.T) {
		got, err := resolveLaunchEntrypoint([]string{target, "hub"}, func() (string, error) { return target, nil })
		if err != nil || got != target {
			t.Fatalf("resolveLaunchEntrypoint = %q, %v; want %q", got, err, target)
		}
	})
	t.Run("symlink preserves the entrypoint", func(t *testing.T) {
		got, err := resolveLaunchEntrypoint([]string{link, "hub"}, func() (string, error) { return target, nil })
		if err != nil || got != link {
			t.Fatalf("resolveLaunchEntrypoint = %q, %v; want the symlink %q", got, err, link)
		}
	})
	t.Run("different executable", func(t *testing.T) {
		if _, err := resolveLaunchEntrypoint([]string{target, "hub"}, func() (string, error) { return other, nil }); err == nil {
			t.Fatal("resolveLaunchEntrypoint accepted a different executable")
		}
	})
	t.Run("empty argv", func(t *testing.T) {
		if _, err := resolveLaunchEntrypoint(nil, func() (string, error) { return target, nil }); err == nil {
			t.Fatal("resolveLaunchEntrypoint accepted empty argv")
		}
	})
}

func TestProbeHubBinaryAcceptsVersionWithStderrDiagnostics(t *testing.T) {
	path := writeProbeScript(t, "echo 'evener abc1234 (2026-10-21T00:00:00Z)'\necho 'diagnostic' >&2")
	got, err := probeHubBinary(path)
	if err != nil {
		t.Fatalf("probeHubBinary: %v", err)
	}
	if got != "abc1234" {
		t.Fatalf("probeHubBinary = %q, want abc1234", got)
	}
}

func TestProbeHubBinaryBoundsForkedChild(t *testing.T) {
	path := writeProbeScript(t, "(sleep 5) & sleep 5")
	previousTimeout := hubProbeTimeout
	hubProbeTimeout = 50 * time.Millisecond
	t.Cleanup(func() { hubProbeTimeout = previousTimeout })

	done := make(chan error, 1)
	go func() {
		_, err := probeHubBinary(path)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, errHubProbeTimeout) {
			t.Fatalf("probe error = %v, want the timeout", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("probe waited for a forked child holding stdout")
	}
}

func TestRestartExecHelperProcess(t *testing.T) {
	if os.Getenv("EVENER_RESTART_HELPER") != "1" {
		return
	}
	target := os.Getenv("EVENER_RESTART_TARGET")
	pin, err := pinInstalledBinary(target)
	if err != nil {
		t.Fatal(err)
	}
	hubRestartDelay = 0
	hubUpdateStderr = io.Discard
	hubUpdateMu.Lock()
	scheduleHubRestartOnlyAfterResponse(context.Background(), pin, target, []string{"hub"})
	// The goroutine should exec the target and replace this process.
	time.Sleep(5 * time.Second)
	t.Fatal("exec did not replace the helper process")
}

func TestRestartExecRunsTheTargetInTheSamePID(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("in-place exec is Unix-only")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "pid")
	target := filepath.Join(dir, "target.sh")
	if err := os.WriteFile(target, []byte("#!/bin/sh\nprintf '%s' \"$$\" > \"$EVENER_RESTART_OUT\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestRestartExecHelperProcess")
	cmd.Env = append(os.Environ(),
		"EVENER_RESTART_HELPER=1",
		"EVENER_RESTART_TARGET="+target,
		"EVENER_RESTART_OUT="+out,
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	childPID := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("helper process: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(data)); got != strconv.Itoa(childPID) {
		t.Fatalf("executed PID = %q, want the helper PID %d", got, childPID)
	}
}

func TestHubRestartRPCIsRegistered(t *testing.T) {
	setBuild(t, "abc1234", "release")
	t.Cleanup(func() { hubUpdateMu = sync.Mutex{} })

	previousSupported := hubRestartSupported
	hubRestartSupported = func() bool { return true }
	t.Cleanup(func() { hubRestartSupported = previousSupported })

	previousPath := hubLaunchEntrypoint
	hubLaunchEntrypoint = writeProbeScript(t, "echo 'evener abc1234 (2026-10-21T00:00:00Z)'")
	t.Cleanup(func() { hubLaunchEntrypoint = previousPath })

	previousSchedule := scheduleHubRestartOnly
	scheduleHubRestartOnly = func(context.Context, restartPin, string, []string) {}
	t.Cleanup(func() { scheduleHubRestartOnly = previousSchedule })
	previousErr := errHubLaunchEntrypoint
	errHubLaunchEntrypoint = nil
	t.Cleanup(func() { errHubLaunchEntrypoint = previousErr })

	appServer := newHubAppServer(hubcore.WebConfig{
		HubStateRoot: t.TempDir(),
		Past:         hubcore.NewPastIndex(""),
	}, appsource.NewRegistry())
	hub := httptest.NewServer(http.HandlerFunc(appServer.ServeWebSocket))
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	var resp appwire.HubRestartResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerHubRestart, appwire.HubRestartParams{}, &resp); err != nil {
		t.Fatalf("evener/hub/restart: %v", err)
	}
	if !resp.Restarting || resp.TargetVersion != "abc1234" {
		t.Fatalf("response = %+v, want restarting into abc1234", resp)
	}
}

func writeProbeScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "evener")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
