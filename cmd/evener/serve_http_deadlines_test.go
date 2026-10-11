package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestServeConfiguresPreAuthHeaderAndIdleDeadlines pins the daemon's
// production http.Server deadlines. Exact values, not merely positivity, so a
// silent widening or removal fails.
func TestServeConfiguresPreAuthHeaderAndIdleDeadlines(t *testing.T) {
	deps, _, args := newClearServeDeps(t)
	var (
		got  *http.Server
		stop context.CancelFunc
	)
	deps.notifyContext = func(ctx context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
		ctx, stop = context.WithCancel(ctx)
		return ctx, stop
	}
	deps.serveHTTP = func(srv *http.Server, _ net.Listener) error {
		got = srv
		// Cancel before returning: the shutdown goroutine waits on ctx.Done()
		// even after serveHTTP returns, so returning without canceling would
		// deadlock the run (same pattern as fuzzRunServeCallbacks).
		stop()
		return http.ErrServerClosed
	}
	if err := runServeWithDeps(args, deps); err != nil {
		t.Fatalf("runServeWithDeps: %v", err)
	}
	if got == nil {
		t.Fatal("serve never constructed an http.Server")
	}
	if want := 10 * time.Second; got.ReadHeaderTimeout != want {
		t.Fatalf("ReadHeaderTimeout = %v, want %v", got.ReadHeaderTimeout, want)
	}
	if want := 120 * time.Second; got.IdleTimeout != want {
		t.Fatalf("IdleTimeout = %v, want %v", got.IdleTimeout, want)
	}
}
