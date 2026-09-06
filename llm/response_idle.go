package llm

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ErrResponseIdleTimeout identifies inactivity while reading a provider response body.
var ErrResponseIdleTimeout = errors.New("response idle timeout")

type responseIdleTimeoutError struct{ timeout time.Duration }

func (e *responseIdleTimeoutError) Error() string {
	return fmt.Sprintf("%s: no bytes received for %v", ErrResponseIdleTimeout, e.timeout)
}
func (e *responseIdleTimeoutError) Is(target error) bool { return target == ErrResponseIdleTimeout }
func (e *responseIdleTimeoutError) Timeout() bool        { return true }

// idleResponseTransport bounds inactivity, not the lifetime of a response.
type idleResponseTransport struct {
	base        http.RoundTripper
	timeout     time.Duration
	compression bool
}

func (t *idleResponseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	parent := req.Context()
	ctx, cancel := context.WithCancel(parent)
	request := req.Clone(ctx)
	addedGzip := t.compression && request.Header.Get("Accept-Encoding") == "" && request.Header.Get("Range") == "" && request.Method != http.MethodHead
	if addedGzip {
		request.Header.Set("Accept-Encoding", "gzip")
	}
	resp, err := t.base.RoundTrip(request)
	if err != nil || resp.Body == nil {
		cancel()
		return resp, err
	}
	resp.Body = newIdleResponseBody(parent, resp.Body, t.timeout, cancel)
	if addedGzip && strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		resp.Body = &idleGzipBody{ReadCloser: resp.Body}
		resp.Header.Del("Content-Encoding")
		resp.Header.Del("Content-Length")
		resp.ContentLength = -1
		resp.Uncompressed = true
	}
	return resp, nil
}

// Match net/http's lazy automatic gzip decoding, but read compressed bytes
// through the idle monitor first. Close only touches the underlying body so it
// can interrupt a simultaneous blocked Read safely.
type idleGzipBody struct {
	io.ReadCloser
	reader *gzip.Reader
	err    error
}

func (b *idleGzipBody) Read(p []byte) (int, error) {
	if b.reader == nil && b.err == nil {
		b.reader, b.err = gzip.NewReader(b.ReadCloser)
	}
	if b.err != nil {
		return 0, b.err
	}
	return b.reader.Read(p)
}

func (t *idleResponseTransport) CloseIdleConnections() {
	if c, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}
func (t *idleResponseTransport) APILogTransportUsesStandardCompression() bool { return t.compression }

type idleResponseBody struct {
	body        io.ReadCloser
	ctx         context.Context
	timeout     time.Duration
	mu          sync.Mutex
	timer       *time.Timer
	stopContext func() bool
	stopped     bool
	expired     bool
	deadline    time.Time
	closeOnce   sync.Once
	closeErr    error
	cancel      context.CancelFunc
}

func newIdleResponseBody(ctx context.Context, body io.ReadCloser, timeout time.Duration, cancel context.CancelFunc) *idleResponseBody {
	b := &idleResponseBody{body: body, ctx: ctx, timeout: timeout, deadline: time.Now().Add(timeout), cancel: cancel}
	b.mu.Lock()
	b.timer = time.AfterFunc(timeout, b.expire)
	b.stopContext = context.AfterFunc(ctx, func() { _ = b.Close() })
	b.mu.Unlock()
	return b
}
func (b *idleResponseBody) closeBody() {
	b.closeOnce.Do(func() {
		if b.cancel != nil {
			b.cancel()
		}
		b.closeErr = b.body.Close()
	})
}
func (b *idleResponseBody) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)
	b.mu.Lock()
	expired := b.expired
	if err != nil {
		b.stopped = true
		b.timer.Stop()
	} else if n > 0 && !b.stopped {
		b.deadline = time.Now().Add(b.timeout)
		b.timer.Reset(b.timeout)
	}
	b.mu.Unlock()
	if err != nil {
		b.stopContext()
		if b.cancel != nil {
			b.cancel()
		}
	}
	if b.ctx.Err() != nil {
		return n, b.ctx.Err()
	}
	if expired {
		return n, &responseIdleTimeoutError{timeout: b.timeout}
	}
	return n, err
}
func (b *idleResponseBody) Close() error {
	b.mu.Lock()
	b.stopped = true
	b.timer.Stop()
	b.mu.Unlock()
	if b.stopContext != nil {
		b.stopContext()
	}
	b.closeBody()
	return b.closeErr
}

func (b *idleResponseBody) expire() {
	b.mu.Lock()
	if b.stopped {
		b.mu.Unlock()
		return
	}
	if remaining := time.Until(b.deadline); remaining > 0 {
		b.timer.Reset(remaining)
		b.mu.Unlock()
		return
	}
	b.expired = true
	b.stopped = true
	b.mu.Unlock()
	b.stopContext()
	b.closeBody()
}
