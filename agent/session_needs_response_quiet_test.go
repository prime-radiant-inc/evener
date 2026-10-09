package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/llm"
)

// newQuietPeriodSession is a session on a fake clock, so the needs_response
// quiet period passes only when the test advances it.
func newQuietPeriodSession(t *testing.T, steps ...func(llm.Request) llm.Response) (*Session, *agenttest.FakeClock) {
	t.Helper()
	fake := agenttest.NewFakeClock()
	return newSession(t, withSteps(steps...), withClock(fake)), fake
}

// settledStatesAfterClose closes sess, waits for its event stream to end, and
// lists the states it announced through EventStatusSettled.
func settledStatesAfterClose(sess *Session, evs *[]events.SessionEvent, mu *sync.Mutex, done <-chan struct{}) []string {
	sess.Close()
	<-done
	mu.Lock()
	defer mu.Unlock()
	var states []string
	for _, ev := range *evs {
		if data, ok := ev.Data.(events.StatusSettledData); ok && ev.Kind == events.EventStatusSettled {
			states = append(states, data.State)
		}
	}
	return states
}

// A turn that ended on needs_response rests idle through the quiet period,
// then rests awaiting and announces it, so a client hears the change.
func TestNeedsResponseRestsAwaitingAfterTheQuietPeriod(t *testing.T) {
	t.Parallel()
	sess, fake := newQuietPeriodSession(t, func(llm.Request) llm.Response { return endReasonResponse("which?", "needs_response") })
	evs, mu, done := collectEvents(sess)
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatal(err)
	}
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state right after the turn = %q, want idle during the quiet period", got)
	}
	fake.Advance(needsResponseQuietPeriodDefault - time.Millisecond)
	fake.Drain()
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state just before the quiet period ends = %q, want idle", got)
	}
	fake.Advance(time.Millisecond)
	// Drain waits for the timer's callback to finish, announcement included.
	fake.Drain()
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state once the quiet period ends = %q, want awaiting", got)
	}
	if got := settledStatesAfterClose(sess, evs, mu, done); len(got) != 1 || got[0] != string(SessionAwaiting) {
		t.Fatalf("status settled events = %v, want one awaiting", got)
	}
}

// A new input inside the quiet period cancels the pending rest: the session
// never shows awaiting for the turn it moved past.
func TestNeedsResponseRestartInsideTheQuietPeriodNeverArms(t *testing.T) {
	t.Parallel()
	sess, fake := newQuietPeriodSession(t,
		func(llm.Request) llm.Response { return endReasonResponse("which?", "needs_response") },
		func(llm.Request) llm.Response { return endReasonResponse("got it", "") },
	)
	evs, mu, done := collectEvents(sess)
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatal(err)
	}
	fake.Advance(needsResponseQuietPeriodDefault / 2)
	if _, err := sess.ProcessInput(ctx, "blue", nil); err != nil {
		t.Fatal(err)
	}
	fake.Advance(needsResponseQuietPeriodDefault)
	fake.Drain()
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state = %q, want idle: the restart moved past the question", got)
	}
	if got := settledStatesAfterClose(sess, evs, mu, done); len(got) != 0 {
		t.Fatalf("status settled events = %v, want none", got)
	}
}

// A pending question needs no quiet period: nothing can move the session
// but its human partner's answer, so it rests awaiting at once.
func TestAskRestsAwaitingWithoutAQuietPeriod(t *testing.T) {
	t.Parallel()
	sess, _ := newQuietPeriodSession(t, func(llm.Request) llm.Response { return toolCallResponse(askUserCall("ask1", askUserArgsValid())) })
	evs, mu, done := collectEvents(sess)
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatal(err)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state = %q, want awaiting at once for a pending question", got)
	}
	if got := settledStatesAfterClose(sess, evs, mu, done); len(got) != 0 {
		t.Fatalf("status settled events = %v, want none: SESSION_END carries a question's rest", got)
	}
}

// A turn stopped inside the quiet period ends that question's wait: the
// earlier turn's timer must not rest the session awaiting afterwards.
func TestNeedsResponseStoppedTurnInsideTheQuietPeriodNeverArms(t *testing.T) {
	t.Parallel()
	sess, fake := newQuietPeriodSession(t,
		func(llm.Request) llm.Response { return endReasonResponse("which?", "needs_response") },
		func(llm.Request) llm.Response { return llm.Response{} },
	)
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatal(err)
	}
	fake.Advance(time.Second)
	// The second turn is cancelled before it settles, as a Stop would.
	stopped, stop := context.WithCancel(ctx)
	stop()
	if _, err := sess.ProcessInput(stopped, "blue", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("stopped turn err = %v, want context.Canceled", err)
	}
	fake.Advance(needsResponseQuietPeriodDefault)
	fake.Drain()
	if got := sess.State(); got == SessionAwaiting {
		t.Fatalf("state = %q, want not awaiting: the stopped turn moved past the question", got)
	}
}

// Work that arrives during the quiet period moves the session on its own, so
// the rest stays idle when the timer fires.
func TestNeedsResponseWorkArrivingInsideTheQuietPeriodNeverArms(t *testing.T) {
	t.Parallel()
	sess, fake := newQuietPeriodSession(t, func(llm.Request) llm.Response { return endReasonResponse("which?", "needs_response") })
	evs, mu, done := collectEvents(sess)
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatal(err)
	}
	sess.enqueueJobNotification(jobNotification{JobID: "job_late"})
	fake.Advance(needsResponseQuietPeriodDefault)
	fake.Drain()
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state = %q, want idle with a notification pending", got)
	}
	if got := settledStatesAfterClose(sess, evs, mu, done); len(got) != 0 {
		t.Fatalf("status settled events = %v, want none", got)
	}
}

// A session closed during the quiet period never rests awaiting. The closed
// state alone fails the timer's idle check, so this pins that a timer
// outliving its session fires harmlessly, not which check stops it.
func TestNeedsResponseCloseInsideTheQuietPeriodNeverArms(t *testing.T) {
	t.Parallel()
	sess, fake := newQuietPeriodSession(t, func(llm.Request) llm.Response { return endReasonResponse("which?", "needs_response") })
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatal(err)
	}
	sess.Close()
	fake.Advance(needsResponseQuietPeriodDefault)
	fake.Drain()
	if got := sess.State(); got == SessionAwaiting {
		t.Fatalf("state = %q after close, want not awaiting", got)
	}
}

// A pending question rests awaiting at the settle even with input queued
// behind it: queued input can't answer the question, and the drain ladder
// holds it until a reply does.
func TestPendingQuestionRestsAwaitingWithInputQueued(t *testing.T) {
	t.Parallel()
	sess, _ := newQuietPeriodSession(t, func(llm.Request) llm.Response { return toolCallResponse(askUserCall("ask1", askUserArgsValid())) })
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatal(err)
	}
	// Settle again from idle with a message queued, as a human-note carrier
	// round that left the question pending would.
	sess.mu.Lock()
	sess.state = SessionIdle
	sess.mu.Unlock()
	if err := sess.Enqueue(ctx, "later"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	sess.armAwaitingAtSettle(true, false)
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state = %q, want awaiting while the question is pending", got)
	}
}

// User steering a Stop parked is not work: nothing runs it until the person
// acts, so steering parked during the quiet period doesn't stop the rest
// from arming.
func TestNeedsResponseRestArmsBesideParkedSteering(t *testing.T) {
	t.Parallel()
	sess, fake := newQuietPeriodSession(t, func(llm.Request) llm.Response { return endReasonResponse("which?", "needs_response") })
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatal(err)
	}
	if err := sess.ensureClientMutationStore(); err != nil {
		t.Fatal(err)
	}
	if err := sess.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error { snapshot.SteeringHeld = true; return nil }); err != nil {
		t.Fatal(err)
	}
	sess.mu.Lock()
	sess.steeringQueue = append(sess.steeringQueue, steeringMessage{Text: "parked", Source: events.SteeringSourceUser})
	sess.mu.Unlock()
	fake.Advance(needsResponseQuietPeriodDefault)
	fake.Drain()
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state = %q, want awaiting: parked steering is not work", got)
	}
}

// Runnable user steering that arrives during the quiet period will start the
// next turn, so the rest stays idle and announces nothing.
func TestNeedsResponseSteeringArrivingInsideTheQuietPeriodNeverArms(t *testing.T) {
	t.Parallel()
	sess, fake := newQuietPeriodSession(t, func(llm.Request) llm.Response { return endReasonResponse("which?", "needs_response") })
	evs, mu, done := collectEvents(sess)
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatal(err)
	}
	sess.mu.Lock()
	sess.steeringQueue = append(sess.steeringQueue, steeringMessage{Text: "also this", Source: events.SteeringSourceUser})
	sess.mu.Unlock()
	fake.Advance(needsResponseQuietPeriodDefault)
	fake.Drain()
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state = %q, want idle with steering waiting to run", got)
	}
	if got := settledStatesAfterClose(sess, evs, mu, done); len(got) != 0 {
		t.Fatalf("status settled events = %v, want none", got)
	}
}

// A turn that starts while a rest is arming waits for the rest's
// announcement, so STATUS_SETTLED reaches the feed before the turn's own
// EXECUTION_STARTED: a client never hears awaiting after the turn began.
func TestNeedsResponseRestAnnouncesBeforeATurnThatStartsWhileItArms(t *testing.T) {
	t.Parallel()
	sess, fake := newQuietPeriodSession(t,
		func(llm.Request) llm.Response { return endReasonResponse("which?", "needs_response") },
		func(llm.Request) llm.Response { return endReasonResponse("done", "done") },
	)
	armed := make(chan struct{})
	release := make(chan struct{})
	sess.cfg.testOnly.needsResponseRestBeforeAnnounce = func() {
		close(armed)
		<-release
	}
	evs, mu, done := collectEvents(sess)
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatal(err)
	}
	go fake.Advance(needsResponseQuietPeriodDefault)
	select {
	case <-armed:
	case <-ctx.Done():
		t.Fatal("the rest never armed")
	}
	turnDone := make(chan error, 1)
	go func() {
		_, err := sess.ProcessInput(ctx, "next", nil)
		turnDone <- err
	}()
	// The turn must park on restMu while the announcement is pending.
	for !turnParkedOnRestMu() {
		select {
		case err := <-turnDone:
			t.Fatalf("turn finished (err %v) while the rest's announcement was pending", err)
		case <-ctx.Done():
			t.Fatal("the turn never reached its start")
		case <-time.After(time.Millisecond): // TRIPWIRE: poll interval only; ctx bounds the wait.
		}
	}
	close(release)
	if err := <-turnDone; err != nil {
		t.Fatal(err)
	}
	sess.Close()
	<-done
	mu.Lock()
	defer mu.Unlock()
	settled, secondStart, starts := -1, -1, 0
	for i, ev := range *evs {
		switch ev.Kind {
		case events.EventStatusSettled:
			settled = i
		case events.EventExecutionStarted:
			starts++
			if starts == 2 {
				secondStart = i
			}
		}
	}
	if settled < 0 || secondStart < 0 || settled > secondStart {
		kinds := make([]events.EventKind, 0, len(*evs))
		for _, ev := range *evs {
			kinds = append(kinds, ev.Kind)
		}
		t.Fatalf("STATUS_SETTLED at %d, second EXECUTION_STARTED at %d: want the announcement first; events %v", settled, secondStart, kinds)
	}
}

// turnParkedOnRestMu reports whether some goroutine is waiting for a mutex
// inside processOneInput: the turn start blocked on restMu.
func turnParkedOnRestMu() bool {
	for _, stack := range strings.Split(goroutineDump(), "\n\n") {
		if strings.Contains(stack, "sync.(*Mutex).Lock") && strings.Contains(stack, ").processOneInput(") {
			return true
		}
	}
	return false
}
