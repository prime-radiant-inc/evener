package mcpstatus

import (
	"context"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"primeradiant.com/serf/agent"
)

const mcpProbeTimeout = 750 * time.Millisecond

// ProbeMCPStatus inspects a configured MCP server and returns a single-snapshot
// status. The hub does not itself run MCP servers — agents spawn them per
// session — so "running" is not something the hub can report. What the hub
// CAN report is whether the configured server is *reachable*: the stdio
// command resolves on PATH, or the HTTP/SSE URL responds. That answers the
// useful question for the settings pane: "if I started a session right now,
// would this MCP server actually start?"
//
// Returned values:
//   - "available" — the configured target resolves (stdio command on PATH,
//     or HTTP/SSE URL responds with any HTTP status)
//   - "missing"   — stdio command not found on PATH
//   - "unreachable" — HTTP/SSE URL does not respond within the probe budget
//   - "unknown"   — config is malformed or type is unrecognized
func ProbeMCPStatus(c agent.MCPServerConfig) string {
	t := strings.ToLower(strings.TrimSpace(c.Type))
	if t == "" {
		t = "stdio"
	}
	switch t {
	case "stdio":
		if c.Command == "" {
			return "unknown"
		}
		if _, err := exec.LookPath(c.Command); err != nil {
			return "missing"
		}
		return "available"
	case "http", "sse":
		if c.URL == "" {
			return "unknown"
		}
		client := &http.Client{Timeout: mcpProbeTimeout}
		headCtx, headCancel := context.WithTimeout(context.Background(), mcpProbeTimeout)
		defer headCancel()
		req, err := http.NewRequestWithContext(headCtx, http.MethodHead, c.URL, nil)
		if err != nil {
			return "unknown"
		}
		for k, v := range c.Headers {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			return "available"
		}
		// Some servers don't accept HEAD; fall back to a 0-byte GET.
		getCtx, getCancel := context.WithTimeout(context.Background(), mcpProbeTimeout)
		defer getCancel()
		req, err = http.NewRequestWithContext(getCtx, http.MethodGet, c.URL, nil)
		if err != nil {
			return "unreachable"
		}
		for k, v := range c.Headers {
			req.Header.Set(k, v)
		}
		resp, err = client.Do(req)
		if err != nil {
			return "unreachable"
		}
		_ = resp.Body.Close()
		return "available"
	default:
		return "unknown"
	}
}
