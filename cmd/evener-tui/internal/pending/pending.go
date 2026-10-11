package pending

import (
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/appwire"
)

const pendingTimeout = 10 * time.Second

// PendingClock abstracts time.AfterFunc so tests can drive timeouts
// deterministically via fakeClock.
type PendingClock interface {
	AfterFunc(d time.Duration, fn func()) PendingTimer
}

type PendingTimer interface {
	Stop() bool
}

type RealClock struct{}

func (RealClock) AfterFunc(d time.Duration, fn func()) PendingTimer {
	return time.AfterFunc(d, fn)
}

// PendingEntry is the unit of state the coordinator tracks. ID is
// stable per Register call so reducers / view code can address an
// entry across re-renders.
type PendingEntry struct {
	ID      int64
	Method  string
	Text    string
	Ref     string
	Pending bool
	Failed  bool
	Reason  string
	// ClientMutationID is the identity minted for the RPC this entry tracks,
	// echoed back on the history item or steering notification that settles
	// it. Empty on a caller that mints none (kept for compatibility; the
	// entry then falls back to method+text matching).
	ClientMutationID string
}

// PendingRegisteredMsg / PendingConfirmedMsg / PendingFailedMsg are
// the tea.Msg types the coordinator emits via the send func. The
// bubbletea model handles them by updating the reducer.
type PendingRegisteredMsg struct{ Entry PendingEntry }
type PendingConfirmedMsg struct{ Entry PendingEntry }
type PendingFailedMsg struct {
	Entry  PendingEntry
	Reason string
}

type PendingCoordinator struct {
	mu      sync.Mutex
	clock   PendingClock
	send    func(tea.Msg)
	nextID  int64
	entries map[int64]*pendingEntryState
	// outbox serialises msg dispatch so Register/Confirm/Fail emissions arrive
	// at the bubbletea program in order. It is an unbounded slice guarded by
	// outboxCond: dispatch must never drop terminal Confirm/Fail messages, but
	// it also must not block callers inside Update while program.Send is busy.
	outbox     []tea.Msg
	outboxCond *sync.Cond
	dispatcher sync.Once
}

type pendingEntryState struct {
	entry PendingEntry
	timer PendingTimer
}

func NewPendingCoordinator(clock PendingClock, send func(tea.Msg)) *PendingCoordinator {
	p := &PendingCoordinator{
		clock:   clock,
		send:    send,
		entries: map[int64]*pendingEntryState{},
	}
	p.outboxCond = sync.NewCond(&p.mu)
	p.dispatcher.Do(func() { go p.runDispatcher() })
	return p
}

// runDispatcher drains the outbox into the registered send func.
// One goroutine, serial delivery, never blocks the caller. The current
// send func is snapshotted under the lock on each iteration so
// SetSend takes effect for subsequent emissions.
func (p *PendingCoordinator) runDispatcher() {
	for {
		p.mu.Lock()
		for len(p.outbox) == 0 {
			p.outboxCond.Wait()
		}
		msg := p.outbox[0]
		copy(p.outbox, p.outbox[1:])
		p.outbox = p.outbox[:len(p.outbox)-1]
		sendFn := p.send
		p.mu.Unlock()
		if sendFn != nil {
			sendFn(msg)
		}
	}
}

// SetSend installs the bubbletea program's Send function. Tests wire
// a buffered channel; production wires program.Send after NewProgram.
// Safe to call before or after Register; the new send replaces the
// old one for subsequent emissions.
func (p *PendingCoordinator) SetSend(fn func(tea.Msg)) {
	p.mu.Lock()
	p.send = fn
	p.mu.Unlock()
}

type PendingHandleImpl struct {
	Coord *PendingCoordinator
	ID    int64
}

func (h *PendingHandleImpl) Fail(reason string) {
	h.Coord.failByID(h.ID, reason)
}

// Register satisfies appwire.PendingCoordinator.
func (p *PendingCoordinator) Register(method, text, ref, clientMutationID string) appwire.PendingHandle {
	p.mu.Lock()
	p.nextID++
	id := p.nextID
	entry := PendingEntry{ID: id, Method: method, Text: text, Ref: strings.TrimSpace(ref), Pending: true, ClientMutationID: strings.TrimSpace(clientMutationID)}
	state := &pendingEntryState{entry: entry}
	p.entries[id] = state
	state.timer = p.clock.AfterFunc(pendingTimeout, func() {
		p.failByID(id, "server did not confirm")
	})
	p.mu.Unlock()
	p.dispatch(PendingRegisteredMsg{Entry: entry})
	return &PendingHandleImpl{Coord: p, ID: id}
}

// dispatch enqueues a tea.Msg for delivery to the bubbletea program.
// program.Send blocks on an unbuffered channel until the event loop
// dequeues; calling it synchronously from inside the event loop (e.g.
// from TryReconcile inside Update) deadlocks because the loop can't
// dequeue while Update is still running. The dispatcher goroutine
// drains outbox serially so order is preserved across Register /
// Confirm / Fail emissions for the same call sequence.
func (p *PendingCoordinator) dispatch(msg tea.Msg) {
	p.mu.Lock()
	p.outbox = append(p.outbox, msg)
	p.outboxCond.Signal()
	p.mu.Unlock()
}

// TryReconcile is called by the renderer's notification dispatcher
// after the authoritative reducer update applies. Returns true when a
// pending entry matched and was confirmed.
//
// Matching rules:
//   - turn/drainAsSteer: oldest in-flight entry with that method wins
//     (text not compared) — the daemon collapses the queue's text into
//     one steering and the placeholder doesn't know that joined text
//     in advance. This is the spec's "drain-special" semantic.
//   - Everything else: (method, normalized-text) exact match.
func (p *PendingCoordinator) TryReconcile(method, text, ref string) bool {
	ref = strings.TrimSpace(ref)
	p.mu.Lock()
	var pendingMatch *pendingEntryState
	var failedMatch *pendingEntryState
	if method == appwire.MethodTurnDrainAsSteer {
		for _, state := range p.entries {
			if state.entry.Method != method || !pendingRefsMatch(state.entry.Ref, ref) {
				continue
			}
			if state.entry.Pending {
				if pendingMatch == nil || state.entry.ID < pendingMatch.entry.ID {
					pendingMatch = state
				}
				continue
			}
			if state.entry.Failed {
				if failedMatch == nil || state.entry.ID < failedMatch.entry.ID {
					failedMatch = state
				}
			}
		}
	} else {
		want := normalizePendingText(text)
		for _, state := range p.entries {
			if state.entry.Method != method || !pendingRefsMatch(state.entry.Ref, ref) {
				continue
			}
			if normalizePendingText(state.entry.Text) == want {
				if state.entry.Pending {
					if pendingMatch == nil || state.entry.ID < pendingMatch.entry.ID {
						pendingMatch = state
					}
					continue
				}
				if state.entry.Failed {
					if failedMatch == nil || state.entry.ID < failedMatch.entry.ID {
						failedMatch = state
					}
				}
			}
		}
	}
	match := pendingMatch
	if match == nil {
		match = failedMatch
	}
	if match == nil {
		p.mu.Unlock()
		return false
	}
	return p.confirmLocked(match)
}

// TryReconcileByMutationID confirms the pending entry of the given method
// whose ClientMutationID equals mutationID — the authoritative identity the
// daemon echoes back on the history item or steering notification that
// settles the mutation, in place of TryReconcile's best-effort text match
// (fragile once the server substitutes an image placeholder, or joins
// several queued texts). Returns false without matching anything when
// mutationID is empty, so a caller that has none simply falls back to
// TryReconcile.
func (p *PendingCoordinator) TryReconcileByMutationID(method, mutationID, ref string) bool {
	mutationID = strings.TrimSpace(mutationID)
	if mutationID == "" {
		return false
	}
	ref = strings.TrimSpace(ref)
	p.mu.Lock()
	var match *pendingEntryState
	for _, state := range p.entries {
		if state.entry.Method != method || state.entry.ClientMutationID != mutationID || !pendingRefsMatch(state.entry.Ref, ref) {
			continue
		}
		if !state.entry.Pending && !state.entry.Failed {
			continue
		}
		match = state
		break
	}
	if match == nil {
		p.mu.Unlock()
		return false
	}
	return p.confirmLocked(match)
}

// Reconcile tries the authoritative mutation-id match (TryReconcileByMutationID)
// first and falls back to the best-effort text match (TryReconcile) only when
// the caller has no mutation id, or nothing pending held it — an older daemon,
// or a caller (like turn/drainAsSteer, which never carries per-item identity)
// that mints none. This is the coordinator's own matching-priority policy, so
// every caller gets it without having to stitch the two primitives together
// itself.
func (p *PendingCoordinator) Reconcile(method, clientMutationID, text, ref string) bool {
	if p.TryReconcileByMutationID(method, clientMutationID, ref) {
		return true
	}
	return p.TryReconcile(method, text, ref)
}

// confirmLocked settles match as confirmed and dispatches its
// PendingConfirmedMsg. Called with p.mu held; unlocks before returning.
func (p *PendingCoordinator) confirmLocked(match *pendingEntryState) bool {
	match.timer.Stop()
	delete(p.entries, match.entry.ID)
	match.entry.Pending = false
	match.entry.Failed = false
	match.entry.Reason = ""
	matchEntry := match.entry
	p.mu.Unlock()
	p.dispatch(PendingConfirmedMsg{Entry: matchEntry})
	return true
}

func pendingRefsMatch(entryRef, ref string) bool {
	entryRef = strings.TrimSpace(entryRef)
	ref = strings.TrimSpace(ref)
	if entryRef == "" || ref == "" || entryRef == ref {
		return true
	}
	if parsed, err := appwire.ParseRef(entryRef); err == nil && parsed.ThreadID == ref {
		return true
	}
	if parsed, err := appwire.ParseRef(ref); err == nil && parsed.ThreadID == entryRef {
		return true
	}
	return false
}

func (p *PendingCoordinator) failByID(id int64, reason string) {
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
	entry := state.entry
	p.mu.Unlock()
	p.dispatch(PendingFailedMsg{Entry: entry, Reason: reason})
}

func normalizePendingText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
