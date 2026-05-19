package main

import (
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/internal/appwire"
)

const pendingTimeout = 10 * time.Second

// pendingClock abstracts time.AfterFunc so tests can drive timeouts
// deterministically via fakeClock.
type pendingClock interface {
	AfterFunc(d time.Duration, fn func()) pendingTimer
}

type pendingTimer interface {
	Stop() bool
}

type realClock struct{}

func (realClock) AfterFunc(d time.Duration, fn func()) pendingTimer {
	return time.AfterFunc(d, fn)
}

// pendingEntry is the unit of state the coordinator tracks. ID is
// stable per Register call so reducers / view code can address an
// entry across re-renders.
type pendingEntry struct {
	ID      int64
	Method  string
	Text    string
	Pending bool
	Failed  bool
	Reason  string
}

// pendingRegisteredMsg / pendingConfirmedMsg / pendingFailedMsg are
// the tea.Msg types the coordinator emits via the send func. The
// bubbletea model handles them by updating the reducer.
type pendingRegisteredMsg struct{ entry pendingEntry }
type pendingConfirmedMsg struct{ entry pendingEntry }
type pendingFailedMsg struct {
	entry  pendingEntry
	reason string
}

type pendingCoordinator struct {
	mu      sync.Mutex
	clock   pendingClock
	send    func(tea.Msg)
	nextID  int64
	entries map[int64]*pendingEntryState
	// outbox serialises msg dispatch so Register/Confirm/Fail
	// emissions arrive at the bubbletea program in the order they
	// were produced, even though we never call send synchronously
	// from the caller (which would deadlock the event loop when
	// reconcile is invoked from inside Update).
	outbox     chan tea.Msg
	dispatcher sync.Once
}

type pendingEntryState struct {
	entry pendingEntry
	timer pendingTimer
}

func newPendingCoordinator(clock pendingClock, send func(tea.Msg)) *pendingCoordinator {
	p := &pendingCoordinator{
		clock:   clock,
		send:    send,
		entries: map[int64]*pendingEntryState{},
		outbox:  make(chan tea.Msg, 32),
	}
	p.dispatcher.Do(func() { go p.runDispatcher() })
	return p
}

// runDispatcher drains the outbox into the registered send func.
// One goroutine, serial delivery, never blocks the caller. The current
// send func is snapshotted under the lock on each iteration so
// setSend takes effect for subsequent emissions.
func (p *pendingCoordinator) runDispatcher() {
	for msg := range p.outbox {
		p.mu.Lock()
		sendFn := p.send
		p.mu.Unlock()
		if sendFn != nil {
			sendFn(msg)
		}
	}
}

// setSend installs the bubbletea program's Send function. Tests wire
// a buffered channel; production wires program.Send after NewProgram.
// Safe to call before or after Register; the new send replaces the
// old one for subsequent emissions.
func (p *pendingCoordinator) setSend(fn func(tea.Msg)) {
	p.mu.Lock()
	p.send = fn
	p.mu.Unlock()
}

type pendingHandleImpl struct {
	coord *pendingCoordinator
	id    int64
}

func (h *pendingHandleImpl) Fail(reason string) {
	h.coord.failByID(h.id, reason)
}

// Register satisfies appwire.PendingCoordinator.
func (p *pendingCoordinator) Register(method, text string) appwire.PendingHandle {
	p.mu.Lock()
	p.nextID++
	id := p.nextID
	entry := pendingEntry{ID: id, Method: method, Text: text, Pending: true}
	state := &pendingEntryState{entry: entry}
	p.entries[id] = state
	state.timer = p.clock.AfterFunc(pendingTimeout, func() {
		p.failByID(id, "server did not confirm")
	})
	p.mu.Unlock()
	p.dispatch(pendingRegisteredMsg{entry: entry})
	return &pendingHandleImpl{coord: p, id: id}
}

// dispatch enqueues a tea.Msg for delivery to the bubbletea program.
// program.Send blocks on an unbuffered channel until the event loop
// dequeues; calling it synchronously from inside the event loop (e.g.
// from TryReconcile inside Update) deadlocks because the loop can't
// dequeue while Update is still running. The dispatcher goroutine
// drains outbox serially so order is preserved across Register /
// Confirm / Fail emissions for the same call sequence.
func (p *pendingCoordinator) dispatch(msg tea.Msg) {
	select {
	case p.outbox <- msg:
	default:
		// outbox is sized for typical bursts (a single steer click
		// emits at most ~3 msgs across Register + Confirm/Fail);
		// dropping silently here is preferable to blocking the caller.
	}
}

// TryReconcile is called by the renderer's notification dispatcher
// after the authoritative reducer update applies. Returns true when a
// pending entry matched and was confirmed.
//
// Matching rules:
//   - turn/drainAsSteer: first in-flight entry with that method wins
//     (text not compared) — the daemon collapses the queue's text into
//     one steering and the placeholder doesn't know that joined text
//     in advance. This is the spec's "drain-special" semantic.
//   - Everything else: (method, normalized-text) exact match.
func (p *pendingCoordinator) TryReconcile(method, text string) bool {
	p.mu.Lock()
	var match *pendingEntryState
	if method == appwire.MethodTurnDrainAsSteer {
		for _, state := range p.entries {
			if !state.entry.Pending {
				continue
			}
			if state.entry.Method != method {
				continue
			}
			match = state
			break
		}
	} else {
		want := normalizePendingText(text)
		for _, state := range p.entries {
			if !state.entry.Pending {
				continue
			}
			if state.entry.Method != method {
				continue
			}
			if normalizePendingText(state.entry.Text) == want {
				match = state
				break
			}
		}
	}
	if match == nil {
		p.mu.Unlock()
		return false
	}
	match.timer.Stop()
	delete(p.entries, match.entry.ID)
	match.entry.Pending = false
	matchEntry := match.entry
	p.mu.Unlock()
	p.dispatch(pendingConfirmedMsg{entry: matchEntry})
	return true
}

func (p *pendingCoordinator) failByID(id int64, reason string) {
	p.mu.Lock()
	state, ok := p.entries[id]
	if !ok || state.entry.Failed || !state.entry.Pending {
		p.mu.Unlock()
		return
	}
	state.timer.Stop()
	state.entry.Pending = false
	state.entry.Failed = true
	state.entry.Reason = reason
	delete(p.entries, id)
	entry := state.entry
	p.mu.Unlock()
	p.dispatch(pendingFailedMsg{entry: entry, reason: reason})
}

func normalizePendingText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
