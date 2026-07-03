package worktree

import "strings"

// Marker is the decoded form of a serf occupancy-lock reason (spec §5
// "Occupancy locks"). DelegateID is empty for a plain session marker;
// non-empty for a delegate marker, where SessionID holds the delegate's
// parent session id (the id whose disposal lifecycle owns the lock).
type Marker struct {
	SessionID  string
	DelegateID string
}

// FormatSessionMarker renders the lock reason a session takes on a managed
// worktree it occupies directly (spec §5).
func FormatSessionMarker(sid string) string {
	return "serf:" + sid
}

// FormatDelegateMarker renders the lock reason for a delegate-created lane
// (spec §5): the delegate id names the occupant, the parent session id names
// the owner whose disposal lifecycle releases the lock.
func FormatDelegateMarker(dlgID, parentSID string) string {
	return "serf:dlg:" + dlgID + ":" + parentSID
}

// ParseMarker decodes a lock reason into a Marker. It returns false for any
// reason that is not a serf marker — including empty, git's own reasonless
// "locked" line, and anything a different tool or a human wrote (spec §5: "a
// lock with no reason or a reason that doesn't parse as a serf marker is
// foreign").
//
// Parsing is strict: serf session and delegate ids never contain ':' (they
// are generated as "ag_<ulid>" and "dlg_<ulid>" — see
// agent/session_model_call.go and agent/internal/jobstore/record.go), so a
// genuine marker is exactly "serf:<sid>" (2 colon-separated segments) or
// "serf:dlg:<dlg>:<sid>" (4 segments). Anything else — a truncated
// "serf:dlg:x", an over-long "serf:dlg:a:b:c", or a segment that is present
// but empty like "serf:dlg::" — is foreign rather than guessed at: a laxer
// parser that filled in a default for a missing or extra segment could
// misattribute a lock to the wrong owner.
func ParseMarker(reason string) (Marker, bool) {
	parts := strings.Split(reason, ":")
	if parts[0] != "serf" {
		return Marker{}, false
	}
	switch len(parts) {
	case 2:
		if parts[1] == "" {
			return Marker{}, false
		}
		return Marker{SessionID: parts[1]}, true
	case 4:
		if parts[1] != "dlg" || parts[2] == "" || parts[3] == "" {
			return Marker{}, false
		}
		return Marker{SessionID: parts[3], DelegateID: parts[2]}, true
	default:
		return Marker{}, false
	}
}
