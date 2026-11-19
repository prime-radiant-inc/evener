package agent

import (
	"testing"

	"primeradiant.com/evener/agent/events"
)

// A Notification hook a close drain dispatches must not hold the drain open on
// a lifecycle send that has no bound of its own.
//
// The warning's own send is bounded: the drain publishes it on the stop context,
// so an unresponsive consumer releases it at the cutoff. The hook that warning
// fires is not the same publication. Its HookStart goes out through the runner's
// event callback, and before this change that callback ran on the session's
// ordinary delivery path -- so a hook that started and then blocked inside the
// runner parked on the WHOLE close cascade, spending the budget teardown needs
// even though the warning that caused it was published correctly.
//
// The assertion is the ROUTE, not a duration: the runner publishes the lifecycle
// event with the context it is dispatching under, and the session's policy must
// choose the bounded path exactly when the dispatcher marked a bound. A test that
// measured how long the send took would agree with a wrong policy under host load.
func TestCloseDrainHookLifecycleTakesThePublicationBound(t *testing.T) {
	// The production policy, exercised as production wires it: install the
	// callback the runner would install, then dispatch the way the runner does.
	s := newPublicationRouteSession(t)

	var routes []bool
	ObserveCloseDrainPublicationRoute = func(bounded bool) { routes = append(routes, bounded) }
	defer func() { ObserveCloseDrainPublicationRoute = nil }()

	// A marked dispatch is what fireNotificationHookBounded hands the runner, so
	// this is the context a close-dispatched hook's HookStart actually runs under.
	dispatch := tripwireCtx(t)
	s.publishHookLifecycle(withPublicationBound(dispatch, dispatch), events.EventHookStart,
		events.HookStartData{Event: "Notification", HookType: "command", Matcher: "*"})

	if len(routes) != 1 {
		t.Fatalf("the lifecycle publication recorded %d route decision(s), want 1: "+
			"a hook dispatched on a bound must publish its lifecycle on that bound", len(routes))
	}
	if !routes[0] {
		t.Fatal("the close-dispatched hook's HOOK_START published on the session's ordinary path, " +
			"want the bounded path: an unresponsive consumer then holds the close drain open " +
			"on a send that has no cutoff")
	}
}

// The other half of the same policy, and the reason it is opt-in: an ordinary
// hook runs under its TURN's context, and applying that context to its
// lifecycle events would drop a live completion the moment the turn was
// interrupted -- even though the session stays open and the completion reached
// the transcript. A reloaded reader would then see a hook exit no live reader
// ever saw.
func TestOrdinaryHookLifecycleKeepsTheUnboundedRoute(t *testing.T) {
	s := newPublicationRouteSession(t)

	var routes []bool
	ObserveCloseDrainPublicationRoute = func(bounded bool) { routes = append(routes, bounded) }
	defer func() { ObserveCloseDrainPublicationRoute = nil }()

	// An ordinary dispatch: a live turn context, never marked with a bound.
	s.publishHookLifecycle(tripwireCtx(t), events.EventHookStart,
		events.HookStartData{Event: "Notification", HookType: "command", Matcher: "*"})

	if len(routes) != 1 {
		t.Fatalf("the lifecycle publication recorded %d route decision(s), want 1", len(routes))
	}
	if routes[0] {
		t.Fatal("an ordinary hook's HOOK_START published on a bounded path, want the session's " +
			"ordinary path: a turn's interruption would then drop a completion the session " +
			"recorded to its transcript")
	}
}

// newPublicationRouteSession is a session that can publish: a real event channel
// and no other machinery, since the lifecycle policy is the only thing under test.
//
// It is deliberately NOT an authoritative consumer. An authoritative send parks
// on a full buffer until something releases it, and this test would then measure
// a wait rather than the route chosen; best-effort is also the case the findings
// describe, where a full buffer drops and the hook's fate is the question.
func newPublicationRouteSession(t *testing.T) *Session {
	t.Helper()
	s := &Session{id: "sess_route", events: make(chan events.SessionEvent, 8)}
	t.Cleanup(func() {
		s.eventsMu.Lock()
		if !s.eventsClosed {
			s.eventsClosed = true
			close(s.events)
		}
		s.eventsMu.Unlock()
	})
	return s
}
