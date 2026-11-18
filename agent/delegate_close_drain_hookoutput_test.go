package agent

import (
	"context"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/hooks"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/internal/testwait"
)

// A publication bound is a deadline a caller hands in because it has a stall
// budget to protect. An ORDINARY Notification hook has no such caller: its
// dispatch context tracks the session, so when Session.Close cancels that context
// it is not speaking about delivery at all.
//
// The defect: runNotificationHook reused its dispatch context as the output
// publication bound. A hook finishing as Close canceled the session therefore
// dropped its user message the instant the stream was full -- even though the
// consumer frees a slot inside the close window the event is entitled to. The
// message is gone, and nothing reports it.
//
// The discriminator is a send that must WAIT, with the dispatch context already
// canceled: the ordinary path delivers once the consumer resumes, while a send
// narrowed to the canceled dispatch context gives up immediately.
func TestOrdinaryNotificationHookOutputOutlivesACanceledDispatchContext(t *testing.T) {
	r := newWorktreeRepo(t)
	root := r.s

	runner := hooks.NewRunner(nil, "test-model")
	runner.Add(plugin.HookNotification, plugin.RegisteredHook{
		Matcher: "*",
		Type:    "command",
		Command: `printf '%s\n' '{"systemMessage":"hook output"}'`,
		Timeout: 30,
	})
	// The dispatch context. It is canceled at the hook's completion, which is
	// when Session.Close cancels the session on a hook already running: the hook
	// has produced its output by then, and what remains is publishing it. The
	// context is NOT marked as a publication bound -- no caller supplied a
	// deadline here.
	//
	// The callback deliberately publishes NO lifecycle events. The output
	// publication is the one send under test, and it must be the FIRST send to
	// reach the blocked-send boundary: with lifecycle sends on the stream the
	// hook's own start event occupies the slot, the helper observes that park
	// instead, and the output then travels through a buffer the consumer has
	// already freed -- which made an earlier version of this test pass with the
	// defect intact.
	dispatch, cancelDispatch := context.WithCancel(t.Context())
	t.Cleanup(cancelDispatch)
	runner.SetEventCallbackContext(func(_ context.Context, kind events.EventKind, _ events.EventData) {
		if kind == events.EventHookEnd {
			cancelDispatch()
		}
	})
	root.hookRunner = runner

	// A full authoritative stream: the hook's output send parks until a slot
	// frees.
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
		root.runNotificationHook(dispatch, "the warning's message", "the warning's title")
	}()

	// The output send must reach the blocked-send boundary, or the canceled
	// context never has to bite and the test proves nothing.
	testwait.Until(t, "the hook's output publication to park on the full stream", func() bool {
		parkedMu.Lock()
		defer parkedMu.Unlock()
		return parked
	})

	// The consumer resumes inside the window the event is entitled to.
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
				if w, ok := ev.Data.(events.WarningData); ok && w.Message == "hook output" {
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
