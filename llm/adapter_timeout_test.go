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

type adapterTimeoutRoundTripper struct {
	called bool
}

func (rt *adapterTimeoutRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.called = true
	return &http.Response{
		StatusCode: http.StatusNoContent,
		Status:     "204 No Content",
		Header:     make(http.Header),
		Body:       http.NoBody,
		Request:    req,
	}, nil
}

func TestAdapterTransport_ConfiguresDefaultTransport(t *testing.T) {
	at := &AdapterTimeout{Connect: 5 * time.Second, Request: 7 * time.Second}
	transport := AdapterTransport(at)
	if transport == nil {
		t.Fatal("expected configured transport")
	}

	defaultTransport := http.DefaultTransport.(*http.Transport)
	if transport == defaultTransport {
		t.Fatal("expected a clone of http.DefaultTransport")
	}
	if transport.ResponseHeaderTimeout != 7*time.Second {
		t.Fatalf("ResponseHeaderTimeout = %v, want 7s", transport.ResponseHeaderTimeout)
	}
	if transport.DialContext == nil {
		t.Fatal("DialContext is nil; connect timeout would not be enforced")
	}
	if transport.TLSHandshakeTimeout != defaultTransport.TLSHandshakeTimeout {
		t.Fatalf("TLSHandshakeTimeout = %v, want %v", transport.TLSHandshakeTimeout, defaultTransport.TLSHandshakeTimeout)
	}
	if transport.ForceAttemptHTTP2 != defaultTransport.ForceAttemptHTTP2 {
		t.Fatalf("ForceAttemptHTTP2 = %v, want %v", transport.ForceAttemptHTTP2, defaultTransport.ForceAttemptHTTP2)
	}
	if (transport.Proxy == nil) != (defaultTransport.Proxy == nil) {
		t.Fatal("default proxy behavior was not preserved")
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan struct{})
	go func() {
		defer close(accepted)
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			_ = conn.Close()
		}
	}()
	conn, err := transport.DialContext(context.Background(), "tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("DialContext to local listener failed: %v", err)
	}
	_ = conn.Close()
	<-accepted
}

func TestAdapterTransport_NoTransportTimeoutReturnsNil(t *testing.T) {
	for _, tc := range []struct {
		name string
		at   *AdapterTimeout
	}{
		{name: "nil", at: nil},
		{name: "zero", at: &AdapterTimeout{}},
		{name: "stream read only", at: &AdapterTimeout{StreamRead: time.Second}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if transport := AdapterTransport(tc.at); transport != nil {
				t.Fatalf("AdapterTransport() = %T, want nil", transport)
			}
		})
	}
}

func TestClientWithAdapterTimeout_ConfiguresStandardTransport(t *testing.T) {
	originalTransport := http.DefaultTransport.(*http.Transport).Clone()
	originalTransport.MaxIdleConnsPerHost = 23
	originalTransport.ResponseHeaderTimeout = 3 * time.Second
	original := &http.Client{Transport: originalTransport, Timeout: 30 * time.Second}

	client := ClientWithAdapterTimeout(original, &AdapterTimeout{
		Connect: 5 * time.Second,
		Request: 7 * time.Second,
	})
	if client == original {
		t.Fatal("expected a copied client")
	}
	if client.Timeout != original.Timeout {
		t.Fatalf("client Timeout = %v, want %v", client.Timeout, original.Timeout)
	}

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport = %T, want *http.Transport", client.Transport)
	}
	if transport == originalTransport {
		t.Fatal("expected a cloned transport")
	}
	if transport.ResponseHeaderTimeout != 7*time.Second {
		t.Fatalf("ResponseHeaderTimeout = %v, want 7s", transport.ResponseHeaderTimeout)
	}
	if transport.MaxIdleConnsPerHost != 23 {
		t.Fatalf("MaxIdleConnsPerHost = %d, want 23", transport.MaxIdleConnsPerHost)
	}
	if transport.DialContext == nil {
		t.Fatal("DialContext is nil; connect timeout would not be enforced")
	}
	if originalTransport.ResponseHeaderTimeout != 3*time.Second {
		t.Fatalf("original ResponseHeaderTimeout mutated to %v", originalTransport.ResponseHeaderTimeout)
	}
}

func TestClientWithAdapterTimeout_ClonesDefaultTransport(t *testing.T) {
	original := &http.Client{Timeout: 30 * time.Second}
	client := ClientWithAdapterTimeout(original, &AdapterTimeout{Request: 7 * time.Second})
	if client == original {
		t.Fatal("expected a copied client")
	}

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport = %T, want *http.Transport", client.Transport)
	}
	defaultTransport := http.DefaultTransport.(*http.Transport)
	if transport == defaultTransport {
		t.Fatal("expected a clone of http.DefaultTransport")
	}
	if transport.ResponseHeaderTimeout != 7*time.Second {
		t.Fatalf("ResponseHeaderTimeout = %v, want 7s", transport.ResponseHeaderTimeout)
	}
	if transport.TLSHandshakeTimeout != defaultTransport.TLSHandshakeTimeout {
		t.Fatalf("TLSHandshakeTimeout = %v, want %v", transport.TLSHandshakeTimeout, defaultTransport.TLSHandshakeTimeout)
	}
	if (transport.Proxy == nil) != (defaultTransport.Proxy == nil) {
		t.Fatal("default proxy behavior was not preserved")
	}
}

func TestClientWithAdapterTimeout_PreservesOpaqueTransport(t *testing.T) {
	transport := &adapterTimeoutRoundTripper{}
	original := &http.Client{Transport: transport}
	client := ClientWithAdapterTimeout(original, &AdapterTimeout{
		Connect: time.Second,
		Request: time.Second,
	})

	if client == original {
		t.Fatal("expected a copied client")
	}
	if client.Transport != transport {
		t.Fatalf("Transport = %T, want original opaque transport", client.Transport)
	}
	req, err := http.NewRequest(http.MethodGet, "http://example.invalid", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_ = resp.Body.Close()
	if !transport.called {
		t.Fatal("opaque transport was not called")
	}
}

func TestClientWithAdapterTimeout_NoTransportTimeoutReturnsOriginal(t *testing.T) {
	original := &http.Client{Timeout: 30 * time.Second}
	for _, tc := range []struct {
		name string
		at   *AdapterTimeout
	}{
		{name: "nil", at: nil},
		{name: "zero", at: &AdapterTimeout{}},
		{name: "stream read only", at: &AdapterTimeout{StreamRead: time.Second}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if client := ClientWithAdapterTimeout(original, tc.at); client != original {
				t.Fatal("client copied without a connect or request timeout")
			}
		})
	}
}
