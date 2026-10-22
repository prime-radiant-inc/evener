package sshconn

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostlock"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/execsupport/shellquote"
)

// This file is Darwin-only. Missing or inconclusive lsof evidence fails, not skips.
func TestDarwinLsofLockStateRuntime(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "hub.lock")
	release, err := hostlock.AcquireLock(file)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	var snapshot []byte
	fr := &fakeRunner{runFn: func(ctx context.Context, argv []string, _ io.Reader) ([]byte, error) {
		remote := strings.Join(argv[12:], " ")
		switch remote {
		case "stat -L -f '%d %i %p' " + shellquote.RemoteWord(file), "lsof -nP -F pftDil 2>&1":
			out, err := exec.CommandContext(ctx, "/bin/sh", "-c", remote).Output()
			if strings.HasPrefix(remote, "lsof ") {
				snapshot = out
			}
			return out, err
		default:
			return nil, fmt.Errorf("unexpected remote command: %s", remote)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{})
	t.Run("exclusive-production-lock", func(t *testing.T) {
		pid, err := m.darwinSelectedLockPID(t.Context(), host, root)
		if err != nil || pid != strconv.Itoa(os.Getpid()) || !strings.Contains(string(snapshot), "\nlW\n") {
			t.Fatalf("real lsof exclusive lock: pid=%q err=%v, want current process %d and lW", pid, err, os.Getpid())
		}
		t.Logf("real global lsof reports lW and selected owner PID %s", pid)
	})
	release()
	f, err := os.OpenFile(file, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	for _, row := range []struct {
		name, field string
		lock        int
	}{
		{"shared-lock-refuses", "lR", syscall.LOCK_SH},
		{"unlocked-open-refuses", "l ", syscall.LOCK_UN},
	} {
		t.Run(row.name, func(t *testing.T) {
			if err := syscall.Flock(int(f.Fd()), row.lock|syscall.LOCK_NB); err != nil {
				t.Fatal(err)
			}
			pid, err := m.darwinSelectedLockPID(t.Context(), host, root)
			if err == nil || pid != "" || !strings.Contains(string(snapshot), "\n"+row.field+"\n") {
				t.Fatalf("real lsof %s: pid=%q err=%v, want refusal and actual %q field", row.name, pid, err, row.field)
			}
			t.Logf("real global lsof reports %q and refuses selected ownership", row.field)
		})
	}
}

// File publication, discovery, lock stat/lsof and atomic repair are real here.
// Process identity, launchd and launch-check remain modeled as in the shared test.
func TestRetainedDarwinLsofRecoveryRuntime(t *testing.T) {
	testRetainedDarwinStartupRecordRecovery(t, true)
}
