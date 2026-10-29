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
	"strconv"
	"strings"
	"sync"
	"unicode"
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
	group  string // qualified-key prefix from WithGroup, "" or ending in "."
	bound  []boundAttr
}

// boundAttr is an attribute fixed by With, remembered with the group prefix
// that was open when it was bound.
type boundAttr struct {
	prefix string
	attr   slog.Attr
}

func (h *handler) Enabled(context.Context, slog.Level) bool { return true }

func (h *handler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(h.prefix)
	b.WriteString(printable(r.Message))
	for _, ba := range h.bound {
		writeAttr(&b, ba.prefix, ba.attr)
	}
	r.Attrs(func(a slog.Attr) bool {
		writeAttr(&b, h.group, a)
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
	clone.bound = append([]boundAttr(nil), h.bound...)
	for _, a := range attrs {
		clone.bound = append(clone.bound, boundAttr{h.group, a})
	}
	return &clone
}

func (h *handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	clone := *h
	clone.group = h.group + name + "."
	return &clone
}

// writeAttr appends " key=value", qualifying the key with prefix and
// flattening group values into dotted keys.
func writeAttr(b *strings.Builder, prefix string, a slog.Attr) {
	if a.Equal(slog.Attr{}) {
		return
	}
	v := a.Value.Resolve()
	if v.Kind() == slog.KindGroup {
		if a.Key != "" {
			prefix += a.Key + "."
		}
		for _, g := range v.Group() {
			writeAttr(b, prefix, g)
		}
		return
	}
	fmt.Fprintf(b, " %s=%s", printable(prefix+a.Key), printable(fmt.Sprint(v.Any())))
}

// printable quotes s when it holds a control character, so no message or value
// can end the record's line or start a forged one.
func printable(s string) string {
	for _, r := range s {
		if unicode.IsControl(r) {
			return strconv.Quote(s)
		}
	}
	return s
}
