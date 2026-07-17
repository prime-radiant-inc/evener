//go:build darwin || linux

package llm

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestAPILogTargetLockRejectsSecondOwnerAndReleasesOnClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.jsonl")
	owner, err := NewAPILogger(path)
	if err != nil {
		t.Fatalf("NewAPILogger owner: %v", err)
	}

	second, err := NewAPILogger(path)
	if err == nil {
		_ = second.Close()
		t.Fatal("NewAPILogger allowed a second owner for the same target")
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second-owner error = %q, want already-running guidance", err)
	}

	if err := owner.Close(); err != nil {
		t.Fatalf("owner Close: %v", err)
	}
	later, err := NewAPILogger(path)
	if err != nil {
		t.Fatalf("NewAPILogger after owner Close: %v", err)
	}
	if err := later.Close(); err != nil {
		t.Fatalf("later Close: %v", err)
	}
}

func TestAPILogTargetLockReleasesWhenOwnerProcessExits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.jsonl")
	cmd := exec.Command(os.Args[0], "-test.run=^TestAPILogTargetLockHolderProcess$")
	cmd.Env = append(os.Environ(), "SERF_TEST_APILOG_LOCK_PATH="+path)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start lock holder: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	ready, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("wait for lock holder: %v", err)
	}
	if strings.TrimSpace(ready) != "ready" {
		t.Fatalf("lock holder readiness = %q", ready)
	}
	if logger, err := NewAPILogger(path); err == nil {
		_ = logger.Close()
		t.Fatal("NewAPILogger acquired a target owned by another process")
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill lock holder: %v", err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("killed lock holder exited successfully")
	}

	later, err := NewAPILogger(path)
	if err != nil {
		t.Fatalf("NewAPILogger after owner process exit: %v", err)
	}
	if err := later.Close(); err != nil {
		t.Fatalf("later Close: %v", err)
	}
}

func TestAPILogTargetLockHolderProcess(t *testing.T) {
	path := os.Getenv("SERF_TEST_APILOG_LOCK_PATH")
	if path == "" {
		t.Skip("lock-holder subprocess only")
	}
	logger, err := NewAPILogger(path)
	if err != nil {
		t.Fatalf("NewAPILogger lock holder: %v", err)
	}
	defer logger.Close() //nolint:errcheck
	fmt.Println("ready")
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)
	<-signals
}

func TestAPILogTargetRejectsSymlinkAndNonRegularLeaf(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target.jsonl")
		if err := os.WriteFile(target, nil, 0o600); err != nil {
			t.Fatalf("write target: %v", err)
		}
		link := filepath.Join(dir, "api.jsonl")
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		if logger, err := NewAPILogger(link); err == nil {
			_ = logger.Close()
			t.Fatal("NewAPILogger followed a symlink leaf")
		}
	})

	t.Run("directory", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "api.jsonl")
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatalf("mkdir target: %v", err)
		}
		if logger, err := NewAPILogger(path); err == nil {
			_ = logger.Close()
			t.Fatal("NewAPILogger accepted a non-regular leaf")
		}
	})
}
