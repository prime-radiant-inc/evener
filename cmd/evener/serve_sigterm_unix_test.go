//go:build unix

package main

import (
	"bytes"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
)

const (
	serveShutdownSIGTERMHelperEnv  = "SERVE_SHUTDOWN_SIGTERM_HELPER"
	serveShutdownSIGTERMReadyEnv   = "SERVE_SHUTDOWN_SIGTERM_READY"
	serveShutdownSIGTERMReleaseEnv = "SERVE_SHUTDOWN_SIGTERM_RELEASE"
	serveShutdownSIGTERMClosedEnv  = "SERVE_SHUTDOWN_SIGTERM_CLOSED"
)

// The child side of TestServeSIGTERMDuringThreadShutdownStillTearsDown. It runs
// a real serve with the production signal wiring, triggers thread/shutdown
// through the daemon's own shutdown callback, and then holds the teardown open
// on its session drain until the parent says to let it finish.
func TestServeSIGTERMDuringThreadShutdownHelperProcess(t *testing.T) {
	if os.Getenv(serveShutdownSIGTERMHelperEnv) != "1" {
		t.Skip("helper process; driven by TestServeSIGTERMDuringThreadShutdownStillTearsDown")
	}
	deps, state, args := newClearServeDeps(t)
	deps.bridge = func(_ serveServer, _ *agent.Session, _ func(events.SessionEvent), onDrained func()) {
		go func() {
			if _, ok := waitForFileContent(os.Getenv(serveShutdownSIGTERMReleaseEnv), time.Minute); ok {
				onDrained()
			}
		}()
	}
	deps.serveHTTP = func(*http.Server, net.Listener) error {
		state.srv.shutdown()
		if err := os.WriteFile(os.Getenv(serveShutdownSIGTERMReadyEnv), []byte("ready"), 0o644); err != nil {
			t.Error(err)
		}
		return http.ErrServerClosed
	}
	if err := runServeWithDeps(args, deps); err != nil {
		t.Fatalf("serve: %v", err)
	}
	if err := os.WriteFile(os.Getenv(serveShutdownSIGTERMClosedEnv), []byte("closed"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// thread/shutdown starts serve's teardown, and teardown is where the session
// closes and kills its background jobs. A supervisor that follows the shutdown
// with SIGTERM -- the hub reaping its daemons, a test harness, a host going
// down -- must not cut that teardown short: if SIGTERM's default disposition is
// back in place by then, the daemon dies on the spot and every background job
// it owns is orphaned to init, still running. Issue #4078.
func TestServeSIGTERMDuringThreadShutdownStillTearsDown(t *testing.T) {
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	release := filepath.Join(dir, "release")
	closed := filepath.Join(dir, "closed")

	cmd := exec.Command(os.Args[0], "-test.run=^TestServeSIGTERMDuringThreadShutdownHelperProcess$")
	cmd.Env = append(os.Environ(),
		serveShutdownSIGTERMHelperEnv+"=1",
		serveShutdownSIGTERMReadyEnv+"="+ready,
		serveShutdownSIGTERMReleaseEnv+"="+release,
		serveShutdownSIGTERMClosedEnv+"="+closed,
	)
	var childOut, childErr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &childOut, &childErr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})

	output := func() string { return childOut.String() + childErr.String() }
	if _, ok := waitForFileContent(ready, 30*time.Second); !ok {
		t.Fatalf("helper never reached thread/shutdown\noutput:\n%s", output())
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal SIGTERM: %v", err)
	}
	// A grace, not a race: an unhandled SIGTERM ends the process within
	// microseconds, so any window that survives scheduling separates the two.
	time.Sleep(250 * time.Millisecond)
	if err := os.WriteFile(release, []byte("release"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("SIGTERM during thread/shutdown killed the daemon mid-teardown (exit %v), "+
			"so its background jobs outlive it\noutput:\n%s", err, output())
	}
	if _, err := os.Stat(closed); err != nil {
		t.Fatalf("serve teardown never completed: %v\noutput:\n%s", err, output())
	}
}
