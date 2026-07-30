package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"primeradiant.com/serf/agent/events"
)

// verboseEventTeeBuffer is how many events the tee will hold for a stderr that
// is not keeping up. It is deliberately larger than the session's own 256-deep
// event channel: the tee must be the thing that falls behind, never the reason
// the session's feed backs up.
const verboseEventTeeBuffer = 1024

// verboseEventTee writes session events to a writer as NDJSON from its own
// goroutine.
//
// It exists because `serf serve --verbose` installs its observer on the BRIDGE,
// and the bridge is the daemon's authoritative consumer: it is the one thing
// draining the session's event channel and updating the projection every
// thread/read is served from. A synchronous write there means the daemon's
// projection advances no faster than whatever is reading its stderr. Point that
// at a pipe nobody drains and the write blocks forever, the session's 256-deep
// channel fills, and events are dropped from the authoritative feed -- silent,
// permanent projection corruption caused by a logging flag.
//
// So the tee never blocks its caller. When it cannot keep up it drops, which is
// the correct trade for a diagnostic log and the wrong one for the projection;
// having the two share a goroutine is what conflated them. A dropped log line
// is announced (see flushDropped) rather than silently swallowed, because a
// truncated NDJSON stream that does not say it was truncated is worse than a
// gap.
type verboseEventTee struct {
	ch   chan events.SessionEvent
	done chan struct{}

	mu      sync.Mutex
	dropped int
}

func newVerboseEventTee(w io.Writer, buffer int) *verboseEventTee {
	t := &verboseEventTee{
		ch:   make(chan events.SessionEvent, buffer),
		done: make(chan struct{}),
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	go func() {
		defer close(t.done)
		for ev := range t.ch {
			t.flushDropped(w)
			_ = enc.Encode(ev)
		}
		t.flushDropped(w)
	}()
	return t
}

// flushDropped reports and clears any accumulated drop count, so the NDJSON
// stream names its own gaps instead of just having them.
func (t *verboseEventTee) flushDropped(w io.Writer) {
	t.mu.Lock()
	n := t.dropped
	t.dropped = 0
	t.mu.Unlock()
	if n > 0 {
		fmt.Fprintf(w, "{\"serfVerboseDropped\":%d}\n", n) //nolint:errcheck
	}
}

// observe queues ev for logging. It never blocks: the caller is the bridge.
func (t *verboseEventTee) observe(ev events.SessionEvent) {
	select {
	case t.ch <- ev:
	default:
		t.mu.Lock()
		t.dropped++
		t.mu.Unlock()
	}
}

// close stops the tee and waits for the writer goroutine to drain what it
// already accepted, so a clean shutdown does not truncate the log.
func (t *verboseEventTee) close() {
	close(t.ch)
	<-t.done
}
