package agent

import (
	"context"
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
	sess := newSession(t, withSteps(steps...), withConfig(SessionConfig{clock: fake}))
	return sess, fake
}

// statusSettledStates lists the states the session announced through
// EventStatusSettled.
func statusSettledStates(evs []events.SessionEvent) []string {
	var states []string
	for _, ev := range evs {
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
	// TRIPWIRE: the timer fired during the advance; this only bounds a hang.
	waitForCondition(t, 15*time.Second, "awaiting once the quiet period ends", func() bool { return sess.State() == SessionAwaiting })
	sess.Close()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if got := statusSettledStates(*evs); len(got) != 1 || got[0] != string(SessionAwaiting) {
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
	sess.Close()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if got := statusSettledStates(*evs); len(got) != 0 {
		t.Fatalf("status settled events = %v, want none", got)
	}
}

// A pending question needs no quiet period: nothing can move the session
// but its human partner's answer, so it rests awaiting at once.
func TestAskRestsAwaitingWithoutAQuietPeriod(t *testing.T) {
	t.Parallel()
	sess, _ := newQuietPeriodSession(t, func(llm.Request) llm.Response { return toolCallResponse(askUserCall("ask1", askUserArgsValid())) })
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatal(err)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state = %q, want awaiting at once for a pending question", got)
	}
}
