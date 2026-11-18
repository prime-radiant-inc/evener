package agent

import (
	"context"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/internal/testwait"
)

// The drain waits for its Notification hook's completion callback, and that
// callback records a transcript turn. Recording takes the transcript door
// (attentionMu) across its write, and that is deliberate: the door is an ordering
// guard a fold publication snapshots against, so narrowing the section would let a
// fold re-append a turn whose write had not landed. What must not happen is the
// drain waiting for that door on the close budget.
//
// So the acquisition is bounded and the section is not. When another holder keeps
// the door past the drain's deadline, the completion records nothing, announces
// that it recorded nothing on the drain's own bound, and returns. A close that
// loses a completion record says so; a silent skip would be the dropped thing
// nobody hears about, which is the defect this change exists to fix.
func TestCloseDrainHookCompletionDoesNotWaitForTheTranscriptDoor(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	s.events = make(chan events.SessionEvent, 64)
	s.authoritativeConsumer = true
	t.Cleanup(func() { s.authoritativeConsumer = false })

	// Another holder keeps the transcript door until cleanup releases it, so an
	// unbounded acquisition cannot outlast it within this test.
	holding := make(chan struct{})
	release := make(chan struct{})
	go func() {
		s.attentionMu.Lock()
		close(holding)
		<-release
		s.attentionMu.Unlock()
	}()
	t.Cleanup(func() { close(release) })
	<-holding

	var returned atomic.Bool

	// The drain's bound is already spent: the deadline the close gave its stop
	// half is what the completion must honour.
	bound, cancelBound := context.WithCancel(t.Context())
	cancelBound()

	go func() {
		s.publishHookLifecycle(withPublicationBound(bound), events.EventHookEnd, events.HookEndData{
			Event:    "Notification",
			HookType: "command",
			Matcher:  "*",
		})
		returned.Store(true)
	}()

	// It must return rather than park on the door. The holder does not release
	// until cleanup, so a bounded acquisition is the only way this is observed;
	// an unbounded one fails here at the test's own deadline instead of hanging.
	testwait.Until(t, "the drain's hook completion to release on its bound", returned.Load)

	// It must have said so: the announcement is a diagnostic on the drain's own
	// bound, so it reached the stream, and it dispatched no Notification hook.
	sawAnnouncement := false
	for {
		select {
		case event := <-s.events:
			if event.Kind == events.EventWarning {
				sawAnnouncement = true
			}
			continue
		default:
		}
		break
	}
	if !sawAnnouncement {
		t.Fatal("the completion was dropped without an announcement: a close that loses a completion record must say so on its own bound")
	}
}
