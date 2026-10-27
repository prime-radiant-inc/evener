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
	stopErr := stopHostListenerProof(host, addr, config)
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
