package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

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
// then arms the same dispose gate so a drive or resume that races the check
// cannot start between the snapshot and the unlock. A resident runtime is
// released non-terminally BEFORE the lock is released, so the lane is never
// handed off while a child runtime is still resident (#2066), and the delegate
// keeps its durable identity and lane for a later revival.
func (s *Session) worktreeUnlock(ctx context.Context, id string) (WorktreeUnlockResult, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return WorktreeUnlockResult{}, errors.New("invalid_request: manage_worktree unlock: id is required")
	}
	if !isDelegateID(id) {
		return WorktreeUnlockResult{}, fmt.Errorf("invalid_request: manage_worktree unlock: %q is not a delegate id", id)
	}
	// Same session-close admission/join gate dispose holds: unlock mutates
	// delegate lane state (git lock, sidecar mtime, controller fence), so a
	// closing session must refuse it and Close() must join an admitted one
	// before draining the lane.
	if !s.beginDispose() {
		return WorktreeUnlockResult{}, errors.New("manage_worktree unlock: session is closing")
	}
	defer s.endDispose()
	return s.unlockDelegateLane(ctx, id)
}

// unlockDelegateLane performs the unlock from the stable delegate descriptor
// and controller state, mirroring disposeDelegateLane's ownership, control-env
// and quiescence handling without the disposal ladder.
func (s *Session) unlockDelegateLane(ctx context.Context, id string) (WorktreeUnlockResult, error) {
	if s.delegateController == nil {
		return WorktreeUnlockResult{}, errors.New("manage_worktree unlock: stable delegate controller is unavailable")
	}
	// Fence the whole subtree before the quiescence snapshot and keep it through
	// the lane release, exactly as dispose does: a point-in-time read cannot see
	// a cold-restore commit, and a descendant that starts after the read would run
	// in a lane being unlocked.
	if s.delegateController.beginSubtreeLaneHandoff(id) != laneHandoffAcquired {
		if ownerErr := s.delegateController.stableWorktreeOwnerError(s, id); ownerErr != nil {
			return WorktreeUnlockResult{}, worktreeDelegateOwnerError("unlock", id, ownerErr)
		}
		return WorktreeUnlockResult{}, fmt.Errorf("manage_worktree unlock: %s became active while unlocking; retry once it is idle", id)
	}
	defer s.delegateController.endSubtreeLaneHandoff(id)
	state, err := s.delegateController.stableWorktreeSnapshotForOwner(s, id)
	if err != nil {
		return WorktreeUnlockResult{}, worktreeDelegateOwnerError("unlock", id, err)
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
	if laneMain, conflict := s.delegateLaneProvenanceConflict(lanePath, originalRoot); conflict {
		return WorktreeUnlockResult{}, fmt.Errorf("manage_worktree unlock: %s lane at %s resolves to main root %s but its sidecar records %s; refusing on a provenance mismatch", id, lanePath, laneMain, originalRoot)
	}
	controlEnv, envErr := s.delegateLaneControlEnv(originalRoot)
	if envErr != nil {
		return WorktreeUnlockResult{}, fmt.Errorf("manage_worktree unlock: %s: %w", id, envErr)
	}
	defer endEnvironmentScratch(controlEnv)
	budgetCtx, cancelBudget := ensureCloseBudget(ctx)
	defer cancelBudget()
	run := s.newWorktreeGitRunner(budgetCtx, controlEnv)

	sub, liveErr := s.delegateLaneLiveWork("unlock", "unlocking", id, state, lanePath, true)
	if liveErr != nil {
		return WorktreeUnlockResult{}, liveErr
	}
	if state.runtime != nil && (sub == nil || sub.sess == nil) {
		// Fail closed rather than release a lane under a resident runtime this
		// operation cannot verify and evict.
		return WorktreeUnlockResult{}, untrackedUnlockRuntimeError(id)
	}
	// Unlock releases only the direct runtime. A resident descendant sharing the
	// lane would keep running there after the lock drops, so refuse until the
	// subtree is quiet rather than evicting a subtree this operation does not
	// own.
	if s.delegateController.delegateHasResidentDescendants(id) {
		return WorktreeUnlockResult{}, fmt.Errorf("manage_worktree unlock: %s still has resident descendant delegates; close or dispose them before unlocking its lane", id)
	}
	if sub != nil {
		// Close the check→release race with the same gate dispose uses. Once
		// armed, the gate freezes new drives/resumes; re-verify quiescence under
		// it before touching the lock.
		if !sub.trySetDisposeGate() {
			return WorktreeUnlockResult{}, fmt.Errorf("manage_worktree unlock: %s became active while unlocking; retry once it is idle", id)
		}
		defer sub.clearDisposeGate()
		state, err = s.delegateController.stableWorktreeSnapshotForOwner(s, id)
		if err != nil {
			return WorktreeUnlockResult{}, worktreeDelegateOwnerError("unlock", id, err)
		}
		var rechecked *subagent
		rechecked, liveErr = s.delegateLaneLiveWork("unlock", "unlocking", id, state, lanePath, true)
		if liveErr != nil {
			return WorktreeUnlockResult{}, liveErr
		}
		sub = rechecked
		if state.runtime != nil && (sub == nil || sub.sess == nil) {
			return WorktreeUnlockResult{}, untrackedUnlockRuntimeError(id)
		}
	}

	locked, reason, lockErr := lockStateOf(run, lanePath)
	if lockErr != nil {
		return WorktreeUnlockResult{}, fmt.Errorf("manage_worktree unlock: %s lock state could not be verified: %w", id, lockErr)
	}
	if !locked {
		return WorktreeUnlockResult{}, fmt.Errorf("manage_worktree unlock: %s lane at %s is not locked with its own evener:dlg: marker (unlocked); refusing", id, lanePath)
	}
	if worktree.Decide(worktree.EvUnlockDelegate, worktree.ClassifyReason(reason, s.id, id)) != worktree.ActUnlock {
		return WorktreeUnlockResult{}, fmt.Errorf("manage_worktree unlock: %s lane at %s is not locked with its own evener:dlg: marker (%s); refusing", id, lanePath, reason)
	}
	// Decide established that the lock is this delegate's own marker. The
	// marker also names the parent that owns the lane; only that parent may
	// release it.
	if marker, markerOK := worktree.ParseMarker(reason); !markerOK || marker.SessionID != s.id {
		return WorktreeUnlockResult{}, fmt.Errorf("manage_worktree unlock: %s lane at %s is locked with %s, whose recorded owner is not this parent; refusing", id, lanePath, reason)
	}

	// Refresh the sidecar mtime BEFORE the lock is released, matching the
	// close-time unlock tail: the residue sweeper's rule is "unlocked ⇒ no live
	// owner", and a stale sidecar would make this still-resumable delegate's lane
	// instantly collectible the moment the lock drops. A failure here aborts
	// while the lane is still locked and the runtime still resident.
	if err := s.updateWorktreeSidecar(metaDir, id, func(*worktree.Sidecar) {}); err != nil {
		return WorktreeUnlockResult{}, fmt.Errorf("manage_worktree unlock: refreshing %s's sidecar before releasing its lane: %w", id, err)
	}

	// Release the resident runtime while the lane lock is still held (#2066),
	// then unlock. A failed release leaves the child resident and the lock
	// held, so the lane is never handed off under a live runtime. A spent
	// teardown pass means an earlier unlock attempt already ran the one-time
	// release (and reported its failure after consuming the pass); the child is
	// closing, not live, so the retry may detach it and proceed (#4530).
	evicted := false
	if sub != nil && sub.sess != nil {
		released := sub.sess
		if relErr := released.releaseChildRuntimeForRetirement(ctx); relErr != nil && !errors.Is(relErr, errRetirementTeardownSpent) {
			return WorktreeUnlockResult{}, fmt.Errorf("manage_worktree unlock: releasing delegate %s's resident runtime failed; the lane lock is still held and the child retained: %w", id, relErr)
		}
		s.subagents.removeSession(state.descriptor.ChildSessionID, released)
		s.delegateController.detachReleasedLaneRuntime(id, released)
		evicted = true
	}

	if _, err := run("worktree", "unlock", lanePath); err != nil {
		suffix := ""
		if evicted {
			suffix = " (the resident runtime was already released; its resumability and worktree are untouched, and the lane remains locked)"
		}
		return WorktreeUnlockResult{}, fmt.Errorf("manage_worktree unlock: releasing the lock on %s: %w%s", lanePath, err, suffix)
	}
	return WorktreeUnlockResult{
		DelegateID: id,
		LanePath:   lanePath,
		Branch:     sc.BranchOrName(),
		Message: fmt.Sprintf(
			"Released delegate %s's isolation lane lock at %s without retiring it; the delegate's resumability, worktree, branch and uncommitted work are untouched. The lane is now switchable like any managed worktree, and a later send re-locks it.",
			id, lanePath),
	}, nil
}

func untrackedUnlockRuntimeError(id string) error {
	return fmt.Errorf("manage_worktree unlock: %s has a resident runtime that is not tracked in the subagent registry; refusing to release its lane", id)
}
