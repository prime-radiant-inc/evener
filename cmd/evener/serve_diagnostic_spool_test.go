package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/server"
)

// gatedWriter stalls any write whose payload contains gate and passes every
// other write through. It stands in for the real deployment the diagnostic
// spool exists for: a daemon whose stderr is a pipe to a reader that stopped
// draining. Ordinary lines get through, so the daemon reaches the line under
// test; that line parks forever.
type gatedWriter struct {
	gate    string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newGatedWriter(gate string) *gatedWriter {
	return &gatedWriter{gate: gate, entered: make(chan struct{}), release: make(chan struct{})}
}

func (w *gatedWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), w.gate) {
		w.once.Do(func() {
			close(w.entered)
			<-w.release
		})
	}
	return len(p), nil
}

func (w *gatedWriter) unblock() { close(w.release) }

// TestServeReturnsFromAStalledStartupDiagnostic pins that serve's own return
// does not depend on the diagnostic sink.
//
// A daemon started with its stderr piped to a reader that exited fills the pipe
// on the first line. serve writes its startup announcements on the goroutine
// that goes on to run the daemon and to join its teardown, so a synchronous
// write there means the process never reaches its serve loop, never answers
// SIGTERM, and never returns -- the supervisor's stop timeout is the only thing
// that ends it. The line is a diagnostic; losing it is the correct price.
//
// The gate matches the startup announcement, so the scenario is the real one
// rather than a writer that blocks before serve has done anything.
func TestServeReturnsFromAStalledStartupDiagnostic(t *testing.T) {
	deps, state, args := newClearServeDeps(t)
	stalled := newGatedWriter("listening on")
	deps.serveLogOut = stalled
	deps.serveHTTP = func(*http.Server, net.Listener) error {
		state.srv.shutdown()
		return http.ErrServerClosed
	}

	served := make(chan error, 1)
	go func() { served <- runServeWithDeps(args, deps) }()

	select {
	case <-stalled.entered:
	case <-time.After(30 * time.Second):
		t.Fatal("serve never wrote its startup diagnostic; this test cannot measure the stall")
	}

	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve never returned with its diagnostic sink stalled: the startup line is still " +
			"written on the goroutine that runs and joins the daemon, so a reader that stopped " +
			"draining its stderr holds the whole teardown")
	}
	stalled.unblock()
}

// TestServeDiagnosticSpoolOfferNeverBlocks pins the sink's own contract: taking
// a line is not a write, so a stalled worker cannot propagate back to the
// caller. Far more lines than the buffer holds are offered, which is what the
// single-slot version of this would need to pass at all.
func TestServeDiagnosticSpoolOfferNeverBlocks(t *testing.T) {
	w := newBlockingWriter()
	spool := newServeDiagnosticSpool(w)
	out := spool.Writer()

	offered := make(chan struct{})
	go func() {
		defer close(offered)
		for range serveDiagnosticSpoolCapacity * 4 {
			_, _ = out.Write([]byte("[serve] probe at=now session=s\n"))
		}
	}()

	select {
	case <-offered:
	case <-time.After(10 * time.Second):
		t.Fatal("a spool offer blocked on the stalled worker: the input loop would wait on its own log")
	}
	select {
	case <-w.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the spool worker never reached the stalled writer; the drop path was not exercised")
	}
	w.unblock()
	spool.Close()
}

// TestServeDiagnosticSpoolCloseIsBounded pins the shutdown join on the worker.
// An accepted line is worth flushing to a live sink, so Close waits; a write
// that can never finish is not worth holding the process for, so Close gives up
// instead of joining it forever.
func TestServeDiagnosticSpoolCloseIsBounded(t *testing.T) {
	w := newBlockingWriter()
	spool := newServeDiagnosticSpool(w)
	_, _ = spool.Writer().Write([]byte("[serve] probe at=now session=s\n"))

	select {
	case <-w.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the spool worker never reached the stalled writer; this test cannot measure the join")
	}

	closed := make(chan struct{})
	go func() {
		defer close(closed)
		spool.Close()
	}()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close waited on a write that can never finish: shutdown is joined to the sink it is leaving")
	}
	w.unblock()
}

// TestServeDiagnosticSpoolFlushesAcceptedLines is the other half of the bound:
// a healthy sink loses nothing. Without it, a Close that returned immediately
// would satisfy the tests above while truncating every shutdown tail.
func TestServeDiagnosticSpoolFlushesAcceptedLines(t *testing.T) {
	var buf syncBuffer
	spool := newServeDiagnosticSpool(&buf)
	out := spool.Writer()
	for _, line := range []string{"first\n", "second\n", "third\n"} {
		_, _ = out.Write([]byte(line))
	}
	spool.Close()
	if got, want := buf.String(), "first\nsecond\nthird\n"; got != want {
		t.Fatalf("flushed %q, want %q", got, want)
	}
}

// TestServeShutdownDoesNotJoinAnInputLoopStalledOnADiagnostic pins the
// invariant the spool exists for: the goroutine that processes input never
// waits on a diagnostic sink.
//
// A failed turn reports itself with a serveLogf line, and that call runs on the
// input loop -- the only goroutine that runs turns and the one shutdown joins
// before it closes the session. Written synchronously, a full pipe parks that
// loop inside the write, so the shutdown pass waits forever on a log line. This
// drives the real loop through a turn the scripted provider fails and parks the
// sink on the line the loop emits.
func TestServeShutdownDoesNotJoinAnInputLoopStalledOnADiagnostic(t *testing.T) {
	installServeScriptedProvider(t, &scriptedProvider{
		name: "openai",
		errorSteps: []func(llm.Request) (llm.Response, error){
			func(llm.Request) (llm.Response, error) {
				return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 403, "sign-in rejected", nil, nil)
			},
		},
	})

	deps := defaultServeDeps()
	deps.ensureConfigDirs = func() error { return nil }
	deps.seedMarketplaces = func(context.Context) error { return nil }
	deps.serveLogOut = newGatedWriter("error:")
	// The bridge is not what this test measures. Reporting its drain at once
	// keeps teardown from waiting on a feed nothing here consumes.
	deps.bridge = func(_ serveServer, _ *agent.Session, _ func(events.SessionEvent), onDrained func()) {
		onDrained()
	}

	stopSignals := make(chan context.CancelFunc, 1)
	deps.notifyContext = func(ctx context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
		next, stop := context.WithCancel(ctx)
		stopSignals <- stop
		return next, stop
	}
	servers := make(chan *inputFeedServer, 1)
	deps.newServer = func(cfg server.ServerConfig) serveServer {
		srv := &inputFeedServer{Server: server.NewServer(cfg), input: make(chan server.InputMessage)}
		servers <- srv
		return srv
	}
	sessions := make(chan *agent.Session, 1)
	buildSession := deps.newSession
	deps.newSession = func(c *llm.Client, p *provider.Profile, e execenv.ExecutionEnvironment, cfg agent.SessionConfig) (*agent.Session, error) {
		sess, err := buildSession(c, p, e, cfg)
		if err == nil {
			sessions <- sess
		}
		return sess, err
	}

	args := []string{
		"--model", "openai/test",
		"--addr", "127.0.0.1:0",
		"--dir", t.TempDir(),
		"--state-dir", t.TempDir(),
		"--run-dir", t.TempDir(),
	}
	served := make(chan error, 1)
	go func() { served <- runServeWithDeps(args, deps) }()

	sess := <-sessions
	srv := <-servers
	stop := <-stopSignals

	select {
	case srv.input <- server.InputMessage{Text: "run a turn", SessionID: sess.ID()}:
	case <-time.After(30 * time.Second):
		t.Fatal("the daemon never accepted input; this test cannot drive a turn")
	}

	stalled := deps.serveLogOut.(*gatedWriter)
	select {
	case <-stalled.entered:
	case <-time.After(30 * time.Second):
		t.Fatal("the input loop never emitted a failed-turn diagnostic; this test cannot measure the stall")
	}

	stop()
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve never returned: the input loop is parked inside a diagnostic write, so the " +
			"shutdown pass that joins it waits on the sink instead of closing the session")
	}
	stalled.unblock()
}
