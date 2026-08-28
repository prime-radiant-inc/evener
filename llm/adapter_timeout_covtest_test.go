package llm

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"sync"
	"sync/atomic"
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

func TestCovResponseHeaderTimeoutTransportNonTimeoutErrorAfterWrite(t *testing.T) {
	sentinel := &scriptedPostWriteReadError{}
	response, conn, err := roundTripWithScriptedReadError(t, sentinel)
	if response != nil {
		t.Fatalf("RoundTrip response = %#v, want nil", response)
	}
	if reflect.TypeOf(err) != reflect.TypeOf(sentinel) || reflect.ValueOf(err).Pointer() != reflect.ValueOf(sentinel).Pointer() {
		t.Fatalf("RoundTrip error = %T %v, want exact sentinel %p", err, err, sentinel)
	}
	if !conn.wrote.Load() {
		t.Fatal("scripted connection observed no successful request write")
	}
}

func TestCovResponseHeaderTimeoutTransportTimeoutAfterWrite(t *testing.T) {
	sentinel := &scriptedPostWriteTimeoutError{}
	response, conn, err := roundTripWithScriptedReadError(t, sentinel)
	if response != nil {
		t.Fatalf("RoundTrip response = %#v, want nil", response)
	}
	if _, ok := errors.AsType[*responseHeaderTimeoutError](err); !ok {
		t.Fatalf("RoundTrip error = %T %v, want *responseHeaderTimeoutError", err, err)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("RoundTrip error = %v, want scripted timeout in error chain", err)
	}
	if !conn.wrote.Load() {
		t.Fatal("scripted connection observed no successful request write")
	}
}

func roundTripWithScriptedReadError(t *testing.T, readErr error) (*http.Response, *scriptedPostWriteConn, error) {
	t.Helper()
	conn := newScriptedPostWriteConn(readErr)
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = nil
	base.DisableKeepAlives = true
	base.DialContext = func(context.Context, string, string) (net.Conn, error) {
		return conn, nil
	}
	transport := &responseHeaderTimeoutTransport{base: base}
	request, err := http.NewRequest(http.MethodGet, "http://provider.test/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.RoundTrip(request)
	return response, conn, err
}

type scriptedPostWriteConn struct {
	readErr error
	wrote   atomic.Bool
	written chan struct{}
	once    sync.Once
}

func newScriptedPostWriteConn(readErr error) *scriptedPostWriteConn {
	return &scriptedPostWriteConn{readErr: readErr, written: make(chan struct{})}
}

func (c *scriptedPostWriteConn) Read([]byte) (int, error) {
	<-c.written
	return 0, c.readErr
}

func (c *scriptedPostWriteConn) Write(p []byte) (int, error) {
	c.wrote.Store(true)
	c.once.Do(func() { close(c.written) })
	return len(p), nil
}

func (*scriptedPostWriteConn) Close() error                     { return nil }
func (*scriptedPostWriteConn) LocalAddr() net.Addr              { return scriptedPostWriteAddr("local") }
func (*scriptedPostWriteConn) RemoteAddr() net.Addr             { return scriptedPostWriteAddr("remote") }
func (*scriptedPostWriteConn) SetDeadline(time.Time) error      { return nil }
func (*scriptedPostWriteConn) SetReadDeadline(time.Time) error  { return nil }
func (*scriptedPostWriteConn) SetWriteDeadline(time.Time) error { return nil }
func (e *scriptedPostWriteTimeoutError) Error() string          { return "scripted post-write timeout" }
func (e *scriptedPostWriteTimeoutError) Timeout() bool          { return true }
func (e *scriptedPostWriteTimeoutError) Temporary() bool        { return false }
func (a scriptedPostWriteAddr) Network() string                 { return "scripted" }
func (a scriptedPostWriteAddr) String() string                  { return string(a) }

type scriptedPostWriteTimeoutError struct{}
type scriptedPostWriteReadError struct{}
type scriptedPostWriteAddr string

func (*scriptedPostWriteReadError) Error() string { return "scripted post-write read failure" }

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
