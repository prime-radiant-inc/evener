package mcp

import (
	"context"
	"sync"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"primeradiant.com/serf/agent/mcpconfig"
)

// TestConnMutex_ServersAndCloseConcurrent is the mutex-scaffold contract test
// for Task 6: conn gains a mutex and a closed flag so that Close() and a
// future reconnect swap (Tasks 7-8) can serialize with each other, and so
// that Servers() reads live per-conn state instead of copying a conn (and
// its mutex) by value. Servers() and Close() are called concurrently from
// two goroutines; under -race, any unsynchronized access to conn's
// session/status/closed fields must be caught. Afterward, Servers() must
// still return the server's row (status may read "connected" or "failed"
// depending on scheduling, but must never panic or produce a torn read),
// and the conn must be marked closed.
func TestConnMutex_ServersAndCloseConcurrent(t *testing.T) {
	ctx := context.Background()
	ct := newParallelTestServer(t, "s1", "t1")

	mgr, outcomes := NewManager(ctx, []mcpconfig.ServerConfig{
		{Name: "s1", Type: "stdio"},
	}, []func(context.Context) (mcpsdk.Transport, error){staticDial(ct)})
	if len(outcomes) != 0 {
		t.Fatalf("NewManager: %+v", outcomes)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			_ = mgr.Servers()
		}
	}()
	go func() {
		defer wg.Done()
		mgr.Close()
	}()
	wg.Wait()

	// Post-Close: Servers() must still return the server's row without
	// panicking or a torn read, and Close must have marked the conn closed.
	servers := mgr.Servers()
	if len(servers) != 1 || servers[0].Name != "s1" {
		t.Fatalf("Servers() after Close = %+v, want one row named s1", servers)
	}
	if servers[0].Status != "connected" && servers[0].Status != "failed" {
		t.Errorf("Servers()[0].Status = %q, want connected or failed", servers[0].Status)
	}
	if !mgr.conns[0].closed {
		t.Error("conn.closed should be true after Close()")
	}
}
