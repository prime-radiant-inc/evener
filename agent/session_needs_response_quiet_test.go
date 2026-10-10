package agent

import (
	"context"
	"errors"
	"runtime"
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

// A pending question upgrades only an open, idle session: a settle that
// finds the session still processing, or closing, leaves that state alone.
func TestPendingQuestionUpgradesOnlyAnOpenIdleSession(t *testing.T) {
	t.Parallel()
	sess, _ := newQuietPeriodSession(t, func(llm.Request) llm.Response { return toolCallResponse(askUserCall("ask1", askUserArgsValid())) })
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatal(err)
	}
	sess.mu.Lock()
	sess.state = SessionProcessing
	sess.mu.Unlock()
	sess.armAwaitingAtSettle(true, false)
	if got := sess.State(); got != SessionProcessing {
		t.Fatalf("state = %q, want processing left alone", got)
	}
	// A retiring session is closing while its state still reads idle, so
	// only the closing check keeps the settle from upgrading it.
	sess.mu.Lock()
	sess.state = SessionIdle
	sess.closing = true
	sess.mu.Unlock()
	sess.armAwaitingAtSettle(true, false)
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state = %q while closing, want idle left alone", got)
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
	// The second turn start reports its goroutine as it reaches restMu, so
	// the wait below reads that goroutine's own stack, not another session's.
	// The timer is parked in its hook, so nothing reads the config now.
	turnGoroutine := make(chan string, 1)
	sess.cfg.testOnly.turnStartBeforeRestMu = func() { turnGoroutine <- currentGoroutineHeader() }
	turnDone := make(chan error, 1)
	go func() {
		_, err := sess.ProcessInput(ctx, "next", nil)
		turnDone <- err
	}()
	// The turn must park on restMu while the announcement is pending: once it
	// has reached the lock, restMu is the next mutex it can wait on.
	var header string
	select {
	case header = <-turnGoroutine:
	case err := <-turnDone:
		t.Fatalf("turn finished (err %v) before reaching restMu", err)
	case <-ctx.Done():
		t.Fatal("the turn never reached its start")
	}
	for !goroutineWaitsOnMutex(header) {
		select {
		case err := <-turnDone:
			t.Fatalf("turn finished (err %v) while the rest's announcement was pending", err)
		case <-ctx.Done():
			t.Fatal("the turn never parked on restMu")
		case <-time.After(10 * time.Millisecond): // TRIPWIRE: poll interval only; ctx bounds the wait.
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

// currentGoroutineHeader is the calling goroutine's "goroutine N [" prefix,
// which names it in a full goroutine dump.
func currentGoroutineHeader() string {
	buf := make([]byte, 64)
	buf = buf[:runtime.Stack(buf, false)]
	header, _, _ := strings.Cut(string(buf), "[")
	return header + "["
}

// goroutineWaitsOnMutex reports whether the goroutine whose dump header is
// header is blocked taking a sync.Mutex.
func goroutineWaitsOnMutex(header string) bool {
	for stack := range strings.SplitSeq(goroutineDump(), "\n\n") {
		if strings.HasPrefix(stack, header) {
			// Lock is inlined, so a blocked wait shows as lockSlow.
			return strings.Contains(stack, "sync.(*Mutex).lockSlow")
		}
	}
	return false
}

// The quiet-period timer holds restMu across its STATUS_SETTLED emit, and a
// watch fired from that emit could start a turn, which takes restMu: the
// event must never be watchable.
func TestStatusSettledIsNotAWatchableEventKind(t *testing.T) {
	t.Parallel()
	for name, kind := range modelEventKinds {
		if kind == events.EventStatusSettled {
			t.Fatalf("modelEventKinds[%q] is EventStatusSettled; the needs_response timer emits it holding restMu", name)
		}
	}
}

// A notification wake the session filters out (nothing deliverable) runs no
// turn, so it leaves a needs_response rest where it was: still awaiting, as
// the server's stored state and restore say.
func TestFilteredWakeKeepsANeedsResponseRest(t *testing.T) {
	t.Parallel()
	sess := newSession(t, withImmediateRest(), withSteps(func(llm.Request) llm.Response { return endReasonResponse("which?", "needs_response") }))
	evs, mu, done := collectEvents(sess)
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatal(err)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state after the needs_response turn = %q, want awaiting", got)
	}
	if _, err := sess.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
		t.Fatalf("filtered wake: %v", err)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state after a filtered wake = %q, want awaiting kept", got)
	}
	if got := sess.WireState(); got != string(SessionAwaiting) {
		t.Fatalf("wire state after a filtered wake = %q, want awaiting, as the server keeps it", got)
	}
	sess.Close()
	<-done
	mu.Lock()
	defer mu.Unlock()
	// The needs_response turn ended its own input once; the filtered wake
	// ended none and announced nothing (immediate rest emits no settle).
	inputEnds := 0
	for _, ev := range *evs {
		if ev.Kind == events.EventStatusSettled {
			t.Fatalf("a filtered wake announced a state change: %+v", ev)
		}
		if d, ok := ev.Data.(events.SessionEndData); ok && ev.Kind == events.EventSessionEnd && d.Reason != "session_closed" {
			inputEnds++
		}
	}
	if inputEnds != 1 {
		t.Fatalf("input-ending SESSION_ENDs = %d, want only the needs_response turn's", inputEnds)
	}
}

// A filtered wake over an idle session leaves it idle.
func TestFilteredWakeKeepsAnIdleRest(t *testing.T) {
	t.Parallel()
	sess := newSession(t, withImmediateRest(), withSteps(func(llm.Request) llm.Response { return endReasonResponse("done", "done") }))
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
		t.Fatalf("filtered wake: %v", err)
	}
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state after a filtered wake = %q, want idle", got)
	}
}

// A filtered wake inside the quiet period runs no turn, so the rest it
// interrupted still stands: the session waits a fresh quiet period from the
// wake, then rests awaiting and announces it.
func TestFilteredWakeInsideTheQuietPeriodStillRestsAwaiting(t *testing.T) {
	t.Parallel()
	sess, fake := newQuietPeriodSession(t, func(llm.Request) llm.Response { return endReasonResponse("which?", "needs_response") })
	evs, mu, done := collectEvents(sess)
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatal(err)
	}
	fake.Advance(needsResponseQuietPeriodDefault / 2)
	if _, err := sess.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
		t.Fatalf("filtered wake: %v", err)
	}
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state right after the filtered wake = %q, want idle", got)
	}
	fake.Advance(needsResponseQuietPeriodDefault - time.Millisecond)
	fake.Drain()
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state just before the fresh quiet period ends = %q, want idle", got)
	}
	fake.Advance(time.Millisecond)
	fake.Drain()
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state once the fresh quiet period ends = %q, want awaiting", got)
	}
	if got := settledStatesAfterClose(sess, evs, mu, done); len(got) != 1 || got[0] != string(SessionAwaiting) {
		t.Fatalf("status settled events = %v, want one awaiting", got)
	}
}

// A real turn inside the quiet period a filtered wake re-armed still cancels
// it: only a wake that runs no turn keeps the rest.
func TestTurnAfterAFilteredWakeInsideTheQuietPeriodNeverArms(t *testing.T) {
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
	if _, err := sess.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
		t.Fatalf("filtered wake: %v", err)
	}
	fake.Advance(needsResponseQuietPeriodDefault / 2)
	if _, err := sess.ProcessInput(ctx, "blue", nil); err != nil {
		t.Fatal(err)
	}
	fake.Advance(2 * needsResponseQuietPeriodDefault)
	fake.Drain()
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state = %q, want idle: the turn moved past the question", got)
	}
	if got := settledStatesAfterClose(sess, evs, mu, done); len(got) != 0 {
		t.Fatalf("status settled events = %v, want none", got)
	}
}

// A filtered wake after the quiet period has rested the session awaiting
// keeps it there and announces nothing more.
func TestFilteredWakeAfterTheQuietPeriodStaysAwaiting(t *testing.T) {
	t.Parallel()
	sess, fake := newQuietPeriodSession(t, func(llm.Request) llm.Response { return endReasonResponse("which?", "needs_response") })
	evs, mu, done := collectEvents(sess)
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatal(err)
	}
	fake.Advance(needsResponseQuietPeriodDefault)
	fake.Drain()
	if _, err := sess.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
		t.Fatalf("filtered wake: %v", err)
	}
	fake.Advance(2 * needsResponseQuietPeriodDefault)
	fake.Drain()
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state after a filtered wake = %q, want awaiting kept", got)
	}
	if got := settledStatesAfterClose(sess, evs, mu, done); len(got) != 1 || got[0] != string(SessionAwaiting) {
		t.Fatalf("status settled events = %v, want only the first rest's awaiting", got)
	}
}

// A filtered wake on a session that has never scheduled a quiet period
// re-arms nothing: there was no rest to interrupt.
func TestFilteredWakeOnAFreshSessionStaysIdle(t *testing.T) {
	t.Parallel()
	sess, fake := newQuietPeriodSession(t)
	evs, mu, done := collectEvents(sess)
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
		t.Fatalf("filtered wake: %v", err)
	}
	fake.Advance(2 * needsResponseQuietPeriodDefault)
	fake.Drain()
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state after a filtered wake = %q, want idle", got)
	}
	if got := settledStatesAfterClose(sess, evs, mu, done); len(got) != 0 {
		t.Fatalf("status settled events = %v, want none", got)
	}
}

// A stale notification queued during the needs_response turn runs inline in
// the same input and is filtered out: it runs no turn, so the input still
// ends on needs_response and rests awaiting after the quiet period.
func TestFilteredWakeInsideTheNeedsResponseInputStillRestsAwaiting(t *testing.T) {
	t.Parallel()
	var sess *Session
	sess, fake := newQuietPeriodSession(t, func(llm.Request) llm.Response {
		sess.enqueueJobNotification(jobNotification{WatchSend: &watchSendToken{ChildSessionID: "gone"}})
		return endReasonResponse("which?", "needs_response")
	})
	evs, mu, done := collectEvents(sess)
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatal(err)
	}
	fake.Advance(needsResponseQuietPeriodDefault)
	fake.Drain()
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state once the quiet period ends = %q, want awaiting", got)
	}
	if got := settledStatesAfterClose(sess, evs, mu, done); len(got) != 1 || got[0] != string(SessionAwaiting) {
		t.Fatalf("status settled events = %v, want one awaiting", got)
	}
}

// A real turn inside the quiet period that ends the wait, then a filtered
// wake, leaves the session idle: the wake has no rest of its own to resume.
func TestFilteredWakeAfterATurnThatMovedOnStaysIdle(t *testing.T) {
	t.Parallel()
	sess, fake := newQuietPeriodSession(t,
		func(llm.Request) llm.Response { return endReasonResponse("which?", "needs_response") },
		func(llm.Request) llm.Response { return endReasonResponse("got it", "done") },
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
	if _, err := sess.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
		t.Fatalf("filtered wake: %v", err)
	}
	fake.Advance(2 * needsResponseQuietPeriodDefault)
	fake.Drain()
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state = %q, want idle: the second turn moved past the question", got)
	}
	if got := settledStatesAfterClose(sess, evs, mu, done); len(got) != 0 {
		t.Fatalf("status settled events = %v, want none", got)
	}
}

// The rest a filtered wake re-arms still waits on work in flight: a wake
// whose notification could not be recorded puts it back in the queue and
// runs no turn, so the session stays idle past the fresh quiet period.
func TestFilteredWakeThatRequeuesWorkInsideTheQuietPeriodStaysIdle(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fake := agenttest.NewFakeClock()
	sess := newSession(t, withDir(dir), withClock(fake), withSteps(func(llm.Request) llm.Response { return endReasonResponse("which?", "needs_response") }))
	evs, mu, done := collectEvents(sess)
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatal(err)
	}
	fake.Advance(needsResponseQuietPeriodDefault / 2)
	jm, err := newJobManager(dir, sess.ID(), sess.enqueueJobNotification)
	if err != nil {
		t.Fatalf("newJobManager: %v", err)
	}
	sess.jobManager = jm
	appendPendingJobNotificationRecord(t, jm, sess.ID())
	sess.enqueueJobNotification(jobNotification{JobID: "job_X", JobType: "shell", Status: "completed", OutputBytes: 42})
	appendFails := context.WithValue(ctx, sessionLifecycleFaultsKey{}, map[string]error{"append_notification": errors.New("append failed")})
	if _, err := sess.ProcessInputKind(appendFails, "", nil, EntryNotification); err != nil {
		t.Fatalf("refused wake: %v", err)
	}
	if sess.peekNotifications() == 0 {
		t.Fatal("the refused wake's notification was not requeued")
	}
	fake.Advance(2 * needsResponseQuietPeriodDefault)
	fake.Drain()
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state = %q, want idle while the requeued notification waits", got)
	}
	if got := settledStatesAfterClose(sess, evs, mu, done); len(got) != 0 {
		t.Fatalf("status settled events = %v, want none", got)
	}
}

// A needs_response turn that ended while a child was still working rests idle,
// but still owes its human partner a rest: once the child is done, a filtered
// wake that finds nothing in flight waits a fresh quiet period, then rests
// awaiting and announces it.
func TestFilteredWakeAfterWorkInFlightEndsRestsAwaiting(t *testing.T) {
	t.Parallel()
	sess, fake := newQuietPeriodSession(t, func(llm.Request) llm.Response { return endReasonResponse("which?", "needs_response") })
	child := newTestSessionForState(t)
	working := &subagent{id: child.ID(), sess: child, running: true}
	sess.subagents.track(working)
	evs, mu, done := collectEvents(sess)
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatal(err)
	}
	fake.Advance(2 * needsResponseQuietPeriodDefault)
	fake.Drain()
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state while the child works = %q, want idle", got)
	}
	// A wake while the child still works keeps the rest owed, not armed.
	if _, err := sess.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
		t.Fatalf("filtered wake: %v", err)
	}
	fake.Advance(2 * needsResponseQuietPeriodDefault)
	fake.Drain()
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state after a wake while the child works = %q, want idle", got)
	}
	working.mu.Lock()
	working.running = false
	working.mu.Unlock()
	if _, err := sess.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
		t.Fatalf("filtered wake: %v", err)
	}
	fake.Advance(needsResponseQuietPeriodDefault - time.Millisecond)
	fake.Drain()
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state just before the fresh quiet period ends = %q, want idle", got)
	}
	fake.Advance(time.Millisecond)
	fake.Drain()
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state once the fresh quiet period ends = %q, want awaiting", got)
	}
	if got := settledStatesAfterClose(sess, evs, mu, done); len(got) != 1 || got[0] != string(SessionAwaiting) {
		t.Fatalf("status settled events = %v, want one awaiting", got)
	}
}
