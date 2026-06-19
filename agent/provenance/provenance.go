// Package provenance provides causal-provenance primitives for watch-origin loop suppression.
//
// The load-bearing structure is a deduped set of WatchKey (watch_id, watch_generation);
// the diagnostic Chain is bounded separately so truncation can never drop watch keys.
package provenance

// maxDiagnosticChain bounds the diagnostic Chain; exceeding it truncates Chain entries
// only, never WatchKeys.
const maxDiagnosticChain = 16

type WatchKey struct {
	WatchID         string `json:"watch_id"`
	WatchGeneration string `json:"watch_generation"`
}

type Causal struct {
	WatchKeys      []WatchKey `json:"watch_keys,omitempty"`
	Chain          []Entry    `json:"chain,omitempty"`
	ChainTruncated bool       `json:"chain_truncated,omitempty"`
}

type Entry struct {
	Kind            string `json:"kind"`
	WatchID         string `json:"watch_id,omitempty"`
	WatchGeneration string `json:"watch_generation,omitempty"`
	DeliveryID      string `json:"delivery_id,omitempty"`
	SessionID       string `json:"session_id,omitempty"`
	JobID           string `json:"job_id,omitempty"`
}

func Clone(p *Causal) *Causal {
	if p == nil {
		return nil
	}
	out := &Causal{ChainTruncated: p.ChainTruncated}
	out.WatchKeys = append(out.WatchKeys, p.WatchKeys...)
	out.Chain = append(out.Chain, p.Chain...)
	truncateChain(out)
	return NilIfEmpty(out)
}

func NilIfEmpty(p *Causal) *Causal {
	if p == nil {
		return nil
	}
	if len(p.WatchKeys) == 0 && len(p.Chain) == 0 && !p.ChainTruncated {
		return nil
	}
	return p
}

func ContainsWatch(p *Causal, watchID, watchGeneration string) bool {
	if p == nil || watchID == "" || watchGeneration == "" {
		return false
	}
	for _, key := range p.WatchKeys {
		if key.WatchID == watchID && key.WatchGeneration == watchGeneration {
			return true
		}
	}
	return false
}

func Union(parts ...*Causal) *Causal {
	var out Causal
	seen := make(map[WatchKey]bool)
	for _, part := range parts {
		if part == nil {
			continue
		}
		for _, key := range part.WatchKeys {
			if key.WatchID == "" || key.WatchGeneration == "" || seen[key] {
				continue
			}
			seen[key] = true
			out.WatchKeys = append(out.WatchKeys, key)
		}
		out.Chain = append(out.Chain, part.Chain...)
		out.ChainTruncated = out.ChainTruncated || part.ChainTruncated
	}
	truncateChain(&out)
	return NilIfEmpty(&out)
}

func WithWatch(base *Causal, watchID, watchGeneration, deliveryID, sessionID, jobID string) *Causal {
	added := &Causal{
		WatchKeys: []WatchKey{{WatchID: watchID, WatchGeneration: watchGeneration}},
		Chain: []Entry{{
			Kind:            "watch",
			WatchID:         watchID,
			WatchGeneration: watchGeneration,
			DeliveryID:      deliveryID,
			SessionID:       sessionID,
			JobID:           jobID,
		}},
	}
	return Union(base, added)
}

func LatestDeliveryID(p *Causal) string {
	if p == nil {
		return ""
	}
	for i := len(p.Chain) - 1; i >= 0; i-- {
		if p.Chain[i].DeliveryID != "" {
			return p.Chain[i].DeliveryID
		}
	}
	return ""
}

func truncateChain(p *Causal) {
	if p == nil || len(p.Chain) <= maxDiagnosticChain {
		return
	}
	keepHead := maxDiagnosticChain / 2
	keepTail := maxDiagnosticChain - keepHead
	next := make([]Entry, 0, maxDiagnosticChain)
	next = append(next, p.Chain[:keepHead]...)
	next = append(next, p.Chain[len(p.Chain)-keepTail:]...)
	p.Chain = next
	p.ChainTruncated = true
}
