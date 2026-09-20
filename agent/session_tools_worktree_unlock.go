package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/worktree"
)

// WorktreeUnlockResult is the structured outcome of a successful unlock
// operation (issue #481): the released delegate lane's id, path, and branch.
type WorktreeUnlockResult struct {
	DelegateID string
	LanePath   string
	Branch     string
	Message    string
}

// worktreeUnlock implements the model-facing unlock operation (issue #481): it
// releases the evener:dlg:<delegate>:<parent> occupancy lock on a direct
// worktree-isolated delegate's lane WITHOUT retiring the delegate. Unlike
// dispose — the only previous releaser, which also closes resumability and may
// remove the worktree — unlock touches neither the delegate's resumability nor
// the lane itself, so a parent can switch into a delegate's lane and recover
// its uncommitted work when the delegate can no longer be revived.
//
// The parent is the legitimate authority for this lock: the marker records the
// parent's own session id as the owner (spec §5 "Occupancy locks"), and
// stableWorktreeSnapshotForOwner further requires the delegate to be a direct
// child of this session before any lock is touched.
//
// SAFETY — a running delegate's lane cannot be stolen. Unlock clears the same
// quiescence ladder dispose does (delegateLaneLiveWork: running/open/outstanding
// subtree work, armed or pending watch sends, live shells rooted in the lane),
// then arms the same dispose gate (trySetDisposeGate) so a drive or resume that
// races the check cannot start between the snapshot and the unlock, and
// re-verifies quiescence under the gate before releasing the lock. Only an idle
// delegate's lock can be released, so a healthy delegate that is still doing
// work keeps its lane. An idle-but-resumable delegate may still be released:
// that hands the lane to the parent by design, and a later revival attempt is
// refused by the existing EvDelegateRevive rule (a session marker on the lane is
// foreign to the reviver), so the two operations cannot both claim the lane.
func (s *Session) worktreeUnlock(ctx context.Context, id string) (WorktreeUnlockResult, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return WorktreeUnlockResult{}, errors.New("invalid_request: manage_worktree unlock: id is required")
	}
	if !isDelegateID(id) {
		return WorktreeUnlockResult{}, fmt.Errorf("invalid_request: manage_worktree unlock: %q is not a delegate id", id)
	}
	// Same session-close admission/join gate dispose holds (issue #481 review):
	// unlock mutates delegate lane state (git lock, sidecar mtime, controller
	// fence), so a closing session must refuse it and Close() must join an
	// admitted one before draining the lane.
	if !s.beginDispose() {
		return WorktreeUnlockResult{}, errors.New("manage_worktree unlock: session is closing")
	}
	defer s.endDispose()
	return s.unlockStableDelegateLane(ctx, id)
}

// unlockStableDelegateLane performs the unlock from the stable delegate
// descriptor and controller state, mirroring disposeStableDelegateLane's
// ownership and control-env resolution without the disposal ladder.
func (s *Session) unlockStableDelegateLane(ctx context.Context, id string) (WorktreeUnlockResult, error) {
	if s.delegateController == nil {
		return WorktreeUnlockResult{}, errors.New("manage_worktree unlock: stable delegate controller is unavailable")
	}
	// Fence the delegate against a start for the whole unlock critical section.
	// This is the controller-level barrier that covers a COLD delegate (no
	// resident subagent to carry a dispose gate) as well as a resident one: a
	// concurrent delegate_send's ReserveStart refuses while it is held, and this
	// refuses when a start reservation already exists.
	if !s.delegateController.beginLaneHandoff(id) {
		return WorktreeUnlockResult{}, errors.New("manage_worktree unlock: the delegate is starting or already has a start in flight; retry once it is idle")
	}
	defer s.delegateController.endLaneHandoff(id)
	state, err := s.delegateController.stableWorktreeSnapshotForOwner(s, id)
	if err != nil {
		if errors.Is(err, errDelegateNotControllable) {
			return WorktreeUnlockResult{}, fmt.Errorf("invalid_request: manage_worktree unlock: %s is not a direct worktree-isolated delegate of this session", id)
		}
		return WorktreeUnlockResult{}, fmt.Errorf("invalid_request: manage_worktree unlock: %w", err)
	}
	lanePath := filepath.Clean(strings.TrimSpace(state.descriptor.WorkingDir))
	if lanePath == "" || lanePath == "." {
		return WorktreeUnlockResult{}, fmt.Errorf("manage_worktree unlock: %s has no recorded lane path", id)
	}
	if !laneWorktreePresent(lanePath) {
		return WorktreeUnlockResult{}, fmt.Errorf("manage_worktree unlock: %s lane at %s is not present", id, lanePath)
	}

	metaDir := metaDirForLane(lanePath)
	sc, scErr := worktree.ReadSidecar(metaDir, id)
	if scErr != nil {
		return WorktreeUnlockResult{}, fmt.Errorf("manage_worktree unlock: %s sidecar unreadable; cannot resolve its git control environment: %w", id, scErr)
	}
	originalRoot := strings.TrimSpace(sc.OriginalRoot)
	if originalRoot == "" {
		return WorktreeUnlockResult{}, fmt.Errorf("manage_worktree unlock: %s sidecar has no original_root; cannot resolve its git control environment", id)
	}
	// Provenance: the lane must still resolve to the main root its sidecar
	// records before we run git from that root (same refusal dispose applies).
	if local, ok := s.currentEnv().(*execenv.LocalExecutionEnvironment); ok {
		if laneMain, conflict := delegateLaneProvenanceConflict(local, lanePath, originalRoot); conflict {
			return WorktreeUnlockResult{}, fmt.Errorf("manage_worktree unlock: %s lane at %s resolves to main root %s but its sidecar records %s; refusing on a provenance mismatch", id, lanePath, laneMain, originalRoot)
		}
	}
	controlEnv, envErr := s.delegateDisposeControlEnv(originalRoot)
	if envErr != nil {
		return WorktreeUnlockResult{}, fmt.Errorf("manage_worktree unlock: %s: %w", id, envErr)
	}
	defer disposeUnadoptedScratch(controlEnv)
	budgetCtx, cancelBudget := ensureCloseBudget(ctx)
	defer cancelBudget()
	run := s.newWorktreeGitRunner(budgetCtx, controlEnv)

	// The safety gate: never release a lane out from under live work. Clear the
	// SAME quiescence ladder dispose clears (it is shared, not re-implemented) —
	// running/open/outstanding subtree work, armed/pending watches, live shells.
	sub, liveErr := s.delegateLaneLiveWork("unlock", id, state, lanePath)
	if liveErr != nil {
		return WorktreeUnlockResult{}, liveErr
	}
	// finalizing is live work the controller-level snapshot can miss (a run can
	// be marked idle while its finalizer still runs), so refuse it explicitly.
	if sub != nil && sub.finalizingSnapshot() {
		return WorktreeUnlockResult{}, fmt.Errorf("manage_worktree unlock: %s is still finalizing; wait for it to finish", id)
	}
	// unlock releases only the direct runtime; a resident descendant would stay
	// bound to its own lane and could collide with a later restore, so refuse
	// until the subtree is quiet (issue #481 review).
	if s.delegateController.delegateHasResidentDescendants(id) {
		return WorktreeUnlockResult{}, fmt.Errorf("manage_worktree unlock: %s still has resident descendant delegates; close or dispose them before unlocking its lane", id)
	}
	// Close the check→unlock race dispose closes with the same gate: a drive or
	// resume that raced ahead makes trySetDisposeGate refuse, and once armed the
	// gate freezes new drives/resumes (subagents.go driveSubagentNotificationTurn
	// and the delegate_send path both refuse while disposeGated). Re-verify
	// quiescence under the gate before releasing the lock.
	if sub != nil {
		if !sub.trySetDisposeGate() {
			return WorktreeUnlockResult{}, fmt.Errorf("manage_worktree unlock: %s became active while unlocking; retry once it is idle", id)
		}
		defer sub.clearDisposeGate()
		state, err = s.delegateController.stableWorktreeSnapshotForOwner(s, id)
		if err != nil {
			return WorktreeUnlockResult{}, fmt.Errorf("invalid_request: manage_worktree unlock: %w", err)
		}
		if _, liveErr := s.delegateLaneLiveWork("unlock", id, state, lanePath); liveErr != nil {
			return WorktreeUnlockResult{}, liveErr
		}
	}

	// Route the lock decision through the state machine. ClassifyReason is given
	// the delegate id as the acting delegate, so OwnDelegate means exactly "this
	// delegate's own evener:dlg: marker"; an unlocked lane, a session marker, or
	// a foreign lock all fail safe to a refusal.
	locked, reason, lockErr := lockStateOf(run, lanePath)
	if lockErr != nil {
		return WorktreeUnlockResult{}, fmt.Errorf("manage_worktree unlock: %s lock state could not be verified: %w", id, lockErr)
	}
	st := worktree.Unlocked
	if locked {
		st = worktree.ClassifyReason(reason, s.id, id)
	}
	if worktree.Decide(worktree.EvUnlockDelegate, st) != worktree.ActUnlock {
		detail := reason
		if !locked {
			detail = "unlocked"
		}
		return WorktreeUnlockResult{}, fmt.Errorf("manage_worktree unlock: %s lane at %s is not locked with its own evener:dlg: marker (%s); refusing", id, lanePath, detail)
	}
	// Refresh the sidecar mtime BEFORE releasing the lock, matching the
	// close-time unlock ordering (touchUnlockLaneTail). The lane is about to be
	// unlocked while its owner is still live and resumable, and the residue
	// sweeper's rule is "unlocked ⇒ no live owner": a stale sidecar would make
	// the lane instantly collectible the moment the lock drops, and its delegate
	// could be marked non-resumable. A failure here must ABORT while the lane is
	// still locked rather than expose a lane with a stale sidecar.
	if err := s.updateWorktreeSidecar(metaDir, id, func(*worktree.Sidecar) {}); err != nil {
		return WorktreeUnlockResult{}, fmt.Errorf("manage_worktree unlock: refreshing %s's sidecar before releasing its lane: %w", id, err)
	}
	if _, err := run("worktree", "unlock", lanePath); err != nil {
		return WorktreeUnlockResult{}, fmt.Errorf("manage_worktree unlock: releasing the lock on %s: %w", lanePath, err)
	}
	// Evict the resident idle child so nothing can drive it in the now-unlocked
	// lane (issue #481 review). Its resumability and the lane are untouched; a
	// later delegate_send restores it and re-applies the lane lock. Without this
	// a resident child stays bound to the lane and a later notification/attention
	// wake would run it alongside a parent that switched in — two writers in one
	// worktree, exactly what the occupancy lock prevents.
	if sub != nil && sub.sess != nil {
		// Non-terminal release: unlock is documented as non-destructive, so a
		// resident child is released WITHOUT the terminal close cascade (which
		// would, for a coordinator child, dispose its own sub-delegates' lanes).
		released := sub.sess
		if relErr := s.releaseChildRuntimeForUnlock(ctx, released); relErr != nil {
			// Do NOT remove the child or clear its runtime pointer on a partial
			// release: re-take the delegate's own marker so the lane is not left
			// loose under a runtime we could not release, and fail the unlock.
			if _, lockErr := run("worktree", "lock", "--reason", worktree.FormatDelegateMarker(id, s.id), lanePath); lockErr != nil {
				s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("manage_worktree unlock: re-locking delegate %s's lane after a failed runtime release also failed: %v", id, lockErr)})
			}
			return WorktreeUnlockResult{}, fmt.Errorf("manage_worktree unlock: releasing delegate %s's resident runtime failed; the lane lock was restored and the child retained: %w", id, relErr)
		}
		s.subagents.removeSession(state.descriptor.ChildSessionID, released)
		// Clear the controller's live pointer for the released runtime, or a
		// later delegate_send's fresh restore collides with it in AttachRuntime.
		s.delegateController.releaseDelegateRuntimePointer(id, released)
	}
	return WorktreeUnlockResult{
		DelegateID: id,
		LanePath:   lanePath,
		Branch:     id,
		Message: fmt.Sprintf(
			"Released delegate %s's isolation lane lock at %s without retiring it; the delegate's resumability and its worktree are untouched. The lane is now switchable like any managed worktree.",
			id, lanePath),
	}, nil
}
