package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
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

// clearDuringWindow runs a real thread/clear to completion while the named
// session's daemon-side handler is parked mid-request: the clear rewrites the
// rendezvous ownership to the replacement and re-roots the controller, and
// the helper then waits for the replacement's startup leases to settle so the
// parked handler's revalidation is decided on ownership, not on a busy fence.
func clearDuringWindow(t *testing.T, srv *clearIdentityServer, sessionID, mutationID string) {
	t.Helper()
	clearErr := make(chan error, 1)
	go func() {
		clearErr <- srv.clear(context.Background(), appwire.ThreadClearParams{
			Ref:                "local:" + sessionID,
			ClientMutationID:   mutationID,
			ExpectedInstanceID: sessionID,
		})
	}()
	if err := <-clearErr; err != nil {
		t.Fatalf("thread/clear during the %s window: %v", mutationID, err)
	}
	awaitRetirementSettled(t, srv)
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

// TestServeIdleTimeoutSetReArmsDeadlineAndExits proves the wire deadline
// change moves the armed timer: evener/daemon/idle-timeout/set with the exact
// ownership identity re-arms the configured 1h to the requested 1m, the
// shortened deadline claims, and serve exits nil through the same retirement
// pipeline the original timer used. The fake clock never moves before the set,
// so every settle-time re-arm is the full 1h; the shortened 1m arm is the one
// that differs.
func TestServeIdleTimeoutSetReArmsDeadlineAndExits(t *testing.T) {
	deps, state, args := newClearServeDeps(t)
	clk := newServeRetireClock()
	deps.retirementClock = clk
	rec := newRetireEventRecorder()
	deps.retirementObserve = rec.observe
	args = append(args, "--daemon-idle-timeout", "1h")
	runDir := serveArgValue(args, "--run-dir")

	done := runRetireServe(t, deps, state, args)
	rootID := rec.await(t, "root_published")
	entry := awaitRendezvousEntry(t, runDir)
	awaitRetirementSettled(t, state.srv)

	out, err := dispatchDaemonRPC(state.srv, appwire.MethodEvenerDaemonIdleTimeoutSet,
		appwire.DaemonIdleTimeoutSetParams{Identity: daemonIdentityFor(entry), TimeoutMillis: 60000})
	if err != nil {
		t.Fatalf("idle-timeout set: %v", err)
	}
	resp, ok := out.(appwire.DaemonIdleTimeoutSetResponse)
	if !ok {
		t.Fatalf("idle-timeout set response type %T", out)
	}
	if resp.Lifecycle.TimeoutMillis != 60000 {
		t.Fatalf("lifecycle TimeoutMillis = %d, want 60000", resp.Lifecycle.TimeoutMillis)
	}
	for {
		d := clk.awaitArm(t)
		if d == time.Minute {
			break
		}
		if d != time.Hour {
			t.Fatalf("arm = %v, want a settle re-arm (1h) or the shortened 1m", d)
		}
	}

	clk.Advance(time.Minute)
	clk.fire(t)
	for _, event := range []string{"claim_consumed", "prepared", "committed", "released"} {
		if id := rec.await(t, event); id != rootID {
			t.Fatalf("event %s carried root %q, want the published root %q", event, id, rootID)
		}
	}
	if err := <-done; err != nil {
		t.Fatalf("serve exit after shortened retirement: %v", err)
	}
}

// TestServeIdleTimeoutSetRefusesBadIdentityAndNegativeDeadline proves the
// setter is identity-fenced like retire: a stale generation conflicts and a
// negative deadline is invalid params, both without retargeting the
// controller.
func TestServeIdleTimeoutSetRefusesBadIdentityAndNegativeDeadline(t *testing.T) {
	assertUnretargeted := func(t *testing.T, srv *clearIdentityServer) {
		t.Helper()
		out, err := dispatchDaemonRPC(srv, appwire.MethodEvenerDaemonStatus, appwire.DaemonStatusParams{})
		if err != nil {
			t.Fatalf("status after refused set: %v", err)
		}
		status, ok := out.(appwire.DaemonStatusResponse)
		if !ok {
			t.Fatalf("status response type %T", out)
		}
		if status.Lifecycle.TimeoutMillis != 3600000 {
			t.Fatalf("refused set changed the deadline: %+v", status.Lifecycle)
		}
	}
	t.Run("stale generation conflicts", func(t *testing.T) {
		deps, state, args := newClearServeDeps(t)
		deps.retirementClock = newServeRetireClock()
		args = append(args, "--daemon-idle-timeout", "1h")
		runRetireServe(t, deps, state, args)
		entry := awaitRendezvousEntry(t, serveArgValue(args, "--run-dir"))
		awaitRetirementSettled(t, state.srv)

		stale := daemonIdentityFor(entry)
		stale.Generation = "not-the-current-ownership"
		_, err := dispatchDaemonRPC(state.srv, appwire.MethodEvenerDaemonIdleTimeoutSet,
			appwire.DaemonIdleTimeoutSetParams{Identity: stale, TimeoutMillis: 60000})
		var wire appwire.WireError
		if !errors.As(err, &wire) || wire.Code != appwire.CodeConflict {
			t.Fatalf("stale-identity idle-timeout set = %v, want conflict", err)
		}
		assertUnretargeted(t, state.srv)
	})
	t.Run("negative deadline is invalid params", func(t *testing.T) {
		deps, state, args := newClearServeDeps(t)
		deps.retirementClock = newServeRetireClock()
		args = append(args, "--daemon-idle-timeout", "1h")
		runRetireServe(t, deps, state, args)
		entry := awaitRendezvousEntry(t, serveArgValue(args, "--run-dir"))
		awaitRetirementSettled(t, state.srv)

		_, err := dispatchDaemonRPC(state.srv, appwire.MethodEvenerDaemonIdleTimeoutSet,
			appwire.DaemonIdleTimeoutSetParams{Identity: daemonIdentityFor(entry), TimeoutMillis: -1})
		var wire appwire.WireError
		if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
			t.Fatalf("negative idle-timeout set = %v, want invalid params", err)
		}
		assertUnretargeted(t, state.srv)
	})
	t.Run("overflowing deadline is invalid params", func(t *testing.T) {
		deps, state, args := newClearServeDeps(t)
		deps.retirementClock = newServeRetireClock()
		args = append(args, "--daemon-idle-timeout", "1h")
		runRetireServe(t, deps, state, args)
		entry := awaitRendezvousEntry(t, serveArgValue(args, "--run-dir"))
		awaitRetirementSettled(t, state.srv)

		// 1<<58 millis is positive, so it passes the negative check today, but
		// its nanosecond conversion wraps to exactly zero: a success response
		// that silently disables automatic retirement.
		_, err := dispatchDaemonRPC(state.srv, appwire.MethodEvenerDaemonIdleTimeoutSet,
			appwire.DaemonIdleTimeoutSetParams{Identity: daemonIdentityFor(entry), TimeoutMillis: 1 << 58})
		var wire appwire.WireError
		if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
			t.Fatalf("overflowing idle-timeout set = %v, want invalid params", err)
		}
		assertUnretargeted(t, state.srv)
	})
}

// TestServeIdleTimeoutSetRevertsDeadlineWhenClearSwapsOwnership proves the
// setter revalidates ownership after the retarget: a thread/clear that
// completes between the pre-retarget fence and the controller write re-roots
// the controller to the replacement, and the stale write must not leave its
// deadline there — the pre-write deadline is restored (here the configured
// 1h, nothing had retargeted it yet) and the stale caller gets the same
// conflict the retire path returns, while a current generation can still
// move the deadline afterwards.
func TestServeIdleTimeoutSetRevertsDeadlineWhenClearSwapsOwnership(t *testing.T) {
	deps, state, args := newClearServeDeps(t)
	deps.retirementClock = newServeRetireClock()
	rec := newRetireEventRecorder()
	deps.retirementObserve = rec.observe
	args = append(args, "--daemon-idle-timeout", "1h")
	runDir := serveArgValue(args, "--run-dir")

	runRetireServe(t, deps, state, args)
	rec.await(t, "root_published")
	entry := awaitRendezvousEntry(t, runDir)
	awaitRetirementSettled(t, state.srv)

	// Park the setter between its ownership fence and the controller write.
	releaseSet := rec.gateAt("idle_timeout_attempted")
	defer releaseSet()
	type setOutcome struct {
		resp appwire.DaemonIdleTimeoutSetResponse
		err  error
	}
	outcomeCh := make(chan setOutcome, 1)
	go func() {
		out, err := dispatchDaemonRPC(state.srv, appwire.MethodEvenerDaemonIdleTimeoutSet,
			appwire.DaemonIdleTimeoutSetParams{Identity: daemonIdentityFor(entry), TimeoutMillis: 60000})
		outcome := setOutcome{err: err}
		if resp, ok := out.(appwire.DaemonIdleTimeoutSetResponse); ok {
			outcome.resp = resp
		}
		outcomeCh <- outcome
	}()
	rec.await(t, "idle_timeout_attempted")

	// A real thread/clear runs to completion in that window: it rewrites the
	// rendezvous ownership to the replacement and re-roots the controller.
	clearDuringWindow(t, state.srv, entry.SessionID, "clear-during-idle-timeout-set")
	replacement := state.session(1)
	if replacement == nil {
		t.Fatal("thread/clear did not build a replacement session")
	}

	releaseSet()
	outcome := <-outcomeCh
	var wire appwire.WireError
	if !errors.As(outcome.err, &wire) || wire.Code != appwire.CodeConflict {
		t.Fatalf("idle-timeout set whose generation went stale = %v (resp %+v), want CodeConflict", outcome.err, outcome.resp)
	}

	// The stale write was reverted: the replacement's controller is back on
	// the configured 1h, not the archived minute the stale caller pushed.
	out, err := dispatchDaemonRPC(state.srv, appwire.MethodEvenerDaemonStatus, appwire.DaemonStatusParams{})
	if err != nil {
		t.Fatalf("status after the refused set: %v", err)
	}
	status, ok := out.(appwire.DaemonStatusResponse)
	if !ok {
		t.Fatalf("status result = %T, want DaemonStatusResponse", out)
	}
	if status.Lifecycle.TimeoutMillis != 3600000 {
		t.Fatalf("replacement deadline = %d ms, want the configured 3600000 restored", status.Lifecycle.TimeoutMillis)
	}
	if got := state.srv.GetStatus().SessionID; got != replacement.ID() {
		t.Fatalf("live session = %q, want the replacement %q", got, replacement.ID())
	}
	if status.Lifecycle.Phase != "resident" {
		t.Fatalf("lifecycle phase = %q, want resident", status.Lifecycle.Phase)
	}

	// A current generation still moves the deadline: the fence refuses stale
	// callers without wedging the setter.
	current := awaitRendezvousEntry(t, runDir)
	out, err = dispatchDaemonRPC(state.srv, appwire.MethodEvenerDaemonIdleTimeoutSet,
		appwire.DaemonIdleTimeoutSetParams{Identity: daemonIdentityFor(current), TimeoutMillis: 90000})
	if err != nil {
		t.Fatalf("idle-timeout set with the current generation: %v", err)
	}
	resp, ok := out.(appwire.DaemonIdleTimeoutSetResponse)
	if !ok || resp.Lifecycle.TimeoutMillis != 90000 {
		t.Fatalf("current-generation set = %+v, want the 90000ms deadline applied", out)
	}
}

// TestServeIdleTimeoutSetUndoYieldsToNewerWrites proves the undo is a pure
// undo by write identity: the setter reverts its own write only while that
// write is still the newest, and yields to anything newer — the root swap's
// configured reset, a later legitimate writer, and an aliased later write
// (the common same-value case a value comparison cannot distinguish) all
// survive the refusal.
func TestServeIdleTimeoutSetUndoYieldsToNewerWrites(t *testing.T) {
	// runParkedSetter serves one daemon, retargets it to a non-configured
	// pre-write deadline, then dispatches a second set parked (via the
	// idle_timeout_written gate) immediately after its controller write, and
	// returns the release func plus the parked set's outcome channel.
	runParkedSetter := func(t *testing.T, preMillis, parkedMillis int64) (release func(), outcome <-chan error, srv *clearIdentityServer, runDir string, entry rendezvous.Entry) {
		t.Helper()
		deps, st, args := newClearServeDeps(t)
		deps.retirementClock = newServeRetireClock()
		rec := newRetireEventRecorder()
		deps.retirementObserve = rec.observe
		args = append(args, "--daemon-idle-timeout", "1h")
		rd := serveArgValue(args, "--run-dir")

		runRetireServe(t, deps, st, args)
		rec.await(t, "root_published")
		e := awaitRendezvousEntry(t, rd)
		awaitRetirementSettled(t, st.srv)

		// A non-configured deadline is in effect before the parked write, so the
		// revert's restore target differs from the configured 1h.
		if _, err := dispatchDaemonRPC(st.srv, appwire.MethodEvenerDaemonIdleTimeoutSet,
			appwire.DaemonIdleTimeoutSetParams{Identity: daemonIdentityFor(e), TimeoutMillis: preMillis}); err != nil {
			t.Fatalf("pre-write set: %v", err)
		}

		rel := rec.gateAt("idle_timeout_written")
		deadlineErr := make(chan error, 1)
		go func() {
			_, err := dispatchDaemonRPC(st.srv, appwire.MethodEvenerDaemonIdleTimeoutSet,
				appwire.DaemonIdleTimeoutSetParams{Identity: daemonIdentityFor(e), TimeoutMillis: parkedMillis})
			deadlineErr <- err
		}()
		rec.await(t, "idle_timeout_written")
		return rel, deadlineErr, st.srv, rd, e
	}

	assertConflict := func(t *testing.T, err error) {
		t.Helper()
		var wire appwire.WireError
		if !errors.As(err, &wire) || wire.Code != appwire.CodeConflict {
			t.Fatalf("stale idle-timeout set = %v, want CodeConflict", err)
		}
	}
	controllerDeadline := func(t *testing.T, srv *clearIdentityServer) int64 {
		t.Helper()
		out, err := dispatchDaemonRPC(srv, appwire.MethodEvenerDaemonStatus, appwire.DaemonStatusParams{})
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		status, ok := out.(appwire.DaemonStatusResponse)
		if !ok {
			t.Fatalf("status result = %T, want DaemonStatusResponse", out)
		}
		return status.Lifecycle.TimeoutMillis
	}
	t.Run("the root swap's configured reset supersedes the stale write", func(t *testing.T) {
		release, outcome, srv, _, entry := runParkedSetter(t, 60000, 120000)
		defer release()
		// The clear completing after the parked write resets the deadline to
		// the configured baseline and mints a newer write, so the parked
		// write's token is superseded: the refusal must leave the reset in
		// place, not revert to the pre-write 60000.
		clearDuringWindow(t, srv, entry.SessionID, "clear-during-parked-idle-timeout-set")

		release()
		assertConflict(t, <-outcome)
		if got := controllerDeadline(t, srv); got != 3600000 {
			t.Fatalf("deadline after the refused stale set = %d ms, want the root swap's configured 3600000 reset", got)
		}
	})
	t.Run("a later writer's deadline survives the refused stale set", func(t *testing.T) {
		release, outcome, srv, runDir, entry := runParkedSetter(t, 60000, 120000)
		defer release()
		clearDuringWindow(t, srv, entry.SessionID, "clear-during-parked-idle-timeout-set")
		// The replacement's own archive decision lands between the stale write
		// and its revalidation, and must not be clobbered by the revert.
		current := awaitRendezvousEntry(t, runDir)
		if _, err := dispatchDaemonRPC(srv, appwire.MethodEvenerDaemonIdleTimeoutSet,
			appwire.DaemonIdleTimeoutSetParams{Identity: daemonIdentityFor(current), TimeoutMillis: 45000}); err != nil {
			t.Fatalf("later-writer set: %v", err)
		}

		release()
		assertConflict(t, <-outcome)
		if got := controllerDeadline(t, srv); got != 45000 {
			t.Fatalf("deadline after the refused stale set = %d ms, want the later writer's 45000 preserved", got)
		}
	})
	t.Run("an aliased later write with the same value survives the refusal", func(t *testing.T) {
		release, outcome, srv, runDir, entry := runParkedSetter(t, 30000, 60000)
		defer release()
		clearDuringWindow(t, srv, entry.SessionID, "clear-during-parked-idle-timeout-set")
		// The replacement's archive decision chooses the same deadline the
		// stale write pushed — the common case, both derive it from the same
		// Hub configuration. Value equality cannot tell the two writes apart;
		// the later writer's deadline must still survive.
		current := awaitRendezvousEntry(t, runDir)
		if _, err := dispatchDaemonRPC(srv, appwire.MethodEvenerDaemonIdleTimeoutSet,
			appwire.DaemonIdleTimeoutSetParams{Identity: daemonIdentityFor(current), TimeoutMillis: 60000}); err != nil {
			t.Fatalf("aliased later-writer set: %v", err)
		}

		release()
		assertConflict(t, <-outcome)
		if got := controllerDeadline(t, srv); got != 60000 {
			t.Fatalf("deadline after the refused stale set = %d ms, want the aliased later writer's 60000 preserved", got)
		}
	})
}

// TestServeManualRetirementResponseSurvivesServeCancel proves the accepted
// manual retire response is delivered over the wire. The manual trigger must
// not cancel the serve context - which closes the HTTP server and aborts the
// in-flight connection - before the caller receives Accepted. This drives the
// RPC over a real WebSocket: dispatchDaemonRPC bypasses the transport where
// the response is written, so it cannot observe a lost response.
func TestServeManualRetirementResponseSurvivesServeCancel(t *testing.T) {
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

	ctx, cancelCtx := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelCtx()
	transport, err := appwire.DialWebSocket(ctx, "ws://"+entry.Address+"/rpc", http.DefaultClient)
	if err != nil {
		t.Fatalf("dial rendezvous endpoint: %v", err)
	}
	client := appwire.NewClient(transport)
	defer client.Close()
	client.Start(context.WithoutCancel(ctx))
	if _, err := client.Initialize(ctx, appwire.InitializeParams{
		ClientInfo: appwire.ClientInfo{Name: "serve-retire-http", Version: "test"},
	}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	resp, err := client.DaemonRetire(ctx, appwire.DaemonRetireParams{Identity: daemonIdentityFor(entry)})
	if err != nil {
		t.Fatalf("manual retire over HTTP = %v, want the accepted response", err)
	}
	if !resp.Accepted {
		t.Fatalf("manual retire response = %+v, want accepted", resp)
	}
	rec.await(t, "released")
	if err := <-done; err != nil {
		t.Fatalf("serve exit: %v", err)
	}
}

// TestServeRetirementRequestCancelAfterCommitStillExits proves a client
// disconnect during the post-commit teardown cannot wedge a committed
// retirement. Commit closes admission permanently, so once it returns the
// daemon must release and exit regardless of the retiring RPC's own request
// context: the consumer is parked immediately after Commit, its request context
// is canceled, and the process must still reach "released" and exit.
func TestServeRetirementRequestCancelAfterCommitStillExits(t *testing.T) {
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

	release := rec.gateAt("committed")
	reqCtx, cancelReq := context.WithCancel(context.Background())
	defer cancelReq()
	retireErr := make(chan error, 1)
	go func() {
		raw, err := json.Marshal(appwire.DaemonRetireParams{Identity: daemonIdentityFor(entry)})
		if err != nil {
			retireErr <- err
			return
		}
		_, err = state.srv.AppServer().Router().Dispatch(reqCtx, appwire.Request{Method: appwire.MethodEvenerDaemonRetire, Params: raw})
		retireErr <- err
	}()
	rec.await(t, "committed") // consumer parked after Commit, before teardown
	cancelReq()               // the client disconnects mid-teardown
	release()

	rec.await(t, "released")
	if err := <-done; err != nil {
		t.Fatalf("serve exit after committed retirement: %v", err)
	}
	<-retireErr
}

// TestServeRetirementPostCommitTeardownFailureStillAccepted pins M5-2: a
// committed retirement never reopens admission and the process exits
// regardless, so a post-commit teardown failure must surface to the caller as
// an ACCEPTED retire with the failure kept observable in the lifecycle — never
// as a refused retire plus an RPC error. The failure is induced the only way
// the daemon itself can produce one: a routed reader is still borrowed when the
// daemon's lifetime context is canceled, so DrainReaders cannot complete.
func TestServeRetirementPostCommitTeardownFailureStillAccepted(t *testing.T) {
	deps, state, args, gated := newGatedReadServeDeps(t)
	clk := newServeRetireClock()
	deps.retirementClock = clk
	rec := newRetireEventRecorder()
	deps.retirementObserve = rec.observe
	serveCtx, cancelServe := context.WithCancel(context.Background())
	t.Cleanup(cancelServe)
	deps.notifyContext = func(context.Context, ...os.Signal) (context.Context, context.CancelFunc) {
		return serveCtx, cancelServe
	}
	// The skipped release leaves a bridge drain the shutdown teardown would wait
	// out for its full 30s budget. That budget is not what this test asserts, so
	// drive its expiry immediately via the documented injectable seam; the
	// retirement drain under test uses retirementReaderDrainBudget, not this one.
	drainExpired := make(chan time.Time)
	close(drainExpired)
	deps.drainWaitExpiry = func() <-chan time.Time { return drainExpired }
	args = append(args, "--daemon-idle-timeout", "1h")
	runDir := serveArgValue(args, "--run-dir")

	done := runRetireServe(t, deps, state, args)
	rec.await(t, "root_published")
	clk.awaitArm(t)
	entry := awaitRendezvousEntry(t, runDir)
	awaitRetirementSettled(t, state.srv)

	// Hold a real routed read so a reader is still borrowed when the drain runs:
	// DrainReaders can only fail while readers > 0 (with none, Commit has
	// already closed readersDone and the drain returns immediately).
	readErr := make(chan error, 1)
	go func() {
		_, err := dispatchDaemonRPC(state.srv, appwire.MethodEvenerJobsList, appwire.JobsListParams{})
		readErr <- err
	}()
	select {
	case <-gated.readEntered:
	case <-time.After(15 * time.Second):
		t.Fatal("routed jobs/list read never entered its handler")
	}
	defer gated.releaseRead()

	releaseGate := rec.gateAt("committed")
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
	rec.await(t, "committed")
	// Commit has returned: admission is closed forever. Cancel the daemon's
	// lifetime context so the post-commit drain (bounded by that context, not
	// the request) fails while the reader is still borrowed.
	cancelServe()
	releaseGate()

	outcome := <-outcomeCh
	if outcome.err != nil {
		t.Fatalf("retire after a post-commit teardown failure = %v, want the accepted response (the process retires regardless)", outcome.err)
	}
	if !outcome.resp.Accepted {
		t.Fatalf("retire after a post-commit teardown failure = %+v, want accepted", outcome.resp)
	}
	if outcome.resp.Lifecycle.Failure != "reader_drain_failed" {
		t.Fatalf("lifecycle failure = %q, want %q: the teardown failure must stay observable, not be swallowed", outcome.resp.Lifecycle.Failure, "reader_drain_failed")
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
	if err := <-done; err != nil {
		t.Fatalf("serve exit after a committed retirement: %v", err)
	}
}

// TestServeRetirementPreparingRefusalTypesAsPreparing pins the phase vocabulary
// the Hub's retirement gate depends on: a mutation refused while a claim is
// still preparing carries LifecycleReason "preparing", never "retiring". Only a
// committed (terminal) retirement reports "retiring" and therefore routes into
// cmd/evener-hub's resumeAfterConfirmedRetirement; a preparing claim that later
// aborts settles back to "resident" and can never be reached through that gate.
func TestServeRetirementPreparingRefusalTypesAsPreparing(t *testing.T) {
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

	// Park the manual claim BEFORE Prepare: the phase is "preparing" and the
	// claim is uncommitted, the only window in which a preparing -> resident
	// rollback is possible.
	releaseGate := rec.gateAt("claim_consumed")
	defer releaseGate()
	retireErr := make(chan error, 1)
	go func() {
		_, err := dispatchDaemonRPC(state.srv, appwire.MethodEvenerDaemonRetire,
			appwire.DaemonRetireParams{Identity: daemonIdentityFor(entry)})
		retireErr <- err
	}()
	rec.await(t, "claim_consumed")

	sessionID := state.session(0).ID()
	_, err := dispatchDaemonRPC(state.srv, appwire.MethodTurnStart, appwire.TurnStartParams{
		ClientMutationID:   "mutation-during-preparing",
		ExpectedInstanceID: sessionID,
		Ref:                "local:" + sessionID,
		Input:              []appwire.InputItem{{Type: "text", Text: "refused while preparing"}},
	})
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
		t.Fatalf("mutation during preparing = %v, want CodeUnavailable", err)
	}
	data, ok := wire.Data.(appwire.LifecycleErrorData)
	if !ok {
		raw, _ := json.Marshal(wire.Data)
		if json.Unmarshal(raw, &data) != nil {
			t.Fatalf("lifecycle error data = %#v, want appwire.LifecycleErrorData", wire.Data)
		}
	}
	if data.LifecycleReason != "preparing" {
		t.Fatalf("lifecycleReason = %q, want %q (an uncommitted claim is not retiring)", data.LifecycleReason, "preparing")
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

// serveRetireResponseDeadline bounds how long the manual-retire caller below
// waits for its response. It is a deadlock tripwire, not the mechanism: once
// the accepted response is delivered the call returns in milliseconds, and
// expiry means the handler started the exit path before answering.
const serveRetireResponseDeadline = 20 * time.Second

// TestServeManualRetirementResponsePrecedesExitCancellation proves the manual
// trigger does not start the serve exit path until the accepted response has
// reached the caller. The serve lifetime cancel IS that path -- the shutdown
// goroutine waits on ctx.Done(), closes the HTTP server and the process
// returns from serve -- so the first cancel invocation is gated on the caller
// having the response. No timing assumption is involved: a handler that
// cancels the serve context synchronously before returning its response parks
// that response behind the gate, and the caller below never sees Accepted.
// This drives the RPC over a real WebSocket: dispatchDaemonRPC bypasses the
// transport where the response is written, so it cannot observe the ordering.
func TestServeManualRetirementResponsePrecedesExitCancellation(t *testing.T) {
	deps, state, args := newClearServeDeps(t)
	clk := newServeRetireClock()
	deps.retirementClock = clk
	rec := newRetireEventRecorder()
	deps.retirementObserve = rec.observe

	responseDelivered := make(chan struct{})
	var deliveredOnce sync.Once
	markDelivered := func() { deliveredOnce.Do(func() { close(responseDelivered) }) }
	notifyContext := deps.notifyContext
	deps.notifyContext = func(ctx context.Context, signals ...os.Signal) (context.Context, context.CancelFunc) {
		next, stop := notifyContext(ctx, signals...)
		var gateOnce sync.Once
		return next, func() {
			gateOnce.Do(func() { <-responseDelivered })
			stop()
		}
	}

	done := runRetireServe(t, deps, state, args)
	rec.await(t, "root_published")
	runDir := serveArgValue(args, "--run-dir")
	entry := awaitRendezvousEntry(t, runDir)
	awaitRetirementSettled(t, state.srv)

	ctx, cancelCtx := context.WithTimeout(context.Background(), serveRetireResponseDeadline)
	defer cancelCtx()
	transport, err := appwire.DialWebSocket(ctx, "ws://"+entry.Address+"/rpc", http.DefaultClient)
	if err != nil {
		t.Fatalf("dial rendezvous endpoint: %v", err)
	}
	client := appwire.NewClient(transport)
	defer client.Close()
	client.Start(context.WithoutCancel(ctx))
	if _, err := client.Initialize(ctx, appwire.InitializeParams{
		ClientInfo: appwire.ClientInfo{Name: "serve-retire-exit-order", Version: "test"},
	}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	resp, err := client.DaemonRetire(ctx, appwire.DaemonRetireParams{Identity: daemonIdentityFor(entry)})
	if err != nil {
		// Unblock the exit path so the failure is the ordering violation, not a
		// daemon left parked in its own shutdown.
		markDelivered()
		t.Fatalf("manual retire over HTTP = %v, want the accepted response before the exit path starts", err)
	}
	if !resp.Accepted {
		markDelivered()
		t.Fatalf("manual retire response = %+v, want accepted", resp)
	}
	markDelivered()
	if err := <-done; err != nil {
		t.Fatalf("serve exit after retirement: %v", err)
	}
	if n := rec.count("released"); n != 1 {
		t.Fatalf("released events = %d, want exactly 1 (single exit owner)", n)
	}
}

// TestServeRetirementClaimRevalidatesOwnershipAfterClear proves the retirement
// claim revalidates rendezvous ownership AFTER it is taken. A thread/clear can
// run entirely inside the window between the pre-claim identity check and
// retirement.TryClaim: the clear rewrites the rendezvous ownership to the
// replacement session — changing the ownership fingerprint — and re-roots the
// controller, so the claim TryClaim then takes owns the REPLACEMENT. Without a
// post-claim revalidation the stale request retires that replacement under the
// caller's pre-clear generation.
//
// The interleaving is deterministic, not pre-seeded: the manual retire is parked
// at the claim_attempted beat (after its pre-claim check accepted the pre-clear
// generation, before TryClaim), a REAL thread/clear is then run to completion,
// and only then is the retire released. With the revalidation the claim aborts
// before the consumer and the replacement stays resident; without it the
// replacement is retired and serve exits.
func TestServeRetirementClaimRevalidatesOwnershipAfterClear(t *testing.T) {
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

	releaseRetire := rec.gateAt("claim_attempted")
	defer releaseRetire()

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
	// The retire has passed its pre-claim check against the pre-clear entry and
	// is parked immediately before TryClaim.
	rec.await(t, "claim_attempted")

	// A real thread/clear runs to completion in that window: it rewrites the
	// rendezvous ownership to the replacement and re-roots the controller.
	clearDuringWindow(t, state.srv, entry.SessionID, "clear-during-retire-claim")
	replacement := state.session(1)
	if replacement == nil {
		t.Fatal("thread/clear did not build a replacement session")
	}

	releaseRetire()

	outcome := <-outcomeCh
	var wire appwire.WireError
	if !errors.As(outcome.err, &wire) || wire.Code != appwire.CodeConflict {
		t.Fatalf("retire whose generation went stale during its claim = %v (resp %+v), want CodeConflict", outcome.err, outcome.resp)
	}
	if n := rec.count("claim_consumed"); n != 0 {
		t.Fatalf("stale retire consumed %d claims, want 0 (the claim must abort before the consumer)", n)
	}
	if n := rec.count("released"); n != 0 {
		t.Fatalf("released events = %d, want 0 (no retirement may run)", n)
	}

	// The replacement is untouched and the daemon keeps serving it.
	select {
	case err := <-done:
		t.Fatalf("serve exited (%v): the stale request retired its replacement", err)
	default:
	}
	if got := state.srv.GetStatus().SessionID; got != replacement.ID() {
		t.Fatalf("live session = %q, want the replacement %q", got, replacement.ID())
	}
	out, err := dispatchDaemonRPC(state.srv, appwire.MethodEvenerDaemonStatus, appwire.DaemonStatusParams{})
	if err != nil {
		t.Fatalf("status after the refused retire: %v", err)
	}
	status, ok := out.(appwire.DaemonStatusResponse)
	if !ok {
		t.Fatalf("status result = %T, want DaemonStatusResponse", out)
	}
	if status.Lifecycle.Phase != "resident" {
		t.Fatalf("lifecycle phase = %q, want resident: the stale claim must abort, not wedge", status.Lifecycle.Phase)
	}

	// The aborted claim left the daemon fully resident: a current generation
	// still retires it cleanly.
	current := awaitRendezvousEntry(t, runDir)
	out, err = dispatchDaemonRPC(state.srv, appwire.MethodEvenerDaemonRetire,
		appwire.DaemonRetireParams{Identity: daemonIdentityFor(current)})
	if err != nil {
		t.Fatalf("retire with the current generation after the refusal: %v", err)
	}
	resp, ok := out.(appwire.DaemonRetireResponse)
	if !ok || !resp.Accepted {
		t.Fatalf("retire with the current generation = %+v, want accepted", out)
	}
	rec.await(t, "released")
	if err := <-done; err != nil {
		t.Fatalf("serve exit after the accepted retirement: %v", err)
	}
}
