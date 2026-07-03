package worktree

import "testing"

// TestDecideEveryCell enumerates every cell of the §5 lock state machine table
// — 14 events × 4 states = 56 cells — one explicit case each, with a comment
// citing the table row (and the body-text section that governs the split of
// the table's single "own marker" column into OwnSession/OwnDelegate). This is
// the review artifact for Task 9: each expected action is the verbatim table
// cell, with unreachable "—" cells failing safe to ActRefuse.
func TestDecideEveryCell(t *testing.T) {
	cases := []struct {
		ev   LockEvent
		st   LockState
		want LockAction
		why  string
	}{
		// Row "create (new worktree)": unlocked → atomic add --lock; locked
		// states are "—" (branch-exists error fired earlier; the tree does not
		// exist yet).
		{EvCreate, Unlocked, ActAtomicAddLock, "create/unlocked: atomic add --lock --reason"},
		{EvCreate, OwnSession, ActRefuse, "create/own: — unreachable"},
		{EvCreate, OwnDelegate, ActRefuse, "create/own-dlg: — unreachable"},
		{EvCreate, Foreign, ActRefuse, "create/foreign: — unreachable"},

		// Row "leave old worktree (switch-away, create-away, exit, clean
		// close)": unlocked → no-op; own marker → unlock; foreign → leave
		// untouched. OwnDelegate is a live lane of ours → left untouched like
		// foreign (§3 step 7; §7 clean close).
		{EvLeave, Unlocked, ActNone, "leave/unlocked: no-op"},
		{EvLeave, OwnSession, ActUnlock, "leave/own: unlock"},
		{EvLeave, OwnDelegate, ActNone, "leave/own-dlg: live lane, leave untouched (foreign col)"},
		{EvLeave, Foreign, ActNone, "leave/foreign: leave untouched"},

		// Row "enter (switch by name, or by path resolving managed)":
		// unlocked → lock; own → adopt (incl. crash residue); foreign → refuse.
		// §4 switch step 2: a delegate marker → a delegate is rooted there →
		// refuse.
		{EvEnter, Unlocked, ActLock, "enter/unlocked: lock"},
		{EvEnter, OwnSession, ActAdopt, "enter/own: adopt (incl. crash residue)"},
		{EvEnter, OwnDelegate, ActRefuse, "enter/own-dlg: live delegate lane → refuse (§9 Guards)"},
		{EvEnter, Foreign, ActRefuse, "enter/foreign: refuse"},

		// Row "switch to the worktree already occupied": own → no-op, lock
		// kept; unlocked and foreign are "—" (§4 switch step 1). Delegates
		// cannot switch, so OwnDelegate is unreachable too.
		{EvEnterCurrent, Unlocked, ActRefuse, "enter-current/unlocked: — unreachable"},
		{EvEnterCurrent, OwnSession, ActNone, "enter-current/own: no-op, lock kept"},
		{EvEnterCurrent, OwnDelegate, ActRefuse, "enter-current/own-dlg: unreachable (delegates cannot switch)"},
		{EvEnterCurrent, Foreign, ActRefuse, "enter-current/foreign: — unreachable"},

		// Row "restore landing in a managed worktree (exit, remove-current)":
		// unlocked → lock; own → adopt; foreign → warn + co-occupy. A restore
		// cannot be refused (§5 "Restores follow the same rule"); OwnDelegate
		// is not the session's own marker → warn + co-occupy.
		{EvRestoreLand, Unlocked, ActLock, "restore/unlocked: lock"},
		{EvRestoreLand, OwnSession, ActAdopt, "restore/own: adopt"},
		{EvRestoreLand, OwnDelegate, ActWarnCoOccupy, "restore/own-dlg: warn + co-occupy (foreign col; cannot refuse)"},
		{EvRestoreLand, Foreign, ActWarnCoOccupy, "restore/foreign: warn + co-occupy"},

		// Row "session init, launch cwd inside a managed worktree": unlocked →
		// lock; own → adopt (resumed same id); foreign → warn + co-occupy
		// (§5 occupancy locks).
		{EvInitInside, Unlocked, ActLock, "init/unlocked: lock"},
		{EvInitInside, OwnSession, ActAdopt, "init/own: adopt (resumed same id)"},
		{EvInitInside, OwnDelegate, ActWarnCoOccupy, "init/own-dlg: warn + co-occupy (foreign col)"},
		{EvInitInside, Foreign, ActWarnCoOccupy, "init/foreign: warn + co-occupy"},

		// Row "resume re-entry (§7)": unlocked → lock; own → adopt; foreign →
		// refuse re-entry, start at restore root + notice. Resume is the one
		// restore that can refuse; OwnDelegate is foreign to the resuming
		// session.
		{EvResumeReenter, Unlocked, ActLock, "resume/unlocked: lock"},
		{EvResumeReenter, OwnSession, ActAdopt, "resume/own: adopt (crash case)"},
		{EvResumeReenter, OwnDelegate, ActRefuseToRestoreRoot, "resume/own-dlg: refuse re-entry → restore root (foreign col)"},
		{EvResumeReenter, Foreign, ActRefuseToRestoreRoot, "resume/foreign: refuse re-entry → restore root + notice"},

		// Row "remove target, session not inside": unlocked → proceed; own →
		// unlock (crash residue) + proceed; foreign → refuse, force does not
		// override (§5 remove step 3). OwnDelegate is not our session marker →
		// refuse.
		{EvRemoveTarget, Unlocked, ActNone, "remove-target/unlocked: proceed"},
		{EvRemoveTarget, OwnSession, ActUnlockProceed, "remove-target/own: unlock crash residue + proceed"},
		{EvRemoveTarget, OwnDelegate, ActRefuse, "remove-target/own-dlg: not our session marker → refuse (§5 remove step 3)"},
		{EvRemoveTarget, Foreign, ActRefuse, "remove-target/foreign: refuse; force does not override"},

		// Row "remove target, session inside": unlocked → proceed; own →
		// unlock at the restore step; foreign → "—" (session inside holds its
		// own lock, §5 remove step 7).
		{EvRemoveCurrent, Unlocked, ActNone, "remove-current/unlocked: proceed"},
		{EvRemoveCurrent, OwnSession, ActUnlock, "remove-current/own: unlock at restore step"},
		{EvRemoveCurrent, OwnDelegate, ActRefuse, "remove-current/own-dlg: — unreachable"},
		{EvRemoveCurrent, Foreign, ActRefuse, "remove-current/foreign: — unreachable"},

		// Row "delegate creation (§9)": unlocked → atomic add --lock with
		// serf:dlg: marker; locked states are "—" (fresh delegate-id lane does
		// not exist yet).
		{EvDelegateCreate, Unlocked, ActAtomicAddLock, "dlg-create/unlocked: atomic add --lock serf:dlg:"},
		{EvDelegateCreate, OwnSession, ActRefuse, "dlg-create/own: — unreachable"},
		{EvDelegateCreate, OwnDelegate, ActRefuse, "dlg-create/own-dlg: — unreachable"},
		{EvDelegateCreate, Foreign, ActRefuse, "dlg-create/foreign: — unreachable"},

		// Row "delegate revival (delegate_send on a kept lane)": unlocked →
		// lock (serf:dlg:); own → adopt; foreign → refuse revival. Here "own
		// marker" is the delegate's own serf:dlg: (OwnDelegate); a plain
		// session marker means someone switched in → foreign → refuse (§7, §9).
		{EvDelegateRevive, Unlocked, ActLock, "dlg-revive/unlocked: lock serf:dlg:"},
		{EvDelegateRevive, OwnSession, ActRefuse, "dlg-revive/own-session: someone switched in → refuse"},
		{EvDelegateRevive, OwnDelegate, ActAdopt, "dlg-revive/own-dlg: adopt our own dlg marker"},
		{EvDelegateRevive, Foreign, ActRefuse, "dlg-revive/foreign: refuse revival"},

		// Row "disposal, unchanged lane": unlocked → unlock (vacuous) →
		// remove; own → unlock → remove; foreign → "—" (the dlg lock is the
		// disposer's). Owner is OwnDelegate. Unlocked is crash residue: no lock
		// to release → ActNone (returning ActUnlock would drive `git worktree
		// unlock` on an unlocked tree = git fatal; the table's "vacuous" flags
		// this). §9 step 4 crash-window note.
		{EvDisposeUnchanged, Unlocked, ActNone, "dispose-unchanged/unlocked: vacuous unlock, proceed to remove"},
		{EvDisposeUnchanged, OwnSession, ActRefuse, "dispose-unchanged/own-session: not the disposer's dlg marker → —"},
		{EvDisposeUnchanged, OwnDelegate, ActUnlock, "dispose-unchanged/own-dlg: unlock → remove"},
		{EvDisposeUnchanged, Foreign, ActRefuse, "dispose-unchanged/foreign: — the dlg lock is the disposer's"},

		// Row "disposal, changed lane": unlocked → keep; own → unlock, keep;
		// foreign → "—". Owner is OwnDelegate; changed lanes stay resumable.
		{EvDisposeChanged, Unlocked, ActNone, "dispose-changed/unlocked: keep"},
		{EvDisposeChanged, OwnSession, ActRefuse, "dispose-changed/own-session: not the disposer's dlg marker → —"},
		{EvDisposeChanged, OwnDelegate, ActUnlock, "dispose-changed/own-dlg: unlock, keep"},
		{EvDisposeChanged, Foreign, ActRefuse, "dispose-changed/foreign: —"},

		// Row "prune candidate": unlocked → eligible (other conditions apply);
		// any locked → skip. Owner-independent (§5 prune sweep 1).
		{EvPruneCandidate, Unlocked, ActNone, "prune/unlocked: eligible, other conditions apply"},
		{EvPruneCandidate, OwnSession, ActSkip, "prune/own-session: locked → skip"},
		{EvPruneCandidate, OwnDelegate, ActSkip, "prune/own-dlg: locked → skip"},
		{EvPruneCandidate, Foreign, ActSkip, "prune/foreign: locked → skip"},
	}

	if len(cases) != 14*4 {
		t.Fatalf("expected 56 cells (14 events × 4 states), enumerated %d", len(cases))
	}

	for _, c := range cases {
		got := Decide(c.ev, c.st)
		if got != c.want {
			t.Errorf("Decide(%s, %s) = %s, want %s — %s", c.ev, c.st, got, c.want, c.why)
		}
	}
}

// TestDecideTotalOutOfRange confirms Decide fails safe to ActRefuse (never
// panics) for out-of-range event and state ints — the brief's totality
// requirement.
func TestDecideTotalOutOfRange(t *testing.T) {
	cases := []struct {
		ev LockEvent
		st LockState
	}{
		{LockEvent(-1), Unlocked},
		{LockEvent(999), OwnSession},
		{EvEnter, LockState(-1)},
		{EvEnter, LockState(999)},
		{EvPruneCandidate, LockState(42)},
		{LockEvent(-7), LockState(-7)},
	}
	for _, c := range cases {
		if got := Decide(c.ev, c.st); got != ActRefuse {
			t.Errorf("Decide(%d, %d) = %s, want ActRefuse (fail safe)", c.ev, c.st, got)
		}
	}
}

func TestClassifyReason(t *testing.T) {
	const (
		ownSID   = "01HXYZSESSION"
		otherSID = "01HXYZOTHER"
		ownDlg   = "dlg_01HXYZMINE"
		otherDlg = "dlg_01HXYZTHEIRS"
	)
	cases := []struct {
		name     string
		reason   string
		ownSID   string
		ownDlgID string
		want     LockState
	}{
		{
			name:   "own session marker",
			reason: FormatSessionMarker(ownSID),
			ownSID: ownSID,
			want:   OwnSession,
		},
		{
			name:     "own delegate marker",
			reason:   FormatDelegateMarker(ownDlg, ownSID),
			ownSID:   ownSID,
			ownDlgID: ownDlg,
			want:     OwnDelegate,
		},
		{
			name:   "another session's marker",
			reason: FormatSessionMarker(otherSID),
			ownSID: ownSID,
			want:   Foreign,
		},
		{
			name:     "another delegate's marker",
			reason:   FormatDelegateMarker(otherDlg, otherSID),
			ownSID:   ownSID,
			ownDlgID: ownDlg,
			want:     Foreign,
		},
		{
			// Our own session id parents this delegate lane, but it is NOT the
			// delegate we are acting as — a live lane of ours is foreign for
			// occupancy (§9 Guards; the brief's key nuance).
			name:     "our session but a different delegate",
			reason:   FormatDelegateMarker(otherDlg, ownSID),
			ownSID:   ownSID,
			ownDlgID: ownDlg,
			want:     Foreign,
		},
		{
			// A parent session (ownDlgID empty) meeting one of its delegates'
			// lanes: foreign — the parent cannot enter/switch a live lane.
			name:   "session actor meeting its own delegate lane",
			reason: FormatDelegateMarker(ownDlg, ownSID),
			ownSID: ownSID,
			want:   Foreign,
		},
		{
			name:   "reasonless lock (bare git worktree lock)",
			reason: "",
			ownSID: ownSID,
			want:   Foreign,
		},
		{
			name:   "non-serf garbage reason",
			reason: "held by hand",
			ownSID: ownSID,
			want:   Foreign,
		},
		{
			name:   "truncated delegate marker",
			reason: "serf:dlg:x",
			ownSID: ownSID,
			want:   Foreign,
		},
		{
			// Empty ownSID must never match a real (non-empty) session id.
			name:   "empty ownSID does not match",
			reason: FormatSessionMarker(ownSID),
			ownSID: "",
			want:   Foreign,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ClassifyReason(c.reason, c.ownSID, c.ownDlgID); got != c.want {
				t.Errorf("ClassifyReason(%q, %q, %q) = %s, want %s", c.reason, c.ownSID, c.ownDlgID, got, c.want)
			}
		})
	}
}
