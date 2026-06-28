package llm

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestApplyAdapterTimeout_Request(t *testing.T) {
	timeout := &AdapterTimeout{
		Connect:    1 * time.Second,
		Request:    5 * time.Second,
		StreamRead: 2 * time.Second,
	}
	ctx := context.Background()
	ctx, cancel := ApplyAdapterTimeout(ctx, timeout, false)
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected deadline on context")
	}
	remaining := time.Until(deadline)
	if remaining < 4*time.Second || remaining > 6*time.Second {
		t.Errorf("expected ~5s remaining, got %v", remaining)
	}
}

func TestApplyAdapterTimeout_Nil(t *testing.T) {
	ctx := context.Background()
	ctx, cancel := ApplyAdapterTimeout(ctx, nil, false)
	defer cancel()

	_, ok := ctx.Deadline()
	if ok {
		t.Error("expected no deadline for nil timeout")
	}
}

func TestApplyAdapterTimeout_Streaming(t *testing.T) {
	timeout := &AdapterTimeout{
		Request:    5 * time.Second,
		StreamRead: 2 * time.Second,
	}
	ctx := context.Background()
	ctx, cancel := ApplyAdapterTimeout(ctx, timeout, true)
	defer cancel()

	_, ok := ctx.Deadline()
	if ok {
		t.Error("expected no deadline for streaming (stream_read is per-event)")
	}
}

func TestAdapterTransport_ConnectTimeout_ReturnsTransport(t *testing.T) {
	at := &AdapterTimeout{Connect: 5 * time.Second}
	transport := AdapterTransport(at)
	if transport == nil {
		t.Fatal("expected non-nil transport")
	}
	if transport.DialContext == nil {
		t.Fatal("transport should have a DialContext with timeout")
	}
	// Verify the DialContext is a real working dialer that can establish connections,
	// not merely a non-nil function pointer.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			c.Close()
		}
	}()
	conn, err := transport.DialContext(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("DialContext to local listener failed: %v", err)
	}
	conn.Close()
}

func TestAdapterTransport_NilTimeout_ReturnsNil(t *testing.T) {
	transport := AdapterTransport(nil)
	if transport != nil {
		t.Error("expected nil transport for nil AdapterTimeout")
	}
}

func TestAdapterTransport_ZeroConnect_ReturnsNil(t *testing.T) {
	at := &AdapterTimeout{Connect: 0}
	transport := AdapterTransport(at)
	if transport != nil {
		t.Error("expected nil transport for zero Connect timeout")
	}
}

func TestClientWithConnectTimeout_AppliesTransport(t *testing.T) {
	orig := &http.Client{Timeout: 30 * time.Second}
	at := &AdapterTimeout{Connect: 5 * time.Second}
	client := ClientWithConnectTimeout(orig, at)
	if client == orig {
		t.Error("expected a new client copy, not the original")
	}
	if client.Transport == nil {
		t.Fatal("expected Transport to be set")
	}
	if client.Timeout != 30*time.Second {
		t.Errorf("expected original timeout preserved, got %v", client.Timeout)
	}
	// Verify the transport carries the connect-timeout dialer.
	// A bare &http.Transport{} (no dialer) would have nil DialContext.
	ht, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", client.Transport)
	}
	if ht.DialContext == nil {
		t.Error("transport DialContext is nil; connect timeout would not be enforced")
	}
}

func TestClientWithConnectTimeout_NilTimeout_ReturnsOriginal(t *testing.T) {
	orig := &http.Client{Timeout: 30 * time.Second}
	client := ClientWithConnectTimeout(orig, nil)
	if client != orig {
		t.Error("expected the original client when AdapterTimeout is nil")
	}
}

func TestClientWithConnectTimeout_ZeroConnect_ReturnsOriginal(t *testing.T) {
	orig := &http.Client{Timeout: 30 * time.Second}
	at := &AdapterTimeout{Connect: 0}
	client := ClientWithConnectTimeout(orig, at)
	if client != orig {
		t.Error("expected the original client when Connect is zero")
	}
}
