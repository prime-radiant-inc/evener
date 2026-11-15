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
	if ApprovalWaiting(state, approvalPending) {
		return "awaiting"
	}
	return state
}

// ApprovalWaiting reports whether a pending approval blocks a session that is
// short of a state an approval cannot answer. A failure, a restart need and an
// ended or never-loaded session outrank it, so a stale escalation never masks
// them. appwire-client's approvalWaiting states the same rule.
func ApprovalWaiting(state string, approvalPending bool) bool {
	return approvalPending && !outranksApproval(state)
}

func outranksApproval(state string) bool {
	switch state {
	case "errored", "restartRequired", "ended", "notLoaded":
		return true
	}
	return false
}

// NeedsAction reports whether a normalized state plus its pending facts is
// actionable enough to put the session in Needs you: a pending question, a
// pending approval, a warning, or a restart. A turn that merely ended on
// needs_response is finished, not blocked on a person, so it does not qualify
// (#2629). A failure is not "needs action" here either; it has its own error
// level and rank.
func NeedsAction(state string, askPending, approvalPending bool) bool {
	if state == "errored" {
		return false
	}
	if state == "warning" || state == "restartRequired" {
		return true
	}
	return blockedOnPerson(state, askPending, approvalPending)
}

// blockedOnPerson reports a session waiting on a person: a pending question on
// an awaiting turn, or a pending approval on a turn an approval can still
// answer (including one it promotes mid-tool). A warning or restart needs
// attention but is not blocked on a person.
func blockedOnPerson(state string, askPending, approvalPending bool) bool {
	if state == "awaiting" && askPending {
		return true
	}
	return ApprovalWaiting(state, approvalPending)
}

// AttentionRank maps a normalized state to a sort key for live-session
// ordering. Higher rank sorts first (most attention-needing first). A pending
// question or approval keeps "awaiting" at the top of the live order; an
// awaiting turn with neither is finished and rests at idle's rank (#2629).
func AttentionRank(state string, askPending, approvalPending bool) int {
	switch {
	case state == "errored":
		return 5
	case blockedOnPerson(state, askPending, approvalPending):
		return 4
	case state == "active":
		return 3
	case state == "warning", state == "restartRequired":
		return 2
	case state == "awaiting", state == "idle":
		return 1
	default: // "ended" and unknown
		return 0
	}
}

// RollupRank maps a normalized state to a sort key for a project's rollup
// dot, where a warning outranks a merely-active child (a stuck warning
// surfaces before routine activity). Deliberately different ordering from
// AttentionRank — kept in the same file so the two rank tables never drift
// apart without a reviewer noticing. A finished turn does not raise a
// project's attention rollup (#2629).
func RollupRank(state string, askPending, approvalPending bool) int {
	switch {
	case state == "errored":
		return 5
	case blockedOnPerson(state, askPending, approvalPending):
		return 4
	case state == "warning", state == "restartRequired":
		return 3
	case state == "active":
		return 2
	case state == "awaiting", state == "idle":
		return 1
	default:
		return 0
	}
}

// RollupRankWord ranks a project's already-collapsed rollup state word. The
// word carries its own actionable fact: a finished awaiting turn is reported
// as "idle" (see hubcore's rollupSessions), so any remaining "awaiting" word
// is a question or an approval and ranks in the blocked band. Navigation
// projection merges project groups across hosts by this word alone.
func RollupRankWord(state string) int {
	return RollupRank(state, state == "awaiting", false)
}

// NeedsResponse reports whether a normalized state is the needs_response
// rest: awaiting with no pending question. The rest is informative, not
// actionable: a finished turn is Idle and does not enter Needs you (#2629).
func NeedsResponse(state string, askPending bool) bool {
	return state == "awaiting" && !askPending
}

// NeedsYouBand ranks a needs-you row into one of three ordering bands:
// errored (2, "broken beats blocked"), blocked on a person (1: a question or
// an approval), or everything else in the tier (0: a warning or a restart).
// Callers sort NeedsYou rows by this band descending, then by recency within a
// band. Meaningful only for the needs-you tier (errored, warning,
// restartRequired, and the awaiting states a question or approval makes
// actionable); callers outside that tier should not invoke it. A turn that
// merely ended on needs_response is finished, not blocked, and is not in the
// tier (#2629). An approval blocks mid-turn, so its session still reports
// "active" and only approvalPending can place it; pass false where the caller
// has no approval information. Both flags are ignored when state is "errored"
// (errored always wins regardless), and approvalPending counts only where
// ApprovalWaiting lets it escalate.
func NeedsYouBand(state string, askPending, approvalPending bool) int {
	switch {
	case state == "errored":
		return 2
	case askPending || ApprovalWaiting(state, approvalPending):
		return 1
	default:
		return 0
	}
}
