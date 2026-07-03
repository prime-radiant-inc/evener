package worktree

import "testing"

// FuzzDecideTotal asserts Decide is total: for any (event, state) int pair —
// including out-of-range values — it returns without panicking and yields an
// action within the defined LockAction range. Totality is the brief's core
// safety property: an unknown combination must fail safe, never crash.
func FuzzDecideTotal(f *testing.F) {
	f.Add(0, 0)
	f.Add(int(EvPruneCandidate), int(Foreign))
	f.Add(-1, -1)
	f.Add(999, 999)
	f.Add(int(EvEnter), 42)

	f.Fuzz(func(t *testing.T, ev, st int) {
		got := Decide(LockEvent(ev), LockState(st))
		if got < ActNone || got > lastAction {
			t.Fatalf("Decide(%d, %d) = %d, out of LockAction range [%d,%d]", ev, st, got, ActNone, lastAction)
		}
	})
}

// FuzzClassifyReason asserts ClassifyReason never panics on arbitrary reason /
// id strings and always returns a valid LockState.
func FuzzClassifyReason(f *testing.F) {
	f.Add("serf:01HXYZ", "01HXYZ", "")
	f.Add("serf:dlg:dlg_1:01HXYZ", "01HXYZ", "dlg_1")
	f.Add("", "", "")
	f.Add("serf:dlg::", "sid", "dlg")
	f.Add("held by hand", "sid", "dlg")
	f.Add("serf:a:b:c:d", "sid", "dlg")

	f.Fuzz(func(t *testing.T, reason, ownSID, ownDlgID string) {
		got := ClassifyReason(reason, ownSID, ownDlgID)
		if got < Unlocked || got > lastState {
			t.Fatalf("ClassifyReason(%q, %q, %q) = %d, out of LockState range [%d,%d]", reason, ownSID, ownDlgID, got, Unlocked, lastState)
		}
	})
}
