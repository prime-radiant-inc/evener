package hubcore

import (
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

// attentionLevel maps a normalized UI state to an attention level.
func attentionLevel(normalized string) string {
	switch normalized {
	case "active":
		return "working"
	case "awaiting", "warning", appwire.ThreadStatusRestartRequired:
		return "needs_you"
	case "errored":
		return "error"
	default:
		return "idle"
	}
}

// promotedAttentionLevel is attentionLevel plus the one escalation-promotion
// rule: a blocked sandbox-exemption escalation (M7) needs the human NOW, but
// it blocks mid-turn so the daemon status is still "active" (level
// "working"). A pending escalation promotes any non-error level to
// needs_you — additive to any other reason, and it never downgrades an
// "error" level. DeriveAttention's summary below and BuildTree's needs-you
// tier (tree.go) both call this single function for their inclusion
// decision, so a live session can never light one without the other — see
// AttentionSummary's doc.
func promotedAttentionLevel(normalized string, pendingEscalation bool) string {
	level := attentionLevel(normalized)
	if pendingEscalation && level != "error" {
		level = "needs_you"
	}
	return level
}

// tierEligible reports whether a session belongs to the tier-eligible
// population both DeriveAttention's summary and BuildTree's needs-you tier
// (tree.go) draw from: top-level — neither a subagent nor a fork-superseded
// parent nested under its active continuation (nested, from tree.go's
// nestedSessionIDs) — and not manually archived. meta may be nil (a live
// session with no persisted meta yet is still top-level and unarchived by
// definition, so nil never excludes on its own). One function, both
// callers, so population membership can't become two independently-
// maintained copies again — the same failure mode promotedAttentionLevel
// above already fixed for state promotion.
func tierEligible(sessionID string, meta *schema.SessionMeta, nested map[string]struct{}, decisions map[ArchiveKey]bool) bool {
	if meta != nil && meta.IsSubagent {
		return false
	}
	if _, isNested := nested[sessionID]; isNested {
		return false
	}
	// Archive suppression: only an explicit user archive decision clears
	// attention — archive is a clearing verb (spec v5, round-4 A4/B7).
	if d := decisionFor(decisions, sessionID); d != nil && *d {
		return false
	}
	return true
}

// DeriveAttention computes the attention map + summary over the same inputs
// BuildTree consumes. Only tier-eligible sessions (live, top-level, not
// manually archived — tierEligible above) carry attention; everything else
// is absent from the map (equivalently: idle). tierEligible is the same call
// BuildTree's needs-you tier filter in tree.go uses, so population
// membership can't drift between the two. The sidebar's 14-day age-based
// auto-archive deliberately does NOT apply here, because needs_you never
// decays (spec v5): a stale-but-live awaiting session stays in the badge
// just as it stays in the tier. Cheap by construction — in-memory inputs
// only, no disk, no BuildTree (spec v5 watcher section); nestedSessionIDs is
// itself a pure, in-memory pass over metas.
func DeriveAttention(metas []schema.SessionMeta, live []LiveEntry, decisions map[ArchiveKey]bool) (map[string]appwire.AttentionEntry, appwire.AttentionSummary) {
	metaByID := make(map[string]*schema.SessionMeta, len(metas))
	for i := range metas {
		metaByID[metas[i].ID] = &metas[i]
	}
	nested, _ := nestedSessionIDs(metas)
	out := make(map[string]appwire.AttentionEntry, len(live))
	var sum appwire.AttentionSummary
	for _, le := range live {
		if le.SessionID == "" {
			continue
		}
		meta := metaByID[le.SessionID]
		if !tierEligible(le.SessionID, meta, nested, decisions) {
			continue
		}
		level := promotedAttentionLevel(NormalizeState(le.Status), le.PendingEscalation)
		e := appwire.AttentionEntry{ID: le.SessionID, Level: level, AskPending: le.PendingAsk}
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
	prev   map[string]appwire.AttentionEntry
	seeded bool
	emit   func(appwire.AttentionChangedPayload)
}

// NewAttentionWatcher wires the emit callback (BroadcastAll in production,
// a recorder in tests).
func NewAttentionWatcher(emit func(appwire.AttentionChangedPayload)) *AttentionWatcher {
	return &AttentionWatcher{emit: emit}
}

// Tick diffs cur against the previous map and emits transitions, including
// disappearances (session gone ⇒ level "idle").
func (w *AttentionWatcher) Tick(cur map[string]appwire.AttentionEntry, sum appwire.AttentionSummary) {
	if !w.seeded {
		w.prev = cur
		w.seeded = true
		return
	}
	var changed []appwire.AttentionChanged
	for id, e := range cur {
		prev, had := w.prev[id]
		if !had || prev.Level != e.Level || prev.AskPending != e.AskPending {
			pl := "idle"
			if had {
				pl = prev.Level
			}
			changed = append(changed, appwire.AttentionChanged{AttentionEntry: e, PrevLevel: pl})
		}
	}
	for id, prev := range w.prev {
		if _, still := cur[id]; !still {
			gone := prev
			gone.Level = "idle"
			gone.AskPending = false
			changed = append(changed, appwire.AttentionChanged{AttentionEntry: gone, PrevLevel: prev.Level})
		}
	}
	w.prev = cur
	if len(changed) == 0 {
		return
	}
	w.emit(appwire.AttentionChangedPayload{Changed: changed, Summary: sum})
}
