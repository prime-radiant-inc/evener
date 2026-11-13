package hub

import (
	"context"
	"log/slog"
	"sync"
	"testing"
)

// capturedLog is one record a logCapture received, with its attributes already
// flattened into a map. Tests assert these values and the record's level and
// message instead of parsing a formatted line.
type capturedLog struct {
	level   slog.Level
	message string
	attrs   map[string]any
}

// logCapture is a slog.Handler that keeps every record for assertions. It is
// safe for the background goroutines a hub server starts.
type logCapture struct {
	mu      sync.Mutex
	records []capturedLog
	// notify, when non-nil, receives a signal after each record is recorded.
	notify chan struct{}
}

func (c *logCapture) Enabled(context.Context, slog.Level) bool { return true }

func (c *logCapture) Handle(_ context.Context, r slog.Record) error {
	attrs := make(map[string]any)
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Resolve().Any()
		return true
	})
	c.mu.Lock()
	c.records = append(c.records, capturedLog{level: r.Level, message: r.Message, attrs: attrs})
	c.mu.Unlock()
	if c.notify != nil {
		select {
		case c.notify <- struct{}{}:
		default:
		}
	}
	return nil
}

func (c *logCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *logCapture) WithGroup(string) slog.Handler      { return c }

func (c *logCapture) logger() *slog.Logger { return slog.New(c) }

func (c *logCapture) all() []capturedLog {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]capturedLog(nil), c.records...)
}

func (c *logCapture) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.records)
}

// testLogger forwards records to the test log, standing in for the printf
// sink tests used to hand a manager or config.
func testLogger(t testing.TB) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(testLogWriter{t}, nil))
}

type testLogWriter struct{ t testing.TB }

func (w testLogWriter) Write(p []byte) (int, error) {
	w.t.Helper()
	w.t.Log(string(p))
	return len(p), nil
}

// silentLogger discards records for tests that passed a no-op printf sink to
// keep a code path quiet.
func silentLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }
