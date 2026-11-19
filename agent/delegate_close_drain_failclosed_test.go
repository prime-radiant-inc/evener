package agent

import (
	"context"
	"errors"
	"sync"
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

// A fail-closed diagnostic a close-dispatched hook completion latched while the
// stream was full must still reach a consumer that resumes during the close.
//
// announceFailClosedOn leaves the diagnostic PENDING when its bounded send cannot
// deliver, and the latch exists so a later announcement on a live bound reports
// it. Nothing made that later announcement during a close: after the worktree
// removals the close retried its retained warnings and went straight to
// teardown, so the one diagnostic a session that can no longer record its
// history owes its operator was lost even though the cascade still had time.
func TestCloseAnnouncesAPendingFailClosedDiagnosticBeforeClosingTheStream(t *testing.T) {
	// Not parallel: shortenCloseCascadeBudget pushes the package-wide budget.
	shortenCloseCascadeBudget(t, closeDrainCascadeBudget)

	s := newQueuePersistTestSession(t, t.TempDir())
	// ONE slot, so a single filler event is the whole buffer. Replaced before the
	// drain is registered, so the drain is the only reader of this channel.
	s.events = make(chan events.SessionEvent, 1)

	consumer := newGatedFailClosedConsumer()
	// Served: failClosed only latches for a daemon-drained session, and a send
	// only parks for an authoritative consumer.
	s.ConsumeEventsLossless(consumer.record, func() { close(consumer.drained) })

	// Put the consumer behind and fill the only slot: it takes one filler and
	// parks, and the second filler the test sends behind it is the full buffer the
	// diagnostic will find.
	consumer.fillAndHold(t, s)

	// A partial write that poisons the writer and cannot be rolled back, which is
	// the branch that latches the fail-closed refusal.
	attachEnvironmentPoisoningWrite(t, s,
		errors.New("injected partial transcript write"),
		errors.New("injected transcript rollback failure"))

	// The completion records on the cascade's publication cutoff. The cutoff is
	// expired here, exactly as it is when a hook overran the publication half of
	// the cascade, so neither the completion's warning nor the fail-closed
	// diagnostic it latches can ride it onto the full stream.
	cutoff, cancelCutoff := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	cancelCutoff()
	s.recordHookCompletionOn(cutoff, events.HookEndData{
		Event: "Notification", HookType: "command", Matcher: "*", ExitCode: 0,
	})

	// The premise: the recording poisoned the writer and latched a refusal, and
	// the diagnostic is still pending because the cutoff could not carry it.
	if !s.attachedTranscript().Poisoned() {
		t.Fatal("the injected partial write did not poison the transcript writer: this test would prove " +
			"nothing about the fail-closed latch")
	}
	if s.failedClosedRefusal() == nil {
		t.Fatal("the poisoned writer did not latch a fail-closed refusal: the recording did not reach " +
			"the path whose announcement is under test")
	}
	if s.failedClosed.announced.Load() {
		t.Fatal("the bounded announcement recorded itself as announced even though the cutoff could not " +
			"deliver it: the diagnostic is then lost to every later reader")
	}

	// The consumer resumes, which is what a healthy daemon does while the close is
	// still spending its cascade.
	consumer.resume()

	// The close must deliver the pending diagnostic on what the removals did not
	// need, before it closes the stream.
	s.Close()
	<-consumer.drained

	if got := consumer.failClosedDiagnostics(); got != 1 {
		t.Fatalf("the close delivered %d typed fail-closed diagnostic(s), want exactly 1: a diagnostic "+
			"left pending by a full stream must be retried on what the worktree removals did not need, "+
			"and a retry that ran twice would announce one refusal as two", got)
	}
	if !s.failedClosed.announced.Load() {
		t.Fatal("the diagnostic reached the stream but the session never marked it announced: the latch " +
			"must record delivery, or a later pass would announce one refusal again")
	}
}

// gatedFailClosedConsumer is a served session's authoritative consumer that
// stops after taking one event, so a test can fill the buffer behind it
// deterministically, and that counts the typed fail-closed diagnostics it
// records.
type gatedFailClosedConsumer struct {
	drained chan struct{}
	gate    chan struct{}
	held    chan struct{}
	started sync.Once
	resumed sync.Once

	mu    sync.Mutex
	diags int
}

func newGatedFailClosedConsumer() *gatedFailClosedConsumer {
	return &gatedFailClosedConsumer{
		drained: make(chan struct{}),
		gate:    make(chan struct{}),
		held:    make(chan struct{}),
	}
}

// record is the consumer callback. It accepts the first event, then parks until
// resumed: from then on the buffer behind it stays full until the test lets it
// drain, which is the shape a healthy-but-behind consumer has.
func (c *gatedFailClosedConsumer) record(ev events.SessionEvent) {
	c.started.Do(func() {
		close(c.held)
		<-c.gate
	})
	if ev.Kind != events.EventError {
		return
	}
	data, ok := ev.Data.(events.ErrorData)
	if !ok || data.Cause == nil || data.Cause.Kind != failClosedCause {
		return
	}
	c.mu.Lock()
	c.diags++
	c.mu.Unlock()
}

// fillAndHold sends the filler the consumer accepts and parks on, then fills the
// session's only slot. It returns with the consumer parked and the buffer full.
func (c *gatedFailClosedConsumer) fillAndHold(t *testing.T, s *Session) {
	t.Helper()
	s.events <- events.New(events.HookStartData{Event: "the consumer takes this one"})
	// Await the park rather than the buffer: once the consumer is parked it has
	// already taken the filler, so the slot this test fills next is the only one.
	<-c.held
	s.events <- events.New(events.HookStartData{Event: "occupies the only slot"})
}

func (c *gatedFailClosedConsumer) resume() { c.resumed.Do(func() { close(c.gate) }) }

func (c *gatedFailClosedConsumer) failClosedDiagnostics() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.diags
}
