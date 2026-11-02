//go:build unix

package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
)

const (
	serveShutdownSIGTERMHelperEnv = "SERVE_SHUTDOWN_SIGTERM_HELPER"
	serveShutdownSIGTERMReadyEnv  = "SERVE_SHUTDOWN_SIGTERM_READY"
)

// The child side of TestServeSIGTERMDuringThreadShutdownStillTearsDown. It runs
// a real serve with the production signal wiring, triggers thread/shutdown
// through the daemon's own shutdown callback, and holds the teardown open on its
// session drain until serve's signal handler has caught a SIGTERM. A SIGTERM
// that finds no handler installed kills the process instead, and the drain is
// never released.
func TestServeSIGTERMDuringThreadShutdownHelperProcess(t *testing.T) {
	if os.Getenv(serveShutdownSIGTERMHelperEnv) != "1" {
		t.Skip("helper process; driven by TestServeSIGTERMDuringThreadShutdownStillTearsDown")
	}
	deps, state, args := newClearServeDeps(t)

	// The signal context ending while its stop has not been called means the
	// handler caught a signal; ending through stop is serve unregistering it.
	caught := make(chan struct{})
	notifyContext := deps.notifyContext
	deps.notifyContext = func(ctx context.Context, signals ...os.Signal) (context.Context, context.CancelFunc) {
		signalCtx, stop := notifyContext(ctx, signals...)
		var stopped atomic.Bool
		go func() {
			<-signalCtx.Done()
			if !stopped.Load() {
				close(caught)
			}
		}()
		return signalCtx, func() {
			stopped.Store(true)
			stop()
		}
	}
	deps.bridge = func(_ serveServer, _ *agent.Session, _ func(events.SessionEvent), onDrained func()) {
		go func() {
			<-caught
			onDrained()
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
}

// thread/shutdown starts serve's teardown, and teardown is where the session
// closes and kills its background jobs. A supervisor that follows the shutdown
// with SIGTERM -- the hub reaping its daemons, a test harness, a host going
// down -- must not cut that teardown short: if SIGTERM's default disposition is
// back in place by then, the daemon dies on the spot and every background job
// it owns is orphaned to init, still running. Issue #4078.
func TestServeSIGTERMDuringThreadShutdownStillTearsDown(t *testing.T) {
	t.Parallel()
	ready := filepath.Join(t.TempDir(), "ready")
	cmd, output := startHelperProcess(t, "TestServeSIGTERMDuringThreadShutdownHelperProcess",
		serveShutdownSIGTERMHelperEnv+"=1",
		serveShutdownSIGTERMReadyEnv+"="+ready,
	)
	if _, ok := waitForFileContent(ready, 30*time.Second); !ok {
		t.Fatalf("helper never reached thread/shutdown\noutput:\n%s", output())
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal SIGTERM: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("SIGTERM during thread/shutdown killed the daemon before its teardown finished (exit %v), "+
			"so its background jobs outlive it\noutput:\n%s", err, output())
	}
}
