package appwire

// RosterEvent is a top-level session change. Kind is new, needs_you, error,
// disappeared, or the initial snapshot's idle state.
type RosterEvent struct {
	AttentionEntry
	Kind string `json:"kind"`
}

// SessionRosterUpdate is one aggregate watch update, not a per-session watch.
type SessionRosterUpdate struct {
	Revision uint64        `json:"revision"`
	Initial  bool          `json:"initial,omitempty"`
	Resync   bool          `json:"resync,omitempty"`
	Events   []RosterEvent `json:"events,omitempty"`
}
