package agent

import (
	"context"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/diagnostic"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/hooks"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/internal/testwait"
)

// A publication bound is a deadline a caller hands in because it has a stall
// budget to protect. An ORDINARY Notification hook has no such caller: its
// dispatch context merely tracks the session.
//
// The defect this pins: an ordinary hook starting before close captures the
// session lifetime as its output context. If it produces a user message and
// Close then cancels that lifetime before the message enters a full buffer, the
// captured context drops the message immediately -- even though the consumer
// frees a slot inside the close window the event is entitled to. The previous
// shape of this test cancelled a separate dispatch context and left the captured
// lifetime active, so it never reached the transition it claimed to cover.
//
// The transition IS the test: the hook captures the session lifetime, the buffer
// fills, a close budget is published, and the lifetime is cancelled while the
// send is parked. The message must still be delivered on the published close
// window.
func TestOrdinaryNotificationHookOutputOutlivesTheCanceledSessionLifetime(t *testing.T) {
	r := newWorktreeRepo(t)
	root := r.s

	// The lifetime the hook captures at start, and cancels mid-flight.
	lifetime, cancelLifetime := context.WithCancel(t.Context())
	root.sessionCtx = lifetime
	t.Cleanup(cancelLifetime)

	runner := hooks.NewRunner(nil, "test-model")
	runner.Add(plugin.HookNotification, plugin.RegisteredHook{
		Matcher: "*",
		Type:    "command",
		Command: `printf '%s\n' '{"systemMessage":"hook output"}'`,
		Timeout: 30,
	})
	// No lifecycle publication: the output send must be the FIRST send to park,
	// or the helper observes a lifecycle park and the output then travels
	// through a buffer the consumer has already freed.
	runner.SetEventCallbackContext(func(_ context.Context, _ events.EventKind, _ events.EventData) {})
	root.hookRunner = runner

	// A full authoritative stream.
	root.events = make(chan events.SessionEvent, 1)
	root.events <- events.New(events.HookStartData{Event: "occupies the only slot"})
	root.authoritativeConsumer = true
	t.Cleanup(func() { root.authoritativeConsumer = false })

	var parked bool
	var parkedMu sync.Mutex
	root.testOnlyBlockedSendEntered = func() {
		parkedMu.Lock()
		parked = true
		parkedMu.Unlock()
	}
	t.Cleanup(func() { root.testOnlyBlockedSendEntered = nil })

	done := make(chan struct{})
	go func() {
		defer close(done)
		root.runNotificationHook(lifetime, "the warning's message", "the warning's title")
	}()

	// The output send parks. An unmarked dispatch has no caller bound, so it is
	// waiting on the session's publication path rather than on a captured
	// context.
	testwait.Until(t, "the hook's output publication to park on the full stream", func() bool {
		parkedMu.Lock()
		defer parkedMu.Unlock()
		return parked
	})

	// The transition: Close publishes its budget, then cancels the lifetime.
	closeBudget, cancelClose := context.WithCancel(t.Context())
	root.closeCtxMu.Lock()
	root.closeCtx = closeBudget
	root.closeCtxMu.Unlock()
	t.Cleanup(func() {
		root.closeCtxMu.Lock()
		root.closeCtx = nil
		root.closeCtxMu.Unlock()
		cancelClose()
	})
	cancelLifetime()

	// The consumer resumes inside the close window. The message is entitled to
	// it: the lifetime's cancellation is not a delivery deadline.
	delivered := make(chan struct{})
	var once sync.Once
	stopConsumer := make(chan struct{})
	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		for {
			select {
			case <-stopConsumer:
				return
			case ev := <-root.events:
				if w, ok := ev.Data.(events.WarningData); ok && w.Source == string(diagnostic.SourceHook) {
					once.Do(func() { close(delivered) })
				}
			}
		}
	}()
	t.Cleanup(func() {
		close(stopConsumer)
		<-consumerDone
	})

	testwait.Until(t, "the hook's user message to reach the resumed consumer", func() bool {
		select {
		case <-delivered:
			return true
		default:
			return false
		}
	})
	<-done
}

// The other half: a dispatcher that DID supply a bound keeps it. The drain marks
// its dispatch context, and the hook's output publication must still narrow to
// that deadline so an unresponsive consumer cannot park the drain.
func TestBoundedNotificationHookOutputStillHonoursItsBound(t *testing.T) {
	r := newWorktreeRepo(t)
	root := r.s

	runner := hooks.NewRunner(nil, "test-model")
	runner.Add(plugin.HookNotification, plugin.RegisteredHook{
		Matcher: "*",
		Type:    "command",
		Command: `printf '%s\n' '{"systemMessage":"hook output"}'`,
		Timeout: 30,
	})
	runner.SetEventCallbackContext(root.publishHookLifecycle)
	root.hookRunner = runner

	root.events = make(chan events.SessionEvent, 1)
	root.events <- events.New(events.HookStartData{Event: "occupies the only slot"})
	root.authoritativeConsumer = true
	t.Cleanup(func() { root.authoritativeConsumer = false })

	// A slurp that never frees a slot: only the bound can release the send.
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	go func() {
		<-hold
	}()

	bound, cancelBound := context.WithCancel(t.Context())
	cancelBound()

	// Marked as a publication bound, so the output send narrows to it and
	// returns instead of parking until the close deadline.
	done := make(chan struct{})
	go func() {
		defer close(done)
		root.runNotificationHook(withPublicationBound(bound), "the warning's message", "the warning's title")
	}()
	<-done
}
