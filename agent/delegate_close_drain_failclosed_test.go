package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
)

// A fail-closed announcement made while recording a close-dispatched hook
// completion must honour the publication bound, and must not lose the
// diagnostic when that bound declines the send.
//
// The path: a served session's transcript writer refuses a completion append, so
// recordTranscriptLocked latches a fail-closed refusal; recordTurnOn reports the
// write failure on the bound and then announces the refusal. Announced on the
// ordinary path, that ErrorData send parks on a full authoritative buffer until
// the whole cascade expires, so the diagnostic itself spends the budget worktree
// disposal needs.
//
// The fault is a PARTIAL WRITE, not a Sync failure: the writer must poison, which
// is the branch that latches fail-closed. A Sync failure reaches the rollback and
// retention branches instead and latches nothing.
//
// The call is recordHookCompletionOn -- the recording half of
// emitHookCompletedBounded -- because that is the step that latches and announces.
// Driving the whole emit would put a HOOK_END send in front of it, and that send
// would be the thing parked on the full buffer, which is a different finding.
func TestFailClosedAnnouncementTakesThePublicationBound(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	// Served: failClosed only latches for a daemon-drained session.
	s.authoritativeConsumer = true
	t.Cleanup(func() { s.Close() })

	// ONE slot, and the warning's own send takes it, so the announcement that
	// follows has nowhere to go. Authoritative, so the send PARKS instead of
	// dropping: a drop would let this pass for the wrong reason.
	s.events = make(chan events.SessionEvent, 1)

	// A partial write that poisons the writer and cannot be rolled back.
	attachEnvironmentPoisoningWrite(t, s,
		errors.New("injected partial transcript write"),
		errors.New("injected transcript rollback failure"))

	// A publication cutoff that has ALREADY expired: the production shape when a
	// close has spent its publication half. It is what makes the announcement's
	// send return undelivered instead of waiting on the cascade.
	cutoff, cancelCutoff := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	// Cancelled HERE, not deferred: a deferred cancel would run before the later
	// announcement below, and the deadline is already past.
	cancelCutoff()

	returned := make(chan struct{})
	go func() {
		defer close(returned)
		s.recordHookCompletionOn(cutoff, events.HookEndData{
			Event: "Notification", HookType: "command", Matcher: "*", ExitCode: 0,
		})
	}()

	select {
	case <-returned:
	// TRIPWIRE: an in-process recording, and the bound below is a cancellation
	// this test owns rather than a duration; this ceiling only fires if the
	// announcement parked on the full buffer.
	case <-time.After(10 * time.Second):
		t.Fatal("the bounded fail-closed announcement never returned: it parked on the full " +
			"authoritative buffer instead of taking the publication bound, so a poisoned writer's " +
			"diagnostic spends the budget the worktree removals need")
	}

	// The premise: the partial write really poisoned the writer and really latched
	// a refusal, so the announcement above had something to send.
	if !s.attachedTranscript().Poisoned() {
		t.Fatal("the injected partial write did not poison the transcript writer: a Sync failure " +
			"reaches a different branch, so this test would prove nothing about the fail-closed latch")
	}
	if s.failedClosedRefusal() == nil {
		t.Fatal("the poisoned writer did not latch a fail-closed refusal: the recording did not reach " +
			"the path whose announcement is under test")
	}
	// The cutoff really was expired, so "returned" means the bound released the
	// send rather than the buffer draining.
	if cutoff.Err() == nil {
		t.Fatal("the publication cutoff was live during the test: an expired cutoff is what makes " +
			"the bounded send return undelivered")
	}
	// The send could not be delivered, so the diagnostic is PENDING: a later
	// announcement on a live bound must still report it. Taking the latch before
	// the send is what loses it.
	if s.failedClosed.announced.Load() {
		t.Fatal("the bounded fail-closed announcement recorded itself as announced even though the " +
			"send could not deliver it: the diagnostic is then lost to every later reader")
	}

	// And the other half: on a live path with room, the pending diagnostic is
	// still reported, and only then is it marked announced.
	<-s.events
	s.announceFailClosed()
	if !s.failedClosed.announced.Load() {
		t.Fatal("the pending fail-closed diagnostic was not announced once a slot was free: leaving " +
			"it pending must not mean dropping it")
	}
	if !warningOrErrorOnStream(s) {
		t.Fatal("the announced fail-closed diagnostic never reached the stream")
	}
}

// warningOrErrorOnStream reports whether an error event reached the session's
// channel, which is the premise that the announcement was delivered.
func warningOrErrorOnStream(s *Session) bool {
	for len(s.events) > 0 {
		if ev := <-s.events; ev.Kind == events.EventError {
			return true
		}
	}
	return false
}
