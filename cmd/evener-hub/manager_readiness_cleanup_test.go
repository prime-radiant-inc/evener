//go:build !windows

package hub

import (
	"context"
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
const hostCleanupBoundaryEnv = "EVENER_HOST_CLEANUP_BOUNDARY"
const hostCleanupBoundaryReportEnv = "EVENER_HOST_CLEANUP_BOUNDARY_REPORT"
const hostCleanupBoundaryMarkEnv = "EVENER_HOST_CLEANUP_BOUNDARY_MARK"
const hostCleanupBoundaryFailEnv = "EVENER_HOST_CLEANUP_BOUNDARY_FAIL"

var managerCleanupConfigFlag = flag.String("manager-cleanup-process-config", "", "config path marker for the listener cleanup process test")

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
	if err := os.WriteFile(os.Getenv(managerCleanupReadyEnv), []byte(listener.Addr().String()), 0o600); err != nil {
		t.Fatalf("write readiness: %v", err)
	}
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGUSR1)
	defer signal.Stop(signals)
	for sig := range signals {
		switch sig {
		case syscall.SIGTERM:
			if err := listener.Close(); err != nil {
				t.Fatalf("close listener: %v", err)
			}
			if err := os.WriteFile(os.Getenv(managerCleanupClosedEnv), []byte("closed"), 0o600); err != nil {
				t.Fatalf("write listener-closed barrier: %v", err)
			}
		case syscall.SIGUSR1:
			return
		}
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
	if err == nil || hostHubProcessesAbsent(out) {
		t.Fatalf("failed ps probe became absence: output=%q err=%v", out, err)
	}
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
			_, statErr := os.Stat(root)
			retained := statErr == nil
			if statErr != nil && !os.IsNotExist(statErr) {
				t.Fatalf("stat child TestMain root %s: %v", root, statErr)
			}
			if retained != tc.wantRetain {
				t.Fatalf("child TestMain root retained=%v, want %v (root %s, output %s)", retained, tc.wantRetain, root, output)
			}
		})
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
