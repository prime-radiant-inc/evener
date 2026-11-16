package mcp

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/mcpconfig"
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

// TestRetryFailed_CancellationStopsBatch pins that a cancelled attempt abandons
// the rest of the batch: the caller runs this on its own turn's path, so a Stop
// must not wait out a stalled handshake on every remaining server, and a
// cancellation must not be recorded as the server being down.
func TestRetryFailed_CancellationStopsBatch(t *testing.T) {
	_, ct := newReconnectTestServer(t, "later", "probe", "reply")
	var laterDialed, stalledCalls atomic.Int32
	dials := []func(context.Context) (mcpsdk.Transport, error){
		// The first server fails its first connect outright, then never answers a
		// retry: only the caller's cancellation ends that attempt.
		func(ctx context.Context) (mcpsdk.Transport, error) {
			if stalledCalls.Add(1) == 1 {
				return nil, errors.New("first connect: server not ready")
			}
			<-ctx.Done()
			return nil, ctx.Err()
		},
		func(context.Context) (mcpsdk.Transport, error) {
			if laterDialed.Add(1) == 1 {
				return nil, errors.New("first connect: server not ready")
			}
			return ct, nil
		},
	}
	configs := []mcpconfig.ServerConfig{{Name: "stalled", Type: "stdio"}, {Name: "later", Type: "stdio"}}
	mgr, outcomes := NewManager(context.Background(), configs, dials)
	if len(outcomes) != 2 {
		t.Fatalf("NewManager outcomes = %+v, want both servers failing their first connect", outcomes)
	}
	defer mgr.Close()

	now := time.Unix(1000, 0)
	for i := range mgr.conns {
		mgr.conns[i].now = func() time.Time { return now }
	}
	reg := tool.NewRegistry()
	if outs := mgr.RegisterTools(reg); len(outs) != 0 {
		t.Fatalf("RegisterTools = %+v, want nothing registered", outs)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	recovered, outs := mgr.RetryFailed(ctx, reg)
	if len(recovered) != 0 || len(outs) != 1 {
		t.Fatalf("RetryFailed = (%+v, %+v), want one stalled attempt and no recovery", recovered, outs)
	}
	if laterDialed.Load() > 1 {
		t.Fatal("the batch kept dialing servers after the caller's context ended")
	}
	// The cancelled attempt says nothing about the server's health, so no
	// backoff window was opened and the next turn may try again.
	mgr.conns[0].mu.Lock()
	windowOpen := mgr.conns[0].clock().Before(mgr.conns[0].backoffUntil)
	mgr.conns[0].mu.Unlock()
	if windowOpen {
		t.Fatal("a cancelled retry opened a backoff window, which would delay the next turn's attempt")
	}
}
