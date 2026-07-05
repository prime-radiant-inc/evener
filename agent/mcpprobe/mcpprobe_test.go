package mcpprobe_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"primeradiant.com/serf/agent/mcpconfig"
	"primeradiant.com/serf/agent/mcpprobe"
)

// newMCPTestServer starts an httptest server speaking real MCP over
// streamable HTTP (JSONResponse, for deterministic single-shot responses,
// mirroring the SDK's own ExampleStreamableHTTPHandler). wrap, if non-nil,
// runs in front of every request before it reaches the MCP handler — used to
// inject artificial latency or capture headers without touching MCP
// internals.
func newMCPTestServer(t *testing.T, wrap func(http.Handler) http.Handler) *httptest.Server {
	t.Helper()
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "probe-test-server", Version: "v1"}, nil)
	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server {
		return server
	}, &mcpsdk.StreamableHTTPOptions{JSONResponse: true})

	var h http.Handler = handler
	if wrap != nil {
		h = wrap(handler)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// A real MCP server that completes the initialize handshake reads "available".
func TestProbe_HTTP_ValidInitialize_Available(t *testing.T) {
	t.Parallel()
	srv := newMCPTestServer(t, nil)

	results := mcpprobe.Probe(context.Background(), []mcpconfig.ServerConfig{
		{Name: "good", Type: "http", URL: srv.URL},
	})

	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	got := results[0]
	if got.Name != "good" || got.Transport != "http" {
		t.Errorf("Name/Transport = %q/%q, want good/http", got.Name, got.Transport)
	}
	if got.Status != "available" {
		t.Errorf("Status = %q, want available (Error=%q)", got.Status, got.Error)
	}
	if got.Error != "" {
		t.Errorf("Error = %q, want empty on success", got.Error)
	}
}

// This is the exact bug the orphaned mcpstatus package had: it treated ANY
// HTTP response — even a 200 with a garbage body — as "available" because it
// never performed a real MCP handshake, just a HEAD-then-GET. A server that
// answers every request with HTTP 200 but a body that isn't a real MCP
// initialize response must fail Probe's real initialize handshake and read
// "unreachable" — a status-code-only check would wrongly call this
// "available".
func TestProbe_HTTP_NonMCPBody_Unreachable(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"message":"welcome to the widget REST API"}`))
	}))
	t.Cleanup(srv.Close)

	results := mcpprobe.Probe(context.Background(), []mcpconfig.ServerConfig{
		{Name: "widget-api", Type: "http", URL: srv.URL},
	})

	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	got := results[0]
	if got.Status != "unreachable" {
		t.Errorf("Status = %q, want unreachable for a 200-but-non-MCP body", got.Status)
	}
	if got.Error == "" {
		t.Error(`Error = "", want a populated handshake failure`)
	}
}

// A connection that is actively refused (nobody listening) reads
// "unreachable" for both the http and sse transports.
func TestProbe_ConnectionRefused_Unreachable(t *testing.T) {
	t.Parallel()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("closing listener: %v", err)
	}
	refusedURL := "http://" + addr

	results := mcpprobe.Probe(context.Background(), []mcpconfig.ServerConfig{
		{Name: "refused-http", Type: "http", URL: refusedURL},
		{Name: "refused-sse", Type: "sse", URL: refusedURL},
	})

	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	for _, got := range results {
		if got.Status != "unreachable" {
			t.Errorf("%s: Status = %q, want unreachable", got.Name, got.Status)
		}
		if got.Error == "" {
			t.Errorf("%s: Error = \"\", want a populated connection failure", got.Name)
		}
	}
}

// cfg.Headers must reach the server, mirroring transportForConfig's own
// header-injection behavior for http/sse (agent/internal/mcp's
// httpClientWithHeaders).
func TestProbe_HTTP_HeadersAttached(t *testing.T) {
	t.Parallel()
	var gotAuth string
	srv := newMCPTestServer(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")
			next.ServeHTTP(w, r)
		})
	})

	results := mcpprobe.Probe(context.Background(), []mcpconfig.ServerConfig{
		{Name: "authed", Type: "http", URL: srv.URL, Headers: map[string]string{"Authorization": "Bearer probe-token"}},
	})

	if len(results) != 1 || results[0].Status != "available" {
		t.Fatalf("results = %+v, want a single available row", results)
	}
	if gotAuth != "Bearer probe-token" {
		t.Errorf("server saw Authorization = %q, want the configured header injected", gotAuth)
	}
}

// A stdio command that resolves on PATH reads "available" (command-present),
// whether Type is spelled out ("stdio") or left at its zero value ("").
func TestProbe_Stdio_CommandOnPath_Available(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("true"); err != nil {
		t.Skipf("`true` not found on PATH: %v", err)
	}

	results := mcpprobe.Probe(context.Background(), []mcpconfig.ServerConfig{
		{Name: "explicit-stdio", Type: "stdio", Command: "true"},
		{Name: "default-type", Type: "", Command: "true"},
	})

	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	for _, got := range results {
		if got.Transport != "stdio" {
			t.Errorf("%s: Transport = %q, want stdio", got.Name, got.Transport)
		}
		if got.Status != "available" {
			t.Errorf("%s: Status = %q, want available (Error=%q)", got.Name, got.Status, got.Error)
		}
		if got.Error != "" {
			t.Errorf("%s: Error = %q, want empty on success", got.Name, got.Error)
		}
	}
}

// A stdio command absent from PATH reads "missing", distinct from
// "unreachable" (reserved for http/sse handshake failures).
func TestProbe_Stdio_CommandMissing_Missing(t *testing.T) {
	t.Parallel()
	results := mcpprobe.Probe(context.Background(), []mcpconfig.ServerConfig{
		{Name: "ghost", Type: "stdio", Command: "serf-mcpprobe-definitely-missing-cmd"},
	})

	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	got := results[0]
	if got.Status != "missing" {
		t.Errorf("Status = %q, want missing", got.Status)
	}
	if got.Error == "" {
		t.Error(`Error = "", want a populated LookPath failure`)
	}
}

// TestProbe_UnresolvableConfig_ErrorPopulated stands in for the plan's
// "a config whose ${VAR} is unset -> per-row Error populated" case.
//
// Probe's actual signature takes already-parsed []mcpconfig.ServerConfig:
// ${VAR} expansion happens upstream, in mcpconfig.serverJSONToConfig, before
// a ServerConfig exists at all. A config whose expansion fails never becomes
// a ServerConfig — it's dropped with a warning at the Discover/ParseServerMap
// layer — so there is no way for Probe itself to observe an unset-${VAR}
// error given its real signature; that class of error is entirely upstream
// of Probe.
//
// What genuinely can reach Probe, and genuinely can't be made sense of, is a
// ServerConfig with an empty required field for its own transport type. Both
// cases below exercise that: Probe populates a per-row Error (and a
// non-"available" Status) for the bad row without the whole call failing or
// the other rows being affected — the same spirit as the ${VAR} case, via a
// path that actually exists in Probe.
func TestProbe_UnresolvableConfig_ErrorPopulated(t *testing.T) {
	t.Parallel()
	results := mcpprobe.Probe(context.Background(), []mcpconfig.ServerConfig{
		{Name: "no-command", Type: "stdio", Command: ""},
		{Name: "no-url", Type: "http", URL: ""},
	})

	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	for _, got := range results {
		if got.Status == "available" {
			t.Errorf("%s: Status = available, want a failure status for an unresolvable config", got.Name)
		}
		if got.Error == "" {
			t.Errorf("%s: Error = \"\", want a populated error for an unresolvable config", got.Name)
		}
	}
}

// N slow http probes must finish in roughly one probe's worth of wall time,
// not N times that — proving configs are actually probed in parallel rather
// than one at a time. sleepEach delays every request at the plain net/http
// layer, ahead of the MCP handler, so the artificial latency is independent
// of MCP SDK internals. A single successful probe makes several sequential
// round trips under the hood (initialize, the standalone SSE stream,
// the "initialized" notification, and the session close) rather than just
// one, so one probe's wall-clock cost is a small multiple of sleepEach, not
// sleepEach itself — measured empirically at ~4*sleepEach. bound sits well
// above that per-probe cost and well below what n serialized probes would
// cost, so ordinary CI scheduling jitter doesn't make this flaky in either
// direction. Not run under t.Parallel() so other tests can't skew the
// wall-clock measurement (mirrors agent/internal/mcp's
// TestParallelMCP_ParallelBound).
func TestProbe_Parallel_BoundedByWallClock(t *testing.T) {
	const sleepEach = 100 * time.Millisecond
	const n = 5
	const bound = 1000 * time.Millisecond // ~2.5x the ~400ms (4*sleepEach) a single probe costs; ~5x below the ~2s n serialized probes would cost

	srv := newMCPTestServer(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(sleepEach)
			next.ServeHTTP(w, r)
		})
	})

	configs := make([]mcpconfig.ServerConfig, n)
	for i := range configs {
		configs[i] = mcpconfig.ServerConfig{Name: fmt.Sprintf("slow%d", i), Type: "http", URL: srv.URL}
	}

	start := time.Now()
	results := mcpprobe.Probe(context.Background(), configs)
	elapsed := time.Since(start)

	if len(results) != n {
		t.Fatalf("len(results) = %d, want %d", len(results), n)
	}
	for _, got := range results {
		if got.Status != "available" {
			t.Errorf("%s: Status = %q, want available (Error=%q)", got.Name, got.Status, got.Error)
		}
	}
	if elapsed >= bound {
		t.Errorf("Probe took %v for %d slow servers, want < %v: probes do not appear to run in parallel", elapsed, n, bound)
	}
}
