package agent

import (
	"sync"

	"primeradiant.com/evener/agent/provenance"
)

// delegateAttentionCauses carries causal provenance across the delegate hop:
// the lineage of what produced an attention item (a delegate's update to its
// parent, a stable watch frame to a receiver delegate), keyed by the attention
// id the receiving session's transcript records it under. The receiving turn
// adopts it, so an event the receiver emits in reply descends from that
// cause. It is process-local: both sides of the hop run in this process, and
// after a restart the map is empty and the receiver's provenance starts fresh.
type delegateAttentionCauses struct {
	mu     sync.Mutex
	causes map[string]*provenance.Causal
}

// remember records p as the cause of attentionID.
func (a *delegateAttentionCauses) remember(attentionID string, p *provenance.Causal) {
	if attentionID == "" || p == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.causes == nil {
		a.causes = make(map[string]*provenance.Causal)
	}
	a.causes[attentionID] = provenance.Clone(p)
}

// union returns the combined cause of the attention items ids, nil when none
// has one.
func (a *delegateAttentionCauses) union(ids []string) *provenance.Causal {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out *provenance.Causal
	for _, id := range ids {
		if cause := a.causes[id]; cause != nil {
			out = provenance.Union(out, cause)
		}
	}
	return out
}

// forget drops the causes of ids once those attention items are resolved or
// will never arrive.
func (a *delegateAttentionCauses) forget(ids ...string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, id := range ids {
		delete(a.causes, id)
	}
}

// clear drops every cause, for a controller that is closing.
func (a *delegateAttentionCauses) clear() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.causes = nil
}

// size reports how many causes are held.
func (a *delegateAttentionCauses) size() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.causes)
}
