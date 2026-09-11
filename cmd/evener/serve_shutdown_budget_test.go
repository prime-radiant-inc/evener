package main

import (
	"context"
	"net"
	"net/http"
	"os"
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

// TestServeWaitsForALiveDrainWithinTheBudget pins the arm the budget must not
// touch: a drain that is merely still working is waited for, in full.
//
// The budget exists for a wedged drain. If it ever expires on a healthy one it
// stops being a backstop and becomes a silent truncation of the very tail the
// wait was added to deliver -- and worse, it would mask a real slowness bug
// behind a shutdown that looks clean. This is the pin that says so: the drain
// finishes only when the test lets it, and serve must not move until then.
//
// The budget it runs against is the PRODUCTION one. drainWaitExpiry is wrapped
// rather than replaced, so shrinking shutdownDrainWaitBudget still reaches the
// timer under test; the wrapper only reports when the wait began, which is what
// makes the assertion below tight instead of a guess about serve's startup
// cost.
func TestServeWaitsForALiveDrainWithinTheBudget(t *testing.T) {
	deps, state, args := newClearServeDeps(t)

	release := make(chan struct{})
	deps.bridge = func(_ serveServer, _ *agent.Session, _ func(events.SessionEvent), onDrained func()) {
		go func() {
			<-release
			onDrained()
		}()
	}

	realExpiry := deps.drainWaitExpiry
	waitStarted := make(chan struct{})
	deps.drainWaitExpiry = func() <-chan time.Time {
		close(waitStarted)
		return realExpiry()
	}

	deps.serveHTTP = func(*http.Server, net.Listener) error {
		state.srv.shutdown()
		return http.ErrServerClosed
	}

	served := make(chan error, 1)
	go func() { served <- runServeWithDeps(args, deps) }()

	select {
	case <-waitStarted:
	case <-time.After(30 * time.Second):
		t.Fatal("serve never reached its drain wait")
	}

	// A grace, not a race. Under the production budget serve cannot leave the
	// select at all until the drain reports; under a budget short enough to
	// absorb real work it leaves within microseconds of the wait starting, so
	// any window that survives goroutine scheduling separates the two.
	select {
	case err := <-served:
		t.Fatalf("serve abandoned a live drain instead of waiting for it (returned %v): "+
			"the shutdown budget is absorbing normal work, so a slow drain now truncates "+
			"the tail silently instead of being delivered", err)
	case <-time.After(250 * time.Millisecond):
	}

	close(release)
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("serve never returned after its drain reported completion")
	}
}

// TestServeAbandonsAWedgedDrainWithoutClosingTheObserver pins the other arm,
// and specifically pins what expiry MUST NOT do.
//
// A drain wedged inside BridgeEvent used to make the daemon ignore SIGTERM
// until a supervisor SIGKILLed it. The budget ends that. But the obvious
// implementation of a budget -- expire, then close the tee and carry on -- puts
// back the crash the wait was added to prevent: observe on a closed tee is
// `t.ch <- ev` on a closed channel, which panics on the DRAIN's goroutine, takes
// the process down and skips every defer registered above the teardown. Expiry
// must therefore abandon the tee, not close it. A truncated diagnostic tail on a
// process that is already exiting is the whole cost.
//
// The probe is the real mechanism rather than a proxy: the observer serve built
// is captured through the bridge and called after serve returns. On an abandoned
// tee that is an ordinary queued write; on a closed one it is the panic.
//
// Expiry is driven by an already-closed channel, so the arm under test is
// selected by construction and no clock is involved.
func TestServeAbandonsAWedgedDrainWithoutClosingTheObserver(t *testing.T) {
	deps, state, args := newClearServeDeps(t)
	args = append(args, "--verbose")
	deps.verboseOut = newDiscardWriter()

	// A drain that never reports: onDrained is dropped on the floor, which is
	// what a bridge wedged inside BridgeEvent looks like from here.
	observed := make(chan func(events.SessionEvent), 1)
	deps.bridge = func(_ serveServer, _ *agent.Session, observer func(events.SessionEvent), _ func()) {
		select {
		case observed <- observer:
		default:
		}
	}

	expired := make(chan time.Time)
	close(expired)
	deps.drainWaitExpiry = func() <-chan time.Time { return expired }

	deps.serveHTTP = func(*http.Server, net.Listener) error {
		state.srv.shutdown()
		return http.ErrServerClosed
	}

	served := make(chan error, 1)
	go func() { served <- runServeWithDeps(args, deps) }()

	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("serve never returned with its drain wedged: the shutdown budget did not expire, " +
			"so the daemon still ignores SIGTERM until something kills it")
	}

	var observer func(events.SessionEvent)
	select {
	case observer = <-observed:
	default:
		t.Fatal("serve never handed its observer to the bridge; this test would prove nothing")
	}

	closed := func() (panicked bool) {
		defer func() { panicked = recover() != nil }()
		observer(events.SessionEvent{Kind: events.EventWarning, SessionID: "wedged"})
		return false
	}()
	if closed {
		t.Fatal("expiry CLOSED the verbose sink with a drain still live: the next event the drain " +
			"delivers panics with send on closed channel, on the drain's own goroutine, which is " +
			"exactly the shutdown crash the wait exists to prevent")
	}
}

// inputFeedServer is the real daemon server with the input channel handed to
// the test. The loop under test is the daemon's own; only the supply of work to
// it is scripted, so nothing about how the loop emits is faked.
type inputFeedServer struct {
	*server.Server

	input chan server.InputMessage
}

func (s *inputFeedServer) InputCh() <-chan server.InputMessage { return s.input }

// TestServeShutdownReleasesAnInputLoopParkedOnAWedgedBridge is the end-to-end
// shape of the deadlock: the daemon's input loop parked in an authoritative
// send, holding eventsMu.RLock, behind a bridge that stopped draining.
//
// Every link is the production one. The bridge takes the authoritative mark
// through ConsumeEventsLossless and then wedges inside its consume callback,
// which is what a bridge stuck in BridgeEvent looks like. Real turns fill the
// session's real 256-event buffer until the loop can neither deliver nor drop,
// and the loop is the goroutine shutdown waits for before it calls
// CloseForShutdown -- the only publisher of the close budget the parked send
// would otherwise need.
//
// The conjunction the test waits on is what makes it deterministic rather than
// timed: nothing drains, so a full buffer never empties again, and the daemon
// cannot leave the processing state without emitting the turn's own terminal
// boundary into that full buffer. Full plus processing is therefore a state the
// loop can only be in because it is parked.
func TestServeShutdownReleasesAnInputLoopParkedOnAWedgedBridge(t *testing.T) {
	shrinkCloseBudget(t, 20*time.Millisecond)

	installServeScriptedProvider(t, &scriptedProvider{name: "openai"})
	deps := defaultServeDeps()
	deps.ensureConfigDirs = func() error { return nil }
	deps.seedMarketplaces = func(context.Context) error { return nil }

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

	// The wedge outlives the whole run and is released only as this test
	// returns: a bridge that resumed draining would release the parked send by
	// DELIVERING the event, which is the one way this test could pass without
	// observing the lifetime at all.
	unwedge := make(chan struct{})
	defer close(unwedge)
	deps.bridge = func(_ serveServer, sess *agent.Session, _ func(events.SessionEvent), onDrained func()) {
		sess.ConsumeEventsLossless(func(events.SessionEvent) { <-unwedge }, onDrained)
	}
	// Teardown must not spend the production drain budget waiting on a drain
	// this test never lets finish.
	expired := make(chan time.Time, 1)
	expired <- time.Now()
	deps.drainWaitExpiry = func() <-chan time.Time { return expired }

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

	stopFeed := make(chan struct{})
	stopFeeding := sync.OnceFunc(func() { close(stopFeed) })
	defer stopFeeding()
	go func() {
		for {
			select {
			case srv.input <- server.InputMessage{Text: "fill the buffer", SessionID: sess.ID()}:
			case <-stopFeed:
				return
			}
		}
	}()

	// TRIPWIRE: real scripted turns fill 256 buffered events in well under a
	// second; 30s only fires if the loop never reaches the saturated state at
	// all, which would mean this test is no longer building the scenario.
	parkDeadline := time.After(30 * time.Second)
	for len(sess.Events()) != cap(sess.Events()) ||
		srv.GetStatus().State != string(agent.SessionProcessing) {
		select {
		case <-parkDeadline:
			t.Fatalf("the daemon's input loop never parked in a saturated authoritative send "+
				"(buffered %d of %d, state %q)", len(sess.Events()), cap(sess.Events()), srv.GetStatus().State)
		case <-time.After(time.Millisecond):
		}
	}

	// Stop supplying work before shutdown so the only turn shutdown interrupts
	// is the parked one: every further message would be another turn cancelled
	// mid-flight, and the daemon logs each of those. Stopping does not unpark
	// the loop -- nothing drains the buffer it is parked on.
	stopFeeding()

	stop()
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("runServeWithDeps: %v", err)
		}
	// TRIPWIRE: shutdown past a released send is in-process teardown over an
	// already-expired drain budget and a 20ms close budget -- milliseconds. 30s
	// only fires on the deadlock this test exists for.
	case <-time.After(30 * time.Second):
		t.Fatal("shutdown never completed: the input loop parked in an authoritative send was not " +
			"released, so the wait on it -- and the close that would publish the budget -- cannot finish")
	}
}
