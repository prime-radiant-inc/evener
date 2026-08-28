//go:build linux || darwin

package hub

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
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
	// Deliberately serial: this launches a real detached process and waits on
	// its filesystem rendezvous. Running it in t.Parallel makes the five-second
	// launch contract compete with this package's process-heavy parallel tests,
	// turning scheduler starvation into a false detach failure.
	bin, runDir := writeDetachFakeDaemon(t)
	entry, err := SpawnDaemon(context.Background(), bin, runDir, hubcore.SpawnRequest{Env: detachHelperEnv(runDir)}, 5*time.Second)
	if err != nil {
		t.Fatalf("SpawnDaemon: %v", err)
	}
	reapFakeDaemon(t, entry.PID)
	assertOwnSessionLeader(t, entry.PID)
}

// The resume path launches the same long-lived daemon; it must detach too.
func TestResumeDaemonDetachesFromHubSession(t *testing.T) {
	// Keep the resume half under the same package-serialization boundary as
	// SpawnDaemon; the rendezvous is an event, not a parallel-test deadline.
	bin, runDir := writeDetachFakeDaemon(t)
	entry, err := ResumeDaemon(context.Background(), bin, runDir, hubcore.ResumeRequest{
		SessionID: hubtest.SessionID(t),
		Env:       detachHelperEnv(runDir),
	}, 5*time.Second)
	if err != nil {
		t.Fatalf("ResumeDaemon: %v", err)
	}
	reapFakeDaemon(t, entry.PID)
	assertOwnSessionLeader(t, entry.PID)
}

// writeDetachFakeDaemon points the spawner at this test binary's fast helper.
// TestMain dispatches the helper before setting up the package's throwaway
// environment, so the rendezvous is the first child-side event rather than a
// shell/mkdir/sleep startup sequence competing with the package scheduler.
func writeDetachFakeDaemon(t *testing.T) (bin, runDir string) {
	t.Helper()
	dir := t.TempDir()
	bin, err := os.Executable()
	if err != nil {
		t.Fatalf("test executable: %v", err)
	}
	runDir = filepath.Join(dir, "run")
	return bin, runDir
}

func detachHelperEnv(runDir string) []string {
	return append(os.Environ(), detachHelperRunDirEnv+"="+runDir)
}

func runDetachFakeDaemon() {
	runDir := os.Getenv(detachHelperRunDirEnv)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	path := filepath.Join(runDir, fmt.Sprintf("%d.json", os.Getpid()))
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	_, writeErr := fmt.Fprintf(f, `{"pid":%d,"address":"127.0.0.1:1","started_at":"2999-01-01T00:00:00Z"}
`, os.Getpid())
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		fmt.Fprintln(os.Stderr, writeErr, closeErr)
		os.Exit(2)
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	<-signals
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
			if err := proc.Signal(syscall.SIGTERM); err != nil {
				_ = proc.Kill()
			}
		}
	})
}
