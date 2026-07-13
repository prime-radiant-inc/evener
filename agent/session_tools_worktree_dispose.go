package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/internal/worktree"
)

// WorktreeDisposeResult is the structured outcome of a successful dispose
// operation (spec §P1). In this build the only success path that returns a
// result (rather than proceeding to execution, which lands in the follow-up
// task) is the idempotent already-disposed remnant cleanup.
type WorktreeDisposeResult struct {
	DelegateID      string
	LanePath        string
	Branch          string
	AlreadyDisposed bool
	Message         string
}

// errDisposeExecutionNotImplemented is the internal marker a successful
// evaluation returns while dispose execution (spec §P1 steps 7-8) is not yet
// built. Every test whose input reaches a clean, collectible evaluation asserts
// this precise stub; it is replaced by the real evict → remove → mark → delete
// sequence in the follow-up task.
var errDisposeExecutionNotImplemented = errors.New("manage_worktree dispose: dispose execution not yet implemented")

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

// disposeDelegateLane runs the dispose validation ladder (spec §P1 steps 1-6).
// It ends at evaluation: a clean, collectible lane returns the
// errDisposeExecutionNotImplemented stub (execution is the follow-up task); the
// idempotent already-disposed path returns a result. Every refusal after the
// dispose gate is armed (step 4) clears the gate before returning.
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
	run := s.newWorktreeGitRunner(ctx, controlEnv)

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
	// EVERY later refusal exit clears the gate.
	gateArmed := false
	if sub != nil {
		if !sub.trySetDisposeGate() {
			return WorktreeDisposeResult{}, fmt.Errorf("manage_worktree dispose: %s became active while disposing; retry once it is idle", id)
		}
		gateArmed = true
	}
	clearGateOnRefusal := func() {
		if gateArmed {
			sub.clearDisposeGate()
		}
	}

	// Step 5: lock state. The lane must carry this session's own serf:dlg: marker
	// (or be unlocked crash residue); a foreign / session marker means someone
	// switched in — refuse (and clear the gate).
	if laneDirPresent {
		locked, reason, lsErr := lockStateOf(run, lanePath)
		if lsErr != nil {
			clearGateOnRefusal()
			return WorktreeDisposeResult{}, fmt.Errorf("manage_worktree dispose: %s lock state could not be verified: %w", id, lsErr)
		}
		st := worktree.Unlocked
		if locked {
			st = worktree.ClassifyReason(reason, s.id, id)
		}
		if worktree.Decide(worktree.EvDisposeUnchanged, st) == worktree.ActRefuse {
			clearGateOnRefusal()
			return WorktreeDisposeResult{}, fmt.Errorf("manage_worktree dispose: %s is locked by another owner; not the disposer's lane to reclaim", id)
		}
	}

	// Step 6: evaluate (D0-model). Half-removed residue (lane dir gone,
	// record+branch+sidecar remain) is judged by the branch tip via the
	// OriginalRoot env; otherwise the lane tree is evaluated in place.
	if !laneDirPresent {
		return s.disposeEvaluateHalfRemoved(run, id, lanePath, sc, force, clearGateOnRefusal)
	}
	return s.disposeEvaluateLane(run, id, lanePath, sc, force, forceDirty, clearGateOnRefusal)
}

// disposeEvaluateLane applies the D0-model predicate to a lane that still exists
// on disk (spec §P1 step 6): clean AND (unchanged OR the full two-arm
// disposable test, cherry arm included). An unmerged lane refuses (force
// overrides); a dirty lane refuses (force_dirty overrides). The two flags are
// orthogonal, exactly like remove. A collectible lane returns the execution
// stub.
func (s *Session) disposeEvaluateLane(run worktree.GitRunner, id, lanePath string, sc worktree.Sidecar, force, forceDirty bool, clearGateOnRefusal func()) (WorktreeDisposeResult, error) {
	clean, _, cErr := worktree.CleanTree(run, lanePath)
	if cErr != nil {
		clearGateOnRefusal()
		return WorktreeDisposeResult{}, fmt.Errorf("manage_worktree dispose: %s clean-tree check: %w", id, cErr)
	}
	if !clean && !forceDirty {
		clearGateOnRefusal()
		return WorktreeDisposeResult{}, fmt.Errorf("manage_worktree dispose: %s has uncommitted changes; pass force_dirty to discard them", id)
	}

	tipOut, tErr := run("-C", lanePath, "rev-parse", "HEAD")
	if tErr != nil {
		clearGateOnRefusal()
		return WorktreeDisposeResult{}, fmt.Errorf("manage_worktree dispose: %s tip resolution: %w", id, tErr)
	}
	tip := strings.TrimSpace(tipOut)
	disposable, reason, dErr := disposableReason(run, tip, sc.BaseSHA, sc.MergeTarget)
	if dErr != nil {
		clearGateOnRefusal()
		return WorktreeDisposeResult{}, fmt.Errorf("manage_worktree dispose: %s merge evaluation: %w", id, dErr)
	}
	if !disposable && !force {
		clearGateOnRefusal()
		ahead := laneAheadCount(run, lanePath, sc.BaseSHA)
		return WorktreeDisposeResult{}, fmt.Errorf("manage_worktree dispose: %s has %d unmerged commit(s) (%s); merge them first or pass force to discard them", id, ahead, reason)
	}

	// Collectible (or force-overridden). Execution (steps 7-8) is the follow-up
	// task. Until it exists, clear the gate before returning the stub so a
	// retained child is not left frozen by an op that cannot yet complete; the
	// execution task takes over gate ownership from the evaluation boundary.
	clearGateOnRefusal()
	return WorktreeDisposeResult{}, errDisposeExecutionNotImplemented
}

// disposeEvaluateHalfRemoved judges the half-removed residue arm (spec §P1
// step 6): the lane dir is gone but the record, branch, and sidecar remain (a
// crash between `git worktree remove` and `branch -D`). The branch tip is judged
// via the OriginalRoot env; a collectible tip proceeds to execution (the stub),
// an unmerged one refuses naming the state.
func (s *Session) disposeEvaluateHalfRemoved(run worktree.GitRunner, id, lanePath string, sc worktree.Sidecar, force bool, clearGateOnRefusal func()) (WorktreeDisposeResult, error) {
	tipOut, tErr := run("rev-parse", "refs/heads/"+id)
	if tErr != nil {
		clearGateOnRefusal()
		return WorktreeDisposeResult{}, fmt.Errorf("manage_worktree dispose: %s lane is gone and its branch tip could not be resolved: %w", id, tErr)
	}
	tip := strings.TrimSpace(tipOut)
	disposable, reason, dErr := disposableReason(run, tip, sc.BaseSHA, sc.MergeTarget)
	if dErr != nil {
		clearGateOnRefusal()
		return WorktreeDisposeResult{}, fmt.Errorf("manage_worktree dispose: %s branch evaluation: %w", id, dErr)
	}
	if !disposable && !force {
		clearGateOnRefusal()
		return WorktreeDisposeResult{}, fmt.Errorf("manage_worktree dispose: %s lane is half-removed (worktree gone, branch %s remains, %s); merge it or pass force to delete the branch", id, id, reason)
	}
	clearGateOnRefusal()
	return WorktreeDisposeResult{}, errDisposeExecutionNotImplemented
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
