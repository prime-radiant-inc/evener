package agent

import (
	"errors"
	"fmt"

	"primeradiant.com/evener/agent/transcript"
)

// refuseOnCurrentUnhealthyTranscript decides the refusal on the writer attached
// now, and treats a refusal as final only when that writer is still attached at
// a fresh s.mu read.
//
// A reopen publishes its replacement under s.mu and closes the writer it
// replaced after releasing s.mu, so a gate that sampled the old writer can read
// it closed or still poisoned from a partial tail the reopen already cut off.
// Discarding a refusal from a writer that is no longer attached is what keeps
// that stale sample from refusing a healthy session.
//
// Each retry requires a completed replacement: reopens install freshly opened
// writers and never re-attach an old one, so each pass decides on a new writer
// generation, and the gate never reopens anything itself. The decision reads the
// writer's own lock outside s.mu; a close after a healthy decision is the
// pre-existing stale read that the next round's gate re-samples.
func (s *Session) refuseOnCurrentUnhealthyTranscript() error {
	writer := s.attachedTranscript()
	for {
		if hook := s.cfg.testOnly.turnGateSampledTranscript; hook != nil {
			hook(writer)
		}
		refusal := s.refuseOnUnhealthyTranscript(writer)
		if refusal == nil {
			return nil
		}
		current := s.attachedTranscript()
		if current != writer {
			writer = current
			continue
		}
		if errors.Is(refusal, transcript.ErrWriterPoisoned) {
			if failedClosed := s.failClosed(refusal); failedClosed != nil {
				refusal = failedClosed
			}
		}
		return refusal
	}
}

// refuseBeforeClaimingOnUnhealthyTranscript reports why a durable claim must not
// be taken when the transcript has stopped accepting records. The turn loop's
// own gate refuses too, but by then the caller has already claimed its mutation
// or taken the queue head, and a claim spent on a turn that never runs is
// recoverable only by restarting the session. This is the first of two guards:
// poisoning can land between this read and that gate, which is why both callers
// also give the claim back when the loop refuses.
//
// Unlike the loop's gate it settles nothing and emits no end of input: at these
// entry points no turn has begun and no input has been published, so there is
// no processing boundary to close and no subscriber waiting to hear this input
// end. A session that failed closed does show its one diagnostic here, since a
// refused claim never reaches the loop's gate. Its callers only reach it when
// they have work in hand, so an idle wake against a dead transcript still
// stands down quietly.
func (s *Session) refuseBeforeClaimingOnUnhealthyTranscript() error {
	s.failClosedOnUnhealthyTranscript()
	refusal := s.refuseOnUnhealthyTranscript(s.attachedTranscript())
	s.announceFailClosed()
	return refusal
}

// refuseOnUnhealthyTranscript is refuseBeforeClaimingOnUnhealthyTranscript for a
// writer the caller already sampled: it reports why a durable claim must not be
// taken when the writer will not accept the turn's records -- poisoned, or
// closed, whose ordinary appends are silent no-ops. It reads only the writer's
// own lock, so a claim inside the mutation-store serializer can decide the
// refusal without waiting on Session.mu: the serializer never waits on s.mu, and
// a Session.mu holder that then reaches the serializer would deadlock. Nil-safe,
// like the writer's own doors -- a session with no state directory has no writer
// and so nothing to refuse.
//
// Both facts are decided here rather than only at the announcement, because the
// claim commits durable state -- the active turn, the queue entry, the budget
// slot -- before that. A closed writer refused only at the announcement would be
// claimed, handed back and woken on every wake, committing and fsyncing twice
// each time.
//
// The read is as tight as the lock order allows, but it is not atomic with the
// claim's commit: the writer's lock and the store's are different resources, and
// holding the writer's across the commit would invert their order. A poisoning
// that lands between this read and the commit is therefore still possible; what
// the claim guarantees is that the refusal and the claim decision see one
// generation, and both claim callers give the claim back when the turn loop then
// refuses it.
//
// A session that failed closed refuses first, with its own reason. Deciding to
// fail closed happens elsewhere (failClosedOnUnhealthyTranscript, the write
// door): this read is lock-free, so it stays safe inside the serializer.
func (s *Session) refuseOnUnhealthyTranscript(writer *transcript.Writer) error {
	if refusal := s.failedClosedRefusal(); refusal != nil {
		return refusal
	}
	if writer.Poisoned() {
		return errTranscriptRefusesRecords()
	}
	if writer.Closed() {
		return errTranscriptClosed()
	}
	return nil
}

// errTranscriptRefusesRecords is the single answer both poisoned-transcript
// guards give, so a caller sees one error whichever guard produced it.
func errTranscriptRefusesRecords() error {
	return fmt.Errorf("session transcript stopped accepting records: %w", transcript.ErrWriterPoisoned)
}

// transcriptRefusal is the single decision every site that a transcript can
// refuse makes: it maps the writer's own refusal -- poisoned or closed, from a
// door that returned one or from a sampled writer -- to the session's answer, and
// returns nil for anything else, including a store failure.
//
// One function, deliberately. This change has needed it three times: a claim
// boundary that refused a closed writer without the matching gate, two post-run
// give-backs that knew only poison, and now the user-input append. Each instance
// was the same class, and each was found only after the previous one was fixed,
// because the sites asked the question in their own words. There is one answer
// now: ask this.
func transcriptRefusal(cause error) error {
	if cause == nil {
		return nil
	}
	if errors.Is(cause, ErrTranscriptFailedClosed) {
		return cause
	}
	if errors.Is(cause, transcript.ErrWriterPoisoned) {
		return errTranscriptRefusesRecords()
	}
	if errors.Is(cause, transcript.ErrWriterClosed) {
		return errTranscriptClosed()
	}
	return nil
}

// errWhileHealthyRefuses is transcriptRefusal under the name the writer door's
// callers use, kept because the door hands back the writer's own sentinel.
func errWhileHealthyRefuses(cause error) error {
	return transcriptRefusal(cause)
}

// errTranscriptClosed is the closed-writer answer: the writer will not record
// the turn, but nothing was poisoned, so it is not reported as if it had been.
func errTranscriptClosed() error {
	return fmt.Errorf("session transcript is closed: %w", transcript.ErrWriterClosed)
}

// transcriptRefusedClaim reports whether a claim's error is the transcript's own
// refusal rather than a store failure. It is transcriptRefusal as a predicate, so
// a site that only branches cannot ask a different question than a site that
// reports: a claim site that classified only the poisoned one would take a closed
// writer's refusal for a write failure, stand down quietly, and leave the claim
// to be retried on every wake.
func transcriptRefusedClaim(err error) bool {
	return transcriptRefusal(err) != nil
}

// transcriptRefusalForWrite is transcriptRefusal for a write that has just run.
// The door's error is the writer's refusal when the writer was already refusing,
// but when THIS write is what broke the writer the error carries the cause (a
// partial line), not the sentinel -- a partial append poisons the writer rather
// than returning ErrWriterPoisoned. The post-write state is therefore part of the
// same decision, asked the same way: refusing. It is stable by then, because the
// failed write is what set it, under the writer's own lock.
func (s *Session) transcriptRefusalForWrite(writeErr error) error {
	if refusal := transcriptRefusal(writeErr); refusal != nil {
		return refusal
	}
	if writeErr == nil {
		return nil
	}
	return s.refuseOnUnhealthyTranscript(s.attachedTranscript())
}
