package main

import (
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// fakeClock implements pendingClock for deterministic timeout tests.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	pending []*fakeTimer
}

type fakeTimer struct {
	c    *fakeClock
	fire time.Time
	fn   func()
	done bool
}

func (f *fakeTimer) Stop() bool {
	f.c.mu.Lock()
	defer f.c.mu.Unlock()
	if f.done {
		return false
	}
	f.done = true
	return true
}

func (c *fakeClock) AfterFunc(d time.Duration, fn func()) pendingTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{c: c, fire: c.now.Add(d), fn: fn}
	c.pending = append(c.pending, t)
	return t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	fires := []*fakeTimer{}
	for _, t := range c.pending {
		if !t.done && !t.fire.After(c.now) {
			t.done = true
			fires = append(fires, t)
		}
	}
	c.mu.Unlock()
	for _, t := range fires {
		t.fn()
	}
}

func drainMessages(ch <-chan tea.Msg, n int, timeout time.Duration) []tea.Msg {
	out := []tea.Msg{}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for len(out) < n {
		select {
		case m := <-ch:
			out = append(out, m)
		case <-deadline.C:
			return out
		}
	}
	return out
}

func TestPendingCoordinator_RegisterEmitsRegisteredMsg(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	msgs := make(chan tea.Msg, 8)
	p := newPendingCoordinator(clock, func(m tea.Msg) { msgs <- m })

	h := p.Register("turn/steer", "look at this")
	if h == nil {
		t.Fatal("Register returned nil")
	}

	got := drainMessages(msgs, 1, 100*time.Millisecond)
	if len(got) != 1 {
		t.Fatalf("expected 1 registered msg, got %d", len(got))
	}
	reg, ok := got[0].(pendingRegisteredMsg)
	if !ok {
		t.Fatalf("got %T, want pendingRegisteredMsg", got[0])
	}
	if reg.entry.Method != "turn/steer" || reg.entry.Text != "look at this" {
		t.Fatalf("entry: %+v", reg.entry)
	}
	if !reg.entry.Pending {
		t.Fatal("new entry should be Pending=true")
	}
}

func TestPendingCoordinator_FailEmitsFailedMsg(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	msgs := make(chan tea.Msg, 8)
	p := newPendingCoordinator(clock, func(m tea.Msg) { msgs <- m })
	h := p.Register("turn/steer", "x")
	drainMessages(msgs, 1, 100*time.Millisecond) // consume Registered

	h.Fail("steer is not available for this session")

	got := drainMessages(msgs, 1, 100*time.Millisecond)
	if len(got) != 1 {
		t.Fatalf("expected 1 failed msg, got %d", len(got))
	}
	fm, ok := got[0].(pendingFailedMsg)
	if !ok {
		t.Fatalf("got %T", got[0])
	}
	if !strings.Contains(fm.reason, "not available") {
		t.Fatalf("reason: %q", fm.reason)
	}
	if !fm.entry.Failed {
		t.Fatal("entry should be Failed=true")
	}
	if fm.entry.Pending {
		t.Fatal("entry should not be Pending after Fail")
	}
}

func TestPendingCoordinator_TimeoutMarksFailed(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	msgs := make(chan tea.Msg, 8)
	p := newPendingCoordinator(clock, func(m tea.Msg) { msgs <- m })
	p.Register("turn/steer", "x")
	drainMessages(msgs, 1, 100*time.Millisecond) // Registered

	clock.Advance(11 * time.Second)

	got := drainMessages(msgs, 1, 100*time.Millisecond)
	if len(got) != 1 {
		t.Fatalf("expected 1 failed msg from timeout, got %d", len(got))
	}
	fm, ok := got[0].(pendingFailedMsg)
	if !ok {
		t.Fatalf("got %T", got[0])
	}
	if !strings.Contains(fm.reason, "server did not confirm") {
		t.Fatalf("reason: %q", fm.reason)
	}
}

func TestPendingCoordinator_TryReconcile_MatchesByMethodAndText(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	msgs := make(chan tea.Msg, 8)
	p := newPendingCoordinator(clock, func(m tea.Msg) { msgs <- m })
	p.Register("turn/steer", "look at this")
	drainMessages(msgs, 1, 100*time.Millisecond)

	if !p.TryReconcile("turn/steer", "look  at  this") {
		t.Fatal("TryReconcile should match (whitespace-normalized)")
	}

	got := drainMessages(msgs, 1, 100*time.Millisecond)
	if len(got) != 1 {
		t.Fatalf("expected 1 confirmed msg, got %d", len(got))
	}
	cm, ok := got[0].(pendingConfirmedMsg)
	if !ok {
		t.Fatalf("got %T", got[0])
	}
	if cm.entry.Pending || cm.entry.Failed {
		t.Fatalf("confirmed entry should not be pending or failed: %+v", cm.entry)
	}
}

func TestPendingCoordinator_TryReconcile_NoMatchReturnsFalse(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	msgs := make(chan tea.Msg, 8)
	p := newPendingCoordinator(clock, func(m tea.Msg) { msgs <- m })
	p.Register("turn/steer", "look at this")
	drainMessages(msgs, 1, 100*time.Millisecond)

	if p.TryReconcile("turn/steer", "completely different text") {
		t.Fatal("TryReconcile should not match unrelated text")
	}
}

func TestPendingCoordinator_FailIsIdempotent(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	msgs := make(chan tea.Msg, 8)
	p := newPendingCoordinator(clock, func(m tea.Msg) { msgs <- m })
	h := p.Register("turn/steer", "x")
	drainMessages(msgs, 1, 100*time.Millisecond)
	h.Fail("a")
	h.Fail("b") // second call must be a no-op
	got := drainMessages(msgs, 2, 100*time.Millisecond)
	if len(got) != 1 {
		t.Fatalf("expected 1 failed msg (idempotent), got %d", len(got))
	}
}
