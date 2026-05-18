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
}

type pendingEntryState struct {
	entry pendingEntry
	timer pendingTimer
}

func newPendingCoordinator(clock pendingClock, send func(tea.Msg)) *pendingCoordinator {
	return &pendingCoordinator{
		clock:   clock,
		send:    send,
		entries: map[int64]*pendingEntryState{},
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
	p.send(pendingRegisteredMsg{entry: entry})
	return &pendingHandleImpl{coord: p, id: id}
}

// TryReconcile is called by the renderer's notification dispatcher
// after the authoritative reducer update applies. Returns true when a
// pending entry matched and was confirmed. Match: (method == entry.Method)
// AND normalizedText(text) == normalizedText(entry.Text).
func (p *pendingCoordinator) TryReconcile(method, text string) bool {
	want := normalizePendingText(text)
	p.mu.Lock()
	var match *pendingEntryState
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
	if match == nil {
		p.mu.Unlock()
		return false
	}
	match.timer.Stop()
	delete(p.entries, match.entry.ID)
	match.entry.Pending = false
	p.mu.Unlock()
	p.send(pendingConfirmedMsg{entry: match.entry})
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
	p.send(pendingFailedMsg{entry: entry, reason: reason})
}

func normalizePendingText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
