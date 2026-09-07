package llm

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// ErrSSEReadTimeout identifies a provider-owned timeout between stream reads.
var ErrSSEReadTimeout = errors.New("stream read timeout")

type sseReadTimeoutError struct {
	timeout time.Duration
}

func (e *sseReadTimeoutError) Error() string {
	return fmt.Sprintf("%s: no data received for %v", ErrSSEReadTimeout, e.timeout)
}

func (*sseReadTimeoutError) Is(target error) bool {
	return target == ErrSSEReadTimeout
}

// APITimeoutSourceForSSE identifies only timeouts emitted by Evener's own SSE
// or response-body timer. Other provider/body errors remain opaque evidence.
func APITimeoutSourceForSSE(err error) APITimeoutSource {
	// errors.As would invoke behavior-bearing As/Unwrap methods on opaque
	// provider errors; timeout ownership must not execute those methods.
	if _, ok := err.(*responseIdleTimeoutError); ok { //nolint:errorlint // Provider errors are untrusted; inspect only the owned inert timeout.
		return APITimeoutResponseIdle
	}
	if _, ok := err.(*sseReadTimeoutError); ok { //nolint:errorlint // Provider errors are untrusted; inspect only Evener's concrete inert wrapper.
		return APITimeoutSSERead
	}
	return APITimeoutNone
}

// SSEEvent is a single Server-Sent Event parsed from a stream, holding the
// event name and its raw data bytes.
type SSEEvent struct {
	Event string
	Data  []byte
}

type sseOptions struct {
	streamReadTimeout time.Duration
}

// SSEOption configures ParseSSE behavior.
type SSEOption func(*sseOptions)

// WithStreamReadTimeout sets a per-read timeout for SSE parsing. If no data
// is received within d, ParseSSE returns an error. The timer resets after
// each underlying read returns bytes, including partial lines and comments. A zero or negative duration disables the timeout.
func WithStreamReadTimeout(d time.Duration) SSEOption {
	return func(o *sseOptions) { o.streamReadTimeout = d }
}

// readResult carries the result of a blocking ReadString call.
type readResult struct {
	line string
	err  error
}

// sseParser holds state for parsing SSE lines into events.
type sseParser struct {
	curEvent string
	dataBuf  bytes.Buffer
}

// flush emits the buffered event (if any) and resets state.
func (p *sseParser) flush(fn func(SSEEvent) error) error {
	if p.curEvent == "" && p.dataBuf.Len() == 0 {
		return nil
	}
	b := bytes.TrimSuffix(p.dataBuf.Bytes(), []byte("\n"))
	ev := SSEEvent{Event: p.curEvent, Data: append([]byte{}, b...)}
	p.curEvent = ""
	p.dataBuf.Reset()
	return fn(ev)
}

// processLine handles a single trimmed SSE line. Returns true if flush was
// triggered (on blank lines).
func (p *sseParser) processLine(line string, fn func(SSEEvent) error) (bool, error) {
	switch {
	case line == "":
		return true, p.flush(fn)
	case strings.HasPrefix(line, ":"):
		// Comment; ignore.
	case strings.HasPrefix(line, "event:"):
		p.curEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
	case strings.HasPrefix(line, "data:"):
		data := strings.TrimPrefix(line, "data:")
		data = strings.TrimLeft(data, " ")
		p.dataBuf.WriteString(data)
		p.dataBuf.WriteString("\n")
	default:
		// retry, unknown fields; ignore.
	}
	return false, nil
}

// ParseSSE parses Server-Sent Events from r and invokes fn for each complete event.
// It handles "event:" and "data:" lines and emits an event on blank-line boundaries.
// The caller retains ownership of r: ParseSSE never closes it. With a read
// timeout, a background read may remain blocked after ParseSSE returns; callers
// must close or otherwise interrupt their reader to release it. An arbitrary
// borrowed io.Reader cannot be forcibly interrupted by this parser.
func ParseSSE(ctx context.Context, r io.Reader, fn func(ev SSEEvent) error, opts ...SSEOption) error {
	var cfg sseOptions
	for _, o := range opts {
		o(&cfg)
	}

	// Fast path: no stream-read timeout — use the simple blocking loop.
	if cfg.streamReadTimeout <= 0 {
		return parseSSEBlocking(ctx, r, fn)
	}

	return parseSSEWithTimeout(ctx, r, fn, cfg.streamReadTimeout)
}

// parseSSEBlocking is the original blocking implementation, used when no
// stream-read timeout is configured.
func parseSSEBlocking(ctx context.Context, r io.Reader, fn func(ev SSEEvent) error) error {
	br := bufio.NewReader(r)
	var p sseParser

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line, err := br.ReadString('\n')
		if err != nil && err != io.EOF {
			return err
		}
		line = strings.TrimRight(line, "\r\n")

		if _, perr := p.processLine(line, fn); perr != nil {
			return perr
		}

		if err == io.EOF {
			return p.flush(fn)
		}
	}
}

// parseSSEWithTimeout runs ParseSSE with a per-read deadline. A background
// goroutine reads lines from r and sends them on a channel. The main loop
// selects between that channel, the timeout timer, and context cancellation.
func parseSSEWithTimeout(ctx context.Context, r io.Reader, fn func(ev SSEEvent) error, timeout time.Duration) error {
	timer := &sseReadTimer{timer: time.NewTimer(timeout), timeout: timeout}
	defer timer.stop()
	done := make(chan struct{})
	defer close(done)
	br := bufio.NewReader(&sseActivityReader{reader: r, progress: timer.progress})

	lineCh := make(chan readResult, 1)
	go func() {
		for {
			line, err := br.ReadString('\n')
			select {
			case lineCh <- readResult{line, err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()

	var p sseParser

	for {
		select {
		case res := <-lineCh:

			line := res.line
			err := res.err
			if err != nil && !errors.Is(err, io.EOF) {
				return err
			}
			line = strings.TrimRight(line, "\r\n")

			if _, perr := p.processLine(line, fn); perr != nil {
				return perr
			}

			if errors.Is(err, io.EOF) {
				return p.flush(fn)
			}

		case <-timer.timer.C:
			return &sseReadTimeoutError{timeout: timeout}

		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

type sseActivityReader struct {
	reader   io.Reader
	progress func()
}

func (r *sseActivityReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 {
		r.progress()
	}
	return n, err
}

// sseReadTimer owns progress and cleanup for the asynchronous SSE reader.
type sseReadTimer struct {
	mu      sync.Mutex
	timer   *time.Timer
	timeout time.Duration
	stopped bool
}

func (t *sseReadTimer) progress() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.stopped {
		t.timer.Reset(t.timeout)
	}
}
func (t *sseReadTimer) stop() { t.mu.Lock(); defer t.mu.Unlock(); t.stopped = true; t.timer.Stop() }
