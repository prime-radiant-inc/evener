package hub

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/internal/hubendpoint"
)

// shutdownProbe records, at the moment the hub closes its listener and
// releases its lock, which of the earlier shutdown steps have already happened.
// The steps are observed through their effects on real hub state: the endpoint
// record on disk, the serve context, and the AppWire server's lifetime.
type shutdownProbe struct {
	mu              sync.Mutex
	stateRoot       string
	serveCtx        context.Context
	web             *WebServer
	listenerClosed  int
	released        int
	atListenerClose *shutdownSnapshot
	atRelease       *shutdownSnapshot
	// A background worker that outlives the cancel by a beat: it records the
	// state it sees when cancelled, then finishes only after a delay, so a hub
	// that stopped waiting for background work would move on without it.
	atBackgroundCancel *shutdownSnapshot
	atWorkerFinish     *shutdownSnapshot
	workerFinished     bool
}

type shutdownSnapshot struct {
	endpointRetired bool
	backgroundDone  bool
	appRPCDrained   bool
	listenerClosed  bool
	workerFinished  bool
}

func (p *shutdownProbe) snapshot() *shutdownSnapshot {
	_, readErr := hubendpoint.Read(p.stateRoot)
	s := &shutdownSnapshot{
		endpointRetired: readErr != nil,
		listenerClosed:  p.listenerClosed > 0,
		workerFinished:  p.workerFinished,
	}
	if p.serveCtx != nil {
		s.backgroundDone = p.serveCtx.Err() != nil
	}
	if p.web != nil {
		s.appRPCDrained = p.web.appRPC.Lifetime().Err() != nil
	}
	return s
}

type recordingListener struct {
	net.Listener
	probe *shutdownProbe
}

func (l *recordingListener) Close() error {
	l.probe.mu.Lock()
	defer l.probe.mu.Unlock()
	if l.probe.atListenerClose == nil {
		l.probe.atListenerClose = l.probe.snapshot()
	}
	l.probe.listenerClosed++
	return l.Listener.Close()
}

// workerCleanupDelay is how long the probe worker keeps running after it is
// cancelled; the hub must wait it out before closing the listener.
const workerCleanupDelay = 100 * time.Millisecond

func newShutdownProbeDeps(t *testing.T) (*shutdownProbe, Config, mainDeps) {
	t.Helper()
	_, cfg, deps := newTraceMainTestDeps(t)
	probe := &shutdownProbe{stateRoot: cfg.HubStateRoot}
	deps.acquireLock = func(string) (func(), error) {
		return func() {
			probe.mu.Lock()
			defer probe.mu.Unlock()
			probe.atRelease = probe.snapshot()
			probe.released++
		}, nil
	}
	plainListen := deps.listen
	deps.listen = func(ctx context.Context, network, addr string) (net.Listener, error) {
		ln, err := plainListen(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return &recordingListener{Listener: ln, probe: probe}, nil
	}
	deps.afterWeb = func(web *WebServer) { probe.web = web }
	deps.startLaunchPrefetch = func(ctx context.Context, _ *WebServer, startBackground func(func())) {
		startBackground(func() {
			<-ctx.Done()
			probe.mu.Lock()
			probe.atBackgroundCancel = probe.snapshot()
			probe.mu.Unlock()
			time.Sleep(workerCleanupDelay)
			probe.mu.Lock()
			probe.atWorkerFinish = probe.snapshot()
			probe.workerFinished = true
			probe.mu.Unlock()
		})
	}
	return probe, cfg, deps
}

func runShutdownProbe(cfg Config, deps mainDeps) error {
	var stderr bytes.Buffer
	return runMain([]string{"-addr", cfg.Addr, "-evener", "/bin/evener"}, &stderr, deps)
}

// Leaving the hub unwinds in a fixed order: the endpoint record is retired,
// background work is cancelled and awaited, the AppWire server drains, the
// listener closes, and only then is the lock released. A new hub may start the
// moment the lock is released, so the record and the listener must be gone by
// then.
func TestRunMainShutsDownInDependencyOrder(t *testing.T) {
	for name, serveErr := range map[string]error{
		"serve returns cleanly":  nil,
		"serve returns an error": errors.New("serve failed"),
	} {
		t.Run(name, func(t *testing.T) {
			probe, cfg, deps := newShutdownProbeDeps(t)
			deps.serve = func(ctx context.Context, _ hubHTTPServer) error {
				probe.mu.Lock()
				probe.serveCtx = ctx
				probe.mu.Unlock()
				if _, err := hubendpoint.Read(probe.stateRoot); err != nil {
					t.Errorf("the endpoint record was not published before serving: %v", err)
				}
				return serveErr
			}
			err := runShutdownProbe(cfg, deps)
			if !errors.Is(err, serveErr) {
				t.Fatalf("runMain error = %v, want %v", err, serveErr)
			}
			assertShutdownOrder(t, probe)
		})
	}
}

// A signal ends serving first: the signal context cancels background work and
// the HTTP server's own shutdown closes the listener before runMain unwinds.
// Whatever the order up to that point, the lock is still released last, after
// the endpoint record is retired and every shutdown step has finished.
func TestRunMainReleasesTheLockLastAfterASignal(t *testing.T) {
	probe, cfg, deps := newShutdownProbeDeps(t)
	signalCtx, signal := context.WithCancel(t.Context())
	deps.notifyContext = func(context.Context, ...os.Signal) (context.Context, context.CancelFunc) {
		return signalCtx, signal
	}
	deps.serve = func(ctx context.Context, srv hubHTTPServer) error {
		probe.mu.Lock()
		probe.serveCtx = ctx
		probe.mu.Unlock()
		signal()
		return serveHub(ctx, srv)
	}
	if err := runShutdownProbe(cfg, deps); err != nil {
		t.Fatalf("runMain: %v", err)
	}
	probe.mu.Lock()
	defer probe.mu.Unlock()
	rel := probe.atRelease
	if rel == nil || probe.released != 1 {
		t.Fatalf("lock released %d times", probe.released)
	}
	if !rel.listenerClosed || !rel.endpointRetired || !rel.backgroundDone || !rel.appRPCDrained || !rel.workerFinished {
		t.Errorf("the lock was released with shutdown unfinished: %+v", rel)
	}
}

// A failure after the listener is bound unwinds through the same closes: the
// listener is closed and the lock released, with no endpoint ever published.
func TestRunMainReleasesListenerAndLockWhenStartupFailsAfterListening(t *testing.T) {
	probe, cfg, deps := newShutdownProbeDeps(t)
	// A recovery state file the hub cannot decode fails startup right after the
	// listener is bound.
	recoveryDir := filepath.Join(cfg.HubStateRoot, "recovery")
	if err := os.MkdirAll(recoveryDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(recoveryDir, "state.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	served := false
	deps.serve = func(context.Context, hubHTTPServer) error { served = true; return nil }

	if err := runShutdownProbe(cfg, deps); err == nil {
		t.Fatal("runMain accepted an undecodable recovery state")
	}
	if served {
		t.Fatal("the hub served despite failing startup")
	}
	probe.mu.Lock()
	defer probe.mu.Unlock()
	if probe.listenerClosed == 0 || probe.released != 1 {
		t.Fatalf("listener closed %d times, lock released %d times; want the listener closed and the lock released once", probe.listenerClosed, probe.released)
	}
	if !probe.atRelease.listenerClosed {
		t.Fatal("the lock was released before the listener closed")
	}
	if !probe.atRelease.endpointRetired {
		t.Fatal("an endpoint record was live at release")
	}
}

func assertShutdownOrder(t *testing.T, probe *shutdownProbe) {
	t.Helper()
	probe.mu.Lock()
	defer probe.mu.Unlock()
	if probe.atListenerClose == nil || probe.atRelease == nil || probe.released != 1 {
		t.Fatalf("listener closed %d times, lock released %d times", probe.listenerClosed, probe.released)
	}
	cancelled := probe.atBackgroundCancel
	if cancelled == nil {
		t.Fatal("the background worker was never cancelled")
	}
	if !cancelled.endpointRetired {
		t.Error("background work was cancelled while the endpoint record was still published")
	}
	if cancelled.appRPCDrained || cancelled.listenerClosed {
		t.Errorf("the AppWire server drained or the listener closed before background work was cancelled: %+v", cancelled)
	}
	if probe.atWorkerFinish == nil || probe.atWorkerFinish.appRPCDrained {
		t.Error("the AppWire server began draining before background work finished")
	}
	at := probe.atListenerClose
	if !at.workerFinished {
		t.Error("the listener closed before background work finished")
	}
	if !at.endpointRetired {
		t.Error("the listener closed while the endpoint record was still published")
	}
	if !at.backgroundDone {
		t.Error("the listener closed before background work was cancelled")
	}
	if !at.appRPCDrained {
		t.Error("the listener closed before the AppWire server drained")
	}
	rel := probe.atRelease
	if !rel.listenerClosed {
		t.Error("the lock was released before the listener closed")
	}
	if !rel.endpointRetired || !rel.backgroundDone || !rel.appRPCDrained || !rel.workerFinished {
		t.Errorf("the lock was released with shutdown unfinished: %+v", rel)
	}
}
