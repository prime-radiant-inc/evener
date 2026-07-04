package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/mcpconfig"
	"primeradiant.com/serf/llm"
)

// These tests cover Task 8's lazy, call-driven reconnect: on a CallTool error
// that errors.Is(err, mcpsdk.ErrConnectionClosed) (SDK taxonomy — only
// ErrClientClosing/ErrServerClosing, never ctx cancellation, never a plain
// JSON-RPC application error), the exec closure demotes the conn to
// "degraded" and, backoff permitting, redials via conn.dial, swaps in the
// healed session, closes the displaced one, and retries the triggering call
// once. Four properties matter (the "reconnect matrix" from the plan), plus
// two more this file adds to pin down behavior the plan's prose left for
// engineering judgment (the backoff window's "else" branch, and the
// structural invariant that a startup-failed conn's exec closure can never
// exist to invoke reconnect at all):
//
//   - The retry-once reaches the NEW session, and backoff starts at zero
//     (the very next call after a drop redials immediately).
//   - Discrimination: a plain JSON-RPC error and a ctx-cancelled call each
//     leave the conn "connected" and never redial.
//   - The displaced (old) session is closed after a successful swap.
//   - Close() and a concurrent reconnect swap serialize correctly: a dial
//     that finishes after Close() has already run is discarded, not leaked
//     or published.
//   - (Added) The 30s backoff window actually suppresses a second redial
//     for a second drop that arrives before it elapses.
//   - (Added) A conn that never leaves "failed" at startup has no tools
//     registered, so its exec closure — and therefore reconnect — can never
//     be invoked.

// newReconnectTestServer builds an in-memory MCP server named name, exposing
// one tool (toolName) that always replies with reply, and returns both the
// server's own *mcpsdk.ServerSession (so a test can force-close the server
// side of the connection) and the client-side transport ready for dial.
func newReconnectTestServer(t *testing.T, name, toolName, reply string) (*mcpsdk.ServerSession, mcpsdk.Transport) {
	t.Helper()
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: name, Version: "v1"}, nil)
	server.AddTool(&mcpsdk.Tool{
		Name:        toolName,
		Description: "test probe tool",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: reply}}}, nil
	})
	st, ct := mcpsdk.NewInMemoryTransports()
	ss, err := server.Connect(context.Background(), st, nil)
	if err != nil {
		t.Fatalf("server %s connect: %v", name, err)
	}
	return ss, ct
}

// spyCloseConnection wraps an mcpsdk.Connection, counting Close calls via
// closes so a test can observe whether (and how many times) the underlying
// transport-level connection was torn down.
type spyCloseConnection struct {
	mcpsdk.Connection
	closes *int32
}

func (s spyCloseConnection) Close() error {
	atomic.AddInt32(s.closes, 1)
	return s.Connection.Close()
}

// spyCloseTransport wraps an mcpsdk.Transport so every Connection it
// produces counts its Close calls into closes.
type spyCloseTransport struct {
	inner  mcpsdk.Transport
	closes *int32
}

func (t spyCloseTransport) Connect(ctx context.Context) (mcpsdk.Connection, error) {
	conn, err := t.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return spyCloseConnection{Connection: conn, closes: t.closes}, nil
}

// execTool executes the named tool via reg.ExecuteCall with empty arguments,
// returning (Output, IsError). It's a small helper shared by every case below
// to keep the setup/assert boilerplate down.
func execTool(ctx context.Context, reg *tool.Registry, t *testing.T, name string) (string, bool) {
	t.Helper()
	env := &agenttest.FakeEnv{WorkDir: t.TempDir()}
	res := reg.ExecuteCall(ctx, env, llm.ToolCallData{
		ID: "c", Name: name, Arguments: json.RawMessage(`{}`),
	})
	return res.Output, res.IsError
}

// execProbe is execTool fixed to the "s__probe" tool name used by every case
// whose server exposes a single "probe" tool.
func execProbe(ctx context.Context, reg *tool.Registry, t *testing.T) (string, bool) {
	t.Helper()
	return execTool(ctx, reg, t, "s__probe")
}

// TestReconnect_ClosureReachesNewSession_ZeroInitBackoff is the primary
// reconnect-matrix case: dial #1 works, gets dropped from the server side,
// and the VERY NEXT call (backoff starts at zero) redials and succeeds
// against dial #2 — proven by dial #2's distinct reply reaching the caller,
// as a retry-once success rather than an error.
func TestReconnect_ClosureReachesNewSession_ZeroInitBackoff(t *testing.T) {
	ctx := context.Background()

	ss1, ct1 := newReconnectTestServer(t, "s", "probe", "dial1-reply")
	_, ct2 := newReconnectTestServer(t, "s", "probe", "dial2-reply")

	var dialCalls int32
	dial := func(context.Context) (mcpsdk.Transport, error) {
		n := atomic.AddInt32(&dialCalls, 1)
		if n == 1 {
			return ct1, nil
		}
		return ct2, nil
	}

	mgr, outcomes := NewManager(ctx, []mcpconfig.ServerConfig{{Name: "s", Type: "stdio"}},
		[]func(context.Context) (mcpsdk.Transport, error){dial})
	if len(outcomes) != 0 {
		t.Fatalf("NewManager: %+v", outcomes)
	}
	defer mgr.Close()

	reg := tool.NewRegistry()
	if outs := mgr.RegisterTools(reg); len(outs) != 0 {
		t.Fatalf("RegisterTools: %+v", outs)
	}

	// First call succeeds against dial #1.
	out, isErr := execProbe(ctx, reg, t)
	if isErr || out != "dial1-reply" {
		t.Fatalf("first call: IsError=%v Output=%q, want dial1-reply", isErr, out)
	}

	// Force the connection closed from the SERVER side.
	if err := ss1.Close(); err != nil {
		t.Fatalf("server-side close: %v", err)
	}

	// The very next call must trigger a lazy reconnect (backoff zero) and
	// succeed against dial #2 via the retry-once — not surface an error.
	out, isErr = execProbe(ctx, reg, t)
	if isErr {
		t.Fatalf("expected retry-once success after reconnect, got error output: %s", out)
	}
	if out != "dial2-reply" {
		t.Errorf("Output = %q, want dial #2's distinct reply %q", out, "dial2-reply")
	}
	if got := atomic.LoadInt32(&dialCalls); got != 2 {
		t.Errorf("dial factory called %d times, want exactly 2 (initial connect + one reconnect)", got)
	}
	if got := mgr.conns[0].status; got != "connected" {
		t.Errorf("conn.status = %q, want connected after a successful reconnect", got)
	}
}

// TestReconnect_Discrimination_PlainJSONRPCError_NoRedial proves the
// discrimination half of the matrix (J1/Decision 4): a tool handler that
// fails at the RPC-application level (a Go error returned from the handler,
// not a dropped transport) must never be mistaken for a dropped connection.
func TestReconnect_Discrimination_PlainJSONRPCError_NoRedial(t *testing.T) {
	ctx := context.Background()

	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "s", Version: "v1"}, nil)
	server.AddTool(&mcpsdk.Tool{
		Name:        "boom",
		Description: "always fails at the RPC-application level",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return nil, errors.New("boom: rpc-level failure")
	})
	st, ct := mcpsdk.NewInMemoryTransports()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}

	var dialCalls int32
	dial := func(context.Context) (mcpsdk.Transport, error) {
		atomic.AddInt32(&dialCalls, 1)
		return ct, nil
	}

	mgr, outcomes := NewManager(ctx, []mcpconfig.ServerConfig{{Name: "s", Type: "stdio"}},
		[]func(context.Context) (mcpsdk.Transport, error){dial})
	if len(outcomes) != 0 {
		t.Fatalf("NewManager: %+v", outcomes)
	}
	defer mgr.Close()

	reg := tool.NewRegistry()
	if outs := mgr.RegisterTools(reg); len(outs) != 0 {
		t.Fatalf("RegisterTools: %+v", outs)
	}

	_, isErr := execTool(ctx, reg, t, "s__boom")
	if !isErr {
		t.Fatal("expected an error result for a plain RPC-application error, got success")
	}
	if got := atomic.LoadInt32(&dialCalls); got != 1 {
		t.Errorf("dial factory called %d times, want exactly 1 (no redial for a plain RPC error)", got)
	}
	if got := mgr.conns[0].status; got != "connected" {
		t.Errorf("conn.status = %q, want connected (a plain RPC error must not degrade the conn)", got)
	}
}

// TestReconnect_Discrimination_CtxCancelled_NoRedial proves the other half
// of the discrimination matrix: a call whose ctx is cancelled must never be
// mistaken for a dropped connection either, even though the server-side
// handler observes the SAME cancellation (via the SDK's own
// cancelled-notification forwarding) and returns ctx.Err().
func TestReconnect_Discrimination_CtxCancelled_NoRedial(t *testing.T) {
	ctx := context.Background()

	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "s", Version: "v1"}, nil)
	server.AddTool(&mcpsdk.Tool{
		Name:        "slow",
		Description: "blocks until the caller gives up",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	st, ct := mcpsdk.NewInMemoryTransports()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}

	var dialCalls int32
	dial := func(context.Context) (mcpsdk.Transport, error) {
		atomic.AddInt32(&dialCalls, 1)
		return ct, nil
	}

	mgr, outcomes := NewManager(ctx, []mcpconfig.ServerConfig{{Name: "s", Type: "stdio"}},
		[]func(context.Context) (mcpsdk.Transport, error){dial})
	if len(outcomes) != 0 {
		t.Fatalf("NewManager: %+v", outcomes)
	}
	defer mgr.Close()

	reg := tool.NewRegistry()
	if outs := mgr.RegisterTools(reg); len(outs) != 0 {
		t.Fatalf("RegisterTools: %+v", outs)
	}

	callCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	_, isErr := execTool(callCtx, reg, t, "s__slow")
	if !isErr {
		t.Fatal("expected an error result for a ctx-cancelled call, got success")
	}
	if got := atomic.LoadInt32(&dialCalls); got != 1 {
		t.Errorf("dial factory called %d times, want exactly 1 (no redial on ctx cancellation)", got)
	}
	if got := mgr.conns[0].status; got != "connected" {
		t.Errorf("conn.status = %q, want connected (ctx cancellation must not degrade the conn)", got)
	}
}

// TestReconnect_DisplacedSessionClosedExactlyOnce is the J7 case: after a
// successful reconnect, the displaced (dial #1) session must be closed, not
// leaked.
//
// Caveat (flagged for review): this SDK auto-closes a connection's own
// transport once it detects the connection is shutting down (e.g. once the
// server side hangs up, causing a client-side read error) — regardless of
// whether ClientSession.Close() is ever called explicitly. Since the only
// way to genuinely produce mcpsdk.ErrConnectionClosed in a hermetic test IS
// to actually drop the connection (as this test does, via ss1.Close()), the
// transport-level spy here cannot cleanly PROVE our code's own old.Close()
// call is what did the closing, as opposed to the SDK's own auto-cleanup
// racing to do it first. What this test DOES reliably establish: closes
// ends up exactly 1 (never left at 0 — i.e. genuinely torn down — and never
// more than 1, e.g. from some double-swap bug), and — together with
// TestReconnect_ClosureReachesNewSession_ZeroInitBackoff's assertion that
// the retry-once actually succeeds against dial #2 — that reconnect did not
// mistakenly close the NEW session instead of the old one (that bug would
// make the retry-once fail, since it would run against an already-closed
// session).
func TestReconnect_DisplacedSessionClosedExactlyOnce(t *testing.T) {
	ctx := context.Background()

	ss1, ct1 := newReconnectTestServer(t, "s", "probe", "dial1-reply")
	_, ct2 := newReconnectTestServer(t, "s", "probe", "dial2-reply")

	var dial1Closes int32
	spiedCt1 := spyCloseTransport{inner: ct1, closes: &dial1Closes}

	var dialCalls int32
	dial := func(context.Context) (mcpsdk.Transport, error) {
		n := atomic.AddInt32(&dialCalls, 1)
		if n == 1 {
			return spiedCt1, nil
		}
		return ct2, nil
	}

	mgr, outcomes := NewManager(ctx, []mcpconfig.ServerConfig{{Name: "s", Type: "stdio"}},
		[]func(context.Context) (mcpsdk.Transport, error){dial})
	if len(outcomes) != 0 {
		t.Fatalf("NewManager: %+v", outcomes)
	}
	defer mgr.Close()

	reg := tool.NewRegistry()
	if outs := mgr.RegisterTools(reg); len(outs) != 0 {
		t.Fatalf("RegisterTools: %+v", outs)
	}

	// Prime the connection against dial #1.
	if _, isErr := execProbe(ctx, reg, t); isErr {
		t.Fatal("priming call against dial #1 unexpectedly failed")
	}

	if err := ss1.Close(); err != nil {
		t.Fatalf("server-side close: %v", err)
	}

	out, isErr := execProbe(ctx, reg, t)
	if isErr {
		t.Fatalf("expected retry-once success after reconnect, got error output: %s", out)
	}
	if out != "dial2-reply" {
		t.Fatalf("Output = %q, want dial #2's distinct reply (proves the NEW session, not the displaced one, served this call)", out)
	}

	if got := atomic.LoadInt32(&dial1Closes); got != 1 {
		t.Errorf("displaced dial #1 connection Close() called %d times, want exactly 1", got)
	}
}

// TestReconnect_BackoffWindow_SkipsRedialUntilElapsed locks in the behavior
// of the backoffUntil "else" branch, which the plan's prose left to
// engineering judgment: after a successful reconnect sets backoffUntil to
// now+30s, a SECOND drop within that window must not trigger a second
// redial — the call simply fails fast with the connection-closed error,
// rather than each paying a fresh dial attempt. This is additional coverage
// beyond the plan's four listed cases, added because backoffUntil's
// non-zero behavior is part of the shared spec but the plan's prose only
// narrates the zero-value ("try immediately") case.
func TestReconnect_BackoffWindow_SkipsRedialUntilElapsed(t *testing.T) {
	ctx := context.Background()

	ss1, ct1 := newReconnectTestServer(t, "s", "probe", "dial1-reply")
	ss2, ct2 := newReconnectTestServer(t, "s", "probe", "dial2-reply")

	var dialCalls int32
	dial := func(context.Context) (mcpsdk.Transport, error) {
		n := atomic.AddInt32(&dialCalls, 1)
		if n == 1 {
			return ct1, nil
		}
		if n == 2 {
			return ct2, nil
		}
		t.Errorf("dial factory called a 3rd time; the backoff window should have suppressed this redial")
		return ct2, nil
	}

	mgr, outcomes := NewManager(ctx, []mcpconfig.ServerConfig{{Name: "s", Type: "stdio"}},
		[]func(context.Context) (mcpsdk.Transport, error){dial})
	if len(outcomes) != 0 {
		t.Fatalf("NewManager: %+v", outcomes)
	}
	defer mgr.Close()

	reg := tool.NewRegistry()
	if outs := mgr.RegisterTools(reg); len(outs) != 0 {
		t.Fatalf("RegisterTools: %+v", outs)
	}

	// Prime dial #1, drop it, and let the first reconnect (zero backoff)
	// swap in dial #2.
	if _, isErr := execProbe(ctx, reg, t); isErr {
		t.Fatal("priming call against dial #1 unexpectedly failed")
	}
	if err := ss1.Close(); err != nil {
		t.Fatalf("server-side close (dial #1): %v", err)
	}
	out, isErr := execProbe(ctx, reg, t)
	if isErr || out != "dial2-reply" {
		t.Fatalf("first reconnect: IsError=%v Output=%q, want success with dial2-reply", isErr, out)
	}
	if got := atomic.LoadInt32(&dialCalls); got != 2 {
		t.Fatalf("dial factory called %d times after the first reconnect, want 2", got)
	}

	// Drop dial #2 immediately (well within the 30s backoff window the
	// first reconnect just set) and confirm the NEXT call fails fast
	// without attempting a third dial.
	if err := ss2.Close(); err != nil {
		t.Fatalf("server-side close (dial #2): %v", err)
	}
	_, isErr = execProbe(ctx, reg, t)
	if !isErr {
		t.Fatal("expected the call to fail while backoff is active (no server to serve it), got success")
	}
	if got := atomic.LoadInt32(&dialCalls); got != 2 {
		t.Errorf("dial factory called %d times, want still 2 (backoff window should suppress the 3rd redial)", got)
	}
	if got := mgr.conns[0].status; got != "degraded" {
		t.Errorf("conn.status = %q, want degraded (dropped again, but backoff suppressed recovery)", got)
	}
}

// TestReconnect_FailedConn_NeverGetsExecClosure pins the structural
// invariant the plan calls out explicitly: a conn that never leaves
// "failed" at startup has session == nil and contributes no tools, so
// RegisterTools never builds an exec closure for it — meaning reconnect is
// never invoked for a conn that was never healthy in the first place. This
// isn't new logic (Tasks 2/3 already guarantee it); it's a regression pin.
func TestReconnect_FailedConn_NeverGetsExecClosure(t *testing.T) {
	ctx := context.Background()
	sentinel := errors.New("reconnect: dial refused")

	mgr, outcomes := NewManager(ctx, []mcpconfig.ServerConfig{{Name: "down", Type: "stdio"}},
		[]func(context.Context) (mcpsdk.Transport, error){
			func(context.Context) (mcpsdk.Transport, error) { return nil, sentinel },
		})
	if len(outcomes) != 1 || outcomes[0].Name != "down" || outcomes[0].Stage != "connect" {
		t.Fatalf("NewManager: %+v", outcomes)
	}
	defer mgr.Close()

	if mgr.conns[0].session != nil {
		t.Error("a startup-failed conn must have a nil session")
	}
	if mgr.conns[0].status != "failed" {
		t.Errorf("conn.status = %q, want failed", mgr.conns[0].status)
	}

	reg := tool.NewRegistry()
	regOutcomes := mgr.RegisterTools(reg)
	if len(regOutcomes) != 0 {
		t.Fatalf("RegisterTools should have nothing to do for an all-failed manager, got %+v", regOutcomes)
	}
	if names := reg.Names(); len(names) != 0 {
		t.Errorf("expected zero registered tools for an all-failed manager, got %v", names)
	}
}

// TestReconnect_CloseVsSwapRace is the highest-value concurrency case (I6):
// Close() and a reconnect's swap must serialize correctly under -race, and a
// dial that completes AFTER Close() has already run must be discarded (its
// freshly-connected session closed), never leaked and never published as
// the conn's live session.
//
// The interleaving is deterministically forced (not left to scheduler luck):
// conn.dial is swapped for a synchronized closure that signals dialStarted
// the instant it's entered (i.e. exactly when reconnect has unlocked c.mu
// for the dial) and then blocks until the test says proceedDial. The test
// waits for dialStarted, THEN calls mgr.Close() — guaranteeing Close() runs,
// and completes, entirely within the window where reconnect's dial is known
// to still be in flight (c.mu unlocked) — before ever unblocking the dial.
// This exercises the "dial finishes after closed" branch on every run, not
// just probabilistically; go test -race (run at -count>1) additionally
// verifies there's no data race in reaching that outcome.
func TestReconnect_CloseVsSwapRace(t *testing.T) {
	ctx := context.Background()

	_, ct1 := newReconnectTestServer(t, "s", "probe", "dial1-reply")
	_, ct2 := newReconnectTestServer(t, "s2", "probe", "dial2-reply")

	var dial2Closes int32
	spiedCt2 := spyCloseTransport{inner: ct2, closes: &dial2Closes}

	mgr, outcomes := NewManager(ctx, []mcpconfig.ServerConfig{{Name: "s", Type: "stdio"}},
		[]func(context.Context) (mcpsdk.Transport, error){staticDial(ct1)})
	if len(outcomes) != 0 {
		t.Fatalf("NewManager: %+v", outcomes)
	}

	dialStarted := make(chan struct{})
	proceedDial := make(chan struct{})
	c := &mgr.conns[0]
	c.mu.Lock()
	c.dial = func(dialCtx context.Context) (mcpsdk.Transport, error) {
		close(dialStarted)
		select {
		case <-proceedDial:
			return spiedCt2, nil
		case <-dialCtx.Done():
			return nil, dialCtx.Err()
		}
	}
	c.mu.Unlock()

	var newSess *mcpsdk.ClientSession
	var ok bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		newSess, ok = c.reconnect(ctx)
	}()

	<-dialStarted      // reconnect has unlocked c.mu and is blocked inside the dial
	mgr.Close()        // must win: sets closed=true (and closes the CURRENT session) while the dial is still in flight
	close(proceedDial) // let the dial complete now that Close() has already run
	<-done

	if ok || newSess != nil {
		t.Errorf("reconnect should report failure with a nil session when Close() wins the race, got ok=%v, newSess=%v", ok, newSess)
	}
	if !c.closed {
		t.Error("conn.closed should be true after Close()")
	}
	if got := atomic.LoadInt32(&dial2Closes); got != 1 {
		t.Errorf("post-Close dial's session Close() called %d times, want exactly 1 (discarded, not leaked)", got)
	}
}
