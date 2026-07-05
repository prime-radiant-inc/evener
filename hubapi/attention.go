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
