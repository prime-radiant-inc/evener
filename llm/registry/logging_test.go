package registry

import (
	"context"
	"log/slog"
	"sync"
)

// capturedRecord is one log record with its attributes flattened for
// assertions.
type capturedRecord struct {
	level   slog.Level
	message string
	attrs   map[string]any
}

// logCapture is a slog.Handler that records what Load logs. The background
// refresh goroutine makes a concurrency-safe recorder necessary.
type logCapture struct {
	mu      sync.Mutex
	records []capturedRecord
}

func (c *logCapture) Enabled(context.Context, slog.Level) bool { return true }

func (c *logCapture) Handle(_ context.Context, r slog.Record) error {
	attrs := make(map[string]any)
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Resolve().Any()
		return true
	})
	c.mu.Lock()
	c.records = append(c.records, capturedRecord{level: r.Level, message: r.Message, attrs: attrs})
	c.mu.Unlock()
	return nil
}

func (c *logCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *logCapture) WithGroup(string) slog.Handler      { return c }

func (c *logCapture) logger() *slog.Logger { return slog.New(c) }

func (c *logCapture) all() []capturedRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]capturedRecord(nil), c.records...)
}
