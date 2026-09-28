package agent

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/transcript"
)

// A served session fails closed when its transcript can no longer record what
// it announces: the transcript could not be created, its writer is poisoned,
// or a COMMUNICATE entry (a message already on its way to the user) or a
// completion entry (a turn's terminal status) was not recorded. Failing closed interrupts the running execution, refuses every
// later input, and shows one diagnostic, so history is never missing a message
// a client was shown and nothing accumulates in memory that restart would
// lose. Any other append that is not recorded leaves the session running, with
// the write's warning. That includes a presentational notice: its live event
// is shown as an ephemeral notice, as it always was, and only COMMUNICATE is
// announced as history before it is recorded.
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

// failClosedUnlessRecorded fails a served session closed when the write of an
// entry whose loss a client would see (a COMMUNICATE message, a turn's
// completion) failed, err being the write's error, and returns the refusal;
// nil when it was recorded, or when the session is not served. A write the
// placement declines without an error recorded nothing because there was
// nothing to record (a completion of an execution that recorded nothing). A
// closed writer is a session shutting down, not a writer failure, so it is
// exempted here too; a caller that must tell that case apart from a genuine
// failure (deliverCommunicate, which must never announce a message a closed
// writer silently dropped) checks Closed() itself before this runs. A missing
// writer is a genuine failure the caller reached without checking first: the
// ordinary path guards its call on attachedTranscript() != nil (a completion,
// which records nothing when there is no writer to record it), so this only
// fires for a caller that skipped that guard, such as deliverCommunicate,
// which has no such guard because its synced write must never be held.
func (s *Session) failClosedUnlessRecorded(rec transcript.Record, err error, what string) error {
	if rec.Recorded || err == nil {
		return nil
	}
	if writer := s.attachedTranscript(); writer != nil && writer.Closed() {
		return nil
	}
	return s.failClosed(fmt.Errorf("%s was not recorded: %w", what, err))
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

// durabilityRetryAttempts bounds the background retry of the durability
// barrier for a recorded-but-unsynced (*transcript.RetainedUnsyncedError)
// COMMUNICATE or completion entry, after the one attempt recordSynced already
// made inline. durabilityRetryDelay/durabilityRetryMaxDelay are the same
// shape of exponential backoff as shellFinalizeBackoff (agent/job_shell.go),
// scaled down: a durability barrier is a local fsync, not a store round trip.
const (
	durabilityRetryAttempts = 5
	durabilityRetryDelay    = 20 * time.Millisecond
	durabilityRetryMaxDelay = 200 * time.Millisecond
)

// durabilityRetryBackoff is attempt's backoff delay: durabilityRetryDelay,
// doubling each attempt, capped at durabilityRetryMaxDelay.
func durabilityRetryBackoff(attempt int) time.Duration {
	delay := durabilityRetryDelay
	for range attempt {
		delay *= 2
		if delay >= durabilityRetryMaxDelay {
			return durabilityRetryMaxDelay
		}
	}
	return delay
}

// retainedUnsyncedError extracts a *transcript.RetainedUnsyncedError from a
// synced write's error, or nil when err is anything else (including nil).
func retainedUnsyncedError(err error) *transcript.RetainedUnsyncedError {
	var retained *transcript.RetainedUnsyncedError
	if errors.As(err, &retained) {
		return retained
	}
	return nil
}

// settleRetainedUnsynced chases durability for a COMMUNICATE or completion
// entry that recordSynced adopted with a *RetainedUnsyncedError: the entry is
// already in the file, already adopted into history, and (for COMMUNICATE)
// already announced. It retries the durability barrier with backoff in its
// own goroutine, so the caller returns immediately and the session keeps
// running while the retry is in flight -- no session lock is held across a
// sleep. If a retry establishes durability, the debt is settled silently. If
// the retry budget is exhausted, a served session fails closed (its terminal
// status or delivered message could not be made durable); an unserved session
// keeps running -- warn-and-continue, matching the ordinary write path -- since
// only a served session's fail-closed rule applies here.
//
// The retry does not start at all if the session is already closing: closing
// tears down the writer itself, and a session shutting down is not a
// durability failure (see failClosedUnlessRecorded's closed-writer exemption).
func (s *Session) settleRetainedUnsynced(retained *transcript.RetainedUnsyncedError, what string) {
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		return
	}
	s.sendersWG.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.sendersWG.Done()
		s.retryDurabilityUntilSettledOrExhausted(retained, what)
	}()
}

// retryDurabilityUntilSettledOrExhausted is settleRetainedUnsynced's retry
// loop; split out so tests can call it synchronously.
func (s *Session) retryDurabilityUntilSettledOrExhausted(retained *transcript.RetainedUnsyncedError, what string) {
	writer := s.attachedTranscript()
	lastErr := error(retained)
	for attempt := range durabilityRetryAttempts {
		s.sclock().Sleep(durabilityRetryBackoff(attempt))
		if writer == nil || writer.Closed() {
			return // the session is shutting down, not a genuine durability failure
		}
		if err := writer.EstablishDurability(); err == nil {
			return // settled
		} else {
			lastErr = err
		}
	}
	if !s.servedByDaemon() {
		return
	}
	if refusal := s.failClosed(fmt.Errorf("%s could not be made durable after retrying: %w", what, lastErr)); refusal != nil {
		s.announceFailClosed()
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
// cancel is; the returned func stops tracking it. The cleanup only clears
// the pointer it stored, by compare-and-swap: two executions can overlap
// (one finishing while a newer one starts), and an unconditional clear would
// let the finishing one erase the newer one's cancel, leaving failClosed
// with nothing to interrupt the run still in flight.
func (s *Session) trackExecutionCancel(cancel context.CancelFunc) func() {
	tracked := &cancel
	s.failedClosed.cancelExecution.Store(tracked)
	if s.failedClosedRefusal() != nil {
		cancel() // failed closed before this execution was tracked
	}
	return func() { s.failedClosed.cancelExecution.CompareAndSwap(tracked, nil) }
}
