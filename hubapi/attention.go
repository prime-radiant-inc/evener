// Package hubapi's attention.go is the single shared source of truth for
// attention-state ranking and display words, imported by both the hub
// (cmd/evener-hub/internal/hubcore, which cannot be imported directly by the
// TUI because it is an `internal` package scoped to cmd/evener-hub) and the
// TUI (cmd/evener-tui). Previously AttentionRank and rollupRank were
// duplicated in hubcore, and the TUI carried a third copy
// (attentionRankLabel) — this file is the one place that ordering logic
// lives now.
package hubapi

// AttentionState is the state a session's attention is judged by: its
// normalized state, except that a pending approval (a sandbox escalation, M7)
// reads as "awaiting". The escalation blocks mid-turn, so the session keeps
// reporting "active" while it waits on a person the way a question does. A
// failure still outranks it: "errored" stays "errored". Rank and level a
// session through this, never through its reported state alone, which stays
// unchanged on every row.
func AttentionState(state string, approvalPending bool) string {
	if approvalPending && state != "errored" {
		return "awaiting"
	}
	return state
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

// StateWord returns the unified display word for a normalized attention
// state — one word, shared verbatim by the web (cmd/evener-hub's stateLabel)
// and the TUI (displayWord) so the two surfaces can never independently
// drift on vocabulary (Track A §1). askPending selects the word for awaiting:
// "Question waiting" for a pending question, "Needs you" for a turn that
// ended on needs_response (#4093). It is ignored for every other state.
func StateWord(state string, askPending bool) string {
	switch state {
	case "errored":
		return "Error"
	case "awaiting":
		if NeedsResponse(state, askPending) {
			return "Needs you"
		}
		return "Question waiting"
	case "active":
		return "Working"
	case "restartRequired":
		return "Restart required"
	case "warning":
		return "Warning"
	case "idle":
		return "Idle"
	case "ended", "closed":
		return "Ended"
	case "notLoaded":
		return "Not loaded"
	default:
		return state
	}
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
// always wins regardless).
func NeedsYouBand(state string, askPending, approvalPending bool) int {
	switch {
	case state == "errored":
		return 2
	case state == "awaiting" || askPending || approvalPending:
		return 1
	default:
		return 0
	}
}
