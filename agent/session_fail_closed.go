package agent

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"primeradiant.com/evener/agent/events"
)

// A served session fails closed when its transcript can no longer record what
// it announces: the transcript could not be created, its writer is poisoned,
// or a COMMUNICATE entry — a message already on its way to the user — was not
// recorded. Failing closed interrupts the running execution, refuses every
// later input, and shows one diagnostic, so history is never missing what a
// client was shown and nothing accumulates in memory that restart would lose.
// Any other append that is not recorded leaves the session running: nothing
// was announced, so nothing is missing.
//
// It applies to served sessions only (those a daemon consumes events from),
// decided at the moment of failure: an unserved session keeps
// warn-and-continue.

// errTranscriptFailedClosed marks the refusal of a session that failed closed.
var errTranscriptFailedClosed = errors.New("session stopped: its transcript cannot record what it shows")

// failClosedCause is the Cause.Kind of the one diagnostic a session that
// failed closed emits, so a consumer can tell it from other errors.
const failClosedCause = "transcript_failed_closed"

// failClosedState is lock-free so any path can read or set it, including
// claim decisions made under the client-mutation store's lock.
type failClosedState struct {
	// cause is the refusal every later input gets; nil until the session fails
	// closed. It is set once.
	cause atomic.Pointer[error]
	// announced is set once the diagnostic is emitted.
	announced atomic.Bool
	// cancelExecution cancels the running execution's context; nil while none
	// runs.
	cancelExecution atomic.Pointer[context.CancelFunc]
}

// failClosed stops a served session for cause: the running execution is
// cancelled and every later input is refused. The diagnostic is emitted by
// announceFailClosed at the next point that holds no session lock. The first
// cause wins; an unserved session is left running. It returns the refusal in
// force, nil for an unserved session.
func (s *Session) failClosed(cause error) error {
	if refusal := s.failedClosedRefusal(); refusal != nil {
		return refusal
	}
	if !s.servedByDaemon() {
		return nil
	}
	refusal := fmt.Errorf("%w: %w", errTranscriptFailedClosed, cause)
	if s.failedClosed.cause.CompareAndSwap(nil, &refusal) {
		if cancel := s.failedClosed.cancelExecution.Load(); cancel != nil {
			(*cancel)()
		}
	}
	return s.failedClosedRefusal()
}

// failClosedOnUnhealthyTranscript fails a served session closed when its
// transcript could not be created or its writer is poisoned. Callers hold no
// session lock and no client-mutation store lock.
func (s *Session) failClosedOnUnhealthyTranscript() {
	if s.transcriptCreateErr != nil {
		_ = s.failClosed(fmt.Errorf("create transcript: %w", s.transcriptCreateErr))
	}
	if s.attachedTranscript().Poisoned() {
		_ = s.failClosed(errTranscriptRefusesRecords())
	}
}

// failedClosedRefusal is the refusal of a session that failed closed, or nil.
func (s *Session) failedClosedRefusal() error {
	if refusal := s.failedClosed.cause.Load(); refusal != nil {
		return *refusal
	}
	return nil
}

// announceFailClosed emits the one diagnostic of a session that failed closed.
// Callers hold no session lock: the event's notification hook takes them.
func (s *Session) announceFailClosed() {
	refusal := s.failedClosedRefusal()
	if refusal == nil || !s.failedClosed.announced.CompareAndSwap(false, true) {
		return
	}
	s.emit(events.EventError, events.ErrorData{
		Error: refusal.Error(),
		Title: "Session stopped",
		Hint:  "Its transcript can no longer record new history. Restart the session to continue from what the transcript holds.",
		Cause: &events.ErrorCause{Kind: failClosedCause},
	})
}

// trackExecutionCancel lets failClosed interrupt the execution whose context
// cancel is; the returned func stops tracking it.
func (s *Session) trackExecutionCancel(cancel context.CancelFunc) func() {
	s.failedClosed.cancelExecution.Store(&cancel)
	if s.failedClosedRefusal() != nil {
		cancel() // failed closed before this execution was tracked
	}
	return func() { s.failedClosed.cancelExecution.Store(nil) }
}
