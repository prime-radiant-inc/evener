// Package mcpprobe checks whether configured MCP servers currently look
// reachable, without registering them for real use. It replaces the
// orphaned cmd/serf-hub/internal/mcpstatus, whose http/sse probe treated any
// HTTP response — even a 400 or a 200 with a garbage body — as "available"
// because it only ever did a HEAD-then-GET, never a real MCP handshake.
// Probe fixes exactly that: for http/sse servers it runs the actual MCP
// initialize handshake via mcpsdk.Client.Connect, so a 200 response that
// isn't a valid MCP initialize response reads as "unreachable", not
// "available".
//
// Probe states its own limits rather than overclaiming what a green result
// means:
//
//   - For http/sse servers, a successful initialize handshake ("available")
//     cannot certify per-tool success: a server can complete initialize and
//     still error on real tool calls (e.g. an auth-gated proxy that 400s on
//     tools/call while initialize itself succeeds). The per-row Error field,
//     when set, is what surfaces that class of failure — Status alone does
//     not.
//   - For stdio servers, "available" only means the configured command is
//     present on PATH (command-present). Probe does not spawn or connect to
//     the process, so command-present cannot certify connectability — the
//     command could still fail to speak MCP once actually launched.
//
// Results reflect what this probe observes from wherever it runs (typically
// the hub process) at the moment it runs. If the daemon that will actually
// launch or reach a server has a different PATH or network reachability,
// its live behavior can diverge from what Probe reported here.
package mcpprobe

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"sync"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"primeradiant.com/serf/agent/mcpconfig"
)

// probeTimeout bounds each individual server probe so one slow or hung
// server cannot delay or starve the others.
const probeTimeout = 3 * time.Second

// Result reports the reachability of one configured MCP server.
type Result struct {
	Name      string // from ServerConfig.Name
	Transport string // "stdio", "sse", or "http" (Type "" normalizes to "stdio")
	Status    string // "available" / "unreachable" / "missing" (stdio command not on PATH)
	Error     string // populated whenever Status isn't "available"; empty otherwise
}

// Probe checks every configured MCP server in parallel — one goroutine per
// config, each under its own probeTimeout derived from ctx — and reports
// whether each currently looks reachable. See the package doc for what
// "available" does and does not certify.
//
// results is preallocated to len(configs) and each goroutine writes exactly
// one index (results[i], matching configs[i]), so the returned slice is
// always in config order regardless of which probe finishes first — no
// mutex is needed on results itself, since every index has exactly one
// writer and results is only read after wg.Wait() establishes
// happens-before (mirrors agent/internal/mcp's NewManager).
func Probe(ctx context.Context, configs []mcpconfig.ServerConfig) []Result {
	results := make([]Result, len(configs))
	var wg sync.WaitGroup
	wg.Add(len(configs))
	for i, cfg := range configs {
		go func(i int, cfg mcpconfig.ServerConfig) {
			defer wg.Done()
			results[i] = probeOne(ctx, cfg)
		}(i, cfg)
	}
	wg.Wait()
	return results
}

// probeOne probes a single server under its own probeTimeout, derived from
// ctx so a caller-level cancellation still cuts every in-flight probe short.
func probeOne(ctx context.Context, cfg mcpconfig.ServerConfig) Result {
	transport := cfg.Type
	if transport == "" {
		transport = "stdio"
	}
	r := Result{Name: cfg.Name, Transport: transport}

	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	switch transport {
	case "stdio":
		// Command-present only: mcpprobe does not spawn the process (see
		// package doc limits).
		if _, err := exec.LookPath(cfg.Command); err != nil {
			r.Status = "missing"
			r.Error = err.Error()
			return r
		}
		r.Status = "available"
		return r

	case "sse", "http":
		clientTransport, err := transportForProbe(cfg)
		if err != nil {
			r.Status = "unreachable"
			r.Error = err.Error()
			return r
		}
		client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "serf-mcpprobe", Version: "v1"}, nil)
		session, err := client.Connect(ctx, clientTransport, nil)
		if err != nil {
			r.Status = "unreachable"
			r.Error = err.Error()
			return r
		}
		_ = session.Close() // probe only: nothing further to do with a live session
		r.Status = "available"
		return r

	default:
		r.Status = "unreachable"
		r.Error = fmt.Sprintf("unknown MCP transport type %q", transport)
		return r
	}
}

// transportForProbe builds a client transport for an http/sse config. It is
// a simpler, mcpprobe-local equivalent of agent/internal/mcp's unexported
// transportForConfig (which mcpprobe cannot use directly): the same
// http/sse transport construction and header injection, minus the stdio
// CommandTransport machinery mcpprobe doesn't need — probeOne handles stdio
// itself via exec.LookPath.
func transportForProbe(cfg mcpconfig.ServerConfig) (mcpsdk.Transport, error) {
	switch cfg.Type {
	case "sse":
		if cfg.URL == "" {
			return nil, errors.New("sse transport requires a url")
		}
		t := &mcpsdk.SSEClientTransport{Endpoint: cfg.URL}
		if len(cfg.Headers) > 0 {
			t.HTTPClient = httpClientWithHeaders(cfg.Headers)
		}
		return t, nil

	case "http":
		if cfg.URL == "" {
			return nil, errors.New("http transport requires a url")
		}
		t := &mcpsdk.StreamableClientTransport{Endpoint: cfg.URL}
		if len(cfg.Headers) > 0 {
			t.HTTPClient = httpClientWithHeaders(cfg.Headers)
		}
		return t, nil

	default:
		return nil, fmt.Errorf("unknown MCP transport type %q", cfg.Type)
	}
}

// headerRoundTripper wraps an http.RoundTripper to inject headers into requests.
type headerRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
}

func (h *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range h.headers {
		req.Header.Set(k, v)
	}
	return h.base.RoundTrip(req)
}

// httpClientWithHeaders returns an *http.Client that injects the given headers.
func httpClientWithHeaders(headers map[string]string) *http.Client {
	return &http.Client{
		Transport: &headerRoundTripper{
			base:    http.DefaultTransport,
			headers: headers,
		},
	}
}
