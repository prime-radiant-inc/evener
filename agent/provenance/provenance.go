// Package provenance provides causal-provenance primitives for watch-origin loop suppression.
//
// The load-bearing structure is a deduped set of WatchKey (watch_id, watch_generation);
// the diagnostic Chain is bounded separately so truncation can never drop watch keys.
// FireKeys record the lineage's distinct watch fires on their own, so the
// runaway guard's count survives a Chain that outgrew its bound.
package provenance

import "slices"

// maxDiagnosticChain bounds the diagnostic Chain; exceeding it truncates Chain entries
// only, never WatchKeys.
const maxDiagnosticChain = 16

// maxCausalFiresPerWatch bounds each watch's retained fires, comfortably past
// the runaway depth the guard trips at; a watch's oldest fire drops only after
// that watch alone long since crossed the depth.
//
// The bound is per watch because the fuse it feeds counts one watch's delivered
// fires. A single global bound starves a chain that cycles through many
// watches: the newest watches spend every slot and the older ones keep too few
// fires to ever reach the depth, so the loop outlives the cap. The record's
// total is therefore proportional to WatchKeys, which is already an unbounded
// deduped set — one entry per watch the lineage has ever passed through.
const maxCausalFiresPerWatch = 16

type WatchKey struct {
	WatchID         string `json:"watch_id"`
	WatchGeneration string `json:"watch_generation"`
}

type Causal struct {
	WatchKeys      []WatchKey `json:"watch_keys,omitempty"`
	Chain          []Entry    `json:"chain,omitempty"`
	FireKeys       []FireKey  `json:"fire_keys,omitempty"`
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

// FireKey names one watch fire of a lineage: which watch fired, in which
// generation, under which delivery id. It is retained independently of the
// diagnostic Chain, whose bound would otherwise hide the fires behind newer
// entries.
type FireKey struct {
	WatchID         string `json:"watch_id,omitempty"`
	WatchGeneration string `json:"watch_generation,omitempty"`
	DeliveryID      string `json:"delivery_id,omitempty"`
}

func Clone(p *Causal) *Causal {
	if p == nil {
		return nil
	}
	out := &Causal{ChainTruncated: p.ChainTruncated}
	out.WatchKeys = append(out.WatchKeys, p.WatchKeys...)
	out.Chain = append(out.Chain, p.Chain...)
	out.FireKeys = append(out.FireKeys, p.FireKeys...)
	truncateChain(out)
	boundFires(out)
	return NilIfEmpty(out)
}

func NilIfEmpty(p *Causal) *Causal {
	if p == nil {
		return nil
	}
	if len(p.WatchKeys) == 0 && len(p.Chain) == 0 && len(p.FireKeys) == 0 && !p.ChainTruncated {
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
	entrySeen := make(map[Entry]bool)
	fireSeen := make(map[FireKey]bool)
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
		// Two causes of one turn commonly share ancestry; a shared entry is
		// one causal event, so it appears once. Only fully-identical entries
		// collapse — a genuinely new cause carries a field the held entries
		// do not and survives untouched.
		for _, entry := range part.Chain {
			if entrySeen[entry] {
				continue
			}
			entrySeen[entry] = true
			out.Chain = append(out.Chain, entry)
		}
		for _, fire := range part.FireKeys {
			if fireSeen[fire] {
				continue
			}
			fireSeen[fire] = true
			out.FireKeys = append(out.FireKeys, fire)
		}
		out.ChainTruncated = out.ChainTruncated || part.ChainTruncated
	}
	truncateChain(&out)
	boundFires(&out)
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
	if deliveryID != "" {
		added.FireKeys = []FireKey{{WatchID: watchID, WatchGeneration: watchGeneration, DeliveryID: deliveryID}}
	}
	return Union(base, added)
}

// boundFires caps each watch's fire record at maxCausalFiresPerWatch, keeping
// the newest fires of every watch and dropping only the surplus.
func boundFires(p *Causal) {
	// A record no larger than one watch's bound holds no watch over it, so the
	// common case stays a length check.
	if p == nil || len(p.FireKeys) <= maxCausalFiresPerWatch {
		return
	}
	keptCount := make(map[WatchKey]int, len(p.WatchKeys)+1)
	kept := make([]FireKey, 0, len(p.FireKeys))
	over := false
	for i := len(p.FireKeys) - 1; i >= 0; i-- {
		fire := p.FireKeys[i]
		key := WatchKey{WatchID: fire.WatchID, WatchGeneration: fire.WatchGeneration}
		// Walking newest-first, the first maxCausalFiresPerWatch fires of a watch
		// are the ones retained; everything older than that is surplus.
		if keptCount[key] >= maxCausalFiresPerWatch {
			over = true
			continue
		}
		keptCount[key]++
		kept = append(kept, fire)
	}
	if !over {
		return
	}
	slices.Reverse(kept)
	p.FireKeys = kept
}

// DropSupersededDelivery removes deliveryID from p's lineage: its fire entry
// and any watch entry naming it. A frame the receiver never took can never
// count toward SelfInfluenceDepth, which counts only delivered priors, so
// keeping it spends one of the record's bounded slots on a dead id. A lineage
// whose frames each stand for many coalesced matches otherwise fills the fire
// record and the diagnostic chain with dead ids and evicts the delivered
// ancestors the fuse counts, and the loop outlives the cap. The caller names
// the id at the moment it supersedes the frame; this package cannot tell a
// superseded frame from one still awaiting delivery.
func DropSupersededDelivery(p *Causal, deliveryID string) {
	if p == nil || deliveryID == "" {
		return
	}
	// The record is rebuilt rather than filtered in place: a coalescing caller
	// may hand this the Causal a drain is reading, and its slots outlive the
	// call.
	fires := make([]FireKey, 0, len(p.FireKeys))
	for _, fire := range p.FireKeys {
		if fire.DeliveryID != deliveryID {
			fires = append(fires, fire)
		}
	}
	p.FireKeys = fires
	chain := make([]Entry, 0, len(p.Chain))
	for _, entry := range p.Chain {
		if entry.Kind == "watch" && entry.DeliveryID == deliveryID {
			continue
		}
		chain = append(chain, entry)
	}
	p.Chain = chain
}

func LatestDeliveryID(p *Causal) string {
	if p == nil {
		return ""
	}
	for _, e := range slices.Backward(p.Chain) {
		if e.DeliveryID != "" {
			return e.DeliveryID
		}
	}
	return ""
}

// SelfInfluenceDepth counts distinct *delivered* prior deliveries of watchID in
// p.Chain. Coalescing-aware: a hop counts only if delivered(hop.DeliveryID) — a
// superseded pending unions its chain entry into a survivor but never
// independently delivered, so it must not inflate the count.
//
//	generation == ""      → count across all generations (the runaway-fuse scope)
//	generation == "<gen>" → count only that generation    (the gradient scope)
func SelfInfluenceDepth(p *Causal, watchID, generation string, delivered func(string) bool) int {
	if p == nil || watchID == "" {
		return 0
	}
	seen := make(map[string]bool)
	count := func(fire FireKey) {
		if fire.WatchID != watchID || fire.DeliveryID == "" {
			return
		}
		if generation != "" && fire.WatchGeneration != generation {
			return
		}
		if !delivered(fire.DeliveryID) || seen[fire.DeliveryID] {
			return
		}
		seen[fire.DeliveryID] = true
	}
	for _, fire := range p.FireKeys {
		count(fire)
	}
	for _, entry := range p.Chain {
		if entry.Kind != "watch" || entry.WatchID != watchID || entry.DeliveryID == "" {
			continue
		}
		if generation != "" && entry.WatchGeneration != generation {
			continue
		}
		if !delivered(entry.DeliveryID) || seen[entry.DeliveryID] {
			continue
		}
		seen[entry.DeliveryID] = true
	}
	return len(seen)
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

// EntryKindMessage marks a lineage entry for a message one session sent
// another, such as a delegate's update to its parent.
const EntryKindMessage = "message"

// WithMessage extends base with an entry for the message deliveryID that
// sessionID sent.
func WithMessage(base *Causal, sessionID, deliveryID string) *Causal {
	return Union(base, &Causal{Chain: []Entry{{Kind: EntryKindMessage, SessionID: sessionID, DeliveryID: deliveryID}}})
}

// MessagesFrom counts the distinct messages sessionID sent in p's lineage.
func MessagesFrom(p *Causal, sessionID string) int {
	if p == nil || sessionID == "" {
		return 0
	}
	var seen []string
	for _, entry := range p.Chain {
		if entry.Kind == EntryKindMessage && entry.SessionID == sessionID && !slices.Contains(seen, entry.DeliveryID) {
			seen = append(seen, entry.DeliveryID)
		}
	}
	return len(seen)
}
