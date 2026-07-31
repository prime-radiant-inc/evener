package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
)

// The session's event buffer. These tests exist to cross it, so they name it
// rather than assuming: a test that emits fewer events than the buffer holds
// proves nothing about delivery discipline, and the entire default suite is in
// that position today.
const testEventBuffer = 256

func losslessTestSession(id string) *Session {
	return &Session{id: id, events: make(chan events.SessionEvent, testEventBuffer)}
}

func emitN(s *Session, n int) {
	for range n {
		s.sendEvent(events.EventWarning, events.WarningData{Message: "x"}, nil)
	}
}

// awaitWithin fails loudly if fn has not returned within the budget. A wedged
// producer and a slow machine are indistinguishable to a bare receive, and only
// one of them should be allowed to consume the package timeout.
func awaitWithin(t *testing.T, budget time.Duration, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(budget):
		t.Fatalf("%s did not finish within %s", what, budget)
	}
}

// registerConsumer attaches an authoritative consumer and asserts the call
// RETURNS. That is half the contract: ConsumeEventsLossless registers and then
// drains on its own goroutine, so a caller can rely on the mark being in effect
// when it comes back. An implementation that blocked instead would leave every
// test below hanging until the package timeout, which reads as a flake rather
// than as the contract being broken.
func registerConsumer(t *testing.T, s *Session, consume func(events.SessionEvent)) {
	t.Helper()
	awaitWithin(t, 10*time.Second, "ConsumeEventsLossless returning once registered", func() {
		s.ConsumeEventsLossless(consume, func() {})
	})
	s.eventsMu.RLock()
	defer s.eventsMu.RUnlock()
	if !s.authoritativeConsumer {
		t.Fatal("ConsumeEventsLossless returned without the session marked; events emitted now would still drop")
	}
}

// TestSessionWithNoConsumerDropsRatherThanWedging is the test the previous
// design could not have: it CROSSES the buffer.
//
// Every subagent and delegate session in production has an event channel that
// nothing ever reads -- children reach their parent through synchronous
// callbacks, and no code in this package receives on the channel at all. Their
// occupancy is therefore monotonic, so a feed that waits for a reader wedges
// each of them permanently once it fills.
//
// This is deliberately the shape no other test in the repo has. The scripted
// provider the default suite runs on emits orders of magnitude fewer events
// than a real model, so a delivery discipline that wedges every subagent in
// production passes the entire suite. The threshold has to be crossed on
// purpose or it is not covered at all.
func TestSessionWithNoConsumerDropsRatherThanWedging(t *testing.T) {
	s := losslessTestSession("no-consumer")

	awaitWithin(t, 10*time.Second, "emitting past the buffer with nobody reading", func() {
		emitN(s, testEventBuffer*3)
	})

	if got := len(s.events); got != testEventBuffer {
		t.Fatalf("buffered %d events, want the buffer full at %d", got, testEventBuffer)
	}
}

// TestAuthoritativeConsumerReceivesEveryEventPastTheBuffer is the other half:
// once something is draining and would be permanently wrong if it missed an
// event, nothing may be dropped -- including well past the buffer, and
// including while the consumer is slower than the producer.
func TestAuthoritativeConsumerReceivesEveryEventPastTheBuffer(t *testing.T) {
	s := losslessTestSession("authoritative")
	const total = testEventBuffer * 4

	var mu sync.Mutex
	seen := 0
	registerConsumer(t, s, func(events.SessionEvent) {
		mu.Lock()
		seen++
		n := seen
		mu.Unlock()
		// Be slower than the producer for the first stretch, so the buffer
		// genuinely fills and the producer genuinely has to wait. Without
		// this the test could pass on a fast consumer that never lets the
		// channel back up.
		if n < testEventBuffer/8 {
			time.Sleep(time.Millisecond)
		}
	})

	awaitWithin(t, 30*time.Second, "emitting past the buffer with a slow consumer", func() {
		emitN(s, total)
	})

	s.eventsMu.Lock()
	s.eventsClosed = true
	close(s.events)
	s.eventsMu.Unlock()

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return seen >= total
	}, "the consumer to drain every event")

	mu.Lock()
	defer mu.Unlock()
	if seen != total {
		t.Fatalf("consumer saw %d of %d events: the authoritative feed dropped %d", seen, total, total-seen)
	}
}

// TestConsumeEventsLosslessRejectsASecondConsumer pins the one-consumer rule.
// Two receivers on one channel split the stream, so each would silently see
// part of the history and believe it saw all of it.
// TestConsumeEventsLosslessRejectsANilOnDrained pins the guard that keeps the
// teardown wait from being skippable.
//
// The wait was a returned channel first, and a returned channel can be dropped:
// Go permits ignoring a non-error return, no linter here flags it, and
// `ConsumeEventsLossless(consume)` compiled cleanly while putting the shutdown
// crash straight back. Making it a parameter fixes that -- but only while nil is
// refused, because nil is the same omission wearing a different hat.
func TestConsumeEventsLosslessRejectsANilOnDrained(t *testing.T) {
	s := losslessTestSession("nil-ondrained")
	defer func() {
		if recover() == nil {
			t.Fatal("a nil onDrained was accepted; the drain wait is skippable again")
		}
		// The session must not be left marked by a rejected registration: a
		// mark with no drain behind it wedges every emitter at 257.
		s.eventsMu.RLock()
		defer s.eventsMu.RUnlock()
		if s.authoritativeConsumer {
			t.Fatal("a rejected registration still marked the session")
		}
	}()
	s.ConsumeEventsLossless(func(events.SessionEvent) {}, nil)
}

func TestConsumeEventsLosslessRejectsASecondConsumer(t *testing.T) {
	s := losslessTestSession("double")
	registerConsumer(t, s, func(events.SessionEvent) {})

	// The budget matters as much as the recover: without the rejection the
	// second call does not return an error, it registers a second drain
	// goroutine that competes for the same channel. A bare recover would let a
	// regression read as a pass.
	recovered := make(chan any, 1)
	awaitWithin(t, 10*time.Second, "a second registration", func() {
		defer func() { recovered <- recover() }()
		s.ConsumeEventsLossless(func(events.SessionEvent) {}, func() {})
	})
	if <-recovered == nil {
		t.Fatal("a second authoritative consumer was accepted; the stream would be split silently")
	}
}

// TestConsumeEventsLosslessOnAClosedSessionReturns keeps teardown ordering from
// hanging a caller that registers during shutdown, and pins that the closed
// session does not come away MARKED.
//
// Liveness alone does not pin the guard: without it the call still returns
// promptly, because ranging a closed channel ends immediately. What the guard
// actually buys is that authoritativeConsumer never becomes true on a session
// that can no longer deliver anything, so the flag continues to mean "something
// is draining this" rather than "something once asked to".
//
// The early return owes the caller onDrained just as much as the drain does,
// and that is asserted rather than assumed. This is the one path where the
// callback is invoked by the guard instead of by the loop, so it is also the one
// path where a missed onDrained is a single deleted line -- and the caller that
// misses it does not get an error, it waits forever. serve.go's teardown is
// exactly such a caller.
func TestConsumeEventsLosslessOnAClosedSessionReturns(t *testing.T) {
	s := losslessTestSession("closed")
	s.eventsMu.Lock()
	s.eventsClosed = true
	close(s.events)
	s.eventsMu.Unlock()

	drained := make(chan struct{})
	awaitWithin(t, 10*time.Second, "registering on a closed session", func() {
		s.ConsumeEventsLossless(func(events.SessionEvent) {}, func() { close(drained) })
	})

	// Budgeted rather than a bare park: the guard fires onDrained synchronously,
	// so this is already closed, but a regression that reroutes the closed
	// session through the drain goroutine would make it merely late rather than
	// absent, and neither should be allowed to consume the package timeout.
	select {
	case <-drained:
	case <-time.After(10 * time.Second):
		t.Fatal("registering on a closed session never ran onDrained; a caller waiting on it waits forever")
	}

	s.eventsMu.RLock()
	defer s.eventsMu.RUnlock()
	if s.authoritativeConsumer {
		t.Fatal("a closed session was marked as having an authoritative consumer")
	}
}

// TestPreparedSubagentGetsNoAuthoritativeConsumer pins the production claim the
// whole design rests on: a child session is safe BY CONSTRUCTION, not because
// someone remembered to leave it alone.
//
// Nothing in this package receives on a child's event channel -- a child
// reaches its parent through synchronous callbacks -- so if a child were ever
// marked as having an authoritative consumer, its emitters would wedge the
// moment its buffer filled. Registering is the only thing that can set the
// mark, and the spawn path does not register.
//
// It asserts on a REAL prepared subagent rather than a hand-built Session,
// because the claim is about the spawn path, not about the struct.
func TestPreparedSubagentGetsNoAuthoritativeConsumer(t *testing.T) {
	parent := newTestSession(t)

	prepared, err := parent.prepareSubagentRun(context.Background(), "child task", "", "", 0, "", "", nil, nil)
	if err != nil {
		t.Fatalf("prepareSubagentRun: %v", err)
	}
	defer releasePreparedTreeSlot(prepared)
	child := prepared.sub.sess
	defer child.Close()

	child.eventsMu.RLock()
	marked := child.authoritativeConsumer
	child.eventsMu.RUnlock()
	if marked {
		t.Fatal("a spawned child is marked as having an authoritative consumer; nothing reads its channel, so it would wedge")
	}

	// And it genuinely survives crossing its own buffer, which is the failure
	// the mark would cause.
	awaitWithin(t, 10*time.Second, "a spawned child emitting past its buffer", func() {
		emitN(child, testEventBuffer*2)
	})
}

// TestSessionConstructionStaysWellInsideTheBuffer pins the one window the
// design cannot close.
//
// A session emits SESSION_START and its construction diagnostics from inside
// NewSession, so those events necessarily precede any consumer: nothing can
// attach to a session that does not exist yet. They are not lost -- they sit in
// the buffer and are delivered the moment the daemon attaches -- but only
// because construction emits far fewer than a bufferful. That is a real
// dependency, and it is the kind that rots silently: a future feature that
// emits per plugin, per MCP server or per skill at construction could approach
// it, and the resulting loss would look like a daemon that started up missing
// its own history.
//
// The margin is asserted rather than assumed. If this fails, the fix is to
// raise the buffer or to attach earlier, not to relax the bound.
func TestSessionConstructionStaysWellInsideTheBuffer(t *testing.T) {
	s := newTestSession(t)

	buffered := len(s.events)
	if buffered == 0 {
		t.Fatal("construction emitted nothing; this test would pass against a session that never starts")
	}
	// A quarter of the buffer is the alarm threshold, not the contract. Well
	// under it today; crossing it means the pre-attach window has become worth
	// designing around.
	if limit := testEventBuffer / 4; buffered >= limit {
		t.Fatalf("construction emitted %d events, within reach of the %d-deep buffer (alarm at %d): "+
			"events emitted before a consumer can attach are at risk", buffered, testEventBuffer, limit)
	}
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}
