package openai

import (
	"context"
	"net"
	"strconv"
	"testing"
)

// occupyPort binds a listener the test holds open, returning its port so a
// second bind attempt on that port is guaranteed to fail.
func occupyPort(t *testing.T, port int) (net.Listener, int) {
	t.Helper()
	ln, err := net.Listen("tcp", net.JoinHostPort("localhost", strconv.Itoa(port)))
	if err != nil {
		if port == 0 {
			t.Fatalf("occupy ephemeral port: %v", err)
		}
		return nil, 0
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln, ln.Addr().(*net.TCPAddr).Port
}

func TestStartCallbackServerSurfacesListenFailure(t *testing.T) {
	_, port := occupyPort(t, 0)

	// A non-default, already-bound port takes the direct error return in
	// listenCallbackPort (no fallback), which StartCallbackServer wraps.
	_, err := StartCallbackServer(context.Background(), DefaultConfig(), port, "state")
	if err == nil {
		t.Fatal("StartCallbackServer() error = nil, want listen failure")
	}
}

func TestListenCallbackPortSurfacesNonDefaultBindFailure(t *testing.T) {
	_, port := occupyPort(t, 0)

	if _, err := listenCallbackPort(context.Background(), port); err == nil {
		t.Fatal("listenCallbackPort() error = nil, want bind failure on occupied non-default port")
	}
}

// TestListenCallbackPortFallsBackToFallbackPort covers the arm where the
// default callback port is busy and the listener falls back to
// FallbackCallbackPort. It is skipped when the well-known ports cannot be
// arranged (e.g. already held by another process on the host).
func TestListenCallbackPortFallsBackToFallbackPort(t *testing.T) {
	blocker, _ := occupyPort(t, DefaultCallbackPort)
	if blocker == nil {
		t.Skipf("default callback port %d unavailable on this host", DefaultCallbackPort)
	}

	ln, err := listenCallbackPort(context.Background(), DefaultCallbackPort)
	if err != nil {
		t.Skipf("fallback port %d unavailable on this host: %v", FallbackCallbackPort, err)
	}
	defer func() { _ = ln.Close() }()

	if got := ln.Addr().(*net.TCPAddr).Port; got != FallbackCallbackPort {
		t.Fatalf("listener port = %d, want fallback %d", got, FallbackCallbackPort)
	}
}
