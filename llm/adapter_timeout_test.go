package llm

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
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

type controlledDeadlineContext struct {
	context.Context
	done <-chan struct{}
}

func (c controlledDeadlineContext) Done() <-chan struct{} { return c.done }

func (c controlledDeadlineContext) Err() error {
	select {
	case <-c.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func clientStandardTransport(t *testing.T, client *http.Client) *http.Transport {
	t.Helper()
	wrapper, ok := client.Transport.(*responseHeaderTimeoutTransport)
	if !ok {
		t.Fatalf("Transport = %T, want *responseHeaderTimeoutTransport", client.Transport)
	}
	return wrapper.base
}

type adapterTimeoutDialObservation struct {
	hasDeadline bool
	remaining   time.Duration
}

func observeAdapterTimeoutDial(ctx context.Context) adapterTimeoutDialObservation {
	deadline, ok := ctx.Deadline()
	if !ok {
		return adapterTimeoutDialObservation{}
	}
	return adapterTimeoutDialObservation{hasDeadline: true, remaining: time.Until(deadline)}
}

func assertAdapterTimeoutDialObservation(t *testing.T, observation adapterTimeoutDialObservation, connectTimeout time.Duration) {
	t.Helper()
	if !observation.hasDeadline {
		t.Fatal("custom dial hook received no Connect deadline")
	}
	if observation.remaining < connectTimeout-time.Second || observation.remaining > connectTimeout {
		t.Fatalf("custom dial hook deadline remaining = %v, want approximately %v", observation.remaining, connectTimeout)
	}
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

	transport := clientStandardTransport(t, client)
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

func TestClientWithAdapterTimeout_WrapsDialContextWithConnectDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	observations := make(chan adapterTimeoutDialObservation, 1)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		select {
		case observations <- observeAdapterTimeoutDial(ctx):
		default:
		}
		var dialer net.Dialer
		return dialer.DialContext(ctx, network, address)
	}

	const connectTimeout = 5 * time.Second
	client := ClientWithAdapterTimeout(&http.Client{Transport: transport}, &AdapterTimeout{Connect: connectTimeout})
	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_ = resp.Body.Close()

	select {
	case observation := <-observations:
		assertAdapterTimeoutDialObservation(t, observation, connectTimeout)
	default:
		t.Fatal("custom DialContext was not called")
	}
}

func TestClientWithAdapterTimeout_PreservesDialAuthority(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	called := make(chan struct{}, 1)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = nil
	transport.Dial = func(network, address string) (net.Conn, error) { //nolint:staticcheck // The test verifies legacy Dial remains caller-authoritative.
		called <- struct{}{}
		var dialer net.Dialer
		return dialer.Dial(network, address)
	}

	client := ClientWithAdapterTimeout(&http.Client{Transport: transport}, &AdapterTimeout{Connect: time.Second})
	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_ = resp.Body.Close()
	select {
	case <-called:
	default:
		t.Fatal("custom Dial was not called")
	}
}

func TestClientWithAdapterTimeout_WrapsDialTLSContextWithConnectDeadline(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	observations := make(chan adapterTimeoutDialObservation, 1)
	transport := srv.Client().Transport.(*http.Transport).Clone()
	transport.Proxy = nil
	tlsConfig := transport.TLSClientConfig.Clone()
	transport.DialTLSContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		select {
		case observations <- observeAdapterTimeoutDial(ctx):
		default:
		}
		var dialer net.Dialer
		conn, err := dialer.DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		config := tlsConfig.Clone()
		config.ServerName, _, err = net.SplitHostPort(address)
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		tlsConn := tls.Client(conn, config)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, err
		}
		return tlsConn, nil
	}

	const connectTimeout = 5 * time.Second
	client := ClientWithAdapterTimeout(&http.Client{Transport: transport}, &AdapterTimeout{Connect: connectTimeout})
	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_ = resp.Body.Close()

	select {
	case observation := <-observations:
		assertAdapterTimeoutDialObservation(t, observation, connectTimeout)
	default:
		t.Fatal("custom DialTLSContext was not called")
	}
}

func TestClientWithAdapterTimeout_PreservesDialTLSAuthority(t *testing.T) {
	dialErr := errors.New("caller DialTLS result")
	called := make(chan struct{}, 1)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialTLSContext = nil
	transport.DialTLS = func(string, string) (net.Conn, error) { //nolint:staticcheck // The test verifies legacy DialTLS remains caller-authoritative.
		called <- struct{}{}
		return nil, dialErr
	}

	client := ClientWithAdapterTimeout(&http.Client{Transport: transport}, &AdapterTimeout{Connect: time.Second})
	req, err := http.NewRequest(http.MethodGet, "https://example.invalid", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Do(req)
	if !errors.Is(err, dialErr) {
		t.Fatalf("Do error = %v, want caller DialTLS error", err)
	}
	select {
	case <-called:
	default:
		t.Fatal("custom DialTLS was not called")
	}
}

func TestClientWithAdapterTimeout_ClonesDefaultTransport(t *testing.T) {
	original := &http.Client{Timeout: 30 * time.Second}
	client := ClientWithAdapterTimeout(original, &AdapterTimeout{Request: 7 * time.Second})
	if client == original {
		t.Fatal("expected a copied client")
	}

	transport := clientStandardTransport(t, client)
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

func TestClientWithAdapterTimeout_PreservesCallerDeadline(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(requestStarted)
		<-releaseHandler
	}))
	defer srv.Close()
	defer close(releaseHandler)

	done := make(chan struct{})
	ctx := controlledDeadlineContext{Context: context.Background(), done: done}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, strings.NewReader("request"))
	if err != nil {
		t.Fatal(err)
	}
	client := ClientWithAdapterTimeout(srv.Client(), &AdapterTimeout{Request: time.Second})
	result := make(chan error, 1)
	go func() {
		_, doErr := client.Do(req)
		result <- doErr
	}()

	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not receive request")
	}
	close(done)

	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Do error = %v, want caller deadline", err)
		}
		var responseHeaderTimeout *responseHeaderTimeoutError
		if errors.As(err, &responseHeaderTimeout) {
			t.Fatalf("caller deadline was reclassified as response-header timeout: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request did not return after caller deadline")
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
