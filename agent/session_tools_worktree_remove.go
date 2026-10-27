package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"primeradiant.com/evener/agent/internal/worktree"
)

// worktreeRemove performs the remove operation (spec §5 remove, all eleven
// steps in order). Removing the worktree directory (step 8) is the primary,
// always-or-error action; a delete_branch refusal (step 9) is reported in the
// result rather than failing the call, since by then the worktree is already
// gone and step 11 requires reporting whether the branch was deleted as part
// of a confirmation.
func (s *Session) worktreeRemove(ctx context.Context, name string, force, forceDirty, deleteBranch bool) (WorktreeRemoveResult, error) {
	// Step 1: resolve the worktree path from name.
	if err := worktree.ValidateName(name); err != nil {
		return WorktreeRemoveResult{}, fmt.Errorf("manage_worktree remove: %w", err)
	}
	st := s.worktreeStateSnapshot()
	if err := st.resolutionError("remove"); err != nil {
		return WorktreeRemoveResult{}, err
	}
	if st.env == nil {
		return WorktreeRemoveResult{}, errors.New("manage_worktree requires a local execution environment")
	}
	if st.mainRepoRoot == "" {
		return WorktreeRemoveResult{}, errors.New("manage_worktree remove: not in a git repository")
	}
	projectDir := filepath.Join(st.worktreeRoot, st.project.ID)
	metaDir := metaDirForProject(projectDir)
	target := filepath.Clean(filepath.Join(projectDir, filepath.FromSlash(name)))

	// Step 2: the target must be under <worktreeRoot>/<Project.ID>, canonicalized
	// as in `list` (spec §5 `list` step 2) — never remove an arbitrary path by
	// name. Structurally guaranteed already (ValidateName rejects ".."; target
	// is built by joining under projectDir), but checked explicitly for
	// defense in depth and for symlinked state homes.
	canonicalTarget := target
	if resolved, err := filepath.EvalSymlinks(target); err == nil {
		canonicalTarget = filepath.Clean(resolved)
	}
	if !isUnderManagedDir(canonicalTarget, projectDir) {
		return WorktreeRemoveResult{}, fmt.Errorf("manage_worktree remove: %s is not a managed worktree", name)
	}
	run, done, err := s.worktreeControlRun(ctx, st.mainRepoRoot)
	if err != nil {
		return WorktreeRemoveResult{}, err
	}
	defer done()

	// "Currently inside" is judged against the session's ACTIVE root, not the
	// create/switch-tracked worktreeCurrentPath, so that a session launched
	// directly inside a managed worktree (which applyInitInsideWorktreeLock
	// also stamps into worktreeCurrentPath, but which never went through
	// create/switch) is still recognized as inside. Both sides are
	// canonicalized the same way as canonicalTarget above so that a launch
	// path reaching the worktree through a differently-spelled symlink still
	// compares equal; step 7's "no safe restore env" refusal exists precisely
	// for the launched-inside case (spec §5 remove step 7's own example).
	activeRoot := canonicalOrClean(st.env.WorkingDirectory())
	currentlyInside := activeRoot == canonicalTarget

	// Step 3: lock guard. A foreign lock refuses regardless of force; this
	// session's own marker on a worktree it is not currently in is crash
	// residue — unlocked here and the operation proceeds (spec §5 remove step
	// 3). When the session IS currently inside the target, its own marker is
	// ordinary occupancy and the unlock (if any) happens at the restore, step
	// 7 below — not here (EvRemoveCurrent's own-marker row: "unlock at the
	// restore step").
	porcelain, err := readWorktreePorcelain(run)
	if err != nil {
		return WorktreeRemoveResult{}, fmt.Errorf("manage_worktree remove: inspecting the target lock: %w", err)
	}
	locked, reason := lockStateFromPorcelain(porcelain, target)
	lockSt := worktree.Unlocked
	if locked {
		lockSt = worktree.ClassifyReason(reason, s.id, "")
	}
	lockDetail := reason
	if lockDetail == "" {
		lockDetail = "no reason"
	}
	var unlockAtRestore bool
	if currentlyInside {
		switch worktree.Decide(worktree.EvRemoveCurrent, lockSt) {
		case worktree.ActNone:
			// unlocked; nothing to do here
		case worktree.ActUnlock:
			unlockAtRestore = true
		default:
			return WorktreeRemoveResult{}, fmt.Errorf("manage_worktree remove: %s is locked (%s); refusing", target, lockDetail)
		}
	} else {
		switch worktree.Decide(worktree.EvRemoveTarget, lockSt) {
		case worktree.ActNone:
			// unlocked; nothing to do here
		case worktree.ActUnlockProceed:
			if _, err := run("worktree", "unlock", target); err != nil {
				return WorktreeRemoveResult{}, fmt.Errorf("manage_worktree remove: unlocking crash-residue lock: %w", err)
			}
		default:
			return WorktreeRemoveResult{}, fmt.Errorf("manage_worktree remove: %s is locked (%s); refusing (force does not override a lock)", target, lockDetail)
		}
	}

	// Step 4: live child/job guard (spec §5 remove step 4, "widened"). Best
	// effort: liveWorkUnder scans running shell/delegate jobs and live
	// subagent envs (see its doc comment); a shell command that `cd`s after
	// launch is invisible to it.
	//
	// force-cascade addendum: when every blocker is a retained-idle, owned,
	// worktree-isolated delegate, force disposes each one through the
	// SANCTIONED dispose path (worktreeDispose — the exact op
	// `manage_worktree op=dispose` runs) rather than bypassing this guard.
	// This step only DECIDES eligibility and records it in
	// forceCascadeDlgIDs; it does NOT dispose anything yet — the dispose
	// calls are destructive to lanes OTHER than target, and running them
	// here, ahead of steps 5-7 (which can still refuse), would let a call
	// that ultimately refuses have already destroyed retained-idle lanes.
	// The cascade actually runs just before step 8, once steps 5-7 have all
	// passed (code-review finding I1;
	// TestWorktreeRemove_Force_CascadeRunsAfterOwnGatesPass pins it). Each
	// cascaded dispose keeps its OWN gates: it always runs with force=false,
	// forceDirty=false, so an unmerged or dirty lane still refuses on
	// dispose's own terms — remove's `force` means "run the sanctioned
	// cascade," never "invent a new force semantics for dispose." A
	// genuinely running job (not a retained-idle subagent) makes
	// retainedIdleDelegateIDs fail closed, so this block never records
	// anything and the live-execution refusal below is unchanged (spec
	// "Running jobs block removal, always").
	var forceCascadeDlgIDs []string
	if live := s.liveWorkUnder(target); len(live) > 0 {
		eligible := false
		if force {
			if dlgIDs, ok := s.retainedIdleDelegateIDs(live); ok {
				forceCascadeDlgIDs = dlgIDs
				eligible = true
			}
		}
		if !eligible {
			if hint := s.disposeHintForRetainedIdle(live); hint != "" {
				return WorktreeRemoveResult{}, fmt.Errorf("manage_worktree remove: live work under %s: %s. %s", target, strings.Join(live, ", "), hint)
			}
			return WorktreeRemoveResult{}, fmt.Errorf("manage_worktree remove: live work under %s: %s", target, strings.Join(live, ", "))
		}
	}

	// Step 5: provenance guard. A worktree with no sidecar has unknown
	// provenance and is treated as another session's — refuse without force
	// (spec §6 "Metadata sidecar"). The cross-session *creator* guard is NOT
	// applied here: it only ever fired for an UNLOCKED lane with a foreign
	// creator (a foreign-locked lane is already refused at step 3), and an
	// unlocked lane has no live owner — the occupancy lock is the real safety,
	// committed work is protected by the merge gate (step 9), and uncommitted
	// work by the force_dirty gate (step 6). Refusing routine cross-session
	// cleanup there was the dominant real-world friction (F1 ergonomics
	// finding) with little safety value, so the lane proceeds.
	sc, scErr := worktree.ReadSidecar(metaDir, name)
	hasSidecar := scErr == nil
	// The lane's git branch. The sidecar names it for a parent-named delegate
	// lane; with no sidecar, git's own worktree list names the checkout, and a
	// lane whose branch stays unresolved (sidecar gone, worktree bare or
	// detached) gets an empty branch rather than a guess from the directory
	// name: the directory name is not the branch of a named lane, and step 9
	// refuses to delete a branch it cannot attribute.
	branch := ""
	if hasSidecar {
		branch = sc.BranchOrName()
	} else if b, ok := branchOfCheckout(porcelain, target); ok {
		branch = b
	}
	if scErr != nil && !os.IsNotExist(scErr) {
		return WorktreeRemoveResult{}, fmt.Errorf("manage_worktree remove: reading metadata: %w", scErr)
	}
	if !force && !hasSidecar {
		return WorktreeRemoveResult{}, fmt.Errorf("manage_worktree remove: %s has no metadata sidecar (unmanaged provenance); refusing without force", target)
	}

	// Step 6: dirtiness preflight (force_dirty:false only). Gated on its OWN
	// flag, not force, so forcing past an ownership/merge gate cannot
	// collaterally discard an uncommitted edit unwarned (F3 ergonomics
	// finding: an agent forcing past a provenance refusal would otherwise
	// never see this — the more destructive condition was masked behind the
	// less destructive one). Nothing above has mutated s.env or removed
	// anything yet, so a refusal here leaves s.env unchanged (spec §5 remove
	// step 6).
	if !forceDirty {
		clean, offending, err := worktree.CleanTree(run, target)
		if err != nil {
			return WorktreeRemoveResult{}, fmt.Errorf("manage_worktree remove: checking for uncommitted changes: %w", err)
		}
		if !clean {
			return WorktreeRemoveResult{}, fmt.Errorf("manage_worktree remove: %s has uncommitted changes (use force_dirty to remove anyway):\n%s", target, strings.Join(offending, "\n"))
		}
	}

	// Step 7: if the session is currently in this worktree, unlock it (if
	// step 3 deferred an unlock) and restore s.env via worktreeGuard, applying
	// the idempotent restore-land lock rule to the landing spot when it is
	// itself a managed worktree (spec §5 "Restores follow the same rule";
	// remove-current's restore reuses exit's applyRestoreLandRelock
	// choreography verbatim). No safe restore env — for example, the session
	// started directly inside the worktree being removed — refuses rather
	// than deleting the active root out from under the session.
	var removeWarning string
	if currentlyInside {
		if st.restoreEnv == nil {
			return WorktreeRemoveResult{}, errors.New("manage_worktree remove: no safe restore env for the currently-occupied worktree (it was never entered via manage_worktree); refusing to remove the active root")
		}
		if unlockAtRestore {
			if _, err := run("worktree", "unlock", target); err != nil {
				return WorktreeRemoveResult{}, fmt.Errorf("manage_worktree remove: unlocking before restore: %w", err)
			}
		}
		restoredRoot, _, err := s.exitWorktree()
		if err != nil {
			return WorktreeRemoveResult{}, err
		}
		warning, err := s.applyRestoreLandRelockFromPorcelain(run, restoredRoot, projectDir, porcelain)
		if err != nil {
			return WorktreeRemoveResult{}, err
		}
		removeWarning = warning
	}

	// Force-cascade execution (deferred from step 4; code-review finding I1):
	// steps 5-7 above are remove's OWN non-cascade refusal gates on target,
	// and none of them is destructive to a retained-idle lane (step 7 only
	// swaps this session's OWN env pointer — already accepted as recoverable
	// if a later step fails, per step 8's own comment below). Only now that
	// they have ALL passed do we run the sanctioned dispose cascade, so a
	// call that refuses anywhere above — or a cascaded dispose's own refusal
	// right here — has never destroyed a lane.
	if len(forceCascadeDlgIDs) > 0 {
		// targetIsCascadeLane is a RECORD-based signal (code-review finding
		// I2): a delegate's own lane is always named exactly its delegate id
		// (spec §9 step 1 — createDelegateWorktree names it `delegateID`), so
		// target IS one of the lanes the cascade is about to dispose iff
		// `name` is literally one of the ids we just resolved from live,
		// owned blockers. This is checked BEFORE running anything, from the
		// same eligibility list step 4 built — never inferred after the fact
		// from a bare stat of target/.git, which cannot distinguish "the
		// cascade removed this" from "this directory happened to already be
		// gone for an unrelated reason" (e.g. deleted out of band while a
		// stale blocker still pointed at it).
		targetIsCascadeLane := slices.Contains(forceCascadeDlgIDs, name)
		// Hazard: step 7 above has already swapped s.env and unlocked target by
		// the time a cascaded dispose can refuse, so a refusal here can leave
		// the session out of a worktree it never removed. That is the accepted
		// cost of I1's ordering (a destructive cascade must not run before
		// remove's own gates pass). EVERY rung of dispose's ladder can still
		// land here — not just the unmerged/dirty verdict, but an armed watch
		// targeting the delegate, a live shell under its lane, and the
		// "became active while disposing" race, none of which the caller
		// necessarily provoked. Recovery is a re-entered worktree, never lost
		// work: no lane is torn down on any of those paths. What is gone is the
		// one refusal that fired with no user error AND no way to avoid it — a
		// RESUMED coordinator whose own idle lane came back re-armed to
		// NotifyPending (armPendingTerminalNotifications) — because
		// delegateRecordQuiescent no longer counts a session's own undelivered
		// render as work (WS8 Task 1).
		for _, dlg := range forceCascadeDlgIDs {
			if _, err := s.worktreeDispose(ctx, dlg, false, false); err != nil {
				// Name dispose's own force, not remove's: remove's force runs
				// the sanctioned cascade and never widens a cascaded dispose's
				// gates, so re-invoking remove force:true would only loop.
				return WorktreeRemoveResult{}, fmt.Errorf(
					"manage_worktree remove: cannot first dispose retained-idle lane %s: %w. "+
						"remove's force runs the sanctioned cascade only and never widens dispose's own gates; "+
						"run `manage_worktree op=dispose id=%s force=true` yourself if you mean to discard that lane's work",
					dlg, err, dlg)
			}
		}
		if targetIsCascadeLane {
			// A retained delegate whose own lane IS the worktree being
			// removed sits exactly at target (identifier.PathUnder's "equal"
			// case). Its dispose call above already ran the full removal
			// sequence — unlock, `git worktree remove`, mark disposed,
			// `branch -D`, delete sidecar — against the very same meta dir
			// steps 8-10 below would use, so target's own removal is
			// entirely owned by that dispose call now; nothing below acts on
			// it, whichever way dispose resolved it.
			if managedWorktreeExists(target) {
				// dispose declined to remove it (the lane turned dirty
				// mid-disposal and was kept resumable — disposeExecute's
				// laneKeptDirty outcome, a nil-error SUCCESS from dispose's
				// own perspective). The requested removal did not happen, so
				// remove must report that honestly rather than a false
				// success, and must not fall through to steps 8-11, which
				// would act on a lane whose sidecar/lock state dispose
				// already re-owns (downgradeRelockKeep).
				return WorktreeRemoveResult{}, fmt.Errorf("manage_worktree remove: %s became dirty during the cascade and was kept by dispose, not removed; it remains resumable via delegate_send or a later remove/dispose", target)
			}
			// The branch is deleted iff it is really gone, never asserted
			// from the fact that a dispose ran (a failed branch delete
			// downgrades to a warning inside dispose, keeping the branch).
			return WorktreeRemoveResult{Path: target, Branch: branch, BranchDeleted: !branchExists(run, branch)}, nil
		}
		// Belt and braces: a successful dispose always evicts its blocker's
		// retained child (disposeExecute step 7) regardless of whether the
		// lane itself was fully torn down or kept dirty, so this is normally
		// empty; kept as a defensive re-check rather than assumed.
		if live := s.liveWorkUnder(target); len(live) > 0 {
			return WorktreeRemoveResult{}, fmt.Errorf("manage_worktree remove: live work under %s: %s", target, strings.Join(live, ", "))
		}
	}

	// Step 8: remove the worktree itself. --force covers git's own
	// dirty/untracked refusal — never locks, which step 3 already resolved
	// (spec §5 remove step 8). A dirty tree only reaches here under
	// force_dirty (step 6), so force alone must leave Git's final dirty check
	// active.
	rmArgs := []string{"worktree", "remove"}
	if forceDirty {
		rmArgs = append(rmArgs, "--force")
	}
	rmArgs = append(rmArgs, "--", target)
	if _, err := run(rmArgs...); err != nil {
		return WorktreeRemoveResult{}, fmt.Errorf("manage_worktree remove: git worktree remove failed: %w", err)
	}

	result := WorktreeRemoveResult{Path: target, Branch: branch, Warning: removeWarning}

	// Step 9: delete_branch, gated by evener's own merge check — never git's
	// `branch -d`, which is HEAD-relative (spec §5 remove step 9: rev-6 review
	// demonstrated `-d` deleting a never-merged branch under a detached HEAD
	// review of that same tip in the main checkout). force bypasses the gate
	// entirely (unconditional -D). A branch checked out in any other worktree
	// cannot be deleted by git at all, gate or no gate — that refusal is
	// surfaced with the checkout location git itself reports.
	if deleteBranch && branch == "" {
		// The branch could not be attributed: no sidecar, and the worktree
		// named no checked-out branch before its removal. Falling back to the
		// directory name would delete any unrelated branch that happens to
		// share it, so nothing is deleted.
		result.BranchKeptReason = "branch could not be resolved (no metadata sidecar, no checked-out branch)"
	} else if deleteBranch {
		tipOut, tipErr := run("rev-parse", "--verify", "refs/heads/"+branch)
		if tipErr != nil {
			result.BranchKeptReason = fmt.Sprintf("branch %q not found: %v", branch, tipErr)
		} else {
			tipSHA := strings.TrimSpace(tipOut)
			passesGate := force
			var evidence string
			if !passesGate {
				if tipSHA == sc.BaseSHA {
					passesGate = true
				} else {
					mr, mErr := worktree.Merged(run, tipSHA, sc.MergeTarget, sc.BaseSHA)
					if mErr != nil {
						return WorktreeRemoveResult{}, fmt.Errorf("manage_worktree remove: checking merge status: %w", mErr)
					}
					switch {
					case mr.Merged:
						passesGate = true
					case mr.TargetUnknown:
						evidence = fmt.Sprintf("branch %q merge target unknown; tip %s not verified merged; re-invoke with force to delete anyway", branch, shortSHA(tipSHA))
					default:
						evidence = fmt.Sprintf("branch %q is not merged into %s (tip %s); neither ancestry nor patch-equivalence holds; merge first or re-invoke with force to delete anyway", branch, mr.TargetRef, shortSHA(tipSHA))
					}
				}
			}
			if passesGate {
				if _, err := run("branch", "-D", branch); err != nil {
					result.BranchKeptReason = fmt.Sprintf("git refused to delete branch %q: %v", branch, err)
				} else {
					result.BranchDeleted = true
				}
			} else {
				result.BranchKeptReason = evidence
			}
		}
	}

	// Step 10: sidecar disposition. Gone (deleted here) → delete the sidecar;
	// survives → keep it and mark worktree_removed + tip_sha_at_removal. The
	// mark write is UpdateSidecar's atomic replace (a temp write renamed over
	// the target), so a crash mid-write leaves the pre-mark record rather than
	// a torn one, which sweep 2's reconciliation (spec §5 prune sweep 2)
	// explicitly tolerates via its "or no removal record" branch: a sidecar
	// carrying no removal record is judged exactly like tip == base_sha for
	// merge-gated collection, so an unmarked sidecar is still handled
	// correctly, just less precisely reported meanwhile.
	if result.BranchDeleted {
		if err := s.deleteWorktreeSidecar(metaDir, name); err != nil && !os.IsNotExist(err) {
			return WorktreeRemoveResult{}, fmt.Errorf("manage_worktree remove: deleting sidecar: %w", err)
		}
	} else if hasSidecar {
		tipOut, tipErr := run("rev-parse", "--verify", "refs/heads/"+branch)
		if tipErr == nil {
			tipSHA := strings.TrimSpace(tipOut)
			if err := s.updateWorktreeSidecar(metaDir, name, func(sc *worktree.Sidecar) {
				sc.WorktreeRemoved = true
				sc.TipSHAAtRemoval = tipSHA
			}); err != nil && !os.IsNotExist(err) {
				return WorktreeRemoveResult{}, fmt.Errorf("manage_worktree remove: marking sidecar removed: %w", err)
			}
		}
	}

	// Step 11: report the path and whether the branch was deleted (already
	// carried on result: Path, Branch, BranchDeleted, BranchKeptReason,
	// Warning).
	return result, nil
}
