package appwire

// AttentionEntry is one live session's derived attention level plus the
// labels notification clients need. Levels: "working" | "needs_you" |
// "error" | "idle" (spec v5 semantics table). Computed by
// cmd/serf-hub/internal/hubcore.DeriveAttention from hub-internal inputs
// (archive decisions, live roster entries) that hubcore must not expose to
// appwire — only this plain-data result crosses the boundary, in the
// direction hubcore already imports (kata 4j2t).
type AttentionEntry struct {
	// serf:naming-ignore
	ID      string `json:"threadId"`
	Title   string `json:"title"`
	Project string `json:"project"`
	Level   string `json:"level"`
	// serf:naming-ignore
	AskPending bool `json:"askPending,omitempty"`
}

// AttentionSummary is the authoritative badge count set, computed over the
// tier-eligible population (hubcore.DeriveAttention's doc has the full
// definition). camelCase: see AttentionEntry.
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

// AttentionChangedPayload is the serf/attention/changed notification body,
// emitted by hubcore.AttentionWatcher.Tick.
type AttentionChangedPayload struct {
	Changed []AttentionChanged `json:"changed"`
	Summary AttentionSummary   `json:"summary"`
}
