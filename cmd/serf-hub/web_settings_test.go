package main

import (
	"net"
	"os"
	"path/filepath"
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
func refusedURL(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("closing listener: %v", err)
	}
	return "http://" + addr
}
