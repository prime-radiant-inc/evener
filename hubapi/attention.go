// Package hubapi's attention.go is the single shared source of truth for
// attention-state ranking and display words, imported by both the hub
// (cmd/serf-hub/internal/hubcore, which cannot be imported directly by the
// TUI because it is an `internal` package scoped to cmd/serf-hub) and the
// TUI (cmd/serf-tui). Previously AttentionRank and rollupRank were
// duplicated in hubcore, and the TUI carried a third copy
// (attentionRankLabel) — this file is the one place that ordering logic
// lives now.
package hubapi

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
	case "warning":
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
	case "warning":
		return 3
	case "active":
		return 2
	case "idle":
		return 1
	default:
		return 0
	}
}

// StateWord returns the unified display word for a normalized attention
// state — one word, shared verbatim by the web (cmd/serf-hub's stateLabel)
// and the TUI (displayWord) so the two surfaces can never independently
// drift on vocabulary (Track A §1). askPending selects between the two
// needs-you bands (Track A §2 ask-tiering) and is ignored for every other
// state.
func StateWord(state string, askPending bool) string {
	switch state {
	case "errored":
		return "Error"
	case "awaiting":
		if askPending {
			return "Question waiting"
		}
		return "Your move"
	case "active":
		return "Working"
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
// errored (2, "broken beats blocked"), ask-pending (1, "blocked beats
// your-move"), or your-move (0, a generic settle). Callers sort NeedsYou
// rows by this band descending, then by recency within a band. Meaningful
// only for the needs-you tier (errored/awaiting/warning states); callers
// outside that tier should not invoke it. askPending is ignored when state
// is "errored" (errored always wins regardless).
func NeedsYouBand(state string, askPending bool) int {
	switch {
	case state == "errored":
		return 2
	case askPending:
		return 1
	default:
		return 0
	}
}
