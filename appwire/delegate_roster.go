package appwire

import "unicode/utf8"

// DelegateRosterTextMaxRunes caps the brief text a thread/read roster row
// carries. Clients label a delegate with the first line of its brief; the whole
// brief is in the activity tree (jobs/list) and the delegate's own transcript.
const DelegateRosterTextMaxRunes = 256

// SlimDelegateForRoster returns d as a thread/read roster row. No client reads
// the final message, structured result or packet kind from the roster
// (delegate/updated and jobs/list still carry them), and the delegate runtime
// stores the brief as both Task and Description, so one capped copy is kept.
func SlimDelegateForRoster(d EvenerDelegateInfo) EvenerDelegateInfo {
	d.Task = truncateRosterText(d.Task)
	d.Description = truncateRosterText(d.Description)
	if d.Task == d.Description {
		d.Task = ""
	}
	d.PacketKind = ""
	d.Message = nil
	d.StructuredResult = nil
	return d
}

func truncateRosterText(s string) string {
	if len(s) <= DelegateRosterTextMaxRunes || utf8.RuneCountInString(s) <= DelegateRosterTextMaxRunes {
		return s
	}
	runes := []rune(s)
	return string(runes[:DelegateRosterTextMaxRunes-1]) + "…"
}
