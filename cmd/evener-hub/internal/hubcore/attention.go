package hubcore

import (
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/hubapi"
)

// attentionLevel maps a normalized UI state plus its pending facts to an
// attention level. Only an actionable condition puts a session in needs_you: a
// pending question or approval, a warning, or a restart. A turn that merely
// ended on needs_response is finished and rests idle (#2629).
func attentionLevel(normalized string, askPending, approvalPending bool) string {
	if hubapi.NeedsAction(normalized, askPending, approvalPending) {
		return "needs_you"
	}
	switch normalized {
	case "active":
		return "working"
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
// "error" level. hubapi.AttentionState states that rule once, for this and
// for the tree's Live order and project rollups. DeriveAttention's summary
// below and BuildTree's needs-you tier (tree.go) both call this single
// function for their inclusion decision, so a live session can never light
// one without the other; see AttentionSummary's doc.
func promotedAttentionLevel(normalized string, askPending, pendingEscalation bool) string {
	return attentionLevel(normalized, askPending, pendingEscalation)
}

// tierEligible reports whether a session belongs to the tier-eligible
// population both DeriveAttention's summary and BuildTree's needs-you tier
// (tree.go) draw from: top-level — neither a subagent nor a fork-superseded
// parent nested under its active continuation (roots) — and not manually
// archived. A subagent is one its persisted meta says so or one a live entry
// reports as its running child (runningSubagents, see RunningSubagentIDs), so
// a subagent whose meta is missing is still excluded. A live session that
// neither source names is top-level and unarchived by definition. One
// function, both callers, so population membership can't become two
// independently-maintained copies again — the same failure mode
// promotedAttentionLevel above already fixed for state promotion.
func tierEligible(sessionID string, roots *RootIndex, runningSubagents map[string]bool, decisions map[ArchiveKey]bool) bool {
	if roots.IsSubagent(sessionID) || runningSubagents[sessionID] || roots.IsNested(sessionID) {
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
// only, no disk, no BuildTree (spec v5 watcher section).
func DeriveAttention(metas []schema.SessionMeta, live []LiveEntry, decisions map[ArchiveKey]bool) (map[string]appwire.AttentionEntry, appwire.AttentionSummary) {
	metaByID := make(map[string]*schema.SessionMeta, len(metas))
	for i := range metas {
		metaByID[metas[i].ID] = &metas[i]
	}
	return DeriveAttentionFromRoots(NewRootIndex(metas), func(id string) (schema.SessionMeta, bool) {
		if m, ok := metaByID[id]; ok {
			return *m, true
		}
		return schema.SessionMeta{}, false
	}, live, decisions)
}

// DeriveAttentionFromRoots is DeriveAttention over a prebuilt RootIndex, with
// meta looking up the persisted meta for a live session (titles only), so a
// caller that already holds the past index's RootIndex pays nothing per call
// for the lineage pass.
func DeriveAttentionFromRoots(roots *RootIndex, meta func(id string) (schema.SessionMeta, bool), live []LiveEntry, decisions map[ArchiveKey]bool) (map[string]appwire.AttentionEntry, appwire.AttentionSummary) {
	runningSubagents := RunningSubagentIDs(live)
	out := make(map[string]appwire.AttentionEntry, len(live))
	var sum appwire.AttentionSummary
	publishedAt := time.Now().UnixMilli()
	for _, le := range live {
		if le.SessionID == "" {
			continue
		}
		if !tierEligible(le.SessionID, roots, runningSubagents, decisions) {
			continue
		}
		state := NormalizeState(le.Status)
		level := promotedAttentionLevel(state, le.PendingAsk, le.PendingEscalation)
		e := appwire.AttentionEntry{
			ID:              le.SessionID,
			Level:           level,
			AskPending:      le.PendingAsk,
			ApprovalPending: le.PendingEscalation,
			NeedsResponse:   hubapi.NeedsResponse(state, le.PendingAsk),
			PublishedAt:     publishedAt,
		}
		if le.PendingAsk {
			e.PendingQuestion = appwire.ClonePendingQuestion(le.PendingQuestion)
			e.AskPageable = le.Capabilities.AskFence
			if pending := le.PendingAskState; pending != nil {
				e.AskBootGeneration, e.AskRevision = pending.BootGeneration, pending.Revision
				e.CompositeList = append([]appwire.PendingAskCall(nil), pending.Calls...)
				e.CompositeTotal, e.CalllessCount = pending.CompositeTotal, pending.CalllessCount
			}
		}
		if m, ok := meta(le.SessionID); ok {
			e.Title = nodeTitle(m, nodeKind(m))
			e.Project = projectName(m)
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
		if !had || prev.Level != e.Level || prev.AskPending != e.AskPending || prev.ApprovalPending != e.ApprovalPending || prev.NeedsResponse != e.NeedsResponse {
			pl := "idle"
			if had {
				pl = prev.Level
			}
			changed = append(changed, appwire.AttentionChanged{AttentionEntry: e, PrevLevel: pl})
		}
	}
	for id, prev := range w.prev {
		if _, still := cur[id]; !still {
			// Built from the labels only, so every pending flag clears.
			gone := appwire.AttentionEntry{ID: prev.ID, Title: prev.Title, Project: prev.Project, Level: "idle"}
			changed = append(changed, appwire.AttentionChanged{AttentionEntry: gone, PrevLevel: prev.Level})
		}
	}
	w.prev = cur
	if len(changed) == 0 {
		return
	}
	w.emit(appwire.AttentionChangedPayload{Changed: changed, Summary: sum})
}
