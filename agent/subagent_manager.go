package agent

import (
	"sync"

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
}

// newSubagentManager creates a manager that captures the parent session's emit
// closure for forwarding subagent lifecycle events.
func newSubagentManager(emit func(events.EventKind, events.EventData)) *subagentManager {
	return &subagentManager{
		subs: map[string]*subagent{},
		emit: emit,
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

// runOutcomeReason maps a subagent status to the last RUN OUTCOME reported in the
// info record's reason field: the terminal outcome (completed|failed|cancelled) or
// empty while running. Task 7: closed/closing records must still surface the last
// run outcome here — derive it from the retained outcome, never report "closed".
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
		Reason:          runOutcomeReason(a.status),
		AgentType:       a.agentType,
		ParentSessionID: parentID,
		TurnsUsed:       a.turnsUsed,
		ResultAvailable: terminalStatus(a.status) && !a.resultConsumed,
		ResultConsumed:  a.resultConsumed,
		CreatedAt:       a.createdAt,
		StartedAt:       a.startedAt,
		EndedAt:         a.endedAt,
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
