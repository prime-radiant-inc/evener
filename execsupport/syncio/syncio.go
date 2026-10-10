// Package syncio provides small io helpers safe for concurrent use, for the
// one recurring shape this repo hits more than once: a caller-supplied
// io.Writer (often a plain bytes.Buffer in tests, which is not safe for
// concurrent use on its own) written to from more than one goroutine — a
// spawned command's own output-drain goroutine racing the caller's own
// writes to the same stream (cmd/evener/run.go's event-drain goroutine vs.
// run_ask_responder.go's --ask-responder logging; cmd/evener-hub/internal/
// sshconn's per-host diagSink vs. an attach's own stderr write).
package syncio

import (
	"io"
	"sync"
)

// Writer serializes concurrent Write calls onto an underlying io.Writer.
type Writer struct {
	mu sync.Mutex
	w  io.Writer
}

// NewWriter wraps w so concurrent Write calls from different goroutines
// serialize onto it instead of racing.
func NewWriter(w io.Writer) *Writer { return &Writer{w: w} }

// Write implements io.Writer, delegating to the wrapped writer under a lock.
func (s *Writer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}
