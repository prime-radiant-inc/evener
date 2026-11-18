package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/hooks"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/internal/testwait"
)

// A retried delivery acknowledgment that succeeded still wakes its receiver, and
// that wake can fail. The failure is a warning, and Reconcile publishes it from
// its deferred unlock -- before the drain ever reaches its bounded warning
// publisher. On the session's own delivery path that warning waits for the full
// close budget, so a stalled authoritative stream spends the time reserved for
// removing collectible worktrees.
//
// The wake is best-effort news about an acknowledgment that already committed, so
// it must be published on the bound the drain supplied: a responsive consumer
// still receives it, and an unresponsive one releases the drain.
func TestDelegateWakeWarningUsesTheDrainBound(t *testing.T) {
	// A stalled authoritative stream: the send can only be delivered to a
	// consumer that resumes, or released by a bound.
	newStalledRoot := func(t *testing.T) *Session {
		s := newTestSessionForEnvctx(t)
		s.events = make(chan events.SessionEvent, 1)
		s.events <- events.New(events.HookStartData{Event: "occupies the only slot"})
		s.authoritativeConsumer = true
		t.Cleanup(func() { s.authoritativeConsumer = false })
		return s
	}

	t.Run("a spent drain bound releases the wake warning", func(t *testing.T) {
		s := newStalledRoot(t)
		controller := s.delegateController
		if controller == nil {
			t.Fatal("session has no delegate controller: this test needs one to publish through")
		}

		// A bound the drain supplied, already spent. MARKED, because that mark is
		// how a publication tells "a caller handed me a stall budget" from "a
		// context merely exists" -- the drain marks it at its one assignment site
		// (delegate_tree_stop.go's evidence.bound). It is passed to the wake
		// rather than installed on the shared controller, so it cannot reach an
		// unrelated delegate's publications.
		spentCtx, cancelBound := context.WithCancel(t.Context())
		cancelBound()
		bound := withPublicationBound(spentCtx)

		var parked atomic.Bool
		s.testOnlyBlockedSendEntered = func() { parked.Store(true) }
		t.Cleanup(func() { s.testOnlyBlockedSendEntered = nil })

		done := make(chan struct{})
		go func() {
			defer close(done)
			controller.reportDelegateWakeError(bound, errTestWakeFailed)
		}()

		// It must return on the drain's bound rather than park on the stream.
		testwait.Until(t, "the wake warning to release on the drain's bound", func() bool {
			select {
			case <-done:
				return true
			default:
				return false
			}
		})
		if !parked.Load() {
			t.Fatal("the wake warning never reached the blocked-send boundary: the send was not the thing under test")
		}
	})
}

// An ordinary delivery supplies NO publication bound, so its wake-failure
// warning must publish on the session's ordinary path -- not on a bound, because
// there is none to keep.
//
// The defect this pins (review 53025, low): delegate_delivery.go passed
// context.Background() to the wake, and reportDelegateWakeError treated every
// non-nil context as a bound. The ordinary path therefore took the BOUNDED
// branch with a context that carries no deadline, and its Notification hook
// marked that context as its publication bound. A hook overlapping close then
// published on a lifetime close cancels, and the cancellation dropped its
// lifecycle and user output off a full buffer even when the consumer resumed
// inside the published close window.
//
// The observation is the route the hook's lifecycle took, which is the policy
// decision. The send's own outcome is a select among ready cases and would agree
// with a wrong policy part of the time.
func TestOrdinaryDeliverySuppliesNoPublicationBound(t *testing.T) {
	r := newWorktreeRepo(t)
	root := r.s

	runner := hooks.NewRunner(nil, "test-model")
	runner.Add(plugin.HookNotification, plugin.RegisteredHook{
		Matcher: "*",
		Type:    "command",
		Command: `true`,
		Timeout: 30,
	})
	runner.SetEventCallbackContext(root.publishHookLifecycle)
	root.hookRunner = runner
	root.events = make(chan events.SessionEvent, 8)
	root.authoritativeConsumer = true
	t.Cleanup(func() { root.authoritativeConsumer = false })

	var sawAny, sawBounded atomic.Bool
	root.testOnlyPublicationRoute = func(bounded bool) {
		sawAny.Store(true)
		if bounded {
			sawBounded.Store(true)
		}
	}
	t.Cleanup(func() { root.testOnlyPublicationRoute = nil })

	// The ordinary delivery path's own context, which is what
	// delegate_delivery.go hands the wake. It carries no deadline and no mark.
	root.delegateController.reportDelegateWakeError(context.Background(), errTestWakeFailed)

	if !sawAny.Load() {
		t.Fatal("the wake failure published no hook lifecycle: the warning was dropped")
	}
	if sawBounded.Load() {
		t.Fatal("the ordinary wake failure published its hook on a BOUND: it supplied none, " +
			"so a hook overlapping close would lose its lifecycle and output to the canceled session")
	}
}

// errTestWakeFailed is the failure a wake reports in this test. It is a plain
// error rather than a message assertion target: the test checks the sink and the
// bound, never the wording.
var errTestWakeFailed = errors.New("wake refused")
