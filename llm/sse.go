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

// readResult carries one line read by the asynchronous reader.
type readResult struct {
	line []byte
	err  error
}

// sseParser holds state for parsing SSE lines into events.
type sseParser struct {
	curEvent string
	// data accumulates the current event's payload, one data line at a time.
	// flush hands it to the callback whole and starts the next event from nil,
	// so the bytes a callback receives are never written again — no copy is
	// needed at the handoff. See [sseParser.flush].
	data []byte
	// lineBuf is the scratch a line is scanned into. It is reused for every
	// line and never escapes: processLine either ignores it or copies the
	// bytes it wants into data or curEvent.
	lineBuf []byte
}

// flush emits the buffered event (if any). The payload is handed to the
// callback as-is and the parser's reference is dropped, so the caller keeps a
// slice nothing writes to afterwards without the flush paying for a copy.
func (p *sseParser) flush(fn func(SSEEvent) error) error {
	if p.curEvent == "" && len(p.data) == 0 {
		return nil
	}
	data := bytes.TrimSuffix(p.data, []byte("\n"))
	if data == nil {
		// A named event can carry no data. Keep today's empty, non-nil slice.
		data = []byte{}
	}
	ev := SSEEvent{Event: p.curEvent, Data: data}
	p.curEvent = ""
	p.data = nil
	return fn(ev)
}

// scanLine reads one line into buf and returns its bytes with the line
// ending removed, plus the error that ended the read. When the returned error
// is nil the line terminator was found, so the bytes are a whole line; on
// [io.EOF] they are whatever preceded the end of the stream, which may be a
// partial line. The result aliases buf and is valid only until the next scan.
//
// Reading this way rather than with bufio.Reader.ReadString keeps the scan
// from building a string per line: the bytes land in one reused buffer, and a
// line longer than the reader's buffer is joined fragment by fragment in that
// same buffer.
func scanLine(br *bufio.Reader, buf []byte) ([]byte, error) {
	buf = buf[:0]
	for {
		frag, err := br.ReadSlice('\n')
		buf = append(buf, frag...)
		if err == bufio.ErrBufferFull {
			continue
		}
		return trimLineEnding(buf), err
	}
}

// trimLineEnding drops every trailing carriage return or newline from line,
// matching the strings.TrimRight(line, "\r\n") this replaced.
func trimLineEnding(line []byte) []byte {
	for n := len(line); n > 0; n = len(line) {
		if line[n-1] != '\n' && line[n-1] != '\r' {
			break
		}
		line = line[:n-1]
	}
	return line
}

// processLine handles a single trimmed SSE line. Returns true if flush was
// triggered (on blank lines).
func (p *sseParser) processLine(line []byte, fn func(SSEEvent) error) (bool, error) {
	switch {
	case len(line) == 0:
		return true, p.flush(fn)
	case line[0] == ':':
		// Comment; ignore.
	case hasFieldPrefix(line, "event:"):
		p.curEvent = strings.TrimSpace(string(line[len("event:"):]))
	case hasFieldPrefix(line, "data:"):
		data := bytes.TrimLeft(line[len("data:"):], " ")
		p.data = append(p.data, data...)
		p.data = append(p.data, '\n')
	default:
		// retry, unknown fields; ignore.
	}
	return false, nil
}

// hasFieldPrefix reports whether line starts with the SSE field name,
// including its colon.
func hasFieldPrefix(line []byte, prefix string) bool {
	return len(line) >= len(prefix) && string(line[:len(prefix)]) == prefix
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
		line, err := scanLine(br, p.lineBuf)
		p.lineBuf = line[:cap(line)]
		if err != nil && err != io.EOF {
			return err
		}

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

	// The reader goroutine fills a line buffer that the main loop consumes, so
	// the two goroutines share those bytes. The loop hands the buffer back on
	// reuseCh once it has finished parsing the line; the reader waits for that
	// before refilling, which is what lets one buffer serve every line without
	// the parser overwriting bytes the loop is still reading.
	lineCh := make(chan readResult, 1)
	reuseCh := make(chan []byte, 1)
	go func() {
		var buf []byte
		for {
			line, err := scanLine(br, buf)
			select {
			case lineCh <- readResult{line, err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
			select {
			case buf = <-reuseCh:
			case <-done:
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

			_, perr := p.processLine(line, fn)
			// The parse has copied every byte it needs out of line, so the
			// buffer can be refilled. The receive above keeps this send from
			// blocking after the parser has returned.
			select {
			case reuseCh <- line[:cap(line)]:
			default:
			}
			if perr != nil {
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
