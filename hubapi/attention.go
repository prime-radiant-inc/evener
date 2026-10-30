// Package hubapi's attention.go is the single shared source of truth for
// attention-state ranking and display words, imported by both the hub
// (cmd/evener-hub/internal/hubcore) and its clients. Previously AttentionRank
// and rollupRank were duplicated in hubcore, and a client carried a third copy
// (attentionRankLabel) — this file is the one place that ordering logic
// lives now.
package hubapi

// AttentionState is the state a session's attention is judged by: its
// normalized state, except that a pending approval (a sandbox escalation, M7)
// reads as "awaiting". The escalation blocks mid-turn, so the session keeps
// reporting "active" while it waits on a person the way a question does. A
// failure, a session that needs a restart and one that has ended or never
// loaded still outrank it: an approval cannot answer them, so a stale
// escalation never masks them. Rank and level a session through this, never
// through its reported state alone, which stays unchanged on every row.
// appwire-client's approvalWaiting states the same rule; testdata/attention
// holds the cases both run.
func AttentionState(state string, approvalPending bool) string {
	if approvalPending && !outranksApproval(state) {
		return "awaiting"
	}
	return state
}

func outranksApproval(state string) bool {
	switch state {
	case "errored", "restartRequired", "ended", "notLoaded":
		return true
	}
	return false
}

// AttentionRank maps a normalized state to a sort key for live-session
// ordering. Higher rank sorts first (most attention-needing first).
func AttentionRank(state string) int {
	switch state {
	case "errored":
		return 5
	case "awaiting":
		return 4
	case "active":
		return 3
	case "warning", "restartRequired":
		return 2
	case "idle":
		return 1
	default: // "ended" and unknown
		return 0
	}
}

// RollupRank maps a normalized state to a sort key for a project's rollup
// dot, where a warning outranks a merely-active child (a stuck warning
// surfaces before routine activity). Deliberately different ordering from
// AttentionRank — kept in the same file so the two rank tables never drift
// apart without a reviewer noticing.
func RollupRank(state string) int {
	switch state {
	case "errored":
		return 5
	case "awaiting":
		return 4
	case "warning", "restartRequired":
		return 3
	case "active":
		return 2
	case "idle":
		return 1
	default:
		return 0
	}
}

// NeedsResponse reports whether a normalized state is the needs_response
// rest: awaiting with no pending question. A plain reply rests idle.
func NeedsResponse(state string, askPending bool) bool {
	return state == "awaiting" && !askPending
}

// NeedsYouBand ranks a needs-you row into one of three ordering bands:
// errored (2, "broken beats blocked"), blocked on a person (1: a question, an
// approval, or an awaiting turn that ended on needs_response, #4093), or
// everything else in the tier (0: a warning or a restart). Callers sort
// NeedsYou rows by this band descending, then by recency within a band.
// Meaningful only for the needs-you tier (errored, awaiting, warning and
// restartRequired states, plus sessions a pending approval promotes);
// callers outside that tier should not invoke it. An approval
// blocks mid-turn, so its session still reports "active" and only
// approvalPending can place it; pass false where the caller has no approval
// information. Both flags are ignored when state is "errored" (errored
// always wins regardless), and approvalPending counts only where
// AttentionState lets it escalate.
func NeedsYouBand(state string, askPending, approvalPending bool) int {
	switch {
	case state == "errored":
		return 2
	case state == "awaiting" || askPending || AttentionState(state, approvalPending) == "awaiting":
		return 1
	default:
		return 0
	}
}
