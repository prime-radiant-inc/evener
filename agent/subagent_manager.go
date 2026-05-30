package agent

import "sync"

// subagentManager owns the parent session's child-subagent map. It exists to
// break the subagent⇄session back-reference cycle: subagents no longer hold a
// *Session, they hold the parent's emit closure, and the parent reaches its
// children only through this manager.
//
// Lock ordering: the manager mutex is the OUTER lock and each sub.mu is the
// INNER lock. Callers that need both must take the manager mutex first. The
// manager mutex is never held while calling into a child's *Session (e.g.
// sub.sess.Close()), which would deadlock against the child's own locking.
type subagentManager struct {
	mu   sync.Mutex
	subs map[string]*subagent
	emit func(EventKind, any)
}

// newSubagentManager creates a manager that captures the parent session's emit
// closure for forwarding subagent lifecycle events.
func newSubagentManager(emit func(EventKind, any)) *subagentManager {
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

// infos enumerates the tracked subagents for status display. It preserves the
// two-level lock order: manager mutex outer, sub.mu inner.
func (m *subagentManager) infos() []SubagentInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	var infos []SubagentInfo
	for id, sub := range m.subs {
		sub.mu.Lock()
		infos = append(infos, SubagentInfo{
			ID:        id,
			Status:    sub.status,
			TurnsUsed: sub.turnsUsed,
		})
		sub.mu.Unlock()
	}
	return infos
}
