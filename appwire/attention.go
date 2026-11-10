package appwire

import "slices"

// AttentionEntry is one live session's derived attention level plus the
// labels notification clients need. Levels: "working" | "needs_you" |
// "error" | "idle" (spec v5 semantics table). Computed by
// cmd/evener-hub/internal/hubcore.DeriveAttention from hub-internal inputs
// (archive decisions, live roster entries) that hubcore must not expose to
// appwire — only this plain-data result crosses the boundary, in the
// direction hubcore already imports (kata 4j2t).
type AttentionEntry struct {
	Ref string `json:"ref,omitempty"`
	// RosterInstanceID identifies the session occupying Ref in roster updates.
	// Clear changes it without changing the workspace ref or threadId.
	RosterInstanceID string `json:"rosterInstanceId,omitempty"`
	ID               string `json:"threadId"`
	Title            string `json:"title"`
	Project          string `json:"project"`
	Level            string `json:"level"`
	AskPending       bool   `json:"askPending,omitempty"`
	// ApprovalPending is true while the session is blocked on a sandbox
	// escalation a human must allow or deny (M7). It is why an
	// escalation-promoted session's Level is needs_you; AskPending is the
	// question's equivalent.
	ApprovalPending bool `json:"approvalPending,omitempty"`
	// NeedsResponse is true while the session rests awaiting with no pending
	// question: its turn ended with end_reason needs_response. The rest is
	// informative rather than actionable — a plain reply and a finished turn
	// both rest idle, and neither makes the session needs_you (#2629).
	// Additive: an older hub omits it, decoding as false.
	NeedsResponse     bool             `json:"needsResponse,omitempty"`
	PendingQuestion   *PendingQuestion `json:"pendingQuestion,omitempty"`
	AskBootGeneration string           `json:"askBootGeneration,omitempty"`
	AskRevision       uint64           `json:"askRevision,omitempty"`
	CompositeList     []PendingAskCall `json:"compositeList,omitempty"`
	CompositeTotal    int              `json:"compositeTotal,omitempty"`
	CalllessCount     int              `json:"calllessCount,omitempty"`
	AskPageable       bool             `json:"askPageable,omitempty"`
	// PublishedAt is the derivation moment, in Unix milliseconds.
	PublishedAt int64 `json:"publishedAt,omitempty"`
}

// EqualAttentionEntry compares values, treating absent and empty lists alike.
func EqualAttentionEntry(a, b AttentionEntry) bool {
	if a.Ref != b.Ref || a.RosterInstanceID != b.RosterInstanceID || a.ID != b.ID ||
		a.Title != b.Title || a.Project != b.Project || a.Level != b.Level ||
		a.AskPending != b.AskPending || a.ApprovalPending != b.ApprovalPending || a.NeedsResponse != b.NeedsResponse ||
		a.AskBootGeneration != b.AskBootGeneration || a.AskRevision != b.AskRevision ||
		a.CompositeTotal != b.CompositeTotal || a.CalllessCount != b.CalllessCount ||
		a.AskPageable != b.AskPageable || a.PublishedAt != b.PublishedAt || !slices.Equal(a.CompositeList, b.CompositeList) {
		return false
	}
	if a.PendingQuestion == nil || b.PendingQuestion == nil {
		return a.PendingQuestion == b.PendingQuestion
	}
	x, y := a.PendingQuestion, b.PendingQuestion
	return x.Question == y.Question && x.Count == y.Count && x.TurnID == y.TurnID && x.CallID == y.CallID &&
		slices.Equal(x.Options, y.Options) && equalAttentionFlag(x.Complete, y.Complete) && equalAttentionFlag(x.Truncated, y.Truncated)
}

func equalAttentionFlag(a, b *bool) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
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
