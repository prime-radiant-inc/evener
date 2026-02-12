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

	var curEvent string
	var dataBuf bytes.Buffer
	flush := func() error {
		if curEvent == "" && dataBuf.Len() == 0 {
			return nil
		}
		b := bytes.TrimSuffix(dataBuf.Bytes(), []byte("\n"))
		ev := SSEEvent{Event: curEvent, Data: append([]byte{}, b...)}
		curEvent = ""
		dataBuf.Reset()
		return fn(ev)
	}

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line, err := br.ReadString('\n')
		if err != nil && err != io.EOF {
			return err
		}
		line = strings.TrimRight(line, "\r\n")

		switch {
		case line == "":
			if err := flush(); err != nil {
				return err
			}
		case strings.HasPrefix(line, ":"):
			// Comment; ignore.
		case strings.HasPrefix(line, "event:"):
			curEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data := strings.TrimPrefix(line, "data:")
			data = strings.TrimLeft(data, " ")
			dataBuf.WriteString(data)
			dataBuf.WriteString("\n")
		default:
			// retry, unknown fields; ignore.
		}

		if err == io.EOF {
			// Flush final partial event.
			return flush()
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

	var curEvent string
	var dataBuf bytes.Buffer
	flush := func() error {
		if curEvent == "" && dataBuf.Len() == 0 {
			return nil
		}
		b := bytes.TrimSuffix(dataBuf.Bytes(), []byte("\n"))
		ev := SSEEvent{Event: curEvent, Data: append([]byte{}, b...)}
		curEvent = ""
		dataBuf.Reset()
		return fn(ev)
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case res := <-lineCh:
			if !timer.Stop() {
				// Drain the timer channel if it already fired.
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

			switch {
			case line == "":
				if err := flush(); err != nil {
					return err
				}
			case strings.HasPrefix(line, ":"):
				// Comment; ignore.
			case strings.HasPrefix(line, "event:"):
				curEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				data := strings.TrimPrefix(line, "data:")
				data = strings.TrimLeft(data, " ")
				dataBuf.WriteString(data)
				dataBuf.WriteString("\n")
			default:
				// retry, unknown fields; ignore.
			}

			if err == io.EOF {
				return flush()
			}

		case <-timer.C:
			return fmt.Errorf("stream read timeout: no data received for %v", timeout)

		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
