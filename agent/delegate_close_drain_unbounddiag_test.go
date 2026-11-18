package agent

import (
	"errors"
	"testing"

	"primeradiant.com/evener/agent/events"
)

// An ordinary transcript write carries no diagnostic bound, and the fail-closed
// announcement it raises must not be handed a nil context. Routing it to the
// unbounded announce keeps the session's own delivery path and never
// dereferences a nil context.
//
// This covers the reachable half: an unbound diagnostic still reaches the
// stream. See the note below on the parked half, which has no deterministic red.
func TestUnboundTranscriptDiagnosticAnnouncesWithoutABound(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	s.events = make(chan events.SessionEvent, 8)
	s.authoritativeConsumer = true
	t.Cleanup(func() { s.authoritativeConsumer = false })
	if err := s.failClosed(errors.New("transcript refuses writes")); err == nil {
		t.Fatal("session did not fail closed")
	}

	// The unbound path, exactly what an ordinary recordTurnTo caller reaches.
	s.surfaceTranscriptWarningsTo(recordDiagnostics{
		warn: func(w events.WarningData) { s.emit(w) },
	})

	if !s.failedClosed.announced.Load() {
		t.Fatal("the unbound diagnostic was never announced")
	}
	var sawDiagnostic bool
	for len(s.events) > 0 {
		ev := <-s.events
		if _, ok := ev.Data.(events.ErrorData); ok {
			sawDiagnostic = true
		}
	}
	if !sawDiagnostic {
		t.Fatal("the stream carries no failure diagnostic: the unbound path dropped it")
	}
}

// KNOWN GAP, disclosed rather than argued: the parked half of this finding -- a
// nil context reaching the blocked-send select, which is where the panic lived --
// has no deterministic local red. Reaching that select requires a FULL
// authoritative buffer, and a send parked there holds eventsMu.RLock for as long
// as it is parked. Session teardown (closeEventStream, session_close.go) takes
// eventsMu.Lock, so every test that parks the send deadlocks its own cleanup
// before the assertion can run, in the fixed and the broken build alike. A
// consumer goroutine that frees the buffer lets the send through, but then the
// nil context is never evaluated and the mutant passes -- which is why the first
// two versions of this test were vacuous.
//
// What does hold the line here: the nil check in surfaceTranscriptWarningsTo
// routes to announceFailClosed, and staticcheck's SA1012 (which lint-golangci
// enforces at zero tolerance in this repo) rejects a literal nil Context at every
// call site. This is unproven by test, not verified.
