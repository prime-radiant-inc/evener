package llm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
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

// sseScanChunk is the capacity the scan buffer starts with, and the amount it
// grows by when one line no longer fits.
const sseScanChunk = 4096

// sseMaxEmptyReads bounds refills that return neither bytes nor an error, the
// same way bufio.Reader's read loop does. An io.Reader is allowed to report no
// progress, but a stream of it is [io.ErrNoProgress], not a line never arriving.
const sseMaxEmptyReads = 100

// sseLineScanner splits a stream into lines the way SSE framing does: a line
// ends at CR, LF, or CRLF. It reads into one buffer and serves lines out of it
// in place, refilling only when the buffered bytes hold no complete line, so a
// line costs no allocation and no copy of its own. A line's bytes alias that
// buffer and are valid only until the next line is taken.
//
// Reading this way rather than with bufio.Reader.ReadString keeps the scan from
// building a string per line, and reading the reader directly rather than
// through ReadSlice keeps a CR from having to trail a whole fragment before it
// can end a line.
type sseLineScanner struct {
	r   io.Reader
	buf []byte // bytes read, from the current line's first byte onward
	at  int    // index in buf of the current line's first byte
	// crFrom and lfFrom resume the search for each line terminator byte. Each
	// is the index at which looking for its byte starts: the end of the buffer
	// after a miss, or the byte itself after a hit, so the next line reuses
	// that answer rather than searching the bytes after it again. A refill
	// therefore costs one pass over the bytes it added, however many lines they
	// hold.
	crFrom int
	lfFrom int
	// skipLF is set when a CR ended a line exactly at the end of the buffered
	// bytes. The next byte, if it is an LF, is the other half of that CRLF
	// rather than a line of its own. The line is already delivered, so nothing
	// waits on that byte arriving.
	skipLF  bool
	pending error // error that ended the read, served once the buffered bytes are used up
	empties int   // consecutive refills that returned neither bytes nor an error
}

// next returns the next line with its terminator removed, plus the error that
// ended the read. When the returned error is nil the line terminator was
// found, so the bytes are a whole line; on [io.EOF] they are whatever preceded
// the end of the stream, which may be a partial line.
func (s *sseLineScanner) next() ([]byte, error) {
	for {
		if line, ok := s.line(); ok {
			return line, nil
		}
		if s.pending != nil {
			s.skipLF = false
			line := s.buf[s.at:]
			s.at = len(s.buf)
			s.crFrom, s.lfFrom = s.at, s.at
			return line, s.pending
		}
		// A read that returned bytes and an error leaves the error here: the
		// bytes it brought are still served, as one read's fragment.
		s.pending = s.fill()
	}
}

// line serves the next complete buffered line, advancing past it and its
// terminator.
func (s *sseLineScanner) line() ([]byte, bool) {
	if !s.skipSplitLF() {
		return nil, false
	}
	// Two vectorized searches rather than one two-character scan: lines are the
	// hot path, and each IndexByte runs at memchr speed.
	cr, lf := s.find(&s.crFrom, '\r'), s.find(&s.lfFrom, '\n')
	// Both surviving cases assign end before it is read, so it carries no seed.
	var end, term int
	switch {
	case cr >= 0 && (lf < 0 || cr < lf):
		end, term = cr, 1
		switch {
		case cr+1 == len(s.buf) && s.pending == nil:
			// The CR is the last byte there is. End the line here rather than
			// wait for a byte that may never come: on a blank line that byte is
			// what flushes the event.
			s.skipLF = true
		case cr+1 < len(s.buf) && s.buf[cr+1] == '\n':
			term = 2
		}
	case lf >= 0:
		end, term = lf, 1
	default:
		return nil, false
	}
	line := s.buf[s.at:end]
	s.advance(end + term)
	return line, true
}

// find returns the index of the first want at or after the resume point, or -1.
// It moves the resume point to that byte, or to the end of the buffer when
// there is none, so no later line searches the same bytes again.
func (s *sseLineScanner) find(from *int, want byte) int {
	start := max(*from, s.at)
	i := bytes.IndexByte(s.buf[start:], want)
	if i < 0 {
		*from = len(s.buf)
		return -1
	}
	*from = start + i
	return *from
}

// skipSplitLF consumes the LF of a CRLF whose CR ended the previous line at the
// end of the buffered bytes. It reports false when that LF may still be on its
// way, which is the one case where the next line cannot be served yet.
func (s *sseLineScanner) skipSplitLF() bool {
	if !s.skipLF {
		return true
	}
	if s.at == len(s.buf) {
		return false
	}
	if s.buf[s.at] == '\n' {
		s.advance(s.at + 1)
	}
	s.skipLF = false
	return true
}

// advance moves the current line's start to n and keeps both search resumes at
// or past it.
func (s *sseLineScanner) advance(n int) {
	s.at = n
	if s.crFrom < n {
		s.crFrom = n
	}
	if s.lfFrom < n {
		s.lfFrom = n
	}
}

// fill reads more bytes behind the current line. It moves the line to the front
// of the buffer and reads into whatever room is left, so a stream of ordinary
// lines is served from one buffer that never has to grow; only a line longer
// than the buffer makes it grow.
func (s *sseLineScanner) fill() error {
	if s.at > 0 {
		s.buf = s.buf[:copy(s.buf, s.buf[s.at:])]
		s.crFrom = max(s.crFrom-s.at, 0)
		s.lfFrom = max(s.lfFrom-s.at, 0)
		s.at = 0
	}
	if len(s.buf) == cap(s.buf) {
		s.buf = slices.Grow(s.buf, sseScanChunk)
	}
	end := len(s.buf)
	n, err := s.r.Read(s.buf[end:cap(s.buf)])
	s.buf = s.buf[:end+n]
	if n == 0 && err == nil {
		s.empties++
		if s.empties > sseMaxEmptyReads {
			return io.ErrNoProgress
		}
		return nil
	}
	s.empties = 0
	return err
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
	sc := &sseLineScanner{r: r}
	var p sseParser

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line, err := sc.next()
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}

		if _, perr := p.processLine(line, fn); perr != nil {
			return perr
		}

		if errors.Is(err, io.EOF) {
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
	sc := &sseLineScanner{r: &sseActivityReader{reader: r, progress: timer.progress}}

	// The reader goroutine fills the scanner's one line buffer and the main loop
	// consumes the bytes in it, so the two goroutines share those bytes. The
	// loop signals on reuseCh once it has finished parsing the line; the reader
	// waits for that before scanning again, which is what lets one buffer serve
	// every line without the scanner overwriting bytes the loop is still
	// reading.
	lineCh := make(chan readResult, 1)
	reuseCh := make(chan struct{}, 1)
	go func() {
		for {
			line, err := sc.next()
			select {
			case lineCh <- readResult{line, err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
			select {
			case <-reuseCh:
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
			// buffer can be reused. The receive above keeps this send from
			// blocking after the parser has returned.
			select {
			case reuseCh <- struct{}{}:
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
