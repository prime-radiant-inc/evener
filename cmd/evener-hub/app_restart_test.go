package hub

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
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
	if calls[0].binary != path || calls[0].pin.path != path || calls[0].pin.sha256Hex == "" {
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
	previousErr := hubLaunchEntrypointErr
	hubLaunchEntrypointErr = nil
	t.Cleanup(func() { hubLaunchEntrypointErr = previousErr })

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
