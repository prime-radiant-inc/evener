//go:build !windows

package hub

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/agent/sandbox/sandboxtest"
)

const managerCleanupHelperEnv = "EVENER_MANAGER_CLEANUP_PROCESS_HELPER"
const managerCleanupListenEnv = "EVENER_MANAGER_CLEANUP_PROCESS_LISTEN"
const managerCleanupReadyEnv = "EVENER_MANAGER_CLEANUP_PROCESS_READY"
const managerCleanupClosedEnv = "EVENER_MANAGER_CLEANUP_PROCESS_CLOSED"
const managerCleanupFailureChildEnv = "EVENER_MANAGER_CLEANUP_FAILURE_CHILD"
const managerCleanupFailureRootReportEnv = "EVENER_MANAGER_CLEANUP_FAILURE_ROOT_REPORT"
const managerCleanupCompositionChildEnv = "EVENER_MANAGER_CLEANUP_COMPOSITION_CHILD"
const managerCleanupCompositionReportEnv = "EVENER_MANAGER_CLEANUP_COMPOSITION_REPORT"
const hostCleanupBoundaryEnv = "EVENER_HOST_CLEANUP_BOUNDARY"
const hostCleanupBoundaryReportEnv = "EVENER_HOST_CLEANUP_BOUNDARY_REPORT"
const hostCleanupBoundaryMarkEnv = "EVENER_HOST_CLEANUP_BOUNDARY_MARK"
const hostCleanupBoundaryFailEnv = "EVENER_HOST_CLEANUP_BOUNDARY_FAIL"

var managerCleanupConfigFlag = flag.String("manager-cleanup-process-config", "", "config path marker for the listener cleanup process test")

type managerCleanupCompositionResult struct {
	Root            string `json:"root"`
	Retained        bool   `json:"retained"`
	HubExited       bool   `json:"hub_exited"`
	HubSignal       string `json:"hub_signal"`
	DaemonExited    bool   `json:"daemon_exited"`
	CleanupReturned bool   `json:"cleanup_returned"`
}

type managerCleanupFailureResult struct {
	CleanupFailed bool   `json:"cleanup_failed"`
	ProcessSignal string `json:"process_signal"`
	Root          string `json:"root"`
	Retained      bool   `json:"retained"`
}

func runManagerCleanupServeHelper() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGUSR1)
	if err := os.WriteFile(os.Getenv(managerCleanupServeReadyEnv), []byte("ready"), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	defer signal.Stop(signals)
	<-signals
}

// TestManagerReadinessCleanupProcessHelper is a real process used by the test
// below. SIGTERM closes its listener, then a filesystem barrier keeps its PID
// present until the parent explicitly releases it.
func TestManagerReadinessCleanupProcessHelper(t *testing.T) {
	if os.Getenv(managerCleanupHelperEnv) != "1" {
		return
	}
	if *managerCleanupConfigFlag == "" {
		t.Fatal("helper process is missing its config identity")
	}
	listener, err := net.Listen("tcp", os.Getenv(managerCleanupListenEnv))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGUSR1)
	defer signal.Stop(signals)
	if err := os.WriteFile(os.Getenv(managerCleanupReadyEnv), []byte(listener.Addr().String()), 0o600); err != nil {
		t.Fatalf("write readiness: %v", err)
	}
	listenerClosed := false
	for sig := range signals {
		switch sig {
		case syscall.SIGTERM:
			if !listenerClosed {
				if err := listener.Close(); err != nil {
					t.Fatalf("close listener: %v", err)
				}
				if err := os.WriteFile(os.Getenv(managerCleanupClosedEnv), []byte("closed"), 0o600); err != nil {
					t.Fatalf("write listener-closed barrier: %v", err)
				}
				listenerClosed = true
			}
		case syscall.SIGUSR1:
			return
		}
	}
}

func TestManagerAcceptanceCleanupReapsDaemonAfterHubProofFailure(t *testing.T) {
	if os.Getenv(managerCleanupCompositionChildEnv) != "1" {
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		reportPath := filepath.Join(t.TempDir(), "composition.json")
		cmd := exec.Command(exe, "-test.run=^TestManagerAcceptanceCleanupReapsDaemonAfterHubProofFailure$", "-test.v")
		cmd.Env = withoutEnv(os.Environ(), sandboxtest.RootVar, "TMPDIR", "TMP", "TEMP", "EVENER_HOST_TEMP_BASES")
		cmd.Env = append(cmd.Env, managerCleanupCompositionChildEnv+"=1", managerCleanupCompositionReportEnv+"="+reportPath, liveStackWarmEnv+"=1")
		output, runErr := cmd.CombinedOutput()
		if _, ok := errors.AsType[*exec.ExitError](runErr); !ok {
			t.Fatalf("composition child exit = %v, want graceful-proof failure; output:\n%s", runErr, output)
		}
		var result managerCleanupCompositionResult
		data, err := os.ReadFile(reportPath)
		if err != nil {
			t.Fatalf("read child process report: %v\n%s", err, output)
		}
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatalf("decode child process report: %v: %s", err, data)
		}
		if !result.CleanupReturned || !result.Retained || !result.HubExited || result.HubSignal != "killed" || !result.DaemonExited {
			t.Fatalf("cleanup result = %+v; output:\n%s", result, output)
		}
		if _, err := os.Stat(filepath.Join(result.Root, retainHostCleanupRootMarker)); err != nil {
			t.Fatalf("child cleanup root was not retained: %v", err)
		}
		if err := os.RemoveAll(result.Root); err != nil {
			t.Fatalf("remove verified child root %s: %v", result.Root, err)
		}
		return
	}

	root := t.TempDir()
	hostDir := filepath.Join(root, "fixture")
	binDir := filepath.Join(hostDir, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(hostDir, "hub.toml")
	if err := os.WriteFile(config, []byte("test fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fixtureBin := filepath.Join(binDir, "evener-real")
	if err := os.Symlink(exe, fixtureBin); err != nil {
		t.Fatal(err)
	}
	readyPath := filepath.Join(root, "hub-ready")
	closedPath := filepath.Join(root, "hub-closed")
	hub := exec.Command(fixtureBin, "-test.run=^TestManagerReadinessCleanupProcessHelper$", "-manager-cleanup-process-config="+config)
	hub.Env = append(os.Environ(), managerCleanupHelperEnv+"=1", managerCleanupListenEnv+"=127.0.0.1:0", managerCleanupReadyEnv+"="+readyPath, managerCleanupClosedEnv+"="+closedPath)
	if err := hub.Start(); err != nil {
		t.Fatal(err)
	}
	hubWaited := false
	defer func() {
		if !hubWaited {
			_ = hub.Process.Signal(syscall.SIGUSR1)
			_ = hub.Wait()
		}
	}()
	waitForCleanupFile(t, readyPath)
	addrBytes, err := os.ReadFile(readyPath)
	if err != nil {
		t.Fatal(err)
	}
	daemonReady := filepath.Join(root, "daemon-ready")
	daemon := exec.Command(fixtureBin, "serve", "--dir="+hostDir, "--state-dir="+hostDir)
	daemon.Env = append(os.Environ(), managerCleanupServeHelperEnv+"=1", managerCleanupServeReadyEnv+"="+daemonReady)
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}
	daemonWaited := false
	defer func() {
		if !daemonWaited {
			_ = daemon.Process.Signal(syscall.SIGTERM)
			_ = daemon.Wait()
		}
	}()
	waitForCleanupFile(t, daemonReady)
	host := &hostSSH{t: t, target: "local-process-fixture", execCommand: func(ctx context.Context, script string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/bash", "-c", script)
	}}
	cleanupManagerAcceptanceFixture(t, host, strings.TrimSpace(string(addrBytes)), config, fixtureBin, hostDir)
	result := managerCleanupCompositionResult{Root: os.Getenv(sandboxtest.RootVar), Retained: hostCleanupRootMustBeRetained(os.Getenv(sandboxtest.RootVar)), CleanupReturned: true}
	hubWait := make(chan error, 1)
	go func() { hubWait <- hub.Wait() }()
	select {
	case waitErr := <-hubWait:
		hubWaited = true
		if exitErr, ok := errors.AsType[*exec.ExitError](waitErr); ok {
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() && status.Signal() == syscall.SIGKILL {
				result.HubExited = true
				result.HubSignal = "killed"
			}
		}
	case <-time.After(3 * time.Second):
		_ = hub.Process.Kill()
		<-hubWait
		hubWaited = true
	}
	daemonWait := make(chan error, 1)
	go func() { daemonWait <- daemon.Wait() }()
	select {
	case waitErr := <-daemonWait:
		daemonWaited = true
		result.DaemonExited = waitErr == nil
	case <-time.After(3 * time.Second):
		_ = daemon.Process.Signal(syscall.SIGTERM)
		<-daemonWait
		daemonWaited = true
	}
	data, marshalErr := json.Marshal(result)
	if marshalErr == nil {
		marshalErr = os.WriteFile(os.Getenv(managerCleanupCompositionReportEnv), data, 0o600)
	}
	if marshalErr != nil {
		t.Errorf("write composition result: %v", marshalErr)
	}
	if !result.HubExited || !result.DaemonExited || !result.Retained {
		t.Errorf("composition did not reap and retain all owned state: %+v", result)
	}
}

// TestStopHostListenerWaitsForTheOwnedHubProcess uses a real listener process
// with a listener-close/exit barrier. It catches cleanup returning safe as soon
// as the port closes while the owned PID remains present.
func TestStopHostListenerWaitsForTheOwnedHubProcess(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	hostDir := filepath.Join(root, "fixture")
	binDir := filepath.Join(hostDir, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(hostDir, "hub.toml")
	if err := os.WriteFile(config, []byte("test fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fixtureBin := filepath.Join(binDir, "evener-real")
	if err := os.Symlink(exe, fixtureBin); err != nil {
		t.Fatal(err)
	}
	readyPath := filepath.Join(root, "ready")
	closedPath := filepath.Join(root, "closed")
	cmd := exec.Command(fixtureBin,
		"-test.run=^TestManagerReadinessCleanupProcessHelper$",
		"-manager-cleanup-process-config="+config,
	)
	cmd.Env = append(os.Environ(),
		managerCleanupHelperEnv+"=1",
		managerCleanupListenEnv+"=127.0.0.1:0",
		managerCleanupReadyEnv+"="+readyPath,
		managerCleanupClosedEnv+"="+closedPath,
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Signal(syscall.SIGUSR1)
			_ = cmd.Wait()
		}
	}()
	waitForCleanupFile(t, readyPath)
	addrBytes, err := os.ReadFile(readyPath)
	if err != nil {
		t.Fatal(err)
	}
	addr := strings.TrimSpace(string(addrBytes))
	if _, _, err := net.SplitHostPort(addr); err != nil {
		t.Fatalf("helper readiness has invalid address %q: %v", addr, err)
	}

	host := &hostSSH{t: t, target: "local-process-fixture", execCommand: func(ctx context.Context, script string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/bash", "-c", script)
	}}
	stopErr := stopHostListenerProof(host, addr, config, fixtureBin)
	waitForCleanupFile(t, closedPath)
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("listener-close barrier did not keep owned PID %d present: %v", cmd.Process.Pid, err)
	}
	if err := cmd.Process.Signal(syscall.SIGUSR1); err != nil {
		t.Fatalf("release listener-close/exit barrier: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait for fixture hub process: %v", err)
	}
	waited = true
	if stopErr == nil {
		t.Fatalf("stopHostListener returned success after the listener closed while its owned PID %d was still held at the exit barrier", cmd.Process.Pid)
	}
}

func TestStopHostListenerReapsOwnedProcessAfterProofFailure(t *testing.T) {
	if os.Getenv(managerCleanupFailureChildEnv) != "1" {
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		reportPath := filepath.Join(t.TempDir(), "retained-root")
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, exe, "-test.run=^TestStopHostListenerReapsOwnedProcessAfterProofFailure$", "-test.v")
		cmd.Env = withoutEnv(os.Environ(), sandboxtest.RootVar, "TMPDIR", "TMP", "TEMP", "EVENER_HOST_TEMP_BASES")
		cmd.Env = append(cmd.Env, managerCleanupFailureChildEnv+"=1", managerCleanupFailureRootReportEnv+"="+reportPath, liveStackWarmEnv+"=1")
		output, runErr := cmd.CombinedOutput()
		if ctx.Err() != nil {
			t.Fatalf("failure-path child exceeded bounded wait: %v; output:\n%s", ctx.Err(), output)
		}
		var exitErr *exec.ExitError
		data, err := os.ReadFile(reportPath)
		if err != nil {
			t.Fatalf("read child process report: %v; child exit %v; output:\n%s", err, runErr, output)
		}
		var result managerCleanupFailureResult
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatalf("decode child process report: %v: %s", err, data)
		}
		if !errors.As(runErr, &exitErr) || !result.CleanupFailed || result.ProcessSignal != "killed" || !result.Retained {
			t.Fatalf("child exit=%v, cleanup_failed=%t, process_signal=%q, retained=%t; output:\n%s", runErr, result.CleanupFailed, result.ProcessSignal, result.Retained, output)
		}
		if _, err := os.Stat(filepath.Join(result.Root, retainHostCleanupRootMarker)); err != nil {
			t.Fatalf("child did not retain cleanup marker at %s: %v", result.Root, err)
		}
		if err := os.RemoveAll(result.Root); err != nil {
			t.Fatalf("remove verified child test root %s: %v", result.Root, err)
		}
		return
	}
	t.Parallel()
	root := t.TempDir()
	hostDir := filepath.Join(root, "fixture")
	binDir := filepath.Join(hostDir, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(hostDir, "hub.toml")
	if err := os.WriteFile(config, []byte("test fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fixtureBin := filepath.Join(binDir, "evener-real")
	if err := os.Symlink(exe, fixtureBin); err != nil {
		t.Fatal(err)
	}
	readyPath := filepath.Join(root, "ready")
	closedPath := filepath.Join(root, "closed")
	cmd := exec.Command(fixtureBin, "-test.run=^TestManagerReadinessCleanupProcessHelper$", "-manager-cleanup-process-config="+config)
	cmd.Env = append(os.Environ(), managerCleanupHelperEnv+"=1", managerCleanupListenEnv+"=127.0.0.1:0", managerCleanupReadyEnv+"="+readyPath, managerCleanupClosedEnv+"="+closedPath)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Signal(syscall.SIGUSR1)
			_ = cmd.Wait()
		}
	}()
	waitForCleanupFile(t, readyPath)
	addrBytes, err := os.ReadFile(readyPath)
	if err != nil {
		t.Fatal(err)
	}
	host := &hostSSH{t: t, target: "local-process-fixture", execCommand: func(ctx context.Context, script string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/bash", "-c", script)
	}}
	proofSucceeded := stopHostListener(t, host, strings.TrimSpace(string(addrBytes)), config, fixtureBin)
	cleanupFailed := t.Failed()
	if proofSucceeded {
		t.Fatal("graceful proof unexpectedly succeeded while the owned process was held at its exit barrier")
	}
	markerPath := filepath.Join(os.Getenv(sandboxtest.RootVar), retainHostCleanupRootMarker)
	_, markerErr := os.Stat(markerPath)
	root = os.Getenv(sandboxtest.RootVar)
	result := managerCleanupFailureResult{CleanupFailed: cleanupFailed, Root: root, Retained: markerErr == nil}
	if markerErr != nil {
		t.Errorf("failed cleanup did not retain its evidence marker %s: %v", markerPath, markerErr)
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-waitCh:
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Signal(syscall.SIGUSR1)
		waitErr = <-waitCh
		waited = true
		t.Errorf("owned process did not exit after bounded cleanup wait")
	}
	if waitErr == nil {
		result.ProcessSignal = "released"
	}
	waited = true
	if exitErr, ok := errors.AsType[*exec.ExitError](waitErr); ok {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() && status.Signal() == syscall.SIGKILL {
			result.ProcessSignal = "killed"
		}
	}
	data, marshalErr := json.Marshal(result)
	if marshalErr == nil {
		marshalErr = os.WriteFile(os.Getenv(managerCleanupFailureRootReportEnv), data, 0o600)
	}
	if marshalErr != nil {
		t.Errorf("write failure-path result: %v", marshalErr)
	}
	if result.ProcessSignal != "killed" {
		t.Errorf("owned process exit = %v, want verified SIGKILL after it held the barrier", waitErr)
	}
}

// TestHostHubKillRecognizesOnlyOwnedArgv runs the generated command with data-only
// process discovery and a kill function that records selection instead of signaling.
func TestHostHubKillRecognizesOnlyOwnedArgv(t *testing.T) {
	const binary = "/fixture/bin/evener"
	const config = "/fixture/hub.toml"
	const hub = binary + " hub --config " + config
	const helper = binary + " -test.run=^TestManagerReadinessCleanupProcessHelper$ -manager-cleanup-process-config=" + config
	for _, tc := range []struct {
		name      string
		argv      string
		rechecked string
		want      bool
	}{
		{name: "real hub", argv: hub, want: true},
		{name: "exact helper", argv: helper, want: true},
		{name: "hub wrong executable", argv: "/other" + hub},
		{name: "helper wrong executable", argv: "/other" + helper},
		{name: "hub wrong config", argv: binary + " hub --config /other/hub.toml"},
		{name: "helper wrong config", argv: binary + " -test.run=^TestManagerReadinessCleanupProcessHelper$ -manager-cleanup-process-config=/other/hub.toml"},
		{name: "hub config substring", argv: hub + ".other"},
		{name: "helper config substring", argv: helper + ".other"},
		{name: "wrong helper selector", argv: binary + " -test.run=^TestOtherHelper$ -manager-cleanup-process-config=" + config},
		{name: "extra helper field", argv: helper + " extra"},
		{name: "wrong hub subcommand", argv: binary + " serve --config " + config},
		{name: "identity changed before signal", argv: helper, rechecked: helper + ".other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			mark := filepath.Join(root, "selected")
			rechecked := tc.rechecked
			if rechecked == "" {
				rechecked = tc.argv
			}
			// Every process boundary is fixture data. In particular, the generated
			// kill resolves to this function, never a signal to the synthetic PID.
			script := `lsof() { printf '%s\n' 424242; }
ps() {
  if [ -s "$SELECTOR_PS_READS" ]; then
    printf '%s\n' "$SELECTOR_RECHECKED_ARGV"
  else
    printf '%s\n' "$SELECTOR_ARGV"
  fi
  printf '%s\n' read >> "$SELECTOR_PS_READS"
}
kill() { printf '%s\n' "$@" > "$SELECTOR_MARK"; }
` + hostHubKillCommand("127.0.0.1:19180", config, binary)
			// TRIPWIRE: bound a stuck generated shell command, not timing under test.
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "/bin/bash", "-c", script)
			cmd.Env = append(os.Environ(),
				"SELECTOR_ARGV="+tc.argv,
				"SELECTOR_RECHECKED_ARGV="+rechecked,
				"SELECTOR_PS_READS="+filepath.Join(root, "ps-reads"),
				"SELECTOR_MARK="+mark,
			)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("run generated selector: %v\n%s", err, out)
			}
			selected, err := os.ReadFile(mark)
			if tc.want {
				if err != nil || string(selected) != "424242\n" {
					t.Fatalf("selected PID = %q, error = %v, want synthetic PID 424242", selected, err)
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unowned argv selected PID %q, error = %v", selected, err)
			}
		})
	}
}

func TestHostHubKillTargetsEvenerFixtureBinary(t *testing.T) {
	root := t.TempDir()
	hostDir := filepath.Join(root, "fixture")
	binDir := filepath.Join(hostDir, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(hostDir, "hub.toml")
	if err := os.WriteFile(config, []byte("test fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fixtureBin := filepath.Join(binDir, "evener")
	if err := os.Symlink(exe, fixtureBin); err != nil {
		t.Fatal(err)
	}
	readyPath := filepath.Join(root, "ready")
	closedPath := filepath.Join(root, "closed")
	cmd := exec.Command(fixtureBin,
		"-test.run=^TestManagerReadinessCleanupProcessHelper$",
		"-manager-cleanup-process-config="+config,
	)
	cmd.Env = append(os.Environ(),
		managerCleanupHelperEnv+"=1",
		managerCleanupListenEnv+"=127.0.0.1:0",
		managerCleanupReadyEnv+"="+readyPath,
		managerCleanupClosedEnv+"="+closedPath,
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Signal(syscall.SIGUSR1)
			_ = cmd.Wait()
		}
	}()
	waitForCleanupFile(t, readyPath)
	addrBytes, err := os.ReadFile(readyPath)
	if err != nil {
		t.Fatal(err)
	}
	addr := strings.TrimSpace(string(addrBytes))
	port := addr[strings.LastIndex(addr, ":")+1:]
	probeDir := filepath.Join(root, "probe-bin")
	if err := os.MkdirAll(probeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	lsof := filepath.Join(probeDir, "lsof")
	if err := os.WriteFile(lsof, []byte("#!/bin/sh\nprintf '%s\\n' \"$MANAGER_CLEANUP_PID\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("/bin/bash", "-c", hostHubKillCommand("127.0.0.1:"+port, config, fixtureBin))
	command.Env = append(os.Environ(),
		"PATH="+probeDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"MANAGER_CLEANUP_PID="+strconv.Itoa(cmd.Process.Pid),
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("run fixture hub kill command: %v\n%s", err, output)
	}
	waitForCleanupFile(t, closedPath)
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("listener-close barrier did not retain the fixture PID %d: %v", cmd.Process.Pid, err)
	}
	if err := cmd.Process.Signal(syscall.SIGUSR1); err != nil {
		t.Fatalf("release listener-close/exit barrier: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait for fixture hub process: %v", err)
	}
	waited = true
}

func TestHostHubKillPreservesProcessWithDifferentConfig(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fixtureBin := filepath.Join(binDir, "evener")
	if err := os.Symlink(exe, fixtureBin); err != nil {
		t.Fatal(err)
	}
	actualConfig := filepath.Join(root, "other.toml")
	expectedConfig := filepath.Join(root, "expected.toml")
	readyPath := filepath.Join(root, "ready")
	closedPath := filepath.Join(root, "closed")
	cmd := exec.Command(fixtureBin, "-test.run=^TestManagerReadinessCleanupProcessHelper$", "-manager-cleanup-process-config="+actualConfig)
	cmd.Env = append(os.Environ(), managerCleanupHelperEnv+"=1", managerCleanupListenEnv+"=127.0.0.1:0", managerCleanupReadyEnv+"="+readyPath, managerCleanupClosedEnv+"="+closedPath)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Signal(syscall.SIGUSR1)
			_ = cmd.Wait()
		}
	}()
	waitForCleanupFile(t, readyPath)
	addrBytes, err := os.ReadFile(readyPath)
	if err != nil {
		t.Fatal(err)
	}
	addr := strings.TrimSpace(string(addrBytes))
	probeDir := filepath.Join(root, "probe-bin")
	if err := os.MkdirAll(probeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	lsof := filepath.Join(probeDir, "lsof")
	if err := os.WriteFile(lsof, []byte("#!/bin/sh\nprintf '%s\\n' \"$MANAGER_CLEANUP_PID\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("/bin/bash", "-c", hostHubKillCommand(addr, expectedConfig, fixtureBin))
	command.Env = append(os.Environ(), "PATH="+probeDir+string(os.PathListSeparator)+os.Getenv("PATH"), "MANAGER_CLEANUP_PID="+strconv.Itoa(cmd.Process.Pid))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("run fixture hub kill command: %v\n%s", err, output)
	}
	if _, err := os.Stat(closedPath); !os.IsNotExist(err) {
		t.Fatalf("process with a different config received a signal, closed marker stat err=%v", err)
	}
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("unrelated fixture process %d did not survive: %v", cmd.Process.Pid, err)
	}
	if err := cmd.Process.Signal(syscall.SIGUSR1); err != nil {
		t.Fatalf("release unrelated fixture process: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait for unrelated fixture process: %v", err)
	}
	waited = true
}

func TestHostHubProcessProbeFailsClosedWhenPsFails(t *testing.T) {
	dir := t.TempDir()
	ps := filepath.Join(dir, "ps")
	if err := os.WriteFile(ps, []byte("#!/bin/sh\nexit 23\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	host := &hostSSH{t: t, target: "local-process-fixture", execCommand: func(ctx context.Context, script string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/bash", "-c", script)
	}}
	out, err := host.runStdout(hostHubProcessProbeCommand("/tmp/hub.toml", "/tmp/bin/evener"))
	var probeExit *exec.ExitError
	if !errors.As(err, &probeExit) || probeExit.ExitCode() != 1 || hostHubProcessesAbsent(out) {
		t.Fatalf("failed ps probe became absence: output=%q err=%v", out, err)
	}
	_, reapErr := host.run(hostHubReapCommand("/tmp/hub.toml", "/tmp/bin/evener"))
	var reapExit *exec.ExitError
	if !errors.As(reapErr, &reapExit) || reapExit.ExitCode() != 23 {
		t.Fatalf("emergency reaper did not preserve failed ps status 23: %v", reapErr)
	}
}

func TestHostHubReapPreservesProcessWithDifferentConfig(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fixtureBin := filepath.Join(binDir, "evener-real")
	if err := os.Symlink(exe, fixtureBin); err != nil {
		t.Fatal(err)
	}
	actualConfig := filepath.Join(root, "actual.toml")
	expectedConfig := filepath.Join(root, "expected.toml")
	readyPath := filepath.Join(root, "ready")
	closedPath := filepath.Join(root, "closed")
	cmd := exec.Command(fixtureBin, "-test.run=^TestManagerReadinessCleanupProcessHelper$", "-manager-cleanup-process-config="+actualConfig)
	cmd.Env = append(os.Environ(), managerCleanupHelperEnv+"=1", managerCleanupListenEnv+"=127.0.0.1:0", managerCleanupReadyEnv+"="+readyPath, managerCleanupClosedEnv+"="+closedPath)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Signal(syscall.SIGUSR1)
			_ = cmd.Wait()
		}
	}()
	waitForCleanupFile(t, readyPath)
	host := &hostSSH{t: t, target: "local-process-fixture", execCommand: func(ctx context.Context, script string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/bash", "-c", script)
	}}
	if err := host.tryRun(hostHubReapCommand(expectedConfig, fixtureBin)); err != nil {
		t.Fatalf("reap command for different config: %v", err)
	}
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("unrelated process %d did not survive emergency reap: %v", cmd.Process.Pid, err)
	}
	if err := cmd.Process.Signal(syscall.SIGUSR1); err != nil {
		t.Fatalf("release unrelated process: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait for unrelated process: %v", err)
	}
	waited = true
}

func TestHostHubReapPreservesConfigPathPrefixCollision(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fixtureBin := filepath.Join(binDir, "evener-real")
	if err := os.Symlink(exe, fixtureBin); err != nil {
		t.Fatal(err)
	}
	requestedConfig := filepath.Join(root, "hub.toml")
	otherConfig := requestedConfig + ".other"
	configs := []string{requestedConfig, otherConfig}
	commands := make([]*exec.Cmd, 0, len(configs))
	closeds := make([]string, 0, len(configs))
	waited := make([]bool, len(configs))
	defer func() {
		for i, cmd := range commands {
			if !waited[i] {
				_ = cmd.Process.Signal(syscall.SIGUSR1)
				_ = cmd.Wait()
			}
		}
	}()
	for i, config := range configs {
		readyPath := filepath.Join(root, fmt.Sprintf("ready-%d", i))
		closedPath := filepath.Join(root, fmt.Sprintf("closed-%d", i))
		cmd := exec.Command(fixtureBin, "-test.run=^TestManagerReadinessCleanupProcessHelper$", "-manager-cleanup-process-config="+config)
		cmd.Env = append(os.Environ(), managerCleanupHelperEnv+"=1", managerCleanupListenEnv+"=127.0.0.1:0", managerCleanupReadyEnv+"="+readyPath, managerCleanupClosedEnv+"="+closedPath)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, cmd)
		closeds = append(closeds, closedPath)
		waitForCleanupFile(t, readyPath)
	}
	host := &hostSSH{t: t, target: "local-process-fixture", execCommand: func(ctx context.Context, script string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/bash", "-c", script)
	}}
	if _, err := host.run(hostHubReapCommand(requestedConfig, fixtureBin)); err != nil {
		t.Fatalf("reap exact-config process: %v", err)
	}
	if _, err := os.Stat(closeds[1]); !os.IsNotExist(err) {
		t.Fatalf("same-binary process with config %q received a signal, marker stat err=%v", otherConfig, err)
	}
	if err := commands[0].Wait(); err == nil {
		t.Fatal("owned process survived emergency reaping")
	}
	waited[0] = true
	if err := commands[1].Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("unrelated prefix-collision process %d did not survive: %v", commands[1].Process.Pid, err)
	}
	if err := commands[1].Process.Signal(syscall.SIGUSR1); err != nil {
		t.Fatalf("release prefix-collision process: %v", err)
	}
	if err := commands[1].Wait(); err != nil {
		t.Fatalf("wait for prefix-collision process: %v", err)
	}
	waited[1] = true
}

func TestHostHubProcessProbeRequiresAnExactAbsentMarker(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  string
		want bool
	}{
		{name: "exact absence", out: "HUB_PROCESSES_ABSENT\n", want: true},
		{name: "owned PID", out: "HUB_PID 1234\n", want: false},
		{name: "unknown output", out: "warning\nHUB_PROCESSES_ABSENT\n", want: false},
		{name: "empty output", out: "", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hostHubProcessesAbsent([]byte(tc.out)); got != tc.want {
				t.Fatalf("hostHubProcessesAbsent(%q) = %v, want %v", tc.out, got, tc.want)
			}
		})
	}
}

func TestRetainHostCleanupRootWritesMarker(t *testing.T) {
	root := t.TempDir()
	t.Setenv(sandboxtest.RootVar, root)
	retainHostCleanupRoot(t)
	marker := filepath.Join(root, retainHostCleanupRootMarker)
	if contents, err := os.ReadFile(marker); err != nil || string(contents) != "hub exit unproved\n" {
		t.Fatalf("retention marker contents = %q, err = %v", contents, err)
	}
	if !hostCleanupRootMustBeRetained(root) {
		t.Fatal("TestMain retention predicate ignored the unsafe-cleanup marker")
	}
	if hostCleanupRootMustBeRetained(t.TempDir()) {
		t.Fatal("TestMain retention predicate retained an unmarked root")
	}
}

func TestHostCleanupRootTestMainBoundary(t *testing.T) {
	if os.Getenv(hostCleanupBoundaryEnv) == "1" {
		root := os.Getenv(sandboxtest.RootVar)
		report := os.Getenv(hostCleanupBoundaryReportEnv)
		if err := os.WriteFile(report, []byte(root), 0o600); err != nil {
			t.Fatal(err)
		}
		if os.Getenv(hostCleanupBoundaryMarkEnv) == "true" {
			if err := os.WriteFile(filepath.Join(root, retainHostCleanupRootMarker), []byte("unsafe"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if os.Getenv(hostCleanupBoundaryFailEnv) == "true" {
			t.Error("intentional child test failure")
		}
		return
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		mark       bool
		fail       bool
		wantRetain bool
	}{
		{name: "safe cleanup removes package root"},
		{name: "unsafe cleanup retains package root", mark: true, wantRetain: true},
		{name: "unrelated test failure still removes package root", fail: true},
	} {
		childRoot := ""
		t.Run(tc.name, func(t *testing.T) {
			report := filepath.Join(t.TempDir(), "root-path")
			cmd := exec.Command(exe, "-test.run=^TestHostCleanupRootTestMainBoundary$")
			cmd.Env = withoutEnv(os.Environ(), sandboxtest.RootVar, "TMPDIR", "TMP", "TEMP", "EVENER_HOST_TEMP_BASES")
			cmd.Env = append(cmd.Env,
				hostCleanupBoundaryEnv+"=1",
				hostCleanupBoundaryReportEnv+"="+report,
				hostCleanupBoundaryMarkEnv+"="+strconv.FormatBool(tc.mark),
				hostCleanupBoundaryFailEnv+"="+strconv.FormatBool(tc.fail),
				liveStackWarmEnv+"=1",
			)
			output, runErr := cmd.CombinedOutput()
			if tc.fail && runErr == nil {
				t.Fatalf("child test unexpectedly passed: %s", output)
			}
			if !tc.fail && runErr != nil {
				t.Fatalf("child test failed: %v\n%s", runErr, output)
			}
			rootBytes, err := os.ReadFile(report)
			if err != nil {
				t.Fatalf("read child root report: %v\n%s", err, output)
			}
			root := strings.TrimSpace(string(rootBytes))
			childRoot = root
			_, statErr := os.Stat(root)
			retained := statErr == nil
			if statErr != nil && !os.IsNotExist(statErr) {
				t.Fatalf("stat child TestMain root %s: %v", root, statErr)
			}
			if retained != tc.wantRetain {
				t.Fatalf("child TestMain root retained=%v, want %v (root %s, output %s)", retained, tc.wantRetain, root, output)
			}
			t.Cleanup(func() {
				if err := os.RemoveAll(root); err != nil {
					t.Errorf("remove verified child TestMain root %s: %v", root, err)
				}
			})
		})
		if _, err := os.Stat(childRoot); err == nil {
			t.Errorf("per-case teardown left child TestMain root %s", childRoot)
		} else if !os.IsNotExist(err) {
			t.Errorf("checking child TestMain root after per-case teardown: %v", err)
		}
	}
}

func TestRemoveHostFixtureAfterProof(t *testing.T) {
	for _, proved := range []bool{false, true} {
		t.Run(fmt.Sprintf("shutdown-proved-%t", proved), func(t *testing.T) {
			hostDir := filepath.Join(t.TempDir(), "fixture")
			if err := os.Mkdir(hostDir, 0o700); err != nil {
				t.Fatal(err)
			}
			host := &hostSSH{t: t, target: "local-fixture", execCommand: func(ctx context.Context, script string) *exec.Cmd {
				return exec.CommandContext(ctx, "/bin/bash", "-c", script)
			}}
			removeHostFixtureAfterProof(t, host, hostDir, proved)
			_, err := os.Stat(hostDir)
			if proved && !os.IsNotExist(err) {
				t.Fatalf("proved fixture directory was not removed: stat err=%v", err)
			}
			if !proved && err != nil {
				t.Fatalf("unproved fixture directory was removed or unreadable: %v", err)
			}
		})
	}
}

func withoutEnv(env []string, names ...string) []string {
	blocked := make(map[string]bool, len(names))
	for _, name := range names {
		blocked[name] = true
	}
	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		if !blocked[name] {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func waitForCleanupFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat barrier %s: %v", path, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for cleanup barrier %s (%s)", path, fmt.Sprint(deadline))
}
