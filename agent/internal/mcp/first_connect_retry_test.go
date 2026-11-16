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

	recovered, outs := mgr.RetryFailed(reg)
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
	if recovered, outs := mgr.RetryFailed(reg); len(recovered) != 0 || len(outs) != 0 {
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
	recovered, outs := mgr.RetryFailed(reg)
	if len(recovered) != 0 || len(outs) != 1 || outs[0].Stage != "connect" {
		t.Fatalf("RetryFailed while down = (%+v, %+v), want one connect-stage outcome", recovered, outs)
	}
	callsAfterFirstRetry := dialCalls.Load()

	// Inside the backoff window: no dial, no outcome, still nothing registered.
	if recovered, outs := mgr.RetryFailed(reg); len(recovered) != 0 || len(outs) != 0 {
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
	recovered, outs = mgr.RetryFailed(reg)
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
