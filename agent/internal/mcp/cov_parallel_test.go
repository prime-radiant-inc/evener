package mcp

import (
	"context"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"primeradiant.com/serf/agent/mcpconfig"
)

// These tests cover NewManager's move from sequential to parallel per-server
// connects (Task 5): every server's transport-build/Connect/ListTools now
// runs in its own goroutine, so two properties that were free with a plain
// for loop become load-bearing and easy to get subtly wrong:
//
//   - Determinism: the final conns/outcomes must land in CONFIG order, not
//     completion order, however the goroutines happen to interleave.
//   - Concurrency: servers must actually connect at the same time, not just
//     "in some order" — a per-server 10s timeout only bounds a slow server
//     if slow servers aren't also serialized behind each other.

// parallel_sleepTransport wraps an mcpsdk.Transport, sleeping for a fixed
// duration before delegating Connect to inner. It lets a test control, from
// the outside, how long a given server's connect takes — independent of
// config order — without touching NewManager itself.
type parallel_sleepTransport struct {
	inner mcpsdk.Transport
	sleep time.Duration
}

func (s parallel_sleepTransport) Connect(ctx context.Context) (mcpsdk.Connection, error) {
	select {
	case <-time.After(s.sleep):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.inner.Connect(ctx)
}

// newParallelTestServer builds an in-memory MCP server named name, exposing
// one no-op tool, and returns the client-side transport ready for NewManager.
func newParallelTestServer(t *testing.T, name, toolName string) mcpsdk.Transport {
	t.Helper()
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: name, Version: "v1"}, nil)
	server.AddTool(&mcpsdk.Tool{
		Name:        toolName,
		Description: "test tool " + toolName,
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "ok"}}}, nil
	})
	st, ct := mcpsdk.NewInMemoryTransports()
	if _, err := server.Connect(context.Background(), st, nil); err != nil {
		t.Fatalf("server %s connect: %v", name, err)
	}
	return ct
}

// TestParallelMCP_ConfigOrder_Deterministic is the config-order-determinism
// contract test. Three servers are configured in order s1, s2, s3, but s1 and
// s2 are wrapped to sleep in REVERSE of that order (s1 longest, s2 shorter)
// while s3 is left unwrapped (fastest) — so connects actually complete in the
// order s3, s2, s1, the exact reverse of config order. A correct
// index-preserving implementation must still report s1, s2, s3 everywhere; an
// implementation that assembled conns/outcomes in completion order (e.g. via
// a naive fan-in channel) would instead report s3, s2, s1, and this test
// would catch that.
func TestParallelMCP_ConfigOrder_Deterministic(t *testing.T) {
	ctx := context.Background()

	ct1 := parallel_sleepTransport{inner: newParallelTestServer(t, "s1", "t1"), sleep: 80 * time.Millisecond}
	ct2 := parallel_sleepTransport{inner: newParallelTestServer(t, "s2", "t2"), sleep: 40 * time.Millisecond}
	ct3 := newParallelTestServer(t, "s3", "t3") // unwrapped: completes fastest

	mgr, outcomes := NewManager(ctx, []mcpconfig.ServerConfig{
		{Name: "s1", Type: "stdio"},
		{Name: "s2", Type: "stdio"},
		{Name: "s3", Type: "stdio"},
	}, []mcpsdk.Transport{ct1, ct2, ct3})
	if len(outcomes) != 0 {
		t.Fatalf("NewManager: %+v", outcomes)
	}
	defer mgr.Close()

	servers := mgr.Servers()
	if len(servers) != 3 {
		t.Fatalf("expected 3 servers, got %d: %+v", len(servers), servers)
	}
	wantNames := []string{"s1", "s2", "s3"}
	for i, want := range wantNames {
		if servers[i].Name != want {
			t.Errorf("Servers()[%d].Name = %q, want %q (full: %+v)", i, servers[i].Name, want, servers)
		}
	}

	defs := mgr.ToolDefinitions()
	wantTools := []string{"s1__t1", "s2__t2", "s3__t3"}
	if len(defs) != len(wantTools) {
		names := make([]string, len(defs))
		for i, d := range defs {
			names[i] = d.Name
		}
		t.Fatalf("ToolDefinitions() = %v, want %v", names, wantTools)
	}
	for i, want := range wantTools {
		if defs[i].Name != want {
			t.Errorf("ToolDefinitions()[%d].Name = %q, want %q", i, defs[i].Name, want)
		}
	}
}

// TestParallelMCP_ParallelBound is the concurrency contract test: two
// servers each delay Connect by sleepEach, so a serial NewManager (one
// server's whole connect at a time) takes roughly 2*sleepEach, while a
// parallel one takes roughly sleepEach. bound sits well above the parallel
// expectation and well below the serial one, so ordinary scheduling jitter
// on a loaded CI box doesn't make this flaky in either direction.
func TestParallelMCP_ParallelBound(t *testing.T) {
	ctx := context.Background()
	const sleepEach = 200 * time.Millisecond
	const bound = 350 * time.Millisecond // > sleepEach, well below 2*sleepEach (400ms)

	ct1 := parallel_sleepTransport{inner: newParallelTestServer(t, "p1", "t1"), sleep: sleepEach}
	ct2 := parallel_sleepTransport{inner: newParallelTestServer(t, "p2", "t2"), sleep: sleepEach}

	start := time.Now()
	mgr, outcomes := NewManager(ctx, []mcpconfig.ServerConfig{
		{Name: "p1", Type: "stdio"},
		{Name: "p2", Type: "stdio"},
	}, []mcpsdk.Transport{ct1, ct2})
	elapsed := time.Since(start)
	if len(outcomes) != 0 {
		t.Fatalf("NewManager: %+v", outcomes)
	}
	defer mgr.Close()

	if elapsed >= bound {
		t.Errorf("NewManager took %v, want < %v (serial would take ~%v): connects do not appear to run in parallel", elapsed, bound, 2*sleepEach)
	}
}
