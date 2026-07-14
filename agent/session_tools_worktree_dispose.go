package agent

import (
	"context"
	"errors"
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

// WorktreeDisposeResult is the structured outcome of a successful dispose
// operation (spec §P1): the disposed / kept-after-eviction / already-disposed
// report with the lane path and branch.
type WorktreeDisposeResult struct {
	DelegateID      string
	LanePath        string
	Branch          string
	AlreadyDisposed bool
	Message         string
}

// worktreeDispose implements the model-facing dispose operation (spec §P1): it
// retires a delegate's isolation worktree lane by id after a validation ladder
// (ownership + quiescence, delivery quiescence, subtree quiescence, dispose-gate
// the retained child, foreign-lock refusal, D0-model evaluation). It is wrapped
// by the closing gate (beginDispose/endDispose): a session already closing
// refuses the op, and an admitted op holds disposeWG so Close() joins it before
// draining (spec §P1 "Dispose-turn vs own-close protocol").
func (s *Session) worktreeDispose(ctx context.Context, id string, force, forceDirty bool) (WorktreeDisposeResult, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return WorktreeDisposeResult{}, errors.New("invalid_request: manage_worktree dispose: id is required")
	}
	if !isDelegateID(id) {
		return WorktreeDisposeResult{}, fmt.Errorf("invalid_request: manage_worktree dispose: %q is not a delegate id", id)
	}
	// Closing gate (spec §P1 step 1): admit the op iff the session is not closing,
	// registering it on disposeWG under one s.mu hold so a successful admission
	// happens-before Close()'s join. Refuse otherwise — a closing session is
	// already collecting its own lanes.
	if !s.beginDispose() {
		return WorktreeDisposeResult{}, errors.New("manage_worktree dispose: session is closing")
	}
	defer s.endDispose()
	return s.disposeDelegateLane(ctx, id, force, forceDirty)
}

// isDelegateID reports whether id has the delegate id shape (dlg_…). Only a
// delegate lane can be disposed; a bare worktree name or job id is refused as an
// invalid_request rather than silently missing.
func isDelegateID(id string) bool { return strings.HasPrefix(id, "dlg_") }

// disposeDelegateLane runs the full dispose operation (spec §P1 steps 1-8): the
// validation ladder (steps 1-6), then — for a collectible lane — eviction of the
// retained child and lane teardown (steps 7-8, via disposeExecute). The
// idempotent already-disposed path returns early. Every refusal after the dispose
// gate is armed (step 4) reverses the gate via a deferred clear-unless-consumed;
// once execution evicts the child the gate is consumed and never reversed.
func (s *Session) disposeDelegateLane(ctx context.Context, id string, force, forceDirty bool) (WorktreeDisposeResult, error) {
	if s.jobManager == nil || s.jobManager.store == nil {
		return WorktreeDisposeResult{}, errors.New("manage_worktree dispose: no job store")
	}

	// Step 1: ownership. The delegate must be recorded in THIS session's store as
	// a worktree-isolated delegate this session created (descriptor
	// ParentSessionID == s.id — the field a forwarded descendant copy preserves as
	// the ORIGINAL creator, so an ancestor never disposes a lane it did not make).
	recs, err := s.jobManager.store.Load()
	if err != nil {
		return WorktreeDisposeResult{}, fmt.Errorf("manage_worktree dispose: load store: %w", err)
	}
	rec, desc := findDelegateLaneRecord(recs, id)
	if desc == nil {
		return WorktreeDisposeResult{}, fmt.Errorf("invalid_request: manage_worktree dispose: %s is not a known isolation delegate of this session", id)
	}
	if desc.ParentSessionID != s.id {
		return WorktreeDisposeResult{}, fmt.Errorf("manage_worktree dispose: %s was created by another session; only its creator can dispose it", id)
	}
	lanePath := filepath.Clean(strings.TrimSpace(desc.WorkingDir))
	if lanePath == "" || lanePath == "." {
		return WorktreeDisposeResult{}, fmt.Errorf("manage_worktree dispose: %s has no recorded lane path", id)
	}

	// Control env is resolved from the sidecar's OriginalRoot, NOT by walking up
	// from the lane path (the lane may be gone). The sidecar lives beside the lane.
	metaDir := metaDirForLane(lanePath)
	sc, scErr := worktree.ReadSidecar(metaDir, id)

	// Idempotent already-disposed with a gone/unreadable sidecar (spec §P1 step 1):
	// a completed dispose deletes the sidecar (and branch) in step 8, so a re-issued
	// dispose on a fully-torn-down lane cannot read it. The durable disposed mark is
	// ground truth — there is nothing left to clean and no control env to resolve —
	// so report a clean already-disposed no-op rather than erroring on the sidecar.
	// This short-circuit MUST precede the sidecar-unreadable hard error below, which
	// only a NOT-disposed lane (still needing a control env to do work) falls into.
	if scErr != nil && rec != nil && rec.Disposed {
		return disposeAlreadyDisposedGone(id, lanePath, scErr), nil
	}
	if scErr != nil {
		return WorktreeDisposeResult{}, fmt.Errorf("manage_worktree dispose: %s sidecar unreadable; cannot resolve its git control environment: %w", id, scErr)
	}
	originalRoot := strings.TrimSpace(sc.OriginalRoot)
	if originalRoot == "" {
		return WorktreeDisposeResult{}, fmt.Errorf("manage_worktree dispose: %s sidecar has no original_root; cannot resolve its git control environment", id)
	}
	controlEnv, envErr := s.delegateDisposeControlEnv(originalRoot)
	if envErr != nil {
		return WorktreeDisposeResult{}, fmt.Errorf("manage_worktree dispose: %s: %w", id, envErr)
	}

	// The dispose op OWNS the cascade budget (spec §P1 step 7, Implementation-order
	// item 4a): mint ONE deadline ctx here and thread it through both the git ops
	// below and the retained child's close (step 7). A descendant's own close-time
	// disposal reuses this deadline rather than minting a fresh per-session budget
	// (ensureCloseBudget reuses an inherited deadline), so the whole cascade shares
	// one budget regardless of subtree depth.
	budgetCtx, cancelBudget := ensureCloseBudget(ctx)
	defer cancelBudget()
	run := s.newWorktreeGitRunner(budgetCtx, controlEnv)

	laneDirPresent := laneWorktreePresent(lanePath)

	// When the lane dir still exists, cross-check that it resolves to the same
	// main root the sidecar's OriginalRoot named — a mismatch means the sidecar's
	// provenance and the on-disk lane disagree, so the control env we would run
	// git under is not this lane's; refuse with a clear error rather than acting
	// on the wrong repository.
	if laneDirPresent {
		if local, ok := s.currentEnv().(*execenv.LocalExecutionEnvironment); ok {
			laneMain := execenv.ResolveMainRepoRoot(local.WithWorkingDirectory(lanePath), lanePath)
			if laneMain != "" && filepath.Clean(laneMain) != filepath.Clean(originalRoot) {
				return WorktreeDisposeResult{}, fmt.Errorf("manage_worktree dispose: %s lane at %s resolves to main root %s but its sidecar records %s; refusing on a provenance mismatch", id, lanePath, laneMain, originalRoot)
			}
		}
	}

	// Idempotent already-disposed: the durable disposed mark exists. Clean up any
	// remnants (branch if its tip judges D0-model-collectible, sidecar) and report
	// already-disposed rather than refusing — a re-issued dispose is a no-op.
	if rec != nil && rec.Disposed {
		return s.disposeAlreadyDisposedRemnants(run, id, lanePath, metaDir, sc), nil
	}

	// Record quiescence (spec §P1 step 1): the delegate's latest job is terminal
	// with no running/queued follow-up and no pending owner notification — checked
	// under one jm.mu hold.
	quiescent, qErr := s.jobManager.delegateRecordQuiescent(id)
	if qErr != nil {
		return WorktreeDisposeResult{}, fmt.Errorf("manage_worktree dispose: %s quiescence check: %w", id, qErr)
	}
	if !quiescent {
		return WorktreeDisposeResult{}, fmt.Errorf("manage_worktree dispose: %s still has running or undelivered work; wait for it to finish", id)
	}

	// Step 2: delivery quiescence. An armed watch routing send_to this delegate,
	// or a pending watch-send targeting it, in this session's manager OR any
	// retained child's, means a frame is still bound for the lane — refuse naming
	// it rather than disposing the lane out from under an in-flight send.
	if s.subtreeWatchesTargeting(id) {
		return WorktreeDisposeResult{}, fmt.Errorf("manage_worktree dispose: %s is the target of an armed or pending watch send; clear the watch before disposing", id)
	}

	// Step 3: subtree quiescence. A retained coordinator child's own subtree must
	// have no outstanding work, and no live background shell may be rooted in the
	// lane tree.
	sub := s.subagents.get(id)
	if sub != nil && sub.sess != nil {
		outstanding, oErr := sub.sess.treeHasOutstandingWork()
		if oErr != nil {
			return WorktreeDisposeResult{}, fmt.Errorf("manage_worktree dispose: %s subtree check: %w", id, oErr)
		}
		if outstanding {
			return WorktreeDisposeResult{}, fmt.Errorf("manage_worktree dispose: %s still has outstanding work in its delegate subtree; wait for it to finish", id)
		}
	}
	if shells := s.liveShellsUnderTree(lanePath); len(shells) > 0 {
		return WorktreeDisposeResult{}, fmt.Errorf("manage_worktree dispose: %s has live background shell(s) rooted in its lane: %s", id, strings.Join(shells, ", "))
	}

	// Step 4: dispose-gate the retained child. Arming freezes a quiescent,
	// retained TERMINAL child so a concurrent notification cannot launch a drive
	// while the lane is disposed. Failure (a raced running/driving child) refuses.
	// EVERY refusal exit up to and including step 6 reverses the gate; the reversal
	// is a DEFERRED clear-unless-consumed at the gate boundary this op owns (spec
	// §P1 step 4). Once execution (step 7) evicts the child, the gate is consumed —
	// the child is gone, the flag moot — so the deferred clear is suppressed and a
	// step-8 late-dirty KEEP is the intended KEPT-after-eviction outcome, NOT a gate
	// reversal (the child cannot be un-evicted).
	gateArmed := false
	if sub != nil {
		if !sub.trySetDisposeGate() {
			return WorktreeDisposeResult{}, fmt.Errorf("manage_worktree dispose: %s became active while disposing; retry once it is idle", id)
		}
		gateArmed = true
	}
	gateConsumed := false
	defer func() {
		if gateArmed && !gateConsumed {
			sub.clearDisposeGate()
		}
	}()

	// Step 5: lock state. The lane must carry this session's own serf:dlg: marker
	// (or be unlocked crash residue); a foreign / session marker means someone
	// switched in — refuse. st carries the classified lock state into execution.
	st := worktree.Unlocked
	if laneDirPresent {
		locked, reason, lsErr := lockStateOf(run, lanePath)
		if lsErr != nil {
			return WorktreeDisposeResult{}, fmt.Errorf("manage_worktree dispose: %s lock state could not be verified: %w", id, lsErr)
		}
		if locked {
			st = worktree.ClassifyReason(reason, s.id, id)
		}
		if worktree.Decide(worktree.EvDisposeUnchanged, st) == worktree.ActRefuse {
			return WorktreeDisposeResult{}, fmt.Errorf("manage_worktree dispose: %s is locked by another owner; not the disposer's lane to reclaim", id)
		}
	}

	// Step 6: evaluate (D0-model). Half-removed residue (lane dir gone,
	// record+branch+sidecar remain) is judged by the branch tip via the
	// OriginalRoot env; otherwise the lane tree is evaluated in place. A refusal
	// returns here (deferred gate clear fires); a collectible verdict proceeds to
	// execution.
	var evalErr error
	if !laneDirPresent {
		evalErr = s.disposeEvaluateHalfRemoved(run, id, sc, force)
	} else {
		evalErr = s.disposeEvaluateLane(run, id, lanePath, sc, force, forceDirty)
	}
	if evalErr != nil {
		return WorktreeDisposeResult{}, evalErr
	}

	// Collectible. Execution (steps 7-8) owns the gate from here: eviction consumes
	// it, so suppress the deferred clear.
	gateConsumed = true
	return s.disposeExecute(budgetCtx, run, id, lanePath, metaDir, sub, laneDirPresent, st, forceDirty)
}

// disposeExecute runs the dispose execution steps (spec §P1 steps 7-8) after
// evaluation judged the lane collectible: evict the retained child (step 7), then
// unlock → `git worktree remove` → mark disposed → `branch -D` → delete sidecar
// (step 8). It reports what happened (disposed / kept-after-eviction) with the
// lane path and branch. The gate is already consumed by the caller; this function
// never touches it.
func (s *Session) disposeExecute(ctx context.Context, run worktree.GitRunner, id, lanePath, metaDir string, sub *subagent, lanePresent bool, st worktree.LockState, forceDirty bool) (WorktreeDisposeResult, error) {
	// Step 7: evict the retained child. Close it with the parent-close pattern
	// (child env cleanup is the parent's job, so close(false) skips it), remove it
	// from the subagent table, then for an OWNED env run a FULL Cleanup() — not just
	// DisposeSandboxScratch — so residual lane processes are killed BEFORE step 8's
	// `git worktree remove` (spec §P1 finding P1). A shared-env child has no
	// separate processes to kill; its lane shells are jm-tracked and were already
	// refused by step 3, so nothing extra is disposed for it. Closing a retained
	// coordinator child recurses through its own close-time lane disposal under the
	// SAME budget ctx (the nested-coordinator cascade, spec §P1 step 7).
	if sub != nil && sub.sess != nil {
		sub.sess.close(ctx, false)
		s.subagents.remove(id)
		if sub.ownsEnv {
			if le, ok := sub.sess.currentEnv().(*execenv.LocalExecutionEnvironment); ok {
				le.Cleanup()
			}
		}
	}

	lane := isolationLane{delegateID: id, path: lanePath}

	// Step 8 for half-removed residue: there is no worktree to remove, so mark
	// disposed and delete the branch + sidecar directly.
	if !lanePresent {
		return s.disposeHalfRemovedExecute(run, lane, metaDir), nil
	}

	// Step 8 for a present lane: the shared unlock → remove → mark → branch-D →
	// sidecar sequence, re-locking the disposer's own marker on a late-dirty KEEP
	// (downgradeRelockKeep: this live op's owner is still around to hold the lock).
	outcome, note := s.disposeUnchangedLaneMechanics(run, st, lane, metaDir, downgradeRelockKeep, forceDirty)
	switch outcome {
	case laneKeptDirty:
		return WorktreeDisposeResult{
			DelegateID: id,
			LanePath:   lanePath,
			Branch:     id,
			Message:    fmt.Sprintf("Evicted delegate %s but kept its lane: %s. It became dirty during disposal and stays resumable via delegate_send.", id, note),
		}, nil
	case laneDeclined:
		return WorktreeDisposeResult{}, fmt.Errorf("manage_worktree dispose: %s was evicted but its lane at %s could not be released for removal; left for prune", id, lanePath)
	default: // laneDisposed
		return WorktreeDisposeResult{
			DelegateID: id,
			LanePath:   lanePath,
			Branch:     id,
			Message:    fmt.Sprintf("Disposed delegate %s: removed its worktree lane at %s and deleted branch %s.", id, lanePath, id),
		}, nil
	}
}

// disposeHalfRemovedExecute finishes disposal of a half-removed lane (spec §P1
// step 8): the worktree dir is already gone, so mark the descriptor disposed and
// best-effort delete the leftover branch + sidecar. It never refuses.
func (s *Session) disposeHalfRemovedExecute(run worktree.GitRunner, lane isolationLane, metaDir string) WorktreeDisposeResult {
	if err := s.jobManager.appendEvent(jobstore.Event{
		Kind:       jobstore.EventDelegateDisposed,
		TS:         s.jobManager.now(),
		DelegateID: lane.delegateID,
	}); err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("delegate lane disposal mark failed for %s: %v", lane.delegateID, err)})
	}
	branchDeleted := false
	if _, err := run("branch", "-D", lane.delegateID); err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("delegate lane branch delete failed for %s: %v", lane.delegateID, err)})
	} else {
		branchDeleted = true
	}
	_ = worktree.DeleteSidecar(metaDir, lane.delegateID)
	msg := fmt.Sprintf("Disposed delegate %s: its worktree was already gone; marked disposed and cleaned up the sidecar.", lane.delegateID)
	if branchDeleted {
		msg = fmt.Sprintf("Disposed delegate %s: its worktree was already gone; deleted leftover branch %s and the sidecar.", lane.delegateID, lane.delegateID)
	}
	return WorktreeDisposeResult{
		DelegateID: lane.delegateID,
		LanePath:   lane.path,
		Branch:     lane.delegateID,
		Message:    msg,
	}
}

// disposeEvaluateLane applies the D0-model predicate to a lane that still exists
// on disk (spec §P1 step 6): clean AND (unchanged OR the full two-arm
// disposable test, cherry arm included). An unmerged lane refuses (force
// overrides); a dirty lane refuses (force_dirty overrides). The two flags are
// orthogonal, exactly like remove. It returns nil when the lane is collectible
// (the caller proceeds to execution) and a refusal error otherwise.
func (s *Session) disposeEvaluateLane(run worktree.GitRunner, id, lanePath string, sc worktree.Sidecar, force, forceDirty bool) error {
	clean, _, cErr := worktree.CleanTree(run, lanePath)
	if cErr != nil {
		return fmt.Errorf("manage_worktree dispose: %s clean-tree check: %w", id, cErr)
	}
	if !clean && !forceDirty {
		return fmt.Errorf("manage_worktree dispose: %s has uncommitted changes; pass force_dirty to discard them", id)
	}

	tipOut, tErr := run("-C", lanePath, "rev-parse", "HEAD")
	if tErr != nil {
		return fmt.Errorf("manage_worktree dispose: %s tip resolution: %w", id, tErr)
	}
	tip := strings.TrimSpace(tipOut)
	disposable, reason, dErr := disposableReason(run, tip, sc.BaseSHA, sc.MergeTarget)
	if dErr != nil {
		return fmt.Errorf("manage_worktree dispose: %s merge evaluation: %w", id, dErr)
	}
	if !disposable && !force {
		ahead := laneAheadCount(run, lanePath, sc.BaseSHA)
		return fmt.Errorf("manage_worktree dispose: %s has %d unmerged commit(s) (%s); merge them first or pass force to discard them", id, ahead, reason)
	}
	return nil // collectible (or force-overridden)
}

// disposeEvaluateHalfRemoved judges the half-removed residue arm (spec §P1
// step 6): the lane dir is gone but the record, branch, and sidecar remain (a
// crash between `git worktree remove` and `branch -D`). The branch tip is judged
// via the OriginalRoot env; it returns nil when the tip is collectible and a
// refusal error naming the state otherwise.
func (s *Session) disposeEvaluateHalfRemoved(run worktree.GitRunner, id string, sc worktree.Sidecar, force bool) error {
	tipOut, tErr := run("rev-parse", "refs/heads/"+id)
	if tErr != nil {
		return fmt.Errorf("manage_worktree dispose: %s lane is gone and its branch tip could not be resolved: %w", id, tErr)
	}
	tip := strings.TrimSpace(tipOut)
	disposable, reason, dErr := disposableReason(run, tip, sc.BaseSHA, sc.MergeTarget)
	if dErr != nil {
		return fmt.Errorf("manage_worktree dispose: %s branch evaluation: %w", id, dErr)
	}
	if !disposable && !force {
		return fmt.Errorf("manage_worktree dispose: %s lane is half-removed (worktree gone, branch %s remains, %s); merge it or pass force to delete the branch", id, id, reason)
	}
	return nil
}

// disposeAlreadyDisposedRemnants runs the idempotent already-disposed cleanup
// (spec §P1 step 1): a re-issued dispose is a no-op that best-effort clears any
// remnants (the branch if its tip judges D0-model-collectible, the sidecar) and
// reports already-disposed. It never refuses.
func (s *Session) disposeAlreadyDisposedRemnants(run worktree.GitRunner, id, lanePath, metaDir string, sc worktree.Sidecar) WorktreeDisposeResult {
	branchDeleted := false
	if tipOut, tErr := run("rev-parse", "refs/heads/"+id); tErr == nil {
		tip := strings.TrimSpace(tipOut)
		if disposable, _, dErr := disposableReason(run, tip, sc.BaseSHA, sc.MergeTarget); dErr == nil && disposable {
			if _, delErr := run("branch", "-D", id); delErr == nil {
				branchDeleted = true
			}
		}
	}
	_ = worktree.DeleteSidecar(metaDir, id)
	msg := fmt.Sprintf("Delegate %s was already disposed; cleaned up remnants.", id)
	if branchDeleted {
		msg = fmt.Sprintf("Delegate %s was already disposed; deleted its leftover branch and sidecar.", id)
	}
	return WorktreeDisposeResult{
		DelegateID:      id,
		LanePath:        lanePath,
		Branch:          id,
		AlreadyDisposed: true,
		Message:         msg,
	}
}

// disposeAlreadyDisposedGone reports the idempotent already-disposed no-op for a
// lane whose sidecar is already gone (spec §P1 step 1): a completed dispose
// deleted the branch and sidecar, so there is nothing left to clean and no
// control env to resolve. A not-exist sidecar is the ordinary fully-torn-down
// case; any other read error means the durable disposed mark is still ground
// truth but cleanup was degraded (permissions/corruption), which the message
// notes. It never refuses — the durable Disposed mark is authoritative.
func disposeAlreadyDisposedGone(id, lanePath string, scErr error) WorktreeDisposeResult {
	msg := fmt.Sprintf("Delegate %s was already disposed; its lane and sidecar are already gone.", id)
	if !os.IsNotExist(scErr) {
		msg = fmt.Sprintf("Delegate %s was already disposed; its sidecar could not be read (%v), but the durable disposed mark is authoritative and its lane is gone.", id, scErr)
	}
	return WorktreeDisposeResult{
		DelegateID:      id,
		LanePath:        lanePath,
		Branch:          id,
		AlreadyDisposed: true,
		Message:         msg,
	}
}

// delegateDisposeControlEnv builds the git control env for a dispose op rooted
// at the sidecar's OriginalRoot (spec §P1: the main repo root, resolved from the
// sidecar rather than by walking up from the possibly-gone lane), mirroring
// delegate revival's control-env dance. It fails closed when the session env is
// not local or the control sandbox policy cannot be satisfied.
func (s *Session) delegateDisposeControlEnv(originalRoot string) (execenv.ExecutionEnvironment, error) {
	local, ok := s.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		return nil, errors.New("dispose requires a local execution environment")
	}
	controlEnv := local.WithWorkingDirectory(originalRoot)
	if err := controlEnv.SandboxReRootError(); err != nil {
		return nil, err
	}
	if err := s.useDelegateWorktreeControlPolicy(controlEnv, originalRoot); err != nil {
		return nil, err
	}
	return controlEnv, nil
}

// laneWorktreePresent reports whether the lane directory still exists as a
// linked worktree (its .git file present). A missing lane routes dispose to the
// half-removed residue arm.
func laneWorktreePresent(lanePath string) bool {
	_, err := os.Stat(filepath.Join(lanePath, ".git"))
	return err == nil
}

// laneAheadCount returns how many commits the lane's HEAD is ahead of its
// recorded base, for a legible unmerged-refusal message. Best-effort: an
// unresolvable count reports 0.
func laneAheadCount(run worktree.GitRunner, lanePath, baseSHA string) int {
	out, err := run("-C", lanePath, "rev-list", "--count", baseSHA+"..HEAD")
	if err != nil {
		return 0
	}
	n, convErr := strconv.Atoi(strings.TrimSpace(out))
	if convErr != nil {
		return 0
	}
	return n
}

// findDelegateLaneRecord locates the folded job record and worktree-isolation
// restore descriptor for a delegate id. It returns the record carrying the
// descriptor (any of a delegate's resumed job records preserves the same
// descriptor); Disposed is read from the folded record. desc is nil when no
// worktree-isolation record for the id exists.
func findDelegateLaneRecord(recs map[string]*jobstore.JobRecord, id string) (*jobstore.JobRecord, *jobstore.DelegateRestoreDescriptor) {
	var withDesc *jobstore.JobRecord
	var desc *jobstore.DelegateRestoreDescriptor
	disposed := false
	for _, r := range recs {
		if r.DelegateID != id {
			continue
		}
		if r.Disposed {
			disposed = true
		}
		if r.DelegateRestore != nil && r.DelegateRestore.Isolation == "worktree" {
			withDesc = r
			desc = r.DelegateRestore
		}
	}
	if desc == nil {
		return nil, nil
	}
	// Reflect the disposed state on the returned record even if the descriptor
	// happened to come from a non-disposed job record for the same delegate.
	if disposed {
		withDesc.Disposed = true
	}
	return withDesc, desc
}

// delegateRecordQuiescent reports whether delegate id has no running/queued job
// and no pending owner notification — the dispose record-quiescence gate (spec
// §P1 step 1), taken under ONE jm.mu hold so the running-map read and the
// durable snapshot are consistent against the finalization sequence (the
// outstandingDelegateCount recipe, scoped to one delegate).
func (jm *jobManager) delegateRecordQuiescent(id string) (bool, error) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	for _, run := range jm.running {
		if run.rec != nil && run.rec.DelegateID == id {
			return false, nil
		}
	}
	recs, err := jm.store.Load()
	if err != nil {
		return false, err
	}
	for _, rec := range recs {
		if rec.DelegateID != id {
			continue
		}
		// Only this session's OWN delegate notifications hold quiescence open; a
		// forwarded descendant copy is the child's attention signal, covered by the
		// subtree walk (matching outstandingDelegateCount's owner filter).
		if rec.OwnerSessionID != "" && rec.OwnerSessionID != jm.sessionID {
			continue
		}
		if rec.Type == jobstore.JobDelegate && rec.NotifyState == jobstore.NotifyPending {
			return false, nil
		}
	}
	return true, nil
}

// subtreeWatchesTargeting reports whether any armed watch routes send_to the
// delegate id, or any pending watch-send targets it, in this session's job
// manager or any retained descendant's (spec §P1 step 2). The recursion is a
// Session-level walk because jobManager holds no subagent reference.
func (s *Session) subtreeWatchesTargeting(id string) bool {
	if s.jobManager != nil && s.jobManager.watchesTargeting(id) {
		return true
	}
	for _, sub := range s.subagents.directSubagents() {
		if sub.sess != nil && sub.sess.subtreeWatchesTargeting(id) {
			return true
		}
	}
	return false
}

// watchesTargeting reports whether any armed watch config sends to id, or any
// pending/terminal-flush watch-send frame resolves send_to id, in this job
// manager. Read under jm.mu.
func (jm *jobManager) watchesTargeting(id string) bool {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	for _, cfg := range jm.watches {
		if cfg.send != nil && cfg.send.To == id {
			return true
		}
		for key := range cfg.pending {
			if key.ResolvedSendTo == id {
				return true
			}
		}
	}
	for cfg := range jm.terminalFlush {
		for key := range cfg.pending {
			if key.ResolvedSendTo == id {
				return true
			}
		}
	}
	return false
}
