package worktree

import "testing"

// FuzzParseMarker drives ParseMarker with arbitrary strings. Two invariants
// hold regardless of input: ParseMarker never panics, and whenever it
// reports a reason as genuine (ok == true), re-formatting the decoded
// Marker with FormatSessionMarker/FormatDelegateMarker reproduces the exact
// input string — the strict 2-or-4-segment parse in ParseMarker guarantees
// this (spec §5), since a genuine marker's segments never themselves
// contain ':'.
func FuzzParseMarker(f *testing.F) {
	seeds := []string{
		"serf:01HXYZABCD0123456789ABCDEF",
		"serf:dlg:dlg_01JXYZABCD0123456789ABCDEF:01HXYZABCD0123456789ABCDEF",
		"",
		"serf:",
		"serf",
		"serf:dlg:x",
		"serf:dlg::",
		"serf:dlg:a:b:c",
		"random text",
		"serf:a:b",
		" serf:01HXYZABCD0123456789ABCDEF",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, reason string) {
		m, ok := ParseMarker(reason) // must not panic regardless of outcome
		if !ok {
			return
		}
		var got string
		if m.DelegateID == "" {
			got = FormatSessionMarker(m.SessionID)
		} else {
			got = FormatDelegateMarker(m.DelegateID, m.SessionID)
		}
		if got != reason {
			t.Fatalf("ParseMarker(%q) = %+v; re-formatting gave %q, want %q", reason, m, got, reason)
		}
	})
}
