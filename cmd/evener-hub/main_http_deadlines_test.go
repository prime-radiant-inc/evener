package hub

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

// TestRunMainConfiguresPreAuthHeaderAndIdleDeadlines pins the hub's production
// http.Server deadlines. Exact values, not merely positivity, so a silent
// widening or removal fails.
func TestRunMainConfiguresPreAuthHeaderAndIdleDeadlines(t *testing.T) {
	_, cfg, deps := newTraceMainTestDeps(t)
	// runMain only closes hubListener on its early error paths; on a stubbed
	// serve success it would leak. Bind one here and own its close.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	deps.listen = func(context.Context, string, string) (net.Listener, error) { return ln, nil }
	var got *http.Server
	deps.serve = func(_ context.Context, srv hubHTTPServer) error {
		hs, ok := srv.(*listenerHTTPServer)
		if !ok {
			t.Fatalf("serve received %T, want *listenerHTTPServer", srv)
		}
		got = hs.Server
		return nil
	}
	var stderr bytes.Buffer
	if err := runMain([]string{"-addr", cfg.Addr, "-evener", "/bin/evener"}, &stderr, deps); err != nil {
		t.Fatalf("runMain: %v, stderr=%s", err, stderr.String())
	}
	if got == nil {
		t.Fatal("hub never constructed its http.Server")
	}
	if want := 10 * time.Second; got.ReadHeaderTimeout != want {
		t.Fatalf("ReadHeaderTimeout = %v, want %v", got.ReadHeaderTimeout, want)
	}
	if want := 120 * time.Second; got.IdleTimeout != want {
		t.Fatalf("IdleTimeout = %v, want %v", got.IdleTimeout, want)
	}
}
