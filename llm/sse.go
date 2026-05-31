package llm

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

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
// each line of data is read. A zero or negative duration disables the timeout.
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
	br := bufio.NewReader(r)

	lineCh := make(chan readResult, 1)
	go func() {
		for {
			line, err := br.ReadString('\n')
			lineCh <- readResult{line, err}
			if err != nil {
				return
			}
		}
	}()

	var p sseParser
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case res := <-lineCh:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(timeout)

			line := res.line
			err := res.err
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

		case <-timer.C:
			return fmt.Errorf("stream read timeout: no data received for %v", timeout)

		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
