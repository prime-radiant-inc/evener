package appwire

// AttentionEntry is one live session's derived attention level plus the
// labels notification clients need. Levels: "working" | "needs_you" |
// "error" | "idle" (spec v5 semantics table). Computed by
// cmd/evener-hub/internal/hubcore.DeriveAttention from hub-internal inputs
// (archive decisions, live roster entries) that hubcore must not expose to
// appwire — only this plain-data result crosses the boundary, in the
// direction hubcore already imports (kata 4j2t).
type AttentionEntry struct {
	ID         string `json:"threadId"`
	Title      string `json:"title"`
	Project    string `json:"project"`
	Level      string `json:"level"`
	AskPending bool   `json:"askPending,omitempty"`
	// ApprovalPending is true while the session is blocked on a sandbox
	// escalation a human must allow or deny (M7). It is why an
	// escalation-promoted session's Level is needs_you; AskPending is the
	// question's equivalent.
	ApprovalPending bool `json:"approvalPending,omitempty"`
	// NeedsResponse is true while the session rests awaiting with no pending
	// question: its turn ended with end_reason needs_response, asking for a
	// reply. A plain reply rests idle and never carries it. Additive: an
	// older hub omits it, decoding as false.
	NeedsResponse bool `json:"needsResponse,omitempty"`
}

// AttentionSummary is the authoritative badge count set, computed over the
// tier-eligible population (hubcore.DeriveAttention's doc has the full
// definition). camelCase: see AttentionEntry.
type AttentionSummary struct {
	NeedsYou int `json:"needsYou"`
	Error    int `json:"error"`
	Working  int `json:"working"`
}

// AttentionChanged is one session's level transition. camelCase: see
// AttentionEntry.
type AttentionChanged struct {
	AttentionEntry
	PrevLevel string `json:"prevLevel"`
}

// AttentionChangedPayload is the evener/attention/changed notification body,
// emitted by hubcore.AttentionWatcher.Tick.
type AttentionChangedPayload struct {
	Changed []AttentionChanged `json:"changed"`
	Summary AttentionSummary   `json:"summary"`
}
