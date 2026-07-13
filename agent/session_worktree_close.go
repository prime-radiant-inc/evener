package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/internal/worktree"
)

// isolationLane names one isolation delegate lane a session created: its
// delegate id (== the worktree name and branch, native worktree tools spec §9
// lifecycle step 1) and the lane's on-disk path (DelegateRestore.WorkingDir).
type isolationLane struct {
	delegateID string
	path       string
}

// ownedIsolationLanes enumerates the isolation delegate lanes THIS session
// created (native worktree tools spec §9 step 4), from the folded job records.
// A lane qualifies when its delegate job carries an isolation="worktree"
// restore descriptor whose ParentSessionID is this session — the field a
// forwarded copy of a descendant's delegate preserves as the ORIGINAL creator,
// so an ancestor never disposes a lane it did not create. Deduped by delegate
// id, since a delegate resumed several times has several job records all
// pointing at the same lane.
func ownedIsolationLanes(recs map[string]*jobstore.JobRecord, sessionID string) []isolationLane {
	seen := make(map[string]bool)
	var lanes []isolationLane
	for _, r := range recs {
		d := r.DelegateRestore
		if d == nil || d.Isolation != "worktree" {
			continue
		}
		if d.ParentSessionID != sessionID {
			continue
		}
		path := strings.TrimSpace(d.WorkingDir)
		if path == "" || r.DelegateID == "" || seen[r.DelegateID] {
			continue
		}
		seen[r.DelegateID] = true
		lanes = append(lanes, isolationLane{delegateID: r.DelegateID, path: path})
	}
	return lanes
}

// disposeDelegateLanesAtClose disposes the isolation delegate lanes this
// session created, in this session's own close path (native worktree tools
// spec §9 step 4). It runs AFTER child sessions are closed and BEFORE the
// jobstore is closed, because the disposed mark (step 5) is a durable append:
// the store must still be open. Per lane, in the load-bearing order the spec
// pins (rev-7 rejected marking first): evaluate the shared unchanged predicate,
// then either UNLOCK+KEEP a changed lane (descriptor untouched, still
// resumable) or UNLOCK → `git worktree remove` (non-force) → mark disposed →
// delete branch+sidecar for an unchanged one. A late dirty write racing the
// clean check makes the non-force remove refuse, which downgrades the lane back
// to KEEP and re-locks it. Every step is best-effort: a lane it cannot cleanly
// dispose is left for `prune` / the crash net (step 5's WorkingDir stat), never
// force-removed. The kept lanes are surfaced as a close-time notice.
func (s *Session) disposeDelegateLanesAtClose(ctx context.Context) {
	if s.jobManager == nil || s.jobManager.store == nil {
		return
	}
	local, ok := s.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		return // env swapping / local git worktrees are a local-env-only feature
	}
	recs, err := s.jobManager.store.Load()
	if err != nil {
		return
	}
	lanes := ownedIsolationLanes(recs, s.id)
	if len(lanes) == 0 {
		return
	}

	// The whole close cascade shares ONE budget (spec §P0, Implementation-order
	// item 4): reuse an incoming cascade deadline, or mint one here when this is
	// the initiating close. laneClosePassBudget bounds evaluation + disposal git
	// work only — never the touch+unlock tail below.
	budgetCtx, cancel := ensureCloseBudget(ctx)
	defer cancel()

	var kept []string
	var tail []string
	for _, lane := range lanes {
		// Budget check at the top of each iteration: once the deadline passes,
		// every remaining lane gets ONLY the budget-exempt touch+unlock tail (no
		// predicate evaluation, no remove/branch-D) so a pathological session
		// never blocks shutdown on git — yet no lane is ever left locked.
		if budgetCtx.Err() != nil {
			if note := s.touchUnlockLaneTail(local, lane); note != "" {
				tail = append(tail, note)
			}
			continue
		}
		if note, wasKept := s.disposeOneDelegateLane(budgetCtx, local, lane); wasKept {
			kept = append(kept, note)
		}
	}
	if len(kept) > 0 {
		s.emit(events.EventWarning, events.WarningData{
			Message: "kept " + strconv.Itoa(len(kept)) + " isolation worktree lane(s) not collected automatically (unmerged or squash-merged), dirty, or unverifiable at session close: " + strings.Join(kept, "; "),
		})
	}
	if len(tail) > 0 {
		s.emit(events.EventWarning, events.WarningData{
			Message: "close budget exhausted; " + strconv.Itoa(len(tail)) + " isolation worktree lane(s) touched+unlocked without disposal (evaluation skipped, left resumable for prune): " + strings.Join(tail, "; "),
		})
		if len(tail) > laneTailWarnThreshold {
			s.emit(events.EventWarning, events.WarningData{
				Message: "delegate lane close tail of " + strconv.Itoa(len(tail)) + " lane(s) exceeded threshold " + strconv.Itoa(laneTailWarnThreshold) + ": this session leaked more isolation lanes than a bounded close pass can collect",
			})
		}
	}
}

// closeBudgetMintHook is a test-only seam invoked exactly when ensureCloseBudget
// MINTS a fresh deadline (not when it reuses an inherited one), so a cascade test
// can assert the whole close/dispose cascade minted a single shared budget. It is
// nil in production.
var closeBudgetMintHook func()

// ensureCloseBudget returns a context carrying the shared close-cascade deadline.
// When ctx already carries a deadline (a descendant reached through the close
// cascade), it is reused unchanged so per-session budgets never stack; otherwise
// this is the initiating close and a fresh laneClosePassBudget deadline is minted.
func ensureCloseBudget(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	if closeBudgetMintHook != nil {
		closeBudgetMintHook()
	}
	return context.WithTimeout(ctx, laneClosePassBudget)
}

// touchUnlockLaneTail runs the budget-exempt tail for a lane the close pass could
// not reach before the budget expired (spec §P0, rev-9.1 finding O2): touch the
// sidecar (so P3's grace keys on a fresh mtime) and release the disposer's own
// lock — two cheap constant-cost ops, no predicate evaluation and no remove. It
// uses a non-expiring background git runner precisely because the budget is
// already spent, so a lane is never left locked. Returns a human-readable note
// for the aggregated tail warning, or "" for a lane not ours to touch.
func (s *Session) touchUnlockLaneTail(local *execenv.LocalExecutionEnvironment, lane isolationLane) string {
	lanePath := filepath.Clean(lane.path)
	if _, err := os.Stat(filepath.Join(lanePath, ".git")); err != nil {
		return ""
	}
	rootedAtLane := local.WithWorkingDirectory(lanePath)
	mainRoot := execenv.ResolveMainRepoRoot(rootedAtLane, lanePath)
	if mainRoot == "" {
		return ""
	}
	controlEnv := local.WithWorkingDirectory(mainRoot)
	run := s.newWorktreeGitRunner(context.Background(), controlEnv)
	metaDir := metaDirForLane(lanePath)

	locked, reason, lsErr := lockStateOf(run, lanePath)
	if lsErr != nil {
		// Lock state unverifiable: still touch so the lane's grace mtime is
		// fresh; leave the lock alone (we cannot prove it is ours to release).
		touchSidecar(metaDir, lane.delegateID)
		return lane.delegateID + " at " + lanePath + " (lock state unverifiable)"
	}
	st := worktree.Unlocked
	if locked {
		st = worktree.ClassifyReason(reason, s.id, lane.delegateID)
	}
	s.unlockLaneIfOwn(run, worktree.EvDisposeChanged, st, lanePath, metaDir, lane.delegateID)
	return lane.delegateID + " at " + lanePath
}

// disposeOneDelegateLane disposes a single isolation lane and reports whether
// it was KEPT (unlocked, descriptor untouched, still resumable) along with a
// human-readable note for the close-time listing. It returns kept=false for a
// removed (unchanged) lane and for any lane it declines to touch (already gone,
// no sidecar, or foreign/session-locked — not the disposer's serf:dlg: lock).
func (s *Session) disposeOneDelegateLane(ctx context.Context, local *execenv.LocalExecutionEnvironment, lane isolationLane) (note string, kept bool) {
	lanePath := filepath.Clean(lane.path)

	// The lane directory must still exist and be a real linked worktree; a
	// crash after remove (or a prior prune) leaves nothing to do — the crash
	// net (step 5's WorkingDir stat) already refuses revival into it.
	if _, err := os.Stat(filepath.Join(lanePath, ".git")); err != nil {
		return "", false
	}
	rootedAtLane := local.WithWorkingDirectory(lanePath)
	mainRoot := execenv.ResolveMainRepoRoot(rootedAtLane, lanePath)
	if mainRoot == "" {
		return "", false
	}
	controlEnv := local.WithWorkingDirectory(mainRoot)
	// Evaluation and disposal git ops run under the shared close budget, so a
	// pass that blows the deadline is interrupted rather than blocking shutdown.
	run := s.newWorktreeGitRunner(ctx, controlEnv)

	// The sidecar carries the recorded base SHA the unchanged predicate needs.
	// Without it (unknown provenance) the lane is not ours to judge — leave it.
	metaDir := metaDirForLane(lanePath)
	sc, scErr := worktree.ReadSidecar(metaDir, lane.delegateID)
	if scErr != nil {
		return "", false
	}

	locked, reason, lsErr := lockStateOf(run, lanePath)
	if lsErr != nil {
		return "", false
	}
	st := worktree.Unlocked
	if locked {
		st = worktree.ClassifyReason(reason, s.id, lane.delegateID)
	}

	// D0-auto predicate (spec §D0): a lane is auto-collectible when it is
	// Unchanged (clean tree AND tip == recorded base SHA) OR clean and
	// ancestry-merged into its LOCAL merge_target branch. Anything else (commits
	// not reachable from a local branch, a cherry-only/remote-tracking merge, or
	// a dirty tree) is a KEPT lane whose work must be preserved.
	collectible, uErr := laneAutoCollectible(run, lanePath, sc.BaseSHA, sc.MergeTarget)
	if uErr != nil {
		// Cannot evaluate cleanly. Fail safe toward preservation: touch the
		// sidecar, release our own lock (if any), and keep the lane resumable
		// rather than risk removing unexamined work.
		s.unlockLaneIfOwn(run, worktree.EvDisposeChanged, st, lanePath, metaDir, lane.delegateID)
		return lane.delegateID + " at " + lanePath + " (state unverifiable)", true
	}

	if !collectible {
		// KEPT → touch the sidecar, unlock, and keep; the descriptor is NEVER
		// touched, so the lane stays resumable and `prune` collects it once its
		// branch merges.
		if !s.unlockLaneIfOwn(run, worktree.EvDisposeChanged, st, lanePath, metaDir, lane.delegateID) {
			return "", false // foreign / session-locked — not the disposer's dlg lock
		}
		dirty := false
		if clean, _, cErr := worktree.CleanTree(run, lanePath); cErr == nil {
			dirty = !clean
		}
		ahead := 0
		if aheadOut, aErr := run("-C", lanePath, "rev-list", "--count", sc.BaseSHA+"..HEAD"); aErr == nil {
			if n, convErr := strconv.Atoi(strings.TrimSpace(aheadOut)); convErr == nil {
				ahead = n
			}
		}
		return fmt.Sprintf("%s at %s (branch %s, %d ahead, dirty=%t)", lane.delegateID, lanePath, lane.delegateID, ahead, dirty), true
	}

	// COLLECTIBLE → unlock, then `git worktree remove` (non-force), then mark the
	// descriptor disposed, then delete branch + sidecar. A late-dirty refusal
	// downgrades back to KEEP; at close the dead owner's lock is left released
	// (downgradeUnlockKeep) rather than re-locked.
	outcome, note := s.disposeUnchangedLaneMechanics(run, st, lane, metaDir, downgradeUnlockKeep, false)
	return note, outcome == laneKeptDirty
}

// laneAutoCollectible applies the D0-auto disposal predicate (spec §D0): a lane
// is collectible when it is Unchanged (clean tree AND tip == recorded base) OR
// clean and ancestry-merged into its LOCAL merge_target branch. It never runs
// the cherry/patch-equivalence arm and never consults remote-tracking refs, so
// automatic disposal (no human in the loop) never deletes commits that are not
// reachable from a local branch. A dirty tree is never collectible. Any
// predicate git failure is returned as err so the caller can fail safe to KEEP.
func laneAutoCollectible(run worktree.GitRunner, lanePath, baseSHA, mergeTarget string) (bool, error) {
	unchanged, err := worktree.Unchanged(run, lanePath, baseSHA)
	if err != nil {
		return false, err
	}
	if unchanged {
		return true, nil
	}
	clean, _, err := worktree.CleanTree(run, lanePath)
	if err != nil {
		return false, err
	}
	if !clean {
		return false, nil // a dirty lane is never auto-collectible
	}
	tip, err := run("-C", lanePath, "rev-parse", "HEAD")
	if err != nil {
		return false, err
	}
	m, err := worktree.MergedAncestryLocal(run, strings.TrimSpace(tip), mergeTarget)
	if err != nil {
		return false, err
	}
	return m.Merged, nil
}

// touchSidecar rewrites the lane's sidecar in place (identical content, fresh
// mtime) so P3's residue-collection grace keys on a recent timestamp for a KEPT
// lane. Best-effort: a touch failure is not fatal — the lane is still unlocked
// and resumable, matching every other best-effort close-time git op.
func touchSidecar(metaDir, delegateID string) {
	_ = worktree.UpdateSidecar(metaDir, delegateID, func(*worktree.Sidecar) {})
}

// downgradePolicy selects how a late-dirty remove refusal is handled when an
// UNCHANGED lane cannot be cleanly removed: a write raced the clean check and
// git refused the non-force remove, so the lane is downgraded back to KEEP.
type downgradePolicy int

const (
	// downgradeRelockKeep re-locks the disposer's own serf:dlg marker on the
	// kept lane (live dispose: the owner is still around to hold the lock).
	downgradeRelockKeep downgradePolicy = iota
	// downgradeUnlockKeep leaves the kept lane unlocked (close path: a dead
	// owner whose lock nobody would ever release again).
	downgradeUnlockKeep
)

// laneDisposalOutcome distinguishes how disposeUnchangedLaneMechanics ended, so
// the model-facing dispose op can report an honest result (the close path only
// needs the kept/not-kept bit). laneDisposed covers both a clean remove and a
// concurrent-collector race where the lane was already gone when git refused
// the remove — either way the descriptor is marked Disposed and remnants are
// cleaned. laneKeptDirty is the late-dirty downgrade (lane preserved, KEEP).
// laneDeclined is a lane the mechanics would not touch (foreign/session lock, or
// its own unlock failed) — left for prune, never marked.
type laneDisposalOutcome int

const (
	laneDisposed laneDisposalOutcome = iota
	laneKeptDirty
	laneDeclined
)

// disposeUnchangedLaneMechanics runs the unlock → `git worktree remove`
// (non-force) → mark disposed → `branch -D` → sidecar-delete sequence for an
// UNCHANGED lane already judged collectible. It is the shared core of close-time
// disposal and the live `dispose` op. It returns laneDisposed when the lane is
// removed (or was already gone to a concurrent collector) and marked, laneDeclined
// for a foreign / session-locked lane it will not touch (ActRefuse) or one whose
// own unlock failed, and laneKeptDirty with a note when git refuses the non-force
// remove because the lane is still present with a late dirty write — then the lane
// is downgraded back to KEEP per the downgrade policy.
// forceRemove uses `git worktree remove --force` (spec §P1 step 8 "non-force
// unless dirty-forced"): the model-facing dispose op sets it when `force_dirty`
// discards a dirty lane, which a non-force remove would refuse. The close path
// never forces — a late dirty write there downgrades back to KEEP instead.
// statLaneGitDir stats <lanePath>/.git through the worktreeLaneStat test seam
// when set, else os.Stat. It is the classifier's view of whether a lane the
// non-force remove refused is still present.
func (s *Session) statLaneGitDir(lanePath string) (os.FileInfo, error) {
	gitPath := filepath.Join(lanePath, ".git")
	if s.worktreeLaneStat != nil {
		return s.worktreeLaneStat(gitPath)
	}
	return os.Stat(gitPath)
}

func (s *Session) disposeUnchangedLaneMechanics(run worktree.GitRunner, st worktree.LockState, lane isolationLane, metaDir string, downgrade downgradePolicy, forceRemove bool) (outcome laneDisposalOutcome, note string) {
	lanePath := filepath.Clean(lane.path)
	switch worktree.Decide(worktree.EvDisposeUnchanged, st) {
	case worktree.ActUnlock:
		// Touch BEFORE the unlock (spec §P0): if a late-dirty write downgrades
		// this to KEEP below, the lane is already released with a fresh sidecar;
		// on the common success path the sidecar is deleted anyway.
		touchSidecar(metaDir, lane.delegateID)
		if _, err := run("worktree", "unlock", lanePath); err != nil {
			return laneDeclined, "" // cannot release our lock; leave the lane for prune
		}
	case worktree.ActNone:
		// Crash residue: already unlocked. The unlock is vacuous (running it on
		// an unlocked tree is fatal on git), so proceed straight to remove.
	default:
		return laneDeclined, "" // ActRefuse: not the disposer's dlg lock — leave it
	}

	if s.worktreeDisposeBeforeRemove != nil {
		s.worktreeDisposeBeforeRemove(lanePath)
	}
	removeArgs := []string{"worktree", "remove", "--", lanePath}
	if forceRemove {
		removeArgs = []string{"worktree", "remove", "--force", "--", lanePath}
	}
	if _, err := run(removeArgs...); err != nil {
		// The non-force remove was refused. Distinguish a concurrent collector
		// (the lane directory is already gone — someone else won the remove) from
		// a late dirty write (the lane is still present). Only a definitively gone
		// lane (ENOENT) falls through to finish the disposal bookkeeping; a present
		// lane AND any transient stat failure (EIO/EACCES) take the conservative
		// KEEP path — a lane may still exist, and destruction must not act on doubt.
		if _, statErr := s.statLaneGitDir(lanePath); !os.IsNotExist(statErr) {
			switch downgrade {
			case downgradeRelockKeep:
				marker := worktree.FormatDelegateMarker(lane.delegateID, s.id)
				if _, lockErr := run("worktree", "lock", "--reason", marker, lanePath); lockErr != nil {
					s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("delegate lane %s kept after eviction but its lock could not be re-acquired at %s: %v", lane.delegateID, lanePath, lockErr)})
				}
				return laneKeptDirty, lane.delegateID + " at " + lanePath + " (dirty at removal; re-locked)"
			default: // downgradeUnlockKeep
				return laneKeptDirty, lane.delegateID + " at " + lanePath + " (dirty at removal; kept unlocked)"
			}
		}
		// Lane gone to a concurrent collector: fall through to mark + remnants.
	}

	// The worktree is gone. Mark the descriptor disposed BEFORE deleting the
	// branch/sidecar. A crash between remove and this mark is covered by the
	// stat crash net; the mark is the fast, explicit refusal.
	if err := s.jobManager.appendEvent(jobstore.Event{
		Kind:       jobstore.EventDelegateDisposed,
		TS:         s.jobManager.now(),
		DelegateID: lane.delegateID,
	}); err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("delegate lane disposal mark failed for %s: %v", lane.delegateID, err)})
	}

	// Delete the branch (unchanged lane: tip == base, no work lost) and the
	// sidecar. Best-effort — the lane is already unrevivable.
	if _, err := run("branch", "-D", lane.delegateID); err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("delegate lane branch delete failed for %s: %v", lane.delegateID, err)})
	}
	_ = worktree.DeleteSidecar(metaDir, lane.delegateID)
	return laneDisposed, ""
}

// unlockLaneIfOwn routes a changed-lane / unverifiable-lane unlock through the
// pure lock decision core (never hand-rolled reason parsing): it releases the
// lane's serf:dlg: lock when the observed state is the disposer's own marker,
// is a no-op for an already-unlocked (crash-residue) lane, and returns false
// when the state is foreign or a plain session marker (someone switched in) so
// the caller declines to touch it. Only ActUnlock and ActNone keep the lane.
// On both keeping outcomes it touches the sidecar BEFORE releasing the lock
// (spec §P0), so once the lane is observable as unlocked its sidecar is already
// fresh for P3's grace; a foreign / declined lane (ActRefuse) is never touched.
func (s *Session) unlockLaneIfOwn(run worktree.GitRunner, ev worktree.LockEvent, st worktree.LockState, lanePath, metaDir, delegateID string) bool {
	switch worktree.Decide(ev, st) {
	case worktree.ActUnlock:
		touchSidecar(metaDir, delegateID)
		if _, err := run("worktree", "unlock", lanePath); err != nil {
			return false
		}
		return true
	case worktree.ActNone:
		touchSidecar(metaDir, delegateID)
		return true
	default:
		return false
	}
}

// unlockOwnManagedWorktreeAtClose unlocks the session's OWN occupied managed
// worktree on a clean close (native worktree tools spec §5 close-unlock), so a
// close→resume round-trip re-enters an unlocked tree. This is distinct from
// delegate-lane disposal: the session's own managed worktree (tracked by
// worktreeCurrentManaged / worktreeCurrentPath) is unlocked on disk, never
// removed. The unlock routes through leaveCurrentWorktree, which applies the
// EvLeave lock rule (own session marker → unlock; unlocked / foreign / delegate
// → no-op).
func (s *Session) unlockOwnManagedWorktreeAtClose() {
	s.mu.Lock()
	path := s.worktreeCurrentPath
	managed := s.worktreeCurrentManaged
	s.mu.Unlock()
	if path == "" || !managed {
		return
	}
	local, ok := s.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		return
	}
	rootedAtPath := local.WithWorkingDirectory(path)
	mainRoot := execenv.ResolveMainRepoRoot(rootedAtPath, path)
	if mainRoot == "" {
		return
	}
	controlEnv := local.WithWorkingDirectory(mainRoot)
	run := s.newWorktreeGitRunner(context.Background(), controlEnv)
	if err := s.leaveCurrentWorktree(run); err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("unlocking own worktree %s at close failed: %v", path, err)})
	}
}
