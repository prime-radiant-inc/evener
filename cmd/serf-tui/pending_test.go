package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/internal/appwire"
	"primeradiant.com/serf/internal/appwire/appwiretest"
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

func TestPendingCoordinator_TryReconcile_DrainSpecialIgnoresText(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	msgs := make(chan tea.Msg, 8)
	p := newPendingCoordinator(clock, func(m tea.Msg) { msgs <- m })
	p.Register("turn/drainAsSteer", "")
	drainMessages(msgs, 1, 100*time.Millisecond) // Registered

	// Drain matches the first in-flight entry regardless of text.
	if !p.TryReconcile("turn/drainAsSteer", "anything joined here") {
		t.Fatal("drain reconcile should match first pending entry regardless of text")
	}
	got := drainMessages(msgs, 1, 100*time.Millisecond)
	if len(got) != 1 {
		t.Fatalf("expected 1 confirmed msg, got %d", len(got))
	}
	if _, ok := got[0].(pendingConfirmedMsg); !ok {
		t.Fatalf("got %T, want pendingConfirmedMsg", got[0])
	}
}

func TestPendingCoordinator_TryReconcile_MatchesOldestDuplicate(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	msgs := make(chan tea.Msg, 8)
	p := newPendingCoordinator(clock, func(m tea.Msg) { msgs <- m })
	first := p.Register("turn/steer", "same text").(*pendingHandleImpl).id
	second := p.Register("turn/steer", "same text").(*pendingHandleImpl).id
	drainMessages(msgs, 2, 100*time.Millisecond)

	if !p.TryReconcile("turn/steer", "same text") {
		t.Fatal("TryReconcile should match duplicate text")
	}
	got := drainMessages(msgs, 1, 100*time.Millisecond)
	if len(got) != 1 {
		t.Fatalf("expected 1 confirmed msg, got %d", len(got))
	}
	cm, ok := got[0].(pendingConfirmedMsg)
	if !ok {
		t.Fatalf("got %T, want pendingConfirmedMsg", got[0])
	}
	if cm.entry.ID != first {
		t.Fatalf("confirmed ID=%d, want oldest %d; newer was %d", cm.entry.ID, first, second)
	}
}

func TestPendingCoordinator_TryReconcile_DrainMatchesOldest(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	msgs := make(chan tea.Msg, 8)
	p := newPendingCoordinator(clock, func(m tea.Msg) { msgs <- m })
	first := p.Register("turn/drainAsSteer", "").(*pendingHandleImpl).id
	second := p.Register("turn/drainAsSteer", "").(*pendingHandleImpl).id
	drainMessages(msgs, 2, 100*time.Millisecond)

	if !p.TryReconcile("turn/drainAsSteer", "joined text") {
		t.Fatal("TryReconcile should match duplicate drains")
	}
	got := drainMessages(msgs, 1, 100*time.Millisecond)
	if len(got) != 1 {
		t.Fatalf("expected 1 confirmed msg, got %d", len(got))
	}
	cm, ok := got[0].(pendingConfirmedMsg)
	if !ok {
		t.Fatalf("got %T, want pendingConfirmedMsg", got[0])
	}
	if cm.entry.ID != first {
		t.Fatalf("confirmed ID=%d, want oldest %d; newer was %d", cm.entry.ID, first, second)
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

func TestReconcilePendingFromNotification_ImageOnlyUserMessage(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	msgs := make(chan tea.Msg, 8)
	p := newPendingCoordinator(clock, func(m tea.Msg) { msgs <- m })
	p.Register(appwire.MethodTurnStart, "")
	drainMessages(msgs, 1, 100*time.Millisecond)

	reconcilePendingFromNotification(p, *appwire.NotificationMessage(appwire.NotifyItemStarted, map[string]any{
		"item": appwire.ThreadItem{
			Type: "user_message",
			Images: []appwire.InputItem{{
				Type:      "image",
				MediaType: "image/png",
			}},
		},
	}).Notification)

	got := drainMessages(msgs, 1, 100*time.Millisecond)
	if len(got) != 1 {
		t.Fatalf("expected 1 confirmed msg, got %d", len(got))
	}
	if _, ok := got[0].(pendingConfirmedMsg); !ok {
		t.Fatalf("got %T, want pendingConfirmedMsg", got[0])
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

func TestPendingCoordinator_DispatchDoesNotDropBeyondFormerOutboxCap(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	msgs := make(chan tea.Msg, 128)
	p := newPendingCoordinator(clock, func(m tea.Msg) { msgs <- m })

	const n = 40
	handles := make([]appwire.PendingHandle, 0, n)
	for i := 0; i < n; i++ {
		handles = append(handles, p.Register("turn/queue", "queued"))
	}
	got := drainMessages(msgs, n, time.Second)
	if len(got) != n {
		t.Fatalf("registered msgs: got %d want %d", len(got), n)
	}
	for _, h := range handles {
		h.Fail("nope")
	}
	got = drainMessages(msgs, n, time.Second)
	if len(got) != n {
		t.Fatalf("failed msgs: got %d want %d", len(got), n)
	}
	for i, msg := range got {
		if _, ok := msg.(pendingFailedMsg); !ok {
			t.Fatalf("msg %d = %T, want pendingFailedMsg", i, msg)
		}
	}
}

func TestHubReducer_RendersPendingChatMessage(t *testing.T) {
	r := newHubTranscriptReducer(nil, nil, nil)

	r.appendPendingSteering("look at this")

	if len(r.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(r.messages))
	}
	got := r.messages[0]
	if got.Kind != msgSteering {
		t.Fatalf("kind = %v, want msgSteering", got.Kind)
	}
	if !got.Pending {
		t.Fatal("Pending should be true")
	}
	if got.Text != "look at this" {
		t.Fatalf("text = %q", got.Text)
	}
}

func TestHubReducer_MarksFailed(t *testing.T) {
	r := newHubTranscriptReducer(nil, nil, nil)
	r.appendPendingSteering("look at this")
	r.markPendingFailed(r.messages[0].PendingID, "boom")

	got := r.messages[0]
	if got.Pending {
		t.Fatal("Pending should be false after fail")
	}
	if !got.Failed {
		t.Fatal("Failed should be true")
	}
	if got.Reason != "boom" {
		t.Fatalf("Reason = %q", got.Reason)
	}
}

func TestHubReducer_RemovesPendingOnConfirm(t *testing.T) {
	r := newHubTranscriptReducer(nil, nil, nil)
	r.appendPendingSteering("look at this")
	id := r.messages[0].PendingID
	r.removePending(id)
	if len(r.messages) != 0 {
		t.Fatal("confirmed entries should be removed; authoritative one renders separately")
	}
}

func TestHubModel_SteerFailsFastOnRPCUnavailable(t *testing.T) {
	t.Parallel()
	transport := appwiretest.NewScriptedTransport()
	client := appwire.NewClient(transport)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client.Start(ctx)

	msgs := make(chan tea.Msg, 16)
	pending := newPendingCoordinator(realClock{}, func(msg tea.Msg) { msgs <- msg })
	client.SetPendingCoordinator(pending)

	go func() {
		req := <-transport.Sent()
		transport.DeliverError(req.Request.ID, appwire.CodeUnavailable, "steer is not available for this session")
	}()

	if err := client.TurnSteer(ctx, appwire.TurnSteerParams{
		Ref:  appwire.Ref{SourceID: "local", ThreadID: "t1"}.String(),
		Text: "go check this",
	}); err == nil {
		t.Fatal("expected error from TurnSteer")
	}

	got := drainMessages(msgs, 2, time.Second)
	if len(got) != 2 {
		t.Fatalf("expected 2 msgs (Registered+Failed), got %d", len(got))
	}
	if _, ok := got[0].(pendingRegisteredMsg); !ok {
		t.Fatalf("first msg = %T", got[0])
	}
	fm, ok := got[1].(pendingFailedMsg)
	if !ok {
		t.Fatalf("second msg = %T", got[1])
	}
	if !strings.Contains(fm.reason, "not available") {
		t.Fatalf("reason: %q", fm.reason)
	}
}

// TestPendingCoordinator_DispatchIsAsync_NoDeadlock guards the
// deadlock that ate hours during live e2e verification: when the
// coordinator's send func is called synchronously from inside the
// bubbletea event loop (e.g. from TryReconcile inside Update), an
// unbuffered program.Send blocks until the loop dequeues — but the
// loop can't dequeue while Update is still running. The coordinator
// dispatches msgs in a goroutine to break the cycle. This test wires
// a blocking-forever send and asserts TryReconcile still returns.
func TestPendingCoordinator_DispatchIsAsync_NoDeadlock(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{now: time.Unix(0, 0)}
	blockForever := make(chan tea.Msg) // unbuffered, never read
	p := newPendingCoordinator(clock, func(m tea.Msg) { blockForever <- m })

	// Register synchronously must not block on send (we always dispatch).
	done := make(chan bool, 1)
	go func() {
		p.Register("turn/drainAsSteer", "")
		done <- true
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Register blocked — send is not being dispatched asynchronously")
	}

	// TryReconcile must also not block on send.
	go func() {
		ok := p.TryReconcile("turn/drainAsSteer", "")
		if !ok {
			t.Errorf("TryReconcile should have matched")
		}
		done <- true
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("TryReconcile blocked — send is not being dispatched asynchronously")
	}
}
