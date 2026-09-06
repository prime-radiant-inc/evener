package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/worktree"
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

const stableWorktreeDisposalReason = "isolation_disposed"

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
	return s.disposeStableDelegateLane(ctx, id, force, forceDirty)
}

// isDelegateID reports whether id has the delegate id shape (dlg_…). Only a
// delegate lane can be disposed; a bare worktree name or job id is refused as an
// invalid_request rather than silently missing.
func isDelegateID(id string) bool { return strings.HasPrefix(id, "dlg_") }

// disposeStableDelegateLane performs explicit isolation disposal from the
// stable delegate descriptor and controller state. jobs.jsonl remains solely
// the shell/watch authority and is never consulted for delegate ownership or
// lifecycle.
func (s *Session) disposeStableDelegateLane(ctx context.Context, id string, force, forceDirty bool) (WorktreeDisposeResult, error) {
	if s.delegateController == nil {
		return WorktreeDisposeResult{}, errors.New("manage_worktree dispose: stable delegate controller is unavailable")
	}
	state, err := s.delegateController.stableWorktreeSnapshotForOwner(s, id)
	if err != nil {
		if errors.Is(err, errDelegateNotControllable) {
			return WorktreeDisposeResult{}, fmt.Errorf("invalid_request: manage_worktree dispose: %s is not a direct worktree-isolated delegate of this session", id)
		}
		return WorktreeDisposeResult{}, fmt.Errorf("invalid_request: manage_worktree dispose: %w", err)
	}
	resumabilityClosed := !state.resumable
	alreadyDisposed := resumabilityClosed && state.notResumableReason == stableWorktreeDisposalReason
	lanePath := filepath.Clean(strings.TrimSpace(state.descriptor.WorkingDir))
	if lanePath == "" || lanePath == "." {
		return WorktreeDisposeResult{}, fmt.Errorf("manage_worktree dispose: %s has no recorded lane path", id)
	}

	metaDir := metaDirForLane(lanePath)
	sc, scErr := worktree.ReadSidecar(metaDir, id)
	if scErr != nil && resumabilityClosed {
		if laneWorktreePresent(lanePath) {
			return WorktreeDisposeResult{
				DelegateID:      id,
				LanePath:        lanePath,
				Branch:          id,
				AlreadyDisposed: alreadyDisposed,
				Message:         fmt.Sprintf("Delegate %s resumability was already closed for %s; retained residue at %s because its sidecar is unreadable: %v", id, state.notResumableReason, lanePath, scErr),
			}, nil
		}
		result := disposeAlreadyDisposedGone(id, lanePath, scErr)
		result.AlreadyDisposed = alreadyDisposed
		if !alreadyDisposed {
			result.Message = fmt.Sprintf("Disposed delegate %s after permanent closure %s; its lane and sidecar were already gone.", id, state.notResumableReason)
		}
		return result, nil
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
	defer disposeUnadoptedScratch(controlEnv)
	budgetCtx, cancelBudget := ensureCloseBudget(ctx)
	defer cancelBudget()
	run := s.newWorktreeGitRunner(budgetCtx, controlEnv)
	laneDirPresent := laneWorktreePresent(lanePath)

	if laneDirPresent {
		if local, ok := s.currentEnv().(*execenv.LocalExecutionEnvironment); ok {
			laneMain := resolveLaneMainRoot(local, lanePath)
			if laneMain != "" && filepath.Clean(laneMain) != filepath.Clean(originalRoot) {
				return WorktreeDisposeResult{}, fmt.Errorf("manage_worktree dispose: %s lane at %s resolves to main root %s but its sidecar records %s; refusing on a provenance mismatch", id, lanePath, laneMain, originalRoot)
			}
		}
	}
	alreadyDisposedHalfRemoved := alreadyDisposed && !laneDirPresent
	if state.active || state.currentRunOpen || state.pendingStopSeq != 0 {
		return WorktreeDisposeResult{}, fmt.Errorf("manage_worktree dispose: %s still has running or unfinished work; wait for it to finish", id)
	}
	if s.subtreeWatchesTargeting(id, state.descriptor.ChildSessionID) {
		return WorktreeDisposeResult{}, fmt.Errorf("manage_worktree dispose: %s is the target of an armed or pending watch send; clear the watch before disposing", id)
	}

	childID := state.descriptor.ChildSessionID
	sub := s.subagents.get(childID)
	if sub != nil && sub.sess != nil {
		outstanding, outstandingErr := sub.sess.treeHasOutstandingWork()
		if outstandingErr != nil {
			return WorktreeDisposeResult{}, fmt.Errorf("manage_worktree dispose: %s subtree check: %w", id, outstandingErr)
		}
		if outstanding {
			return WorktreeDisposeResult{}, fmt.Errorf("manage_worktree dispose: %s still has outstanding work in its delegate subtree; wait for it to finish", id)
		}
	}
	if shells := s.liveShellsUnderTree(lanePath); len(shells) > 0 {
		return WorktreeDisposeResult{}, fmt.Errorf("manage_worktree dispose: %s has live background shell(s) rooted in its lane: %s", id, strings.Join(shells, ", "))
	}

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

	st := worktree.Unlocked
	if laneDirPresent {
		locked, reason, lockErr := lockStateOf(run, lanePath)
		if lockErr != nil {
			return WorktreeDisposeResult{}, fmt.Errorf("manage_worktree dispose: %s lock state could not be verified: %w", id, lockErr)
		}
		if locked {
			st = worktree.ClassifyReason(reason, s.id, id)
		}
		if worktree.Decide(worktree.EvDisposeUnchanged, st) == worktree.ActRefuse {
			return WorktreeDisposeResult{}, fmt.Errorf("manage_worktree dispose: %s is locked by another owner; not the disposer's lane to reclaim", id)
		}
	}

	if laneDirPresent {
		err = s.disposeEvaluateLane(run, id, lanePath, sc, force, forceDirty)
	} else if !alreadyDisposedHalfRemoved {
		err = s.disposeEvaluateHalfRemoved(run, id, sc, force)
	}
	if err != nil {
		return WorktreeDisposeResult{}, err
	}

	closedState, already, plans, closeErr := s.delegateController.closeStableWorktreeResumability(s, id, stableWorktreeDisposalReason, false)
	if closeErr != nil {
		if errors.Is(closeErr, errDelegateTargetBusy) {
			return WorktreeDisposeResult{}, fmt.Errorf("manage_worktree dispose: %s became active while disposing; retry once it is idle", id)
		}
		return WorktreeDisposeResult{}, fmt.Errorf("manage_worktree dispose: close resumability: %w", closeErr)
	}
	s.delegateController.emitDelegateUpdates(plans)
	state = closedState
	alreadyDisposed = already && state.notResumableReason == stableWorktreeDisposalReason
	if alreadyDisposedHalfRemoved {
		return s.disposeAlreadyDisposedRemnants(run, id, lanePath, metaDir, sc), nil
	}
	gateConsumed = true
	return s.disposeStableExecute(budgetCtx, run, state, lanePath, metaDir, sub, laneDirPresent, st, forceDirty, alreadyDisposed)
}

func (s *Session) disposeStableExecute(ctx context.Context, run worktree.GitRunner, state stableDelegateWorktreeSnapshot, lanePath, metaDir string, sub *subagent, lanePresent bool, st worktree.LockState, forceDirty, alreadyClosed bool) (WorktreeDisposeResult, error) {
	id := state.delegateID
	if sub != nil && sub.sess != nil {
		teardownChildSession(ctx, sub.sess, retainChildScratch)
		s.subagents.removeSession(state.descriptor.ChildSessionID, sub.sess)
	}
	lane := isolationLane{delegateID: id, path: lanePath}
	if !lanePresent {
		result := s.disposeStableHalfRemoved(run, lane, metaDir)
		result.AlreadyDisposed = alreadyClosed
		return result, nil
	}
	outcome, note := s.disposeUnchangedLaneMechanics(run, st, lane, metaDir, downgradeUnlockKeep, forceDirty)
	switch outcome {
	case laneKeptDirty:
		return WorktreeDisposeResult{
			DelegateID:      id,
			LanePath:        lanePath,
			Branch:          id,
			AlreadyDisposed: alreadyClosed,
			Message:         fmt.Sprintf("Closed delegate %s resumability but retained residue: %s. The lane remains non-resumable and requires validation or manual cleanup.", id, note),
		}, nil
	case laneDeclined:
		return WorktreeDisposeResult{}, fmt.Errorf("manage_worktree dispose: %s resumability is closed but cleanup retained residue at %s because its lock could not be released; left for prune", id, lanePath)
	default:
		return WorktreeDisposeResult{
			DelegateID:      id,
			LanePath:        lanePath,
			Branch:          id,
			AlreadyDisposed: alreadyClosed,
			Message:         fmt.Sprintf("Disposed delegate %s: removed its worktree lane at %s and deleted branch %s.", id, lanePath, id),
		}, nil
	}
}

func (s *Session) disposeStableHalfRemoved(run worktree.GitRunner, lane isolationLane, metaDir string) WorktreeDisposeResult {
	branchDeleted := false
	if _, err := run("branch", "-D", lane.delegateID); err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("stable delegate lane branch delete failed for %s: %v", lane.delegateID, err)})
	} else {
		branchDeleted = true
	}
	_ = worktree.DeleteSidecar(metaDir, lane.delegateID)
	message := fmt.Sprintf("Disposed delegate %s: its worktree was already gone; cleaned up the sidecar.", lane.delegateID)
	if branchDeleted {
		message = fmt.Sprintf("Disposed delegate %s: its worktree was already gone; deleted leftover branch %s and the sidecar.", lane.delegateID, lane.delegateID)
	}
	return WorktreeDisposeResult{DelegateID: lane.delegateID, LanePath: lane.path, Branch: lane.delegateID, Message: message}
}

// disposeEvaluateLane applies the dirty-tree and ancestry checks before a
// stable delegate lane is removed.
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
		aheadDesc := "unmerged commit(s) unknown"
		if ahead, ok := laneAheadCount(run, lanePath, sc.BaseSHA); ok {
			aheadDesc = fmt.Sprintf("%d unmerged commit(s)", ahead)
		}
		return fmt.Errorf("manage_worktree dispose: %s has %s (%s); merge them first or pass force to discard them", id, aheadDesc, reason)
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
// recorded base, for a legible unmerged-refusal message. It is decoration on
// an already-decided refusal, not an input to the decision, so an unresolvable
// count reports ok=false rather than a bogus 0 — the caller says the count is
// unknown instead of claiming zero commits are what stands in the way.
func laneAheadCount(run worktree.GitRunner, lanePath, baseSHA string) (n int, ok bool) {
	out, err := run("-C", lanePath, "rev-list", "--count", baseSHA+"..HEAD")
	if err != nil {
		return 0, false
	}
	n, convErr := strconv.Atoi(strings.TrimSpace(out))
	if convErr != nil {
		return 0, false
	}
	return n, true
}

// subtreeWatchesTargeting reports whether any watch anywhere in s's subtree
// targets id, the delegate being disposed. receiverSessionID is id's own
// child session ID (the receiver identity a stable-receiver watch on id
// carries; #695), threaded unchanged through the recursion.
func (s *Session) subtreeWatchesTargeting(id, receiverSessionID string) bool {
	if s.jobManager != nil && s.jobManager.watchesTargeting(id, receiverSessionID) {
		return true
	}
	for _, sub := range s.subagents.directSubagents() {
		if sub.sess != nil && sub.sess.subtreeWatchesTargeting(id, receiverSessionID) {
			return true
		}
	}
	return false
}

// watchesTargeting reports whether any armed watch config sends to id, any
// pending/terminal-flush watch-send frame resolves send_to id, or a
// stable-receiver watch's receiver identity names id, in this job manager.
// Read under jm.mu.
//
// A stable-receiver watch (the observer-sidecar class, #655,
// configureStableWatchOnSource) synthesizes send.To as the
// stableWatchReceiverTarget sentinel, never the delegate ID
// (applyStableReceiverWatchSend), so the plain send.To/ResolvedSendTo checks
// above never match id for that class of watch. watchConfigMatchesReceiver —
// the same receiver-keyed matching liveWatchSummariesForReceiver and the
// #655 job_stop live-watch inventory use — catches it by receiver identity
// instead: id plus receiverSessionID (id's own child session ID, resolved by
// the caller).
//
// This does NOT catch the structurally distinct descendant-receiver watch
// class configureDescendantReceiverWatch installs (job_watch source="job_…"
// against a descendant's concrete job): that class always stamps an empty
// ReceiverDelegateID (session.ID() only, never owningDelegateID), so
// watchConfigMatchesReceiver — which requires both fields — can never match
// it here. That gap is real but verified benign (#695 adversarial review,
// round 2): the watch lives in the OWNER descendant's own job manager, which
// Session.close's subagent cascade also tears down (jobManager.
// closeRuntimeState deletes it from jm.watches, and routeWatchNotifications
// separately refuses on jm.closing) synchronously, within the same dispose
// call, before a disposed receiver could ever see a delivery — see
// TestDescendantReceiverWatchSurvivesJobManagerClose.
func (jm *jobManager) watchesTargeting(id, receiverSessionID string) bool {
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
		if watchConfigMatchesReceiver(cfg, receiverSessionID, id) {
			return true
		}
	}
	for cfg := range jm.terminalFlush {
		for key := range cfg.pending {
			if key.ResolvedSendTo == id {
				return true
			}
		}
		if watchConfigMatchesReceiver(cfg, receiverSessionID, id) {
			return true
		}
	}
	return false
}
