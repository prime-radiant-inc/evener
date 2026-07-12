package pending

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/appwire"
)

var fuzzCoverageUnion = func(*testing.T) {}

type fuzzTimer struct {
	fn      func()
	stopped bool
}

func (t *fuzzTimer) Stop() bool {
	wasActive := !t.stopped
	t.stopped = true
	return wasActive
}

type fuzzClock struct{ timers []*fuzzTimer }

func (c *fuzzClock) AfterFunc(d time.Duration, fn func()) PendingTimer {
	t := &fuzzTimer{fn: fn}
	c.timers = append(c.timers, t)
	return t
}

func receivePending(t *testing.T, messages <-chan tea.Msg) tea.Msg {
	t.Helper()
	select {
	case msg := <-messages:
		return msg
	case <-time.After(time.Second):
		t.Fatal("pending dispatcher did not deliver a message")
		return nil
	}
}

func FuzzPendingCoordinator(f *testing.F) {
	f.Add("turn/send", " hello  world ", " thread ", false)
	f.Add(appwire.MethodTurnDrainAsSteer, "ignored", "", true)
	f.Fuzz(func(t *testing.T, method, text, ref string, timeout bool) {
		fuzzCoverageUnion(t)
		clock := &fuzzClock{}
		messages := make(chan tea.Msg, 8)
		coord := NewPendingCoordinator(clock, nil)
		coord.SetSend(func(msg tea.Msg) { messages <- msg })
		handle := coord.Register(method, text, ref).(*PendingHandleImpl)
		if _, ok := receivePending(t, messages).(PendingRegisteredMsg); !ok {
			t.Fatal("first message was not registration")
		}
		if len(clock.timers) != 1 {
			t.Fatalf("timer count = %d, want 1", len(clock.timers))
		}

		if timeout {
			clock.timers[0].fn()
			if _, ok := receivePending(t, messages).(PendingFailedMsg); !ok {
				t.Fatal("timeout did not emit failure")
			}
			if !coord.TryReconcile(method, text, ref) {
				t.Fatal("failed entry did not reconcile")
			}
			if _, ok := receivePending(t, messages).(PendingConfirmedMsg); !ok {
				t.Fatal("reconciliation did not emit confirmation")
			}
			handle.Fail("late")
			return
		}

		if coord.TryReconcile(method+"-other", text, ref) {
			t.Fatal("mismatched method reconciled")
		}
		handle.Fail("rejected")
		if _, ok := receivePending(t, messages).(PendingFailedMsg); !ok {
			t.Fatal("handle failure did not emit failure")
		}
		handle.Fail("duplicate")
		if !coord.TryReconcile(method, text, ref) {
			t.Fatal("failed entry did not reconcile")
		}
		if _, ok := receivePending(t, messages).(PendingConfirmedMsg); !ok {
			t.Fatal("reconciliation did not emit confirmation")
		}
	})
}

func TestPendingReferenceMatchingAndRealClock(t *testing.T) {
	thread := "thread-1"
	qualified := appwire.Ref{SourceID: "source", ThreadID: thread}.String()
	if !pendingRefsMatch(qualified, thread) || !pendingRefsMatch(thread, qualified) {
		t.Fatal("qualified and thread-only references should match")
	}
	if pendingRefsMatch("one", "two") {
		t.Fatal("distinct references matched")
	}
	if timer := (RealClock{}).AfterFunc(time.Hour, func() {}); !timer.Stop() {
		t.Fatal("new real timer was already stopped")
	}
}

func TestPendingReconcileOrdering(t *testing.T) {
	for _, method := range []string{"turn/send", appwire.MethodTurnDrainAsSteer} {
		clock := &fuzzClock{}
		messages := make(chan tea.Msg, 16)
		coord := NewPendingCoordinator(clock, func(msg tea.Msg) { messages <- msg })
		coord.Register(method, "same text", "ref")
		coord.Register(method, "same text", "ref")
		receivePending(t, messages)
		receivePending(t, messages)
		if !coord.TryReconcile(method, "same text", "ref") {
			t.Fatal("first entry did not reconcile")
		}
		receivePending(t, messages)
		clock.timers[1].fn()
		receivePending(t, messages)
		if !coord.TryReconcile(method, "same text", "ref") {
			t.Fatal("failed second entry did not reconcile")
		}
		receivePending(t, messages)
	}
	clock := &fuzzClock{}
	coord := NewPendingCoordinator(clock, nil)
	coord.Register("other", "text", "ref")
	if coord.TryReconcile(appwire.MethodTurnDrainAsSteer, "", "ref") {
		t.Fatal("drain reconciliation matched a different method")
	}
}
