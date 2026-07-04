package hubcore

import (
	"primeradiant.com/serf/agent/schema"
)

// AttentionEntry is one live session's derived attention level plus the
// labels notification clients need. Levels: "working" | "needs_you" |
// "error" | "idle" (spec v5 semantics table). This struct (and
// AttentionSummary/AttentionChanged/AttentionChangedPayload below) rides the
// AppWire notification wire — serf/attention/changed is broadcast via
// appserver.Server.BroadcastAll, the same JSON-RPC channel every other
// camelCase appwire.Notifications payload uses — so its fields are camelCase
// like the rest of that protocol even though the type must live in hubcore,
// not appwire: DeriveAttention needs hub-internal inputs (ArchiveKey,
// LiveEntry) appwire must not depend on.
type AttentionEntry struct {
	// serf:naming-ignore
	ID      string `json:"threadId"`
	Title   string `json:"title"`
	Project string `json:"project"`
	Level   string `json:"level"`
}

// AttentionSummary is the authoritative badge count set, computed over the
// tier-eligible population (live, top-level, not manually archived) — the
// same definition as the NeedsYou tier by construction. camelCase: see
// AttentionEntry.
type AttentionSummary struct {
	// serf:naming-ignore
	NeedsYou int `json:"needsYou"`
	Error    int `json:"error"`
	Working  int `json:"working"`
}

// AttentionChanged is one session's level transition. camelCase: see
// AttentionEntry.
type AttentionChanged struct {
	AttentionEntry
	// serf:naming-ignore
	PrevLevel string `json:"prevLevel"`
}

// AttentionChangedPayload is the serf/attention/changed notification body.
type AttentionChangedPayload struct {
	Changed []AttentionChanged `json:"changed"`
	Summary AttentionSummary   `json:"summary"`
}

// attentionLevel maps a normalized UI state to an attention level.
func attentionLevel(normalized string) string {
	switch normalized {
	case "active":
		return "working"
	case "awaiting", "warning":
		return "needs_you"
	case "errored":
		return "error"
	default:
		return "idle"
	}
}

// DeriveAttention computes the attention map + summary over the same inputs
// BuildTree consumes. Only tier-eligible sessions (live, top-level, not
// manually archived) carry attention; everything else is absent from the map
// (equivalently: idle). Archive suppression is manual-decision-only,
// mirroring the NeedsYou tier filter in tree.go exactly — the sidebar's
// 14-day age-based auto-archive deliberately does NOT apply here, because
// needs_you never decays (spec v5): a stale-but-live awaiting session stays
// in the badge just as it stays in the tier. Cheap by construction —
// in-memory inputs only, no disk, no BuildTree (spec v5 watcher section).
func DeriveAttention(metas []schema.SessionMeta, live []LiveEntry, decisions map[ArchiveKey]bool) (map[string]AttentionEntry, AttentionSummary) {
	metaByID := make(map[string]*schema.SessionMeta, len(metas))
	for i := range metas {
		metaByID[metas[i].ID] = &metas[i]
	}
	out := make(map[string]AttentionEntry, len(live))
	var sum AttentionSummary
	for _, le := range live {
		if le.SessionID == "" {
			continue
		}
		meta := metaByID[le.SessionID]
		if meta != nil && meta.IsSubagent {
			continue
		}
		// Archive suppression: only an explicit user archive decision clears
		// attention — archive is a clearing verb (spec v5, round-4 A4/B7).
		// Same check, same semantics as the NeedsYou tier builder.
		if d := decisionFor(decisions, le.SessionID); d != nil && *d {
			continue
		}
		level := attentionLevel(NormalizeState(le.Status))
		e := AttentionEntry{ID: le.SessionID, Level: level}
		if meta != nil {
			e.Title = nodeTitle(*meta, nodeKind(*meta))
			e.Project = projectName(*meta)
		} else {
			e.Title = ShortID(le.SessionID)
		}
		out[le.SessionID] = e
		switch level {
		case "needs_you":
			sum.NeedsYou++
		case "error":
			sum.Error++
		case "working":
			sum.Working++
		}
	}
	return out, sum
}

// AttentionWatcher diffs successive attention maps and emits one payload per
// changed set. The first tick seeds silently (hub restart must not re-notify —
// spec v5). Not safe for concurrent Tick calls; the caller owns a single loop.
type AttentionWatcher struct {
	prev   map[string]AttentionEntry
	seeded bool
	emit   func(AttentionChangedPayload)
}

// NewAttentionWatcher wires the emit callback (BroadcastAll in production,
// a recorder in tests).
func NewAttentionWatcher(emit func(AttentionChangedPayload)) *AttentionWatcher {
	return &AttentionWatcher{emit: emit}
}

// Tick diffs cur against the previous map and emits transitions, including
// disappearances (session gone ⇒ level "idle").
func (w *AttentionWatcher) Tick(cur map[string]AttentionEntry, sum AttentionSummary) {
	if !w.seeded {
		w.prev = cur
		w.seeded = true
		return
	}
	var changed []AttentionChanged
	for id, e := range cur {
		prev, had := w.prev[id]
		if !had || prev.Level != e.Level {
			pl := "idle"
			if had {
				pl = prev.Level
			}
			changed = append(changed, AttentionChanged{AttentionEntry: e, PrevLevel: pl})
		}
	}
	for id, prev := range w.prev {
		if _, still := cur[id]; !still {
			gone := prev
			gone.Level = "idle"
			changed = append(changed, AttentionChanged{AttentionEntry: gone, PrevLevel: prev.Level})
		}
	}
	w.prev = cur
	if len(changed) == 0 {
		return
	}
	w.emit(AttentionChangedPayload{Changed: changed, Summary: sum})
}
