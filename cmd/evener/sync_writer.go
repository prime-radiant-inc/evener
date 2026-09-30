package main

import (
	"io"
	"sync"
)

// syncWriter serializes concurrent Write calls onto w. run()'s own
// goroutine (the --ask-responder loop's stderr logging) and the event-drain
// goroutine (drainEventsHuman/drainEventsVerbose) both write to cfg.stderr
// independently, so without this a race detector (and, in principle, an
// interleaved write) sees two unsynchronized writers on the same
// io.Writer/*bytes.Buffer. Mirrors cmd/evener-hub/internal/sshconn/runner.go's
// syncWriter, which exists for the identical reason (a command's stdout/
// stderr pipes drained on their own goroutine, written to from the caller's
// too); kept as its own small copy here rather than shared, since sshconn's
// is unexported in a different binary's internal package.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func newSyncWriter(w io.Writer) *syncWriter { return &syncWriter{w: w} }

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}
