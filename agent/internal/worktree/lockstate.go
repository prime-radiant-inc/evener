package worktree

// Lock state machine — the normative summary of spec §5 "Lock state machine
// (normative summary)", implemented verbatim as a pure decision core. Every
// lock decision in the native-worktree tools reduces to Decide: given the
// event that is about to happen and the observed lock state of the target
// worktree, it returns the single lock action to take. The table in §5 is a
// 3-column grid (unlocked / own marker / foreign-or-reasonless); this file
// splits the "own marker" column into OwnSession and OwnDelegate so the same
// core serves both the session-occupancy events (whose owner is the session
// marker) and the delegate lifecycle events (whose owner is the serf:dlg:
// marker). ClassifyReason is what places a parsed lock reason into one of
// those states; Decide never parses — it only decides.

// LockState is the occupancy classification of a target worktree, relative to
// the actor deciding what to do with it (spec §5, columns of the table).
type LockState int

const (
	// Unlocked: the worktree carries no git lock (table column "Target
	// unlocked").
	Unlocked LockState = iota
	// OwnSession: locked with this session's own serf:<sid> marker (the
	// "own marker" column, for session-occupancy events).
	OwnSession
	// OwnDelegate: locked with the acting delegate/disposer's own
	// serf:dlg:<dlg>:<sid> marker (the "own marker" column, for the delegate
	// lifecycle events). For session-occupancy events a delegate marker is a
	// *live lane of ours* and is treated as foreign — see Decide and §4/§9.
	OwnDelegate
	// Foreign: locked with any marker that is not the actor's own — another
	// session, a delegate that is not the one acting, or a reasonless /
	// unparseable lock (spec §5: "A lock with no reason or a reason that
	// doesn't parse as a serf marker is foreign").
	Foreign
)

// LockEvent is the operation about to be performed on the target worktree.
// One event per row of the §5 table (the "hard crash" row is not an event —
// nobody is deciding anything during a crash, the lock simply stays; it has no
// LockEvent by design).
type LockEvent int

const (
	// EvCreate: `create` a brand-new managed worktree (§3, table row
	// "create").
	EvCreate LockEvent = iota
	// EvLeave: leave the currently-occupied worktree — `switch`-away,
	// `create`-away (§3 step 7), `exit`, or clean close (table row "leave old
	// worktree").
	EvLeave
	// EvEnter: enter a worktree by name, or by a path that resolves inside the
	// managed dir (§4 switch, table row "enter").
	EvEnter
	// EvEnterCurrent: `switch` to the worktree the session already occupies
	// (§4 switch step 1, table row "switch to the worktree already occupied").
	EvEnterCurrent
	// EvRestoreLand: a restore lands the session's env in a managed worktree
	// without going through `switch` — `exit`'s restore root, or
	// `remove`-current's restore (§4 exit step 4; §5 "Restores follow the same
	// rule", table row "restore landing in a managed worktree").
	EvRestoreLand
	// EvInitInside: session init whose launch cwd is already inside a managed
	// worktree (§5 occupancy locks, table row "session init, launch cwd
	// inside").
	EvInitInside
	// EvResumeReenter: resume re-entry into the persisted active worktree
	// (§7 "Persistence and resume", table row "resume re-entry").
	EvResumeReenter
	// EvRemoveTarget: `remove` a target the session is not currently inside
	// (§5 remove step 3, table row "remove target, session not inside").
	EvRemoveTarget
	// EvRemoveCurrent: `remove` the worktree the session is currently inside
	// (§5 remove step 7, table row "remove target, session inside").
	EvRemoveCurrent
	// EvDelegateCreate: parent-side creation of an isolated delegate lane
	// (§9 lifecycle step 1, table row "delegate creation").
	EvDelegateCreate
	// EvDelegateRevive: `delegate_send(on_idle:"start")` reviving a kept lane
	// (§7 delegate revival; §9 Guards, table row "delegate revival").
	EvDelegateRevive
	// EvDisposeUnchanged: close-time disposal of an unchanged delegate lane
	// (§9 lifecycle step 4, table row "disposal, unchanged lane").
	EvDisposeUnchanged
	// EvDisposeChanged: close-time disposal of a changed delegate lane
	// (§9 lifecycle step 4, table row "disposal, changed lane").
	EvDisposeChanged
	// EvPruneCandidate: `prune` evaluating a candidate managed worktree
	// (§5 prune sweep 1, table row "prune candidate").
	EvPruneCandidate
)

// LockAction is the single lock manipulation Decide prescribes. The caller
// maps it to git operations and control flow; Decide itself performs nothing.
type LockAction int

const (
	// ActNone: take no lock action. Covers the table's "no-op" (leave an
	// unlocked tree on leave), "leave untouched" (foreign tree on leave),
	// "proceed" (remove an unlocked target), "keep" (disposal of a changed
	// unlocked lane), and "eligible" (prune of an unlocked candidate — other
	// conditions still apply). The caller proceeds with whatever the event
	// otherwise entails; only the *lock* is left alone.
	ActNone LockAction = iota
	// ActLock: take this actor's lock on the (currently unlocked) tree
	// (`git worktree lock --reason <marker>`).
	ActLock
	// ActAdopt: the tree already carries our own marker — adopt it as a no-op
	// (the crash-resume case; a literal re-lock is fatal on git, so adopt
	// rather than re-lock).
	ActAdopt
	// ActUnlock: release our own lock (`git worktree unlock`).
	ActUnlock
	// ActUnlockProceed: the tree carries our own marker as crash residue while
	// we are not inside it — unlock it and proceed with the operation
	// (§5 remove step 3).
	ActUnlockProceed
	// ActRefuse: refuse the operation. Covers the table's explicit "refuse"
	// cells and every unreachable "—" cell — Decide is total and fails safe
	// (an unexpected (event, state) combination never panics and never
	// silently proceeds).
	ActRefuse
	// ActWarnCoOccupy: a restore or init cannot be refused (the session must
	// land somewhere), so on a foreign lock warn loudly and co-occupy
	// (§5 "Restores follow the same rule").
	ActWarnCoOccupy
	// ActRefuseToRestoreRoot: resume re-entry found the worktree foreign-locked
	// — do not re-enter; start at the restore root with a notice (§7). Resume
	// is the one restore that *can* refuse, because the restore root is a safe
	// alternative.
	ActRefuseToRestoreRoot
	// ActSkip: `prune` skips this candidate because it is locked, regardless of
	// owner (§5 prune sweep 1: the occupancy rule protects the current session,
	// other sessions, and live delegates with no creator comparison).
	ActSkip
	// ActAtomicAddLock: create the worktree and take the lock in one atomic
	// `git worktree add --lock --reason` (§3 step 6, §9 step 1). Atomicity
	// closes the mid-create window a separate add-then-lock would open.
	ActAtomicAddLock
)

// lastAction is the highest valid LockAction value; used by fuzz totality
// checks to bound Decide's range.
const lastAction = ActAtomicAddLock

// lastState is the highest valid LockState value; used to bound
// ClassifyReason's range in fuzz checks.
const lastState = Foreign

func (s LockState) String() string {
	switch s {
	case Unlocked:
		return "Unlocked"
	case OwnSession:
		return "OwnSession"
	case OwnDelegate:
		return "OwnDelegate"
	case Foreign:
		return "Foreign"
	default:
		return "LockState(?)"
	}
}

func (e LockEvent) String() string {
	switch e {
	case EvCreate:
		return "EvCreate"
	case EvLeave:
		return "EvLeave"
	case EvEnter:
		return "EvEnter"
	case EvEnterCurrent:
		return "EvEnterCurrent"
	case EvRestoreLand:
		return "EvRestoreLand"
	case EvInitInside:
		return "EvInitInside"
	case EvResumeReenter:
		return "EvResumeReenter"
	case EvRemoveTarget:
		return "EvRemoveTarget"
	case EvRemoveCurrent:
		return "EvRemoveCurrent"
	case EvDelegateCreate:
		return "EvDelegateCreate"
	case EvDelegateRevive:
		return "EvDelegateRevive"
	case EvDisposeUnchanged:
		return "EvDisposeUnchanged"
	case EvDisposeChanged:
		return "EvDisposeChanged"
	case EvPruneCandidate:
		return "EvPruneCandidate"
	default:
		return "LockEvent(?)"
	}
}

func (a LockAction) String() string {
	switch a {
	case ActNone:
		return "ActNone"
	case ActLock:
		return "ActLock"
	case ActAdopt:
		return "ActAdopt"
	case ActUnlock:
		return "ActUnlock"
	case ActUnlockProceed:
		return "ActUnlockProceed"
	case ActRefuse:
		return "ActRefuse"
	case ActWarnCoOccupy:
		return "ActWarnCoOccupy"
	case ActRefuseToRestoreRoot:
		return "ActRefuseToRestoreRoot"
	case ActSkip:
		return "ActSkip"
	case ActAtomicAddLock:
		return "ActAtomicAddLock"
	default:
		return "LockAction(?)"
	}
}

// Decide returns the lock action for an event against an observed lock state,
// implementing the §5 table verbatim. It is total: any unknown event or state
// (including out-of-range ints) yields ActRefuse rather than panicking.
//
// The split of the table's single "own marker" column into OwnSession and
// OwnDelegate follows the body text deliberately, per row:
//
//   - Session-occupancy events (EvLeave, EvEnter, EvEnterCurrent,
//     EvRestoreLand, EvInitInside, EvResumeReenter, EvRemoveTarget,
//     EvRemoveCurrent): the owner is the session marker, so OwnSession is the
//     "own marker" column. A delegate marker (OwnDelegate) on such a tree is a
//     *live lane of ours* and is handled like Foreign — §4 switch step 2 ("If
//     it is locked with a reason other than this session's own marker, refuse —
//     another session (or a delegate) is rooted there") and §9 Guards ("the
//     parent cannot `switch` into an isolated delegate's worktree at all while
//     the delegate exists").
//   - Delegate lifecycle events (EvDelegateRevive, EvDisposeUnchanged,
//     EvDisposeChanged): the owner is the serf:dlg: marker, so OwnDelegate is
//     the "own marker" column (§9 step 4; §5 table note "the dlg lock is the
//     disposer's"). A plain session marker (OwnSession) on such a tree is not
//     the reviver's/disposer's own → treated as Foreign.
//   - Create events (EvCreate, EvDelegateCreate): the tree does not exist yet,
//     so every locked state is unreachable ("—") and refuses.
//   - EvPruneCandidate: owner-independent — any lock skips.
func Decide(ev LockEvent, st LockState) LockAction {
	switch ev {

	case EvCreate:
		// Row "create (new worktree)": unlocked → atomic add --lock; the
		// worktree does not exist yet, so every locked state is unreachable
		// ("—", the branch-exists error fired earlier).
		switch st {
		case Unlocked:
			return ActAtomicAddLock
		default:
			return ActRefuse
		}

	case EvLeave:
		// Row "leave old worktree (switch-away, create-away, exit, clean
		// close)": unlocked → no-op; own marker → unlock; foreign → leave
		// untouched. The owner is the session marker; a delegate marker is not
		// the leaving session's to release, so it is left untouched like a
		// foreign lock.
		switch st {
		case Unlocked:
			return ActNone // no-op
		case OwnSession:
			return ActUnlock
		case OwnDelegate:
			return ActNone // live lane of ours: leave untouched (foreign column)
		case Foreign:
			return ActNone // leave untouched
		default:
			return ActRefuse
		}

	case EvEnter:
		// Row "enter (switch by name, or by path resolving managed)":
		// unlocked → lock; own marker → adopt (incl. crash residue); foreign →
		// refuse. §4 switch step 2: a delegate marker means a delegate is
		// rooted there → refuse.
		switch st {
		case Unlocked:
			return ActLock
		case OwnSession:
			return ActAdopt
		case OwnDelegate:
			return ActRefuse // live delegate lane (§9 Guards)
		case Foreign:
			return ActRefuse
		default:
			return ActRefuse
		}

	case EvEnterCurrent:
		// Row "switch to the worktree already occupied": own marker → no-op,
		// lock kept; unlocked and foreign are unreachable ("—") — a session
		// already occupying a tree holds its own session lock (§4 switch
		// step 1).
		switch st {
		case OwnSession:
			return ActNone // no-op, lock kept
		default:
			return ActRefuse // "—" unreachable, and OwnDelegate: delegates cannot switch
		}

	case EvRestoreLand:
		// Row "restore landing in a managed worktree (exit, remove-current)":
		// unlocked → lock; own marker → adopt; foreign → warn + co-occupy.
		// A restore cannot be refused (§5 "Restores follow the same rule"); a
		// delegate marker is not the session's own → warn + co-occupy.
		switch st {
		case Unlocked:
			return ActLock
		case OwnSession:
			return ActAdopt
		case OwnDelegate:
			return ActWarnCoOccupy
		case Foreign:
			return ActWarnCoOccupy
		default:
			return ActRefuse
		}

	case EvInitInside:
		// Row "session init, launch cwd inside a managed worktree": unlocked →
		// lock; own marker → adopt (resumed same id); foreign → warn +
		// co-occupy (§5 occupancy locks; the init-time foreign-lock behavior
		// the restore rule mirrors).
		switch st {
		case Unlocked:
			return ActLock
		case OwnSession:
			return ActAdopt
		case OwnDelegate:
			return ActWarnCoOccupy
		case Foreign:
			return ActWarnCoOccupy
		default:
			return ActRefuse
		}

	case EvResumeReenter:
		// Row "resume re-entry (§7)": unlocked → lock; own marker → adopt;
		// foreign → refuse re-entry, start at restore root + notice. Resume is
		// the one restore that can refuse (the restore root is a safe
		// alternative). A delegate marker is foreign to the resuming session.
		switch st {
		case Unlocked:
			return ActLock
		case OwnSession:
			return ActAdopt
		case OwnDelegate:
			return ActRefuseToRestoreRoot
		case Foreign:
			return ActRefuseToRestoreRoot
		default:
			return ActRefuse
		}

	case EvRemoveTarget:
		// Row "remove target, session not inside": unlocked → proceed; own
		// marker → unlock (crash residue) + proceed; foreign → refuse (force
		// does not override, §5 remove step 3). A delegate marker is not the
		// removing session's own → refuse.
		switch st {
		case Unlocked:
			return ActNone // proceed (no lock obstacle)
		case OwnSession:
			return ActUnlockProceed
		case OwnDelegate:
			return ActRefuse // not our session marker (§5 remove step 3)
		case Foreign:
			return ActRefuse
		default:
			return ActRefuse
		}

	case EvRemoveCurrent:
		// Row "remove target, session inside": unlocked → proceed; own marker →
		// unlock at the restore step; foreign → unreachable ("—") — a session
		// inside a worktree holds its own lock (§5 remove step 7).
		switch st {
		case Unlocked:
			return ActNone // proceed
		case OwnSession:
			return ActUnlock // unlock at the restore step
		case OwnDelegate:
			return ActRefuse // "—" unreachable
		case Foreign:
			return ActRefuse // "—" unreachable
		default:
			return ActRefuse
		}

	case EvDelegateCreate:
		// Row "delegate creation (§9)": unlocked → atomic add --lock with
		// serf:dlg: marker; every locked state is unreachable ("—") — the lane
		// is named for a fresh delegate id and does not exist yet.
		switch st {
		case Unlocked:
			return ActAtomicAddLock
		default:
			return ActRefuse
		}

	case EvDelegateRevive:
		// Row "delegate revival (delegate_send on a kept lane)": unlocked →
		// lock (serf:dlg:); own marker → adopt; foreign → refuse revival
		// (§7 delegate revival; §9 Guards). Here the "own marker" is the
		// delegate's own serf:dlg: marker (OwnDelegate); a plain session marker
		// (OwnSession) means someone switched in → foreign → refuse.
		switch st {
		case Unlocked:
			return ActLock
		case OwnDelegate:
			return ActAdopt
		case OwnSession:
			return ActRefuse // someone switched into the kept lane
		case Foreign:
			return ActRefuse
		default:
			return ActRefuse
		}

	case EvDisposeUnchanged:
		// Row "disposal, unchanged lane": unlocked → unlock (vacuous) → remove;
		// own marker → unlock → remove; foreign → "—" (the dlg lock is the
		// disposer's). Owner is the serf:dlg: marker (OwnDelegate). The
		// unlocked cell is crash residue (§9 step 4: "a crash after unlock but
		// before remove leaves an unlocked unchanged lane → prune collects
		// it"); there is no lock to release, so the *lock action* is ActNone
		// and the caller proceeds straight to remove. (Returning ActUnlock
		// here would drive `git worktree unlock` on an already-unlocked tree,
		// which git reports as fatal — the table's "vacuous" flags exactly
		// this.)
		switch st {
		case Unlocked:
			return ActNone // vacuous unlock; proceed to remove
		case OwnDelegate:
			return ActUnlock // unlock → remove
		case OwnSession:
			return ActRefuse // "—": not the disposer's dlg marker
		case Foreign:
			return ActRefuse // "—"
		default:
			return ActRefuse
		}

	case EvDisposeChanged:
		// Row "disposal, changed lane": unlocked → keep; own marker → unlock,
		// keep; foreign → "—" (the dlg lock is the disposer's). Owner is the
		// serf:dlg: marker (OwnDelegate). Changed lanes stay resumable; only
		// the lock is released.
		switch st {
		case Unlocked:
			return ActNone // keep
		case OwnDelegate:
			return ActUnlock // unlock, keep
		case OwnSession:
			return ActRefuse // "—": not the disposer's dlg marker
		case Foreign:
			return ActRefuse // "—"
		default:
			return ActRefuse
		}

	case EvPruneCandidate:
		// Row "prune candidate": unlocked → eligible (other conditions apply);
		// any locked state → skip. Owner-independent (§5 prune sweep 1: "not
		// locked" protects the current session, other sessions, and live
		// delegates with no creator comparison).
		switch st {
		case Unlocked:
			return ActNone // eligible — other prune conditions still apply
		case OwnSession, OwnDelegate, Foreign:
			return ActSkip
		default:
			return ActRefuse
		}

	default:
		return ActRefuse
	}
}

// ClassifyReason places a lock reason into a LockState relative to the actor
// identified by ownSID (the acting session id) and ownDlgID (the acting
// delegate id, empty when the actor is a session rather than a delegate or a
// disposer acting on a specific lane).
//
// ClassifyReason is only meaningful for a *locked* tree: it answers "whose
// lock is this?". An unlocked tree is LockState Unlocked by construction and
// callers must not route it here — an empty reason is a reasonless (bare `git
// worktree lock`) lock, which §5 classifies as Foreign, NOT as Unlocked. So
// callers check locked-ness first (from the porcelain `locked` line) and call
// ClassifyReason only on the reason of an actual lock.
//
// Attribution (spec §5, using ParseMarker's strict decode):
//   - a session marker serf:<sid> with sid == ownSID → OwnSession;
//   - a delegate marker serf:dlg:<dlg>:<sid> with dlg == ownDlgID (and
//     ownDlgID != "") → OwnDelegate;
//   - anything else → Foreign. This includes a delegate marker whose parent
//     sid == ownSID but whose delegate id is NOT ours: that is a live delegate
//     lane of our own session, and for occupancy purposes it is foreign — the
//     dlg lock belongs to that delegate's disposal lifecycle, and §9's guard
//     refuses the parent entering/switching into it. It also includes a
//     reasonless or unparseable lock (ParseMarker returns false).
func ClassifyReason(reason, ownSID, ownDlgID string) LockState {
	m, ok := ParseMarker(reason)
	if !ok {
		return Foreign // reasonless or non-serf lock (§5)
	}
	if m.DelegateID == "" {
		// Plain session marker serf:<sid>.
		if ownSID != "" && m.SessionID == ownSID {
			return OwnSession
		}
		return Foreign
	}
	// Delegate marker serf:dlg:<dlg>:<sid>. Ours only if we are acting as that
	// exact delegate; a different delegate of our own session is foreign.
	if ownDlgID != "" && m.DelegateID == ownDlgID {
		return OwnDelegate
	}
	return Foreign
}
