package hub

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// writeMCPFixture writes a minimal .mcp.json file with one stdio server and
// one http server, matching mcpprobe's own test fixtures for a
// deterministic available/unreachable pair.
func writeMCPFixture(t *testing.T, dir, stdioCommand, httpURL string) string {
	t.Helper()
	path := filepath.Join(dir, "mcp.json")
	data := `{"mcpServers": {
		"local-tool": {"command": "` + stdioCommand + `"},
		"remote-api": {"type": "http", "url": "` + httpURL + `"}
	}}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("writing mcp.json fixture: %v", err)
	}
	return path
}

// refusedURL returns an http:// URL nobody is listening on: bind a loopback
// listener, then close it immediately, mirroring mcpprobe's own
// TestProbe_ConnectionRefused_Unreachable fixture.
//
// The port is picked on 127.0.0.2 because the kernel can hand a port freed on
// 127.0.0.1 to a parallel httptest server before the probe dials it, which
// turns a refused-connection probe into a success. A socket bound to 127.0.0.1
// does not answer connections addressed to 127.0.0.2, so no such server can
// answer this one. A dial to the closed 127.0.0.2 port is refused immediately
// rather than dropped, so the probe still reports the connection failure its
// callers assert. A host that cannot bind the alias skips rather than fails.
func refusedURL(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.2:0")
	if errors.Is(err, syscall.EADDRNOTAVAIL) {
		t.Skipf("host cannot bind loopback alias: %v", err)
	}
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("closing listener: %v", err)
	}
	return "http://" + addr
}
