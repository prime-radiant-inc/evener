package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
	"primeradiant.com/evener/server"
)

// serveRetireClock is the cmd/evener-local fake retirement clock (the agent
// module's agenttest.FakeClock is internal and unreachable from here). Now
// advances only via Advance; NewTimer hands out test-driven timers that never
// fire on their own. Sleep/After/AfterFunc/NewTicker delegate to the standard
// library — the retirement controller uses none of them, and anything else in
// serve that might keeps real-time behavior.
type serveRetireClock struct {
	mu      sync.Mutex
	now     time.Time
	arms    chan time.Duration
	disarms chan struct{}
	timers  []*serveRetireTimer
}

func newServeRetireClock() *serveRetireClock {
	return &serveRetireClock{
		now:     time.Unix(2000, 0).UTC(),
		arms:    make(chan time.Duration, 32),
		disarms: make(chan struct{}, 32),
	}
}

func (c *serveRetireClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *serveRetireClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func (c *serveRetireClock) Sleep(d time.Duration)                  { time.Sleep(d) }
func (c *serveRetireClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

func (c *serveRetireClock) AfterFunc(d time.Duration, f func()) agent.RetirementTimer {
	return serveRealTimer{t: time.AfterFunc(d, f)}
}

func (c *serveRetireClock) NewTicker(d time.Duration) agent.RetirementTicker {
	return serveRealTicker{t: time.NewTicker(d)}
}

func (c *serveRetireClock) NewTimer(d time.Duration) agent.RetirementTimer {
	tm := &serveRetireTimer{clk: c, c: make(chan time.Time, 1)}
	c.mu.Lock()
	c.timers = append(c.timers, tm)
	c.mu.Unlock()
	c.arms <- d
	return tm
}

func (c *serveRetireClock) fire(t *testing.T) {
	t.Helper()
	c.mu.Lock()
	if len(c.timers) == 0 {
		c.mu.Unlock()
		t.Fatal("fire with no retirement timer created")
	}
	tm := c.timers[len(c.timers)-1]
	c.mu.Unlock()
	select {
	case tm.c <- c.Now():
	case <-time.After(10 * time.Second):
		t.Fatal("retirement timer channel never drained; Run is not selecting on it")
	}
}

func (c *serveRetireClock) awaitArm(t *testing.T) time.Duration {
	t.Helper()
	select {
	case d := <-c.arms:
		return d
	case <-time.After(10 * time.Second):
		t.Fatal("no retirement timer arm observed")
		return 0
	}
}

func (c *serveRetireClock) assertNoArmWithin(t *testing.T, grace time.Duration) {
	t.Helper()
	deadline := time.After(grace)
	for {
		select {
		case d := <-c.arms:
			t.Fatalf("unexpected retirement timer arm %v", d)
		case <-c.disarms:
		case <-deadline:
			return
		}
	}
}

type serveRetireTimer struct {
	clk *serveRetireClock
	c   chan time.Time
}

func (t *serveRetireTimer) C() <-chan time.Time { return t.c }

func (t *serveRetireTimer) Reset(d time.Duration) bool {
	t.clk.arms <- d
	return true
}

func (t *serveRetireTimer) Stop() bool {
	t.clk.disarms <- struct{}{}
	return true
}

type serveRealTimer struct{ t *time.Timer }

func (a serveRealTimer) C() <-chan time.Time        { return a.t.C }
func (a serveRealTimer) Stop() bool                 { return a.t.Stop() }
func (a serveRealTimer) Reset(d time.Duration) bool { return a.t.Reset(d) }

type serveRealTicker struct{ t *time.Ticker }

func (a serveRealTicker) C() <-chan time.Time   { return a.t.C }
func (a serveRealTicker) Stop()                 { a.t.Stop() }
func (a serveRealTicker) Reset(d time.Duration) { a.t.Reset(d) }

// retireEventRecorder collects retirementObserve events and lets a test park
// the consumer at a named event (gate) to hold a claim open.
type retireEventRecorder struct {
	mu     sync.Mutex
	events []retireEvent
	roots  map[string]int // event name -> times observed
	subs   map[string][]chan string
	gates  map[string]chan struct{}
}

type retireEvent struct {
	name   string
	rootID string
}

func newRetireEventRecorder() *retireEventRecorder {
	return &retireEventRecorder{
		roots: make(map[string]int),
		subs:  make(map[string][]chan string),
		gates: make(map[string]chan struct{}),
	}
}

func (r *retireEventRecorder) observe(name, rootID string) {
	r.mu.Lock()
	r.events = append(r.events, retireEvent{name: name, rootID: rootID})
	r.roots[name]++
	chans := r.subs[name]
	delete(r.subs, name)
	gate := r.gates[name]
	delete(r.gates, name)
	r.mu.Unlock()
	for _, ch := range chans {
		ch <- rootID
	}
	if gate != nil {
		<-gate
	}
}

func (r *retireEventRecorder) await(t *testing.T, name string) string {
	t.Helper()
	ch := make(chan string, 1)
	r.mu.Lock()
	for _, e := range r.events {
		if e.name == name {
			r.mu.Unlock()
			return e.rootID
		}
	}
	r.subs[name] = append(r.subs[name], ch)
	r.mu.Unlock()
	select {
	case id := <-ch:
		return id
	case <-time.After(15 * time.Second):
		t.Fatalf("retirement event %q never observed (events so far: %v)", name, r.events)
		return ""
	}
}

func (r *retireEventRecorder) count(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.roots[name]
}

// gateAt parks the next observation of name until the returned release func
// is called. Must be installed before the event can fire.
func (r *retireEventRecorder) gateAt(name string) func() {
	gate := make(chan struct{})
	r.mu.Lock()
	r.gates[name] = gate
	r.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { close(gate) }) }
}

func serveArgValue(args []string, flagName string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flagName {
			return args[i+1]
		}
	}
	return ""
}

func awaitRendezvousEntry(t *testing.T, runDir string) rendezvous.Entry {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		entries, err := rendezvous.List(runDir)
		if err == nil && len(entries) == 1 {
			return entries[0]
		}
		if time.Now().After(deadline) {
			t.Fatalf("no rendezvous entry in %s", runDir)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// awaitRetirementSettled waits until the daemon's own published lifecycle
// reports a settled, blocker-free resident, with an armed deadline when idle
// retirement is enabled. A manual retire — or the timer tick that re-proves
// eligibility — is only consumable once session startup has stopped taking
// retirement-mutation leases: TryClaim declines with a nil claim while any
// lease is active, so a trigger sent before this point is correctly refused
// rather than consumed. Session startup takes and releases leases in bursts,
// so the condition must hold continuously across a settle-hold window: a
// single observation can land in a gap between startup leases and let the
// trigger race the next one. The loop condition-watches the daemon's published
// state; the wall-clock deadline is a tripwire, never the mechanism.
func awaitRetirementSettled(t *testing.T, srv *clearIdentityServer) {
	t.Helper()
	const settleHold = 500 * time.Millisecond
	deadline := time.Now().Add(15 * time.Second)
	var last appwire.DaemonLifecycle
	var lastErr error
	var settledSince time.Time
	for {
		out, err := dispatchDaemonRPC(srv, appwire.MethodEvenerDaemonStatus, appwire.DaemonStatusParams{})
		settled := false
		if err != nil {
			lastErr = err
		} else if status, ok := out.(appwire.DaemonStatusResponse); ok {
			last, lastErr = status.Lifecycle, nil
			settled = status.Lifecycle.Phase == "resident" && len(status.Lifecycle.Blockers) == 0 &&
				(status.Lifecycle.TimeoutMillis == 0 || status.Lifecycle.Deadline != "")
		} else {
			lastErr = errors.New("daemon status returned an unexpected result type")
		}
		if settled {
			if settledSince.IsZero() {
				settledSince = time.Now()
			} else if time.Since(settledSince) >= settleHold {
				return
			}
		} else {
			settledSince = time.Time{}
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon never settled: phase=%q deadline=%q blockers=%+v err=%v", last.Phase, last.Deadline, last.Blockers, lastErr)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func daemonIdentityFor(entry rendezvous.Entry) appwire.DaemonIdentity {
	return appwire.DaemonIdentity{
		Ref:        "local:" + entry.SessionID,
		PID:        entry.PID,
		StartedAt:  entry.StartedAt.UTC().Format(time.RFC3339Nano),
		Generation: rendezvous.OwnershipFingerprint(entry),
	}
}

// dispatchDaemonRPC mirrors the server package's dispatchDaemon helper; it
// takes no *testing.T because some callers run it on their own goroutine.
func dispatchDaemonRPC(srv *clearIdentityServer, method string, params any) (any, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	return srv.AppServer().Router().Dispatch(context.Background(), appwire.Request{Method: method, Params: raw})
}

// runRetireServe starts a real serve in the background and returns its exit
// channel. Cleanup forces a shutdown if the test left the daemon running.
func runRetireServe(t *testing.T, deps serveDeps, state *clearTestState, args []string) chan error {
	t.Helper()
	done := make(chan error, 1)
	exited := make(chan struct{})
	go func() {
		done <- runServeWithDeps(args, deps)
		close(exited)
	}()
	t.Cleanup(func() {
		select {
		case <-exited:
			return
		case <-time.After(100 * time.Millisecond):
		}
		if state.srv != nil && state.srv.shutdown != nil {
			state.srv.shutdown()
		}
		select {
		case <-exited:
		case <-time.After(15 * time.Second):
			t.Errorf("serve did not exit after forced shutdown")
		}
	})
	return done
}

// TestServeRetirementAutomaticExpiryReachesRelease is the end-to-end daemon
// path: the one-hour deadline arrives, the daemon-owned timer claims, the
// claim is prepared, committed and released by the SAME root that was
// published, the rendezvous file is removed by the daemon's normal exit, the
// transcript survives, and serve exits nil.
func TestServeRetirementAutomaticExpiryReachesRelease(t *testing.T) {
	deps, state, args := newClearServeDeps(t)
	clk := newServeRetireClock()
	deps.retirementClock = clk
	rec := newRetireEventRecorder()
	deps.retirementObserve = rec.observe
	args = append(args, "--daemon-idle-timeout", "1h")
	runDir := serveArgValue(args, "--run-dir")

	done := runRetireServe(t, deps, state, args)
	rootID := rec.await(t, "root_published")
	if d := clk.awaitArm(t); d != time.Hour {
		t.Fatalf("retirement arm = %v, want the configured 1h", d)
	}
	entry := awaitRendezvousEntry(t, runDir)
	awaitRetirementSettled(t, state.srv)

	clk.Advance(time.Hour)
	clk.fire(t)
	for _, event := range []string{"claim_consumed", "prepared", "committed", "released"} {
		if id := rec.await(t, event); id != rootID {
			t.Fatalf("event %s carried root %q, want the published root %q", event, id, rootID)
		}
	}
	if err := <-done; err != nil {
		t.Fatalf("serve exit after retirement: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runDir, strconv.Itoa(entry.PID)+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rendezvous file must be removed by retirement exit, stat err = %v", err)
	}
	if _, err := os.Stat(state.session(0).TranscriptPath()); err != nil {
		t.Fatalf("transcript must survive retirement: %v", err)
	}
}

// TestServeRetirementManualTimerSingleOwner proves exactly one consumer owns
// the exit path regardless of which trigger arrives first: while one claim is
// parked in preparation, the other trigger is refused (manual) or retires
// nothing (timer tick re-proves eligibility), and the daemon exits once.
func TestServeRetirementManualTimerSingleOwner(t *testing.T) {
	t.Run("manual first blocks the timer", func(t *testing.T) {
		deps, state, args := newClearServeDeps(t)
		clk := newServeRetireClock()
		deps.retirementClock = clk
		rec := newRetireEventRecorder()
		deps.retirementObserve = rec.observe
		args = append(args, "--daemon-idle-timeout", "1h")
		runDir := serveArgValue(args, "--run-dir")

		done := runRetireServe(t, deps, state, args)
		rec.await(t, "root_published")
		clk.awaitArm(t)
		entry := awaitRendezvousEntry(t, runDir)
		awaitRetirementSettled(t, state.srv)

		release := rec.gateAt("claim_consumed")
		type retireOutcome struct {
			resp appwire.DaemonRetireResponse
			err  error
		}
		outcomeCh := make(chan retireOutcome, 1)
		go func() {
			out, err := dispatchDaemonRPC(state.srv, appwire.MethodEvenerDaemonRetire,
				appwire.DaemonRetireParams{Identity: daemonIdentityFor(entry)})
			outcome := retireOutcome{err: err}
			if resp, ok := out.(appwire.DaemonRetireResponse); ok {
				outcome.resp = resp
			}
			outcomeCh <- outcome
		}()
		rec.await(t, "claim_consumed") // manual consumer parked holding the claim

		clk.Advance(2 * time.Hour)
		clk.fire(t) // timer tick while the manual claim is open
		time.Sleep(200 * time.Millisecond)
		if n := rec.count("claim_consumed"); n != 1 {
			t.Fatalf("claim_consumed events = %d, want exactly 1 (timer must not double-consume)", n)
		}

		release()
		outcome := <-outcomeCh
		if outcome.err != nil {
			t.Fatalf("manual retire: %v", outcome.err)
		}
		if !outcome.resp.Accepted {
			t.Fatalf("manual retire not accepted: %+v", outcome.resp)
		}
		if err := <-done; err != nil {
			t.Fatalf("serve exit: %v", err)
		}
		if n := rec.count("released"); n != 1 {
			t.Fatalf("released events = %d, want exactly 1 (single exit owner)", n)
		}
	})

	t.Run("timer first blocks manual", func(t *testing.T) {
		deps, state, args := newClearServeDeps(t)
		clk := newServeRetireClock()
		deps.retirementClock = clk
		rec := newRetireEventRecorder()
		deps.retirementObserve = rec.observe
		args = append(args, "--daemon-idle-timeout", "1h")
		runDir := serveArgValue(args, "--run-dir")

		done := runRetireServe(t, deps, state, args)
		rec.await(t, "root_published")
		clk.awaitArm(t)
		entry := awaitRendezvousEntry(t, runDir)
		awaitRetirementSettled(t, state.srv)

		release := rec.gateAt("claim_consumed")
		clk.Advance(time.Hour)
		clk.fire(t)
		rec.await(t, "claim_consumed") // timer consumer parked holding the claim

		_, err := dispatchDaemonRPC(state.srv, appwire.MethodEvenerDaemonRetire,
			appwire.DaemonRetireParams{Identity: daemonIdentityFor(entry)})
		var wire appwire.WireError
		if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
			t.Fatalf("manual retire during timer preparation = %v, want lifecycle-unavailable conflict", err)
		}

		release()
		if err := <-done; err != nil {
			t.Fatalf("serve exit: %v", err)
		}
		if n := rec.count("claim_consumed"); n != 1 {
			t.Fatalf("claim_consumed events = %d, want exactly 1", n)
		}
		if n := rec.count("released"); n != 1 {
			t.Fatalf("released events = %d, want exactly 1", n)
		}
	})
}

// TestServeRetirementStaleIdentityRefused proves the retire RPC revalidates
// exact ownership before touching the admission fence: a stale generation —
// same PID and ref, drifted start instant, state dir or address — is refused
// with a conflict, no claim is consumed, and the daemon keeps serving; the
// CURRENT generation is then accepted and the daemon exits cleanly.
func TestServeRetirementStaleIdentityRefused(t *testing.T) {
	deps, state, args := newClearServeDeps(t)
	clk := newServeRetireClock()
	deps.retirementClock = clk
	rec := newRetireEventRecorder()
	deps.retirementObserve = rec.observe

	done := runRetireServe(t, deps, state, args)
	rec.await(t, "root_published")
	runDir := serveArgValue(args, "--run-dir")
	entry := awaitRendezvousEntry(t, runDir)
	awaitRetirementSettled(t, state.srv)

	fingerprint := func(mutate func(*rendezvous.Entry)) string {
		e := entry
		mutate(&e)
		return rendezvous.OwnershipFingerprint(e)
	}
	stale := map[string]string{
		"empty generation": "",
		"started-at drift": fingerprint(func(e *rendezvous.Entry) { e.StartedAt = e.StartedAt.Add(time.Second) }),
		"state-dir drift":  fingerprint(func(e *rendezvous.Entry) { e.StateDir += "/other" }),
		"address drift":    fingerprint(func(e *rendezvous.Entry) { e.Address = "127.0.0.1:1" }),
	}
	for name, generation := range stale {
		t.Run(name, func(t *testing.T) {
			identity := appwire.DaemonIdentity{
				Ref:        "local:" + entry.SessionID,
				PID:        entry.PID,
				StartedAt:  entry.StartedAt.UTC().Format(time.RFC3339Nano),
				Generation: generation,
			}
			_, err := dispatchDaemonRPC(state.srv, appwire.MethodEvenerDaemonRetire,
				appwire.DaemonRetireParams{Identity: identity})
			var wire appwire.WireError
			if !errors.As(err, &wire) || wire.Code != appwire.CodeConflict {
				t.Fatalf("stale identity retire = %v, want CodeConflict", err)
			}
			if n := rec.count("claim_consumed"); n != 0 {
				t.Fatalf("stale identity consumed %d claims, want 0", n)
			}
		})
	}

	// The daemon is unharmed: status still answers.
	out, err := dispatchDaemonRPC(state.srv, appwire.MethodEvenerDaemonStatus, appwire.DaemonStatusParams{})
	if err != nil {
		t.Fatalf("status after stale refusals: %v", err)
	}
	if _, ok := out.(appwire.DaemonStatusResponse); !ok {
		t.Fatalf("status result = %T, want DaemonStatusResponse", out)
	}

	// The current generation is accepted and the daemon exits cleanly.
	out, err = dispatchDaemonRPC(state.srv, appwire.MethodEvenerDaemonRetire,
		appwire.DaemonRetireParams{Identity: daemonIdentityFor(entry)})
	if err != nil {
		t.Fatalf("retire with current generation: %v", err)
	}
	resp, ok := out.(appwire.DaemonRetireResponse)
	if !ok || !resp.Accepted {
		t.Fatalf("retire with current generation = %+v, want accepted", out)
	}
	rec.await(t, "released")
	if err := <-done; err != nil {
		t.Fatalf("serve exit: %v", err)
	}
}

// TestServeRetirementNegativeTimeoutRejected proves flag validation happens
// before any listener or session exists: a negative timeout is a startup
// error, a malformed one is a parse error, and neither opens the listener.
func TestServeRetirementNegativeTimeoutRejected(t *testing.T) {
	newFlagSet := func(name string, _ flag.ErrorHandling) *flag.FlagSet {
		fs := flag.NewFlagSet(name, flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		return fs
	}
	t.Run("negative refused before listen", func(t *testing.T) {
		deps, _, args := newClearServeDeps(t)
		deps.newFlagSet = newFlagSet
		listened := false
		origListen := deps.listen
		deps.listen = func(ctx context.Context, network, address string) (net.Listener, error) {
			listened = true
			return origListen(ctx, network, address)
		}
		args = append(args, "--daemon-idle-timeout", "-1s")
		if err := runServeWithDeps(args, deps); err == nil {
			t.Fatal("serve accepted a negative --daemon-idle-timeout")
		}
		if listened {
			t.Fatal("listener opened before --daemon-idle-timeout validation")
		}
	})
	t.Run("malformed is a parse error", func(t *testing.T) {
		deps, _, args := newClearServeDeps(t)
		deps.newFlagSet = newFlagSet
		args = append(args, "--daemon-idle-timeout", "soon")
		if err := runServeWithDeps(args, deps); err == nil {
			t.Fatal("serve accepted a malformed --daemon-idle-timeout")
		}
	})
}

// TestServeRetirementStandaloneDefaultDoesNotRetire pins the standalone
// contract: without --daemon-idle-timeout the daemon runs forever — no
// expiry timer is armed, no claim is consumed across days of virtual time,
// and the lifecycle status reports the disabled timeout explicitly.
func TestServeRetirementStandaloneDefaultDoesNotRetire(t *testing.T) {
	deps, state, args := newClearServeDeps(t)
	clk := newServeRetireClock()
	deps.retirementClock = clk
	rec := newRetireEventRecorder()
	deps.retirementObserve = rec.observe

	done := runRetireServe(t, deps, state, args)
	rec.await(t, "root_published")
	clk.assertNoArmWithin(t, 200*time.Millisecond)
	clk.Advance(72 * time.Hour)
	if n := rec.count("claim_consumed"); n != 0 {
		t.Fatalf("standalone daemon consumed %d retirement claims, want 0", n)
	}
	out, err := dispatchDaemonRPC(state.srv, appwire.MethodEvenerDaemonStatus, appwire.DaemonStatusParams{})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	status, ok := out.(appwire.DaemonStatusResponse)
	if !ok {
		t.Fatalf("status result = %T, want DaemonStatusResponse", out)
	}
	if status.Lifecycle.TimeoutMillis != 0 {
		t.Fatalf("standalone TimeoutMillis = %d, want 0 (retirement disabled)", status.Lifecycle.TimeoutMillis)
	}

	state.srv.shutdown()
	if err := <-done; err != nil {
		t.Fatalf("serve exit: %v", err)
	}
	if n := rec.count("released"); n != 0 {
		t.Fatalf("released events on shutdown = %d, want 0 (shutdown is not retirement)", n)
	}
}

// gatedReadServer gates the first routed evener/jobs/list read inside the
// serve-installed jobs callback. It lets a test hold a REAL routed read (and
// the admission borrow the router took to admit it) while a retirement
// commits, without substituting a counter for the router's own admission.
type gatedReadServer struct {
	*clearIdentityServer
	readEntered chan struct{}
	readGate    chan struct{}
	gateOnce    sync.Once
	releaseOnce sync.Once
}

func (s *gatedReadServer) SetJobsFunc(fn func(appwire.JobsListParams) (any, error)) {
	s.clearIdentityServer.SetJobsFunc(func(params appwire.JobsListParams) (any, error) {
		s.gateOnce.Do(func() {
			close(s.readEntered)
			<-s.readGate
		})
		return fn(params)
	})
}

// releaseRead unblocks the held read exactly once.
func (s *gatedReadServer) releaseRead() {
	s.releaseOnce.Do(func() { close(s.readGate) })
}

// newGatedReadServeDeps is newClearServeDeps with the jobs read callback gated.
func newGatedReadServeDeps(t *testing.T) (serveDeps, *clearTestState, []string, *gatedReadServer) {
	t.Helper()
	deps, state, args := newClearServeDeps(t)
	gated := &gatedReadServer{readEntered: make(chan struct{}), readGate: make(chan struct{})}
	deps.newServer = func(cfg server.ServerConfig) serveServer {
		gated.clearIdentityServer = &clearIdentityServer{Server: server.NewServer(cfg), state: state}
		state.srv = gated.clearIdentityServer
		return gated
	}
	return deps, state, args, gated
}

// TestServeRetirementAdmissionRefusesMutationWithTypedLifecycleError proves the
// daemon's own admission boundary refuses a routed mutation while the process
// is retiring, and types that refusal so a peer can retry automatically. The
// manual retirement is parked immediately after Commit: the phase is
// "retiring" but the stores ReleaseForRetirement closes are still open, so the
// refusal can only come from the admission fence. Before this fix no admission
// was installed, the turn/start handler ran, and its retirement fence failure
// was normalized into CodeInternalError with cause "persistenceUnavailable" --
// a shape cmd/evener-hub's isLifecycleRetiringError never matches, so
// resumeAfterConfirmedRetirement never fires.
func TestServeRetirementAdmissionRefusesMutationWithTypedLifecycleError(t *testing.T) {
	deps, state, args := newClearServeDeps(t)
	clk := newServeRetireClock()
	deps.retirementClock = clk
	rec := newRetireEventRecorder()
	deps.retirementObserve = rec.observe
	args = append(args, "--daemon-idle-timeout", "1h")
	runDir := serveArgValue(args, "--run-dir")

	done := runRetireServe(t, deps, state, args)
	rec.await(t, "root_published")
	clk.awaitArm(t)
	entry := awaitRendezvousEntry(t, runDir)
	awaitRetirementSettled(t, state.srv)

	releaseGate := rec.gateAt("committed")
	defer releaseGate()
	retireErr := make(chan error, 1)
	go func() {
		_, err := dispatchDaemonRPC(state.srv, appwire.MethodEvenerDaemonRetire,
			appwire.DaemonRetireParams{Identity: daemonIdentityFor(entry)})
		retireErr <- err
	}()
	rec.await(t, "committed")

	sessionID := state.session(0).ID()
	_, err := dispatchDaemonRPC(state.srv, appwire.MethodTurnStart, appwire.TurnStartParams{
		ClientMutationID:   "mutation-during-retirement",
		ExpectedInstanceID: sessionID,
		Ref:                "local:" + sessionID,
		Input:              []appwire.InputItem{{Type: "text", Text: "must be refused"}},
	})
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("mutation during retirement = %v (%T), want a typed appwire.WireError", err, err)
	}
	if wire.Code != appwire.CodeUnavailable {
		t.Fatalf("mutation during retirement returned code %d (%s, data %#v); want %d CodeUnavailable, not CodeInternalError/persistenceUnavailable",
			wire.Code, wire.Message, wire.Data, appwire.CodeUnavailable)
	}
	data, ok := wire.Data.(appwire.LifecycleErrorData)
	if !ok {
		raw, _ := json.Marshal(wire.Data)
		if json.Unmarshal(raw, &data) != nil {
			t.Fatalf("lifecycle error data = %#v, want appwire.LifecycleErrorData", wire.Data)
		}
	}
	if data.EvenerErrorInfo != appwire.ErrorActionUnavailable {
		t.Errorf("evenerErrorInfo = %q, want %q", data.EvenerErrorInfo, appwire.ErrorActionUnavailable)
	}
	if data.LifecycleReason != "retiring" {
		t.Errorf("lifecycleReason = %q, want %q", data.LifecycleReason, "retiring")
	}
	if !data.Retryable || data.RetryDisposition != appwire.RetryDispositionAutomatic {
		t.Errorf("retryable = %v, retryDisposition = %q; want true/automatic", data.Retryable, data.RetryDisposition)
	}
	if data.Cause == "persistenceUnavailable" {
		t.Errorf("cause = %q, want a typed lifecycle refusal rather than persistenceUnavailable", data.Cause)
	}

	releaseGate()
	if err := <-retireErr; err != nil {
		t.Fatalf("retire: %v", err)
	}
	rec.await(t, "released")
	if err := <-done; err != nil {
		t.Fatalf("serve exit: %v", err)
	}
}

// TestServeRetirementAdmissionDrainReadersWaitsForInFlightRead proves the drain
// is live: a read admitted through the daemon's router holds a real borrow, and
// after Commit closes admission the retirement must wait for that in-flight
// read before ReleaseForRetirement can run. Before this fix no admission was
// installed, readers was always zero, and DrainReaders returned immediately
// while the read was still inside its handler.
func TestServeRetirementAdmissionDrainReadersWaitsForInFlightRead(t *testing.T) {
	deps, state, args, gated := newGatedReadServeDeps(t)
	clk := newServeRetireClock()
	deps.retirementClock = clk
	rec := newRetireEventRecorder()
	deps.retirementObserve = rec.observe
	args = append(args, "--daemon-idle-timeout", "1h")
	runDir := serveArgValue(args, "--run-dir")

	done := runRetireServe(t, deps, state, args)
	rec.await(t, "root_published")
	clk.awaitArm(t)
	entry := awaitRendezvousEntry(t, runDir)
	awaitRetirementSettled(t, state.srv)

	// Hold a real routed read: Router admission Borrows for the in-flight
	// handler, and the registered evener/jobs/list handler parks inside the
	// serve-installed jobs callback until readGate closes.
	readErr := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				readErr <- fmt.Errorf("held read panicked: %v", r)
			}
		}()
		_, err := dispatchDaemonRPC(state.srv, appwire.MethodEvenerJobsList, appwire.JobsListParams{})
		readErr <- err
	}()
	select {
	case <-gated.readEntered:
	case <-time.After(15 * time.Second):
		t.Fatal("routed jobs/list read never entered its handler")
	}
	defer gated.releaseRead()

	retireErr := make(chan error, 1)
	go func() {
		_, err := dispatchDaemonRPC(state.srv, appwire.MethodEvenerDaemonRetire,
			appwire.DaemonRetireParams{Identity: daemonIdentityFor(entry)})
		retireErr <- err
	}()
	rec.await(t, "committed")

	// DrainReaders must now be waiting for the borrowed read, so retirement must
	// not complete while the read is held. This bounded wait is a tripwire for
	// the drain contract, not the mechanism: the mechanism is the committed
	// retirement blocked on readersDone.
	drainedWhileHeld := false
	select {
	case <-done:
		drainedWhileHeld = true
	case <-time.After(2 * time.Second):
	}
	if n := rec.count("released"); n != 0 {
		t.Errorf("released events while a read is in flight = %d, want 0", n)
	}

	gated.releaseRead()
	select {
	case err := <-readErr:
		if err != nil {
			t.Fatalf("held read: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("held read did not return after its gate was released")
	}
	rec.await(t, "released")
	if err := <-retireErr; err != nil {
		t.Fatalf("retire: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("serve exit: %v", err)
	}
	if drainedWhileHeld {
		t.Fatal("retirement completed while a routed read was still in flight: DrainReaders did not wait for the borrowed reader")
	}
}
