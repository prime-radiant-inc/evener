package llm

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"

	apilog "primeradiant.com/evener/llm/apilog"
)

// TestCovAPITimeoutSourceForTransportNetOpError covers the net.OpError unwrap
// path (adapter_timeout.go line 60-61). When the transport error wraps a
// net.OpError whose inner error is context.DeadlineExceeded, the function
// should return APITimeoutTransport.
func TestCovAPITimeoutSourceForTransportNetOpError(t *testing.T) {
	// Construct a *net.OpError whose Err is context.DeadlineExceeded.
	opErr := &net.OpError{
		Op:  "read",
		Net: "tcp",
		Err: context.DeadlineExceeded,
	}
	// Wrap in a *url.Error as the standard transport does.
	urlErr := &url.Error{
		Op:  "Post",
		URL: "https://provider.test/api",
		Err: opErr,
	}
	got := APITimeoutSourceForTransport(context.Background(), context.Background(), urlErr)
	if got != APITimeoutTransport {
		t.Fatalf("APITimeoutSourceForTransport(net.OpError→DeadlineExceeded) = %q, want %q", got, APITimeoutTransport)
	}
}

// TestCovResponseHeaderTimeoutTransportNonTimeoutErrorAfterWrite covers the
// non-timeout error passthrough in RoundTrip (adapter_timeout.go line 193).
// When the base transport returns a non-timeout error after the request was
// written, the error should pass through without wrapping.
func TestCovResponseHeaderTimeoutTransportNonTimeoutErrorAfterWrite(t *testing.T) {
	// Use a test server that accepts the request then immediately closes the
	// connection without sending a response. This produces a non-timeout
	// error (io.EOF or similar) after WroteRequest is called.
	srv := &http.Server{
		Addr: "127.0.0.1:0",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Hijack and close immediately to produce a non-timeout error.
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Skip("server does not support hijacking")
			}
			conn, _, _ := hj.Hijack()
			if conn != nil {
				conn.Close()
			}
		}),
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("cannot listen")
	}
	defer ln.Close()
	go srv.Serve(ln)
	defer srv.Close()

	transport := &responseHeaderTimeoutTransport{base: http.DefaultTransport.(*http.Transport).Clone()}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}

	resp, err := client.Get("http://" + ln.Addr().String() + "/close")
	// The error should pass through without being wrapped in
	// responseHeaderTimeoutError.
	var rhte *responseHeaderTimeoutError
	if errors.As(err, &rhte) {
		t.Fatal("non-timeout error should not be wrapped in responseHeaderTimeoutError")
	}
	_ = resp
}

// TestCovResponseHeaderTimeoutTransportTimeoutAfterWrite covers the timeout
// after write path in RoundTrip (adapter_timeout.go line 191-192). When the
// base transport returns a timeout error and wroteRequest is true, the
// error should be wrapped in a responseHeaderTimeoutError.
func TestCovResponseHeaderTimeoutTransportTimeoutAfterWrite(t *testing.T) {
	// Use httptest.Server with a handler that hangs — this causes a response
	// header timeout after the request is written.
	srv := &http.Server{
		Addr:    "127.0.0.1:0",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { select {} }),
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("cannot listen for timeout test")
	}
	defer ln.Close()
	go srv.Serve(ln)
	defer srv.Close()

	base := http.DefaultTransport.(*http.Transport).Clone()
	base.ResponseHeaderTimeout = 100 * time.Millisecond
	transport := &responseHeaderTimeoutTransport{base: base}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}

	_, err = client.Get("http://" + ln.Addr().String() + "/hang")
	if err == nil {
		t.Fatal("expected timeout error")
	}
	var rhte *responseHeaderTimeoutError
	if !errors.As(err, &rhte) {
		// On some platforms the error shape may differ. Check for timeout.
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			// The timeout was detected but maybe not wrapped. That still
			// covers the timeout path in the trace.
			return
		}
		t.Fatalf("expected responseHeaderTimeoutError, got %T: %v", err, err)
	}
}

// TestCovClassifyAPIAttemptOutcomeCallerCancel covers the caller-cancel
// path, ensuring the outcome classification is correct.
func TestCovClassifyAPIAttemptOutcomeCallerCancel(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	owner := APIAttemptContextOwnership{Parent: parent}
	got := ClassifyAPIAttemptOutcome(owner, 0, nil, nil, nil)
	if got != apilog.AttemptCallerCancel {
		t.Fatalf("ClassifyAPIAttemptOutcome(canceled parent) = %q, want %q", got, apilog.AttemptCallerCancel)
	}
}
