package main

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
)

// blockingWriter stalls its first Write until released, standing in for a
// stderr pipe with nobody on the other end. That is a real deployment: a daemon
// launched with its stderr piped to a reader that exits.
type blockingWriter struct {
	release chan struct{}
	once    sync.Once

	mu  sync.Mutex
	buf bytes.Buffer
}

func newBlockingWriter() *blockingWriter {
	return &blockingWriter{release: make(chan struct{})}
}

func (w *blockingWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { <-w.release })
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *blockingWriter) unblock() { close(w.release) }

func (w *blockingWriter) contents() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// TestVerboseEventTeeDoesNotBlockOnAStalledWriter is the whole point of the
// tee, and it is a property of the daemon's authoritative consumer, not of
// logging.
//
// `serf serve --verbose` hangs this observer off the BRIDGE, which is the one
// goroutine draining the session's event channel and advancing the projection.
// A synchronous write there couples the daemon's correctness to whatever is
// reading its stderr: block the pipe and the 256-deep channel fills, events are
// dropped from the authoritative feed, and thread/read is permanently wrong.
//
// The assertion is on the CALLER's liveness, with a budget, so a regression
// fails loudly instead of hanging until the package timeout.
func TestVerboseEventTeeDoesNotBlockOnAStalledWriter(t *testing.T) {
	w := newBlockingWriter()
	tee := newVerboseEventTee(w, 8)

	// Far more events than the buffer holds, so this cannot pass merely by
	// fitting: it has to survive the writer being wedged AND the queue filling.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 500 {
			tee.observe(events.SessionEvent{Kind: events.EventWarning, SessionID: "s1"})
		}
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		// Deliberately NOT closing the tee here, and not in a t.Cleanup either.
		// A regression leaves that goroutine parked inside observe, and closing
		// the tee underneath it panics with "send on closed channel" on ITS
		// stack -- which kills the binary before the failure below is printed,
		// turning a named assertion into a crash dump. The parked goroutine
		// leaks; the test has already failed, and a leak is the cheap half.
		t.Fatal("observe blocked on a stalled writer: the bridge is coupled to stderr")
	}
	// Reached only when every observe returned, so nothing is inside the tee.
	w.unblock()
	tee.close()
}

// TestVerboseEventTeeWritesWhatItAccepts proves the tee is not merely fast
// because it throws everything away. Liveness alone would pass against a stub
// that discards every event.
func TestVerboseEventTeeWritesWhatItAccepts(t *testing.T) {
	var buf syncBuffer
	tee := newVerboseEventTee(&buf, 64)
	tee.observe(events.SessionEvent{Kind: events.EventWarning, SessionID: "s1"})
	tee.observe(events.SessionEvent{Kind: events.EventWarning, SessionID: "s2"})
	tee.close()

	lines := nonEmptyLines(buf.String())
	if len(lines) != 2 {
		t.Fatalf("wrote %d lines, want 2: %q", len(lines), buf.String())
	}
	for i, want := range []string{"s1", "s2"} {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(lines[i]), &decoded); err != nil {
			t.Fatalf("line %d is not JSON: %v (%q)", i, err, lines[i])
		}
		if decoded["session_id"] != want && decoded["sessionId"] != want {
			t.Fatalf("line %d = %q, want the event for %s", i, lines[i], want)
		}
	}
}

// TestVerboseEventTeeAnnouncesWhatItDropped pins the gap being NAMED. A
// truncated NDJSON stream that does not say it was truncated is worse than one
// with an announced hole: a reader counts events and concludes the daemon went
// quiet.
func TestVerboseEventTeeAnnouncesWhatItDropped(t *testing.T) {
	w := newBlockingWriter()
	tee := newVerboseEventTee(w, 4)
	// Same reason as the liveness test: emit off the test goroutine with a
	// budget, so a tee that blocks names itself instead of hanging the package.
	emitted := make(chan struct{})
	go func() {
		defer close(emitted)
		for range 200 {
			tee.observe(events.SessionEvent{Kind: events.EventWarning, SessionID: "s1"})
		}
	}()
	select {
	case <-emitted:
	case <-time.After(10 * time.Second):
		t.Fatal("observe blocked; this test cannot measure drops against a tee that waits")
	}
	w.unblock()
	tee.close()

	if !strings.Contains(w.contents(), "serfVerboseDropped") {
		t.Fatalf("log does not announce the dropped events: %q", truncate(w.contents()))
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func nonEmptyLines(s string) []string {
	var out []string
	for line := range strings.SplitSeq(s, "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

func truncate(s string) string {
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}

var _ io.Writer = (*syncBuffer)(nil)
