package mcp

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/mcpconfig"
	"primeradiant.com/evener/internal/testwait"
)

// TestRetryFailed_FirstConnectFailureRecovers pins the recovery contract #6126
// asks for: a server whose FIRST connect fails registers no tools, so the lazy
// reconnect inside a registered tool's closure can never run for it. Retrying
// the dial through RetryFailed must connect the server, register its tools into
// the caller's registry, and report the recovery — all without a new manager.
func TestRetryFailed_FirstConnectFailureRecovers(t *testing.T) {
	ctx := context.Background()

	// The transport the second dial hands back. Built before NewManager so the
	// dial factory itself never calls into the test's *testing.T from the
	// manager's own goroutine.
	_, ct := newReconnectTestServer(t, "s", "probe", "recovered-reply")
	startupErr := errors.New("first connect: server not ready")

	var dialCalls atomic.Int32
	dial := func(context.Context) (mcpsdk.Transport, error) {
		dialCalls.Add(1)
		if dialCalls.Load() == 1 {
			return nil, startupErr
		}
		return ct, nil
	}

	// A fixed clock: the failed retry below must suppress an immediate second
	// attempt deterministically, not by racing the real 30s window.
	now := time.Unix(1000, 0)
	mgr, outcomes := newManagerWithClock(ctx, []mcpconfig.ServerConfig{{Name: "s", Type: "stdio"}},
		[]func(context.Context) (mcpsdk.Transport, error){dial},
		func() time.Time { return now })
	if len(outcomes) != 1 || outcomes[0].Stage != "connect" {
		t.Fatalf("NewManager outcomes = %+v, want one connect-stage failure", outcomes)
	}
	defer mgr.Close()

	reg := tool.NewRegistry()
	if outs := mgr.RegisterTools(reg); len(outs) != 0 {
		t.Fatalf("RegisterTools = %+v, want nothing registered for a failed conn", outs)
	}
	if reg.Get("s__probe") != nil {
		t.Fatal("a server that failed its first connect registered a tool")
	}

	recovered, outs := mgr.RetryFailed(ctx, reg)
	if len(outs) != 0 {
		t.Fatalf("RetryFailed outcomes = %+v, want none", outs)
	}
	if len(recovered) != 1 || recovered[0].Name != "s" {
		t.Fatalf("RetryFailed recovered = %+v, want the one server", recovered)
	}
	if len(recovered[0].Tools) != 1 || recovered[0].Tools[0] != "s__probe" {
		t.Fatalf("RetryFailed recovery tools = %v, want [s__probe]", recovered[0].Tools)
	}
	if reg.Get("s__probe") == nil {
		t.Fatal("the recovered server's tool is missing from the registry")
	}
	if out, isErr := execProbe(ctx, reg, t); isErr || out != "recovered-reply" {
		t.Fatalf("recovered tool = (%q, isErr=%v), want (%q, false)", out, isErr, "recovered-reply")
	}

	// The conn is connected now: a second retry has nothing to do and must not
	// dial again.
	callsAfterRecovery := dialCalls.Load()
	if recovered, outs := mgr.RetryFailed(ctx, reg); len(recovered) != 0 || len(outs) != 0 {
		t.Fatalf("second RetryFailed = (%+v, %+v), want no work", recovered, outs)
	}
	if got := dialCalls.Load(); got != callsAfterRecovery {
		t.Fatalf("second RetryFailed dialed: %d calls, want %d", got, callsAfterRecovery)
	}
}

// TestRetryFailed_FailedRetryBacksOff pins that a retry which fails keeps the
// server retryable and starts the same backoff window a failed lazy reconnect
// starts, so a caller retrying once per round does not pay a dial timeout every
// round against a server that is still down — and recovers once it answers.
func TestRetryFailed_FailedRetryBacksOff(t *testing.T) {
	ctx := context.Background()

	_, ct := newReconnectTestServer(t, "s", "probe", "late-reply")

	var dialCalls atomic.Int32
	var serverUp bool
	dial := func(context.Context) (mcpsdk.Transport, error) {
		dialCalls.Add(1)
		if !serverUp {
			return nil, errors.New("connect: still down")
		}
		return ct, nil
	}

	now := time.Unix(1000, 0)
	mgr, outcomes := newManagerWithClock(ctx, []mcpconfig.ServerConfig{{Name: "s", Type: "stdio"}},
		[]func(context.Context) (mcpsdk.Transport, error){dial},
		func() time.Time { return now })
	if len(outcomes) != 1 {
		t.Fatalf("NewManager outcomes = %+v, want one connect-stage failure", outcomes)
	}
	defer mgr.Close()

	reg := tool.NewRegistry()
	if outs := mgr.RegisterTools(reg); len(outs) != 0 {
		t.Fatalf("RegisterTools = %+v, want nothing registered for a failed conn", outs)
	}

	// First retry: the server is still down, so the attempt fails and is
	// reported as a connect-stage outcome.
	recovered, outs := mgr.RetryFailed(ctx, reg)
	if len(recovered) != 0 || len(outs) != 1 || outs[0].Stage != "connect" {
		t.Fatalf("RetryFailed while down = (%+v, %+v), want one connect-stage outcome", recovered, outs)
	}
	callsAfterFirstRetry := dialCalls.Load()

	// Inside the backoff window: no dial, no outcome, still nothing registered.
	if recovered, outs := mgr.RetryFailed(ctx, reg); len(recovered) != 0 || len(outs) != 0 {
		t.Fatalf("RetryFailed inside backoff = (%+v, %+v), want no attempt", recovered, outs)
	}
	if got := dialCalls.Load(); got != callsAfterFirstRetry {
		t.Fatalf("RetryFailed inside backoff dialed: %d calls, want %d", got, callsAfterFirstRetry)
	}
	if reg.Get("s__probe") != nil {
		t.Fatal("a server that is still down registered a tool")
	}

	// The cause clears and the backoff elapses: the next retry connects the
	// server and registers its tool.
	serverUp = true
	now = now.Add(reconnectBackoff + time.Second)
	recovered, outs = mgr.RetryFailed(ctx, reg)
	if len(outs) != 0 {
		t.Fatalf("RetryFailed after recovery outcomes = %+v, want none", outs)
	}
	if len(recovered) != 1 || recovered[0].Name != "s" {
		t.Fatalf("RetryFailed after recovery = %+v, want the one server", recovered)
	}
	if reg.Get("s__probe") == nil {
		t.Fatal("the recovered server's tool is missing from the registry")
	}
	if out, isErr := execProbe(ctx, reg, t); isErr || out != "late-reply" {
		t.Fatalf("recovered tool = (%q, isErr=%v), want (%q, false)", out, isErr, "late-reply")
	}
}

// TestRetryFailed_RecoveredConnSurvivesLaterDrop pins that a recovered conn is
// as complete as one that connected at init: after its connection drops, the
// lazy reconnect in the registered tool's closure must redial it. A conn built
// from a failed first connect has no client, so recovery must install the
// manager's shared one along with the session.
func TestRetryFailed_RecoveredConnSurvivesLaterDrop(t *testing.T) {
	ctx := context.Background()

	ss1, ct1 := newReconnectTestServer(t, "s", "probe", "first-reply")
	ss2, ct2 := newReconnectTestServer(t, "s", "probe", "second-reply")
	transports := []mcpsdk.Transport{ct1, ct2}
	var dialCalls atomic.Int32
	dial := func(context.Context) (mcpsdk.Transport, error) {
		n := dialCalls.Add(1)
		if n == 1 {
			return nil, errors.New("first connect: server not ready")
		}
		if int(n)-2 >= len(transports) {
			return nil, errors.New("redial refused")
		}
		return transports[n-2], nil
	}

	now := time.Unix(1000, 0)
	mgr, outcomes := newManagerWithClock(ctx, []mcpconfig.ServerConfig{{Name: "s", Type: "stdio"}},
		[]func(context.Context) (mcpsdk.Transport, error){dial},
		func() time.Time { return now })
	if len(outcomes) != 1 {
		t.Fatalf("NewManager outcomes = %+v, want one connect-stage failure", outcomes)
	}
	defer mgr.Close()

	reg := tool.NewRegistry()
	if outs := mgr.RegisterTools(reg); len(outs) != 0 {
		t.Fatalf("RegisterTools = %+v, want nothing registered for a failed conn", outs)
	}
	recovered, outs := mgr.RetryFailed(ctx, reg)
	if len(recovered) != 1 || len(outs) != 0 {
		t.Fatalf("RetryFailed = (%+v, %+v), want one recovery", recovered, outs)
	}
	if out, isErr := execProbe(ctx, reg, t); isErr || out != "first-reply" {
		t.Fatalf("recovered tool = (%q, isErr=%v), want (%q, false)", out, isErr, "first-reply")
	}

	// Drop the recovered connection. The next call must redial through the same
	// lazy path an init-connected server uses, and reach the NEW session.
	if err := ss1.Close(); err != nil {
		t.Fatalf("server-side close: %v", err)
	}
	now = now.Add(reconnectBackoff + time.Second)
	out, isErr := execProbe(ctx, reg, t)
	if isErr || out != "second-reply" {
		t.Fatalf("post-drop call = (%q, isErr=%v), want (%q, false) via a lazy reconnect", out, isErr, "second-reply")
	}
	if got := dialCalls.Load(); got != 3 {
		t.Fatalf("dial factory called %d times, want 3 (first connect, retry, lazy reconnect)", got)
	}
	_ = ss2 // the second server's session exists only to serve the redial
}

// TestRetryFailed_DialsConcurrently pins the batch's cost invariant: its total
// dial time is ONE reconnectDialTimeout, not one per server. The caller runs it
// on the turn path, so a serial batch of stalled servers would delay every
// round — and, once the batch outlasts reconnectBackoff, would find the earlier
// servers' windows already elapsed and pay the whole batch again each round.
//
// The proof is overlap rather than elapsed wall time: every dial announces
// itself before blocking, and the test releases the batch only after all of them
// have arrived. A serial loop deadlocks here until the test's own timeout, so
// there is no duration assertion to flake in CI.
func TestRetryFailed_DialsConcurrently(t *testing.T) {
	const servers = 4
	var arrived atomic.Int32
	var calls [4]atomic.Int32
	arrivedAll := make(chan struct{})
	release := make(chan struct{})
	dials := make([]func(context.Context) (mcpsdk.Transport, error), servers)
	for i := range servers {
		dials[i] = func(ctx context.Context) (mcpsdk.Transport, error) {
			// The initial connect fails fast; only the retry dial blocks, so the
			// batch under test is the only thing waiting on release.
			if calls[i].Add(1) == 1 {
				return nil, errors.New("first connect: server not ready")
			}
			if arrived.Add(1) == servers {
				close(arrivedAll)
			}
			select {
			case <-release:
				return nil, errors.New("released without a server")
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	configs := make([]mcpconfig.ServerConfig, servers)
	for i := range configs {
		configs[i] = mcpconfig.ServerConfig{Name: fmt.Sprintf("s%d", i), Type: "stdio"}
	}
	mgr, outcomes := NewManager(context.Background(), configs, dials)
	if len(outcomes) != servers {
		t.Fatalf("NewManager outcomes = %+v, want every server failing its first connect", outcomes)
	}
	defer mgr.Close()
	now := time.Unix(1000, 0)
	for i := range mgr.conns {
		mgr.conns[i].now = func() time.Time { return now }
	}
	if outs := mgr.RegisterTools(tool.NewRegistry()); len(outs) != 0 {
		t.Fatalf("RegisterTools = %+v, want nothing registered", outs)
	}

	retried := make(chan struct{})
	go func() {
		defer close(retried)
		mgr.RetryFailed(context.Background(), tool.NewRegistry())
	}()

	// Every dial must be in flight at once. A serial batch never closes
	// arrivedAll, because its first dial blocks until the test releases it.
	select {
	case <-arrivedAll:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatalf("only %d of %d dials started together; the batch is serial", arrived.Load(), servers)
	}
	close(release)
	select {
	case <-retried:
	case <-time.After(30 * time.Second):
		t.Fatal("RetryFailed did not return after the dials were released")
	}
}

// TestRetryFailed_CancellationBoundsTheBatch pins the two cancellation
// contracts the concurrent batch must keep: ctx ending releases a stalled dial
// without waiting out reconnectDialTimeout, and a cancelled attempt opens no
// backoff window, because a cancellation says nothing about the server's
// health.
func TestRetryFailed_CancellationBoundsTheBatch(t *testing.T) {
	var stalledCalls atomic.Int32
	dial := func(ctx context.Context) (mcpsdk.Transport, error) {
		if stalledCalls.Add(1) == 1 {
			return nil, errors.New("first connect: server not ready")
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	mgr, outcomes := NewManager(context.Background(), []mcpconfig.ServerConfig{{Name: "stalled", Type: "stdio"}},
		[]func(context.Context) (mcpsdk.Transport, error){dial})
	if len(outcomes) != 1 {
		t.Fatalf("NewManager outcomes = %+v, want one connect-stage failure", outcomes)
	}
	defer mgr.Close()
	now := time.Unix(1000, 0)
	mgr.conns[0].now = func() time.Time { return now }
	reg := tool.NewRegistry()
	if outs := mgr.RegisterTools(reg); len(outs) != 0 {
		t.Fatalf("RegisterTools = %+v, want nothing registered", outs)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var recovered []ServerRecovery
	var outs []ServerOutcome
	retried := make(chan struct{})
	go func() {
		defer close(retried)
		recovered, outs = mgr.RetryFailed(ctx, reg)
	}()
	// Cancel only once the retry's dial is in flight, so the cancellation lands
	// on the attempt rather than before it.
	testwait.Until(t, "the retry's dial to start", func() bool { return stalledCalls.Load() >= 2 })
	cancel()
	testwait.Until(t, "RetryFailed to return after cancellation", func() bool {
		select {
		case <-retried:
			return true
		default:
			return false
		}
	})
	if len(recovered) != 0 || len(outs) != 1 || outs[0].Stage != "connect" {
		t.Fatalf("RetryFailed = (%+v, %+v), want one connect-stage outcome and no recovery", recovered, outs)
	}
	mgr.conns[0].mu.Lock()
	windowOpen := mgr.conns[0].clock().Before(mgr.conns[0].backoffUntil)
	mgr.conns[0].mu.Unlock()
	if windowOpen {
		t.Fatal("a cancelled retry opened a backoff window, which would delay the next turn's attempt")
	}
}
