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
	mu   sync.Mutex
	subs map[string]*subagent
	emit func(events.EventKind, events.EventData)
	// maxRetainedTerminal bounds how many terminal records (completed|failed|
	// cancelled|closed) the manager retains per parent. Enforced at spawn time via
	// reserveSlot, which GCs reclaimable records before failing loudly.
	maxRetainedTerminal int
}

// defaultMaxRetainedTerminal is the default cap on retained terminal child records.
const defaultMaxRetainedTerminal = 128

// newSubagentManager creates a manager that captures the parent session's emit
// closure for forwarding subagent lifecycle events.
func newSubagentManager(emit func(events.EventKind, events.EventData)) *subagentManager {
	return &subagentManager{
		subs:                map[string]*subagent{},
		emit:                emit,
		maxRetainedTerminal: defaultMaxRetainedTerminal,
	}
}

// track registers a subagent under its id.
func (m *subagentManager) track(sub *subagent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.subs[sub.id] = sub
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

// countsTowardCap reports whether a status occupies a retention slot: only the
// retained terminal records (completed|failed|cancelled|closed). running children
// and closing/close-timed-out records never count, so they cannot deadlock spawns.
func countsTowardCap(status SubagentStatus) bool {
	switch status {
	case SubagentCompleted, SubagentFailed, SubagentCancelled, SubagentClosed:
		return true
	default:
		return false
	}
}

// retentionState is a per-sub snapshot taken under that sub's mutex, used by the
// cap GC so it never holds a sub.mu across the manager-level bookkeeping loop.
type retentionState struct {
	id          string
	status      SubagentStatus
	consumed    bool
	endedAt     time.Time
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
		var endedAt time.Time
		if sub.endedAt != nil {
			endedAt = *sub.endedAt
		}
		sub.mu.Unlock()
		if !countsTowardCap(status) {
			continue
		}
		counted++
		states = append(states, retentionState{
			id:          id,
			status:      status,
			consumed:    consumed,
			endedAt:     endedAt,
			reclaimable: status == SubagentClosed || consumed,
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
		ci := reclaimable[i].status == SubagentClosed
		cj := reclaimable[j].status == SubagentClosed
		if ci != cj {
			return ci // closed records evicted before consumed terminal ones
		}
		return reclaimable[i].endedAt.Before(reclaimable[j].endedAt)
	})

	need := counted - m.maxRetainedTerminal + 1 // free enough to leave room for the new child
	if len(reclaimable) < need {
		return nil, fmt.Errorf("retained subagent limit reached (%d): close_agent or wait on a finished agent to reclaim a slot before spawning", m.maxRetainedTerminal)
	}
	for i := 0; i < need; i++ {
		id := reclaimable[i].id
		evicted = append(evicted, m.subs[id])
		delete(m.subs, id)
	}
	return evicted, nil
}

// runOutcomeReason maps a subagent status to the last RUN OUTCOME reported in the
// info record's reason field: the terminal outcome (completed|failed|cancelled) or
// empty while running. It is the fallback for records without a retained outcome;
// closing/closed records surface their retained lastOutcome via reasonLocked.
func runOutcomeReason(status SubagentStatus) SubagentStatus {
	switch status {
	case SubagentCompleted, SubagentFailed, SubagentCancelled:
		return status
	default:
		return ""
	}
}

// terminalStatus reports whether a status represents a finished run (one that has
// a result to surface). Closing/closed are not terminal run states here.
func terminalStatus(status SubagentStatus) bool {
	return runOutcomeReason(status) != ""
}

// infoLocked builds the snapshot record for one subagent. It is called with sub.mu
// held (hence "Locked"); it reads sub fields plus the immutable sub.sess.cfg.spawn
// and sub.sess.ID(). parentID is the parent session id supplied by the caller.
// result_available is true only for a terminal run whose result has not yet been
// consumed.
func (a *subagent) infoLocked(parentID string) SubagentInfo {
	info := SubagentInfo{
		AgentID:         a.id,
		ID:              a.id,
		Status:          a.status,
		Reason:          a.reasonLocked(),
		AgentType:       a.agentType,
		ParentSessionID: parentID,
		TurnsUsed:       a.turnsUsed,
		ResultAvailable: terminalStatus(a.status) && !a.resultConsumed,
		ResultConsumed:  a.resultConsumed,
		CreatedAt:       a.createdAt,
		StartedAt:       a.startedAt,
		EndedAt:         a.endedAt,
		CloseTimedOut:   a.closeTimedOut,
	}
	if a.sess != nil {
		info.Task = a.sess.cfg.spawn.subagentTask
		info.TranscriptRef = encodeRef("", a.sess.ID())
	}
	return info
}

// infos enumerates the tracked subagents for /status display, hiding closed
// records so retained-but-closed children do not accumulate (running/completed/
// failed/cancelled stay visible). It preserves the two-level lock order: manager
// mutex outer, sub.mu inner.
func (m *subagentManager) infos() []SubagentInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	var infos []SubagentInfo
	for _, sub := range m.subs {
		sub.mu.Lock()
		if sub.status != SubagentClosed {
			infos = append(infos, sub.infoLocked(""))
		}
		sub.mu.Unlock()
	}
	return infos
}

// listAgents answers the list_agents query. With no status and includeClosed false
// it returns all non-closed records. A status filter returns only records in that
// status; status=closed implies includeClosed. includeClosed surfaces closed
// records. It preserves the manager-outer/sub-inner lock order.
func (m *subagentManager) listAgents(parentID, status string, includeClosed bool) (agents []SubagentInfo, count int) {
	if status == string(SubagentClosed) {
		includeClosed = true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, sub := range m.subs {
		sub.mu.Lock()
		subStatus := sub.status
		include := subagentMatchesFilter(subStatus, status, includeClosed)
		var info SubagentInfo
		if include {
			info = sub.infoLocked(parentID)
		}
		sub.mu.Unlock()
		if include {
			agents = append(agents, info)
		}
	}
	return agents, len(agents)
}

// subagentMatchesFilter decides whether a subagent in subStatus passes the
// list_agents filter. status "" means all (subject to includeClosed); status "all"
// is the same sentinel; any other status matches only that status.
func subagentMatchesFilter(subStatus SubagentStatus, status string, includeClosed bool) bool {
	switch status {
	case "", "all":
		if subStatus == SubagentClosed {
			return includeClosed
		}
		return true
	default:
		return string(subStatus) == status
	}
}
