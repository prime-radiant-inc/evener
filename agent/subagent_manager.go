package agent

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"primeradiant.com/serf/agent/events"
)

// subagentManager owns the parent session's child-subagent map. It exists to
// break the subagent⇄session back-reference cycle: a subagent no longer holds a
// back-reference to its parent *Session, it holds the parent's emit closure, and
// the parent reaches its children only through this manager. (A subagent still
// holds sub.sess, its own child *Session, as downward composition.)
//
// Lock ordering: the manager mutex is the OUTER lock and each sub.mu is the
// INNER lock. Callers that need both must take the manager mutex first. The
// manager mutex is never held while calling into a child's *Session (e.g.
// sub.sess.Close()), which would deadlock against the child's own locking.
type subagentManager struct {
	mu             sync.Mutex
	subs           map[string]*subagent
	reconstructing map[string]*subagentReconstruction
	emit           func(events.EventKind, events.EventData)
	// maxRetainedTerminal bounds how many terminal records (completed|failed|
	// cancelled) the manager retains per parent; a closed record still counts until
	// reclaimed. Enforced at spawn time via reserveSlot, which GCs reclaimable
	// records before failing loudly.
	maxRetainedTerminal int
}

// defaultMaxRetainedTerminal is the default cap on retained terminal child records.
const defaultMaxRetainedTerminal = 128

// newSubagentManager creates a manager that captures the parent session's emit
// closure for forwarding subagent lifecycle events.
func newSubagentManager(emit func(events.EventKind, events.EventData)) *subagentManager {
	return &subagentManager{
		subs:                map[string]*subagent{},
		reconstructing:      map[string]*subagentReconstruction{},
		emit:                emit,
		maxRetainedTerminal: defaultMaxRetainedTerminal,
	}
}

type subagentReconstruction struct {
	done chan struct{}
	sub  *subagent
	err  error
}

func (r *subagentReconstruction) wait() (*subagent, error) {
	<-r.done
	return r.sub, r.err
}

// track registers a subagent under its id.
func (m *subagentManager) track(sub *subagent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.subs[sub.id] = sub
}

func (m *subagentManager) beginReconstruction(childID string) (*subagent, *subagentReconstruction, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing := m.subs[childID]; existing != nil {
		return existing, nil, false
	}
	if pending := m.reconstructing[childID]; pending != nil {
		return nil, pending, false
	}
	pending := &subagentReconstruction{done: make(chan struct{})}
	m.reconstructing[childID] = pending
	return nil, pending, true
}

func (m *subagentManager) finishReconstruction(childID string, pending *subagentReconstruction, sub *subagent, err error) {
	m.mu.Lock()
	if m.reconstructing[childID] == pending {
		delete(m.reconstructing, childID)
	}
	pending.sub = sub
	pending.err = err
	close(pending.done)
	m.mu.Unlock()
}

// trackIfAbsent registers sub unless another runtime already owns that child id.
// The existing runtime is returned on collision; callers close the rejected
// candidate outside the manager lock.
func (m *subagentManager) trackIfAbsent(sub *subagent) (*subagent, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing := m.subs[sub.id]; existing != nil {
		return existing, false
	}
	m.subs[sub.id] = sub
	return sub, true
}

// get returns the subagent for id, or nil if absent. This is the single locked
// accessor for child lookup; both the agent-management tools and the send_input
// handler route through it so the read is always under the manager mutex.
func (m *subagentManager) get(id string) *subagent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.subs[id]
}

// remove deletes the subagent for id.
func (m *subagentManager) remove(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.subs, id)
}

// drainForClose collects all tracked subagents and clears the map under the
// mutex, returning the collected slice. The caller closes each child OUTSIDE
// the lock (the manager mutex must not be held while a child *Session closes).
func (m *subagentManager) drainForClose() []*subagent {
	m.mu.Lock()
	defer m.mu.Unlock()
	subs := make([]*subagent, 0, len(m.subs))
	for id, sub := range m.subs {
		subs = append(subs, sub)
		delete(m.subs, id)
	}
	return subs
}

// countsTowardCap reports whether a record occupies a retention slot: a terminal
// record (completed|failed|cancelled) whose close has not timed out. running
// children and close-timed-out records never count, so they cannot deadlock
// spawns. A closed record DOES count (it is terminal history) but is reclaimed
// first by the GC.
func countsTowardCap(status SubagentStatus, closeTimedOut bool) bool {
	return terminalStatus(status) && !closeTimedOut
}

// retentionState is a per-sub snapshot taken under that sub's mutex, used by the
// cap GC so it never holds a sub.mu across the manager-level bookkeeping loop.
type retentionState struct {
	id          string
	status      SubagentStatus
	consumed    bool
	endedAt     time.Time
	closed      bool
	reclaimable bool // closed, or a consumed terminal run — safe to evict
}

// reserveSlot enforces the retained-terminal cap before a new child is tracked.
// Counting only terminal records (countsTowardCap), it GCs reclaimable records —
// closed first, then consumed completed|failed|cancelled — oldest by endedAt, until
// the terminal count drops below the cap. If the count is still at the cap with no
// reclaimable record left (every counted record holds an unconsumed result), it
// returns an error naming the remedy and reclaims nothing.
//
// It returns the evicted records so the CALLER can close their child Sessions
// OUTSIDE the manager mutex (a consumed terminal child's Session is still live; an
// already-closed one closes idempotently) — the manager mutex must never be held
// while a child Session closes. The mutex is the outer lock; each sub.mu is taken
// only briefly to snapshot, never held across the loop.
func (m *subagentManager) reserveSlot() (evicted []*subagent, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	states := make([]retentionState, 0, len(m.subs))
	counted := 0
	for id, sub := range m.subs {
		sub.mu.Lock()
		status := sub.status
		consumed := sub.resultConsumed
		closed := sub.closed
		closeTimedOut := sub.closeTimedOut
		var endedAt time.Time
		if sub.endedAt != nil {
			endedAt = *sub.endedAt
		}
		sub.mu.Unlock()
		if !countsTowardCap(status, closeTimedOut) {
			continue
		}
		counted++
		states = append(states, retentionState{
			id:          id,
			status:      status,
			consumed:    consumed,
			endedAt:     endedAt,
			closed:      closed,
			reclaimable: closed || consumed,
		})
	}

	if counted < m.maxRetainedTerminal {
		return nil, nil
	}

	// Reclaim oldest-first, closed before consumed-terminal, until below the cap.
	reclaimable := make([]retentionState, 0, len(states))
	for _, st := range states {
		if st.reclaimable {
			reclaimable = append(reclaimable, st)
		}
	}
	sort.Slice(reclaimable, func(i, j int) bool {
		ci := reclaimable[i].closed
		cj := reclaimable[j].closed
		if ci != cj {
			return ci // closed records evicted before consumed terminal ones
		}
		return reclaimable[i].endedAt.Before(reclaimable[j].endedAt)
	})

	need := counted - m.maxRetainedTerminal + 1 // free enough to leave room for the new child
	if len(reclaimable) < need {
		return nil, fmt.Errorf("retained delegate limit reached (%d): finished delegate sessions are still finalizing; retry after job records finish before spawning another delegate", m.maxRetainedTerminal)
	}
	for i := 0; i < need; i++ {
		id := reclaimable[i].id
		evicted = append(evicted, m.subs[id])
		delete(m.subs, id)
	}
	return evicted, nil
}

// terminalStatus reports whether a status is a finished run outcome
// (completed|failed|cancelled) — one with a result to surface.
func terminalStatus(status SubagentStatus) bool {
	switch status {
	case SubagentCompleted, SubagentFailed, SubagentCancelled:
		return true
	default:
		return false
	}
}
