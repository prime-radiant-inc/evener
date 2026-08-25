//go:build linux || darwin

package hub

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
)

// A daemon that stays in the hub's process group dies with the hub: Ctrl-C on
// the hub's terminal delivers SIGINT to the whole foreground process group,
// closing the terminal hangs up on that same group, and `evener serve` honors
// both. Spawned daemons are documented to outlive a hub restart (SpawnDaemon),
// so each one must launch detached into a session of its own — observable as
// the daemon leading both its own session and its own process group.
func TestSpawnDaemonDetachesFromHubSession(t *testing.T) {
	t.Parallel()
	bin, runDir := writeDetachFakeDaemon(t)
	entry, err := SpawnDaemon(context.Background(), bin, runDir, hubcore.SpawnRequest{}, 5*time.Second)
	if err != nil {
		t.Fatalf("SpawnDaemon: %v", err)
	}
	reapFakeDaemon(t, entry.PID)
	assertOwnSessionLeader(t, entry.PID)
}

// The resume path launches the same long-lived daemon; it must detach too.
func TestResumeDaemonDetachesFromHubSession(t *testing.T) {
	t.Parallel()
	bin, runDir := writeDetachFakeDaemon(t)
	entry, err := ResumeDaemon(context.Background(), bin, runDir, hubcore.ResumeRequest{
		SessionID: hubtest.SessionID(t),
	}, 5*time.Second)
	if err != nil {
		t.Fatalf("ResumeDaemon: %v", err)
	}
	reapFakeDaemon(t, entry.PID)
	assertOwnSessionLeader(t, entry.PID)
}

// writeDetachFakeDaemon installs a stand-in `evener` that rendezvouses with
// its own PID and then sleeps, giving the test a live process to inspect.
func writeDetachFakeDaemon(t *testing.T) (bin, runDir string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "fake-evener")
	runDir = filepath.Join(dir, "run")
	if err := os.Mkdir(runDir, 0o755); err != nil {
		t.Fatalf("create run dir: %v", err)
	}
	script := fmt.Sprintf(`#!/bin/sh
printf '{"pid":%%s,"address":"127.0.0.1:1","started_at":"2999-01-01T00:00:00Z"}\n' $$ > %[1]s/$$.json
exec sleep 30
`, strconv.Quote(runDir))
	writeFakeEvener(t, bin, script)
	return bin, runDir
}

// assertOwnSessionLeader proves the daemon cannot receive the terminal
// signals aimed at the hub: a setsid'd process leads its own session, so its
// sid equals its pid. The pgid check alone would also pass under a
// group-only regression (Setpgid without Setsid), which keeps the hub's
// session and controlling terminal — the sid check pins the real invariant.
func assertOwnSessionLeader(t *testing.T, pid int) {
	t.Helper()
	sid, err := unix.Getsid(pid)
	if err != nil {
		t.Fatalf("Getsid(%d): %v", pid, err)
	}
	if sid != pid {
		t.Fatalf("daemon pid %d lives in session %d shared with the hub: terminal signals to the hub (Ctrl-C, hangup) reach it; want its own session (sid == pid)", pid, sid)
	}
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		t.Fatalf("Getpgid(%d): %v", pid, err)
	}
	if pgid != pid {
		t.Fatalf("daemon pid %d lives in process group %d shared with the hub: want its own group (pgid == pid)", pid, pgid)
	}
}

// reapFakeDaemon stops the stand-in daemon. SpawnDaemon/ResumeDaemon already
// run cmd.Wait in a goroutine, so the kill is reaped in-process.
func reapFakeDaemon(t *testing.T, pid int) {
	t.Helper()
	t.Cleanup(func() {
		if proc, err := os.FindProcess(pid); err == nil {
			_ = proc.Kill()
		}
	})
}
