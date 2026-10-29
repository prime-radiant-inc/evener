// Package componentlog builds the *slog.Logger a daemon component uses for its
// operational diagnostics: one line per record on stderr, prefixed with the
// component in brackets, followed by the message and any key=value attributes.
package componentlog

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
)

// New returns a logger that writes "[component] message key=value ..." lines to
// the process's current stderr. The destination is resolved here, once, so a
// long-lived goroutine never reads the mutable os.Stderr global.
func New(component string) *slog.Logger {
	return NewWriter(component, os.Stderr)
}

// NewWriter is New with an explicit destination.
func NewWriter(component string, w io.Writer) *slog.Logger {
	return slog.New(&handler{prefix: "[" + component + "] ", out: &lockedWriter{w: w}})
}

// lockedWriter is shared by every handler derived from one logger so records
// from different goroutines do not interleave.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

type handler struct {
	prefix string
	out    *lockedWriter
	attrs  []slog.Attr
}

func (h *handler) Enabled(context.Context, slog.Level) bool { return true }

func (h *handler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(h.prefix)
	b.WriteString(r.Message)
	for _, a := range h.attrs {
		writeAttr(&b, a)
	}
	r.Attrs(func(a slog.Attr) bool {
		writeAttr(&b, a)
		return true
	})
	b.WriteByte('\n')
	h.out.mu.Lock()
	defer h.out.mu.Unlock()
	_, err := io.WriteString(h.out.w, b.String())
	return err
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *h
	clone.attrs = append(append([]slog.Attr(nil), h.attrs...), attrs...)
	return &clone
}

// WithGroup is a no-op: component loggers do not nest attribute groups.
func (h *handler) WithGroup(string) slog.Handler { return h }

func writeAttr(b *strings.Builder, a slog.Attr) {
	if a.Equal(slog.Attr{}) {
		return
	}
	fmt.Fprintf(b, " %s=%v", a.Key, a.Value.Resolve().Any())
}
