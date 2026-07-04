package mcp

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"primeradiant.com/serf/agent/mcpconfig"
)

// These tests cover Task 7's redial seam: NewManager's third parameter
// becomes a per-conn dial FACTORY (func(context.Context) (mcpsdk.Transport,
// error)) instead of a one-shot mcpsdk.Transport, so a later reconnect (Task
// 8) can call it again after the initial connect. Two properties matter:
//
//   - The factory is invoked exactly once for the initial connect — no
//     wasted re-dials, no silent skip.
//   - The resulting conn actually retains the factory (conn.dial), even when
//     the initial connect fails, since a currently-down server is exactly
//     the case a later reconnect needs to retry.

// TestRedial_NewManager_CallsDialFactoryExactlyOnce is the seam test that
// forces NewManager's signature change: it constructs a manager via a
// factory slice and asserts the factory is called exactly once for the
// initial connect, and that the successful conn stores it.
func TestRedial_NewManager_CallsDialFactoryExactlyOnce(t *testing.T) {
	ctx := context.Background()
	ct := newParallelTestServer(t, "r1", "t1")

	var calls int32
	spy := func(context.Context) (mcpsdk.Transport, error) {
		atomic.AddInt32(&calls, 1)
		return ct, nil
	}

	mgr, outcomes := NewManager(ctx, []mcpconfig.ServerConfig{
		{Name: "r1", Type: "stdio"},
	}, []func(context.Context) (mcpsdk.Transport, error){spy})
	if len(outcomes) != 0 {
		t.Fatalf("NewManager: %+v", outcomes)
	}
	defer mgr.Close()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("dial factory called %d times, want exactly 1", got)
	}
	if mgr.conns[0].dial == nil {
		t.Error("conn.dial should be stored after a successful connect")
	}
}

// TestRedial_FailedConn_StillStoresDial proves the other half of the Task 8
// prerequisite: a conn whose initial connect fails must still retain its
// dial factory. Losing dial on failure would make the most-needed case (a
// server that's currently down) unable to ever recover.
func TestRedial_FailedConn_StillStoresDial(t *testing.T) {
	ctx := context.Background()
	sentinel := errors.New("redial: dial refused")

	var calls int32
	spy := func(context.Context) (mcpsdk.Transport, error) {
		atomic.AddInt32(&calls, 1)
		return nil, sentinel
	}

	mgr, outcomes := NewManager(ctx, []mcpconfig.ServerConfig{
		{Name: "r2", Type: "stdio"},
	}, []func(context.Context) (mcpsdk.Transport, error){spy})
	if len(outcomes) != 1 || outcomes[0].Name != "r2" || outcomes[0].Stage != "connect" {
		t.Fatalf("want one connect outcome for %q, got %+v", "r2", outcomes)
	}
	defer mgr.Close()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("dial factory called %d times, want exactly 1", got)
	}
	if mgr.conns[0].dial == nil {
		t.Error("conn.dial should be stored even when the initial connect fails")
	}
}
