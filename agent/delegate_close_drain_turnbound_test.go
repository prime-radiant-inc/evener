package agent

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent/events"
)

// Disposal is not the only budget a bound can be spent from, and the close drain
// is not the only dispatcher a hook runs under. An ordinary hook -- a tool's
// PreToolUse, a turn's Stop -- runs on the turn's context, and that context is
// cancelled the moment the turn is interrupted. Publishing the hook's lifecycle
// events on that context drops the live completion of a hook that genuinely
// finished, because a cancelled context is one of the arms a blocked send
// selects on. The session stays open and the completion was written to the
// transcript, so a reloaded reader then sees a hook exit no live reader ever
// saw: history contradicting the stream, in the one direction nobody looks for.
//
// Both halves are asserted at the decision the policy makes, not at the send it
// causes. A send's outcome is a select among ready cases, so a test watching the
// send would agree with a wrong policy some of the time; the route the policy
// chose cannot drift without this failing every time.
func TestOrdinaryHookCompletionSurvivesTurnCancellation(t *testing.T) {
	completion := events.HookEndData{
		Event:      "PreToolUse",
		HookType:   "command",
		Matcher:    "*",
		PluginName: "bash",
		ExitCode:   0,
	}

	t.Run("an ordinary hook's dispatch takes the unbounded path", func(t *testing.T) {
		s := newTestSessionForEnvctx(t)
		s.events = make(chan events.SessionEvent, 4)
		s.authoritativeConsumer = true
		t.Cleanup(func() { s.authoritativeConsumer = false })

		var routes []bool
		s.testOnlyPublicationRoute = func(bounded bool) { routes = append(routes, bounded) }
		t.Cleanup(func() { s.testOnlyPublicationRoute = nil })

		// The context an ordinary hook dispatches on is its turn's, and a turn
		// interrupted mid-flight is still just a turn: it supplied no bound.
		interrupted, cancel := context.WithCancel(t.Context())
		cancel()
		s.publishHookLifecycle(interrupted, events.EventHookEnd, completion)

		if len(routes) != 1 {
			t.Fatalf("policy reported %d routes, want exactly 1 publication", len(routes))
		}
		if routes[0] {
			t.Fatal("an ordinary hook's completion took the bounded path: the cancelled turn's context bounded a publication it did not own, dropping the hook exit from the live stream")
		}
		// And the completion really is on the stream.
		select {
		case event := <-s.events:
			if event.Kind != events.EventHookEnd {
				t.Fatalf("stream carried %v, want the hook's completion", event.Kind)
			}
		default:
			t.Fatal("an ordinary hook's completion never reached the stream")
		}
	})

	t.Run("the close drain's dispatch takes the bounded path", func(t *testing.T) {
		s := newTestSessionForEnvctx(t)
		s.events = make(chan events.SessionEvent, 4)

		var routes []bool
		s.testOnlyPublicationRoute = func(bounded bool) { routes = append(routes, bounded) }
		t.Cleanup(func() { s.testOnlyPublicationRoute = nil })

		stopCtx, cancelStop := context.WithCancel(t.Context())
		cancelStop()
		s.publishHookLifecycle(withPublicationBound(stopCtx), events.EventHookEnd, completion)

		if len(routes) != 1 {
			t.Fatalf("policy reported %d routes, want exactly 1 publication", len(routes))
		}
		if !routes[0] {
			t.Fatal("the close drain's hook completion took the unbounded path: an unresponsive consumer would hold the teardown budget, since the drain waits for this publication")
		}
	})
}
