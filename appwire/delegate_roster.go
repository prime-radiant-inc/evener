package appwire

// DelegateRosterTextMaxRunes caps the brief text a thread/read roster row
// carries. Clients label a delegate with the first line of its brief; the whole
// brief is in the activity tree (jobs/list) and the delegate's own transcript.
const DelegateRosterTextMaxRunes = 256

// SlimDelegateForRoster returns d as a thread/read roster row. No client reads
// the final message, structured result or packet kind from the roster
// (delegate/updated and jobs/list still carry them), and the delegate runtime
// stores the brief as both Task and Description, so the Task copy is kept and
// Description is dropped when it repeats it (the TUI subagent rail names a row
// from Task alone). The bounded report preview and the logical-owner session id
// are activity-store data too: the roster omits report payloads by contract, and
// scoping is the activity read's, not the roster's.
func SlimDelegateForRoster(d EvenerDelegateInfo) EvenerDelegateInfo {
	if d.Description == d.Task {
		d.Description = ""
	}
	d.Task = truncateRosterText(d.Task)
	d.Description = truncateRosterText(d.Description)
	d.PacketKind = ""
	d.Message = nil
	d.StructuredResult = nil
	d.ReportPreview = ""
	d.ReportPreviewTruncated = false
	d.LogicalOwnerSessionID = ""
	return d
}

// truncateRosterText cuts s to DelegateRosterTextMaxRunes runes, ellipsis
// included, walking only as far into s as the cap.
func truncateRosterText(s string) string {
	if len(s) <= DelegateRosterTextMaxRunes {
		return s
	}
	cutAt, runes := 0, 0
	for i := range s {
		if runes == DelegateRosterTextMaxRunes-1 {
			cutAt = i
		}
		if runes == DelegateRosterTextMaxRunes {
			return s[:cutAt] + "…"
		}
		runes++
	}
	return s
}
