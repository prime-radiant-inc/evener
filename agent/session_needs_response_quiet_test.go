package agent

import (
	"context"
	"errors"
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

// A session closed during the quiet period never rests awaiting.
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
// acts, so it keeps no needs_response rest from arming, at the settle or when
// the quiet period ends.
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
