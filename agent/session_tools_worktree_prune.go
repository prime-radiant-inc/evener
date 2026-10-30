package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/worktree"
	"primeradiant.com/evener/identifier"
)

// worktreePrune performs the prune operation (spec §5 prune, all three
// sweeps in order): registered managed worktrees, sidecar reconciliation,
// then git registry hygiene.
func (s *Session) worktreePrune(ctx context.Context, orphanedDelegateDays float64) (WorktreePruneResult, error) {
	st := s.worktreeStateSnapshot()
	if err := st.resolutionError("prune"); err != nil {
		return WorktreePruneResult{}, err
	}
	if st.env == nil {
		return WorktreePruneResult{}, errors.New("manage_worktree requires a local execution environment")
	}
	if st.mainRepoRoot == "" {
		return WorktreePruneResult{}, errors.New("manage_worktree prune: not in a git repository")
	}
	run, files, done, err := s.worktreeControlRunWithFiles(ctx, st.mainRepoRoot)
	if err != nil {
		return WorktreePruneResult{}, err
	}
	defer done()

	projectDir := filepath.Join(st.worktreeRoot, st.project.ID)
	metaDir := metaDirForProject(projectDir)

	out, err := run("worktree", "list", "--porcelain")
	if err != nil {
		return WorktreePruneResult{}, fmt.Errorf("manage_worktree prune: listing worktrees: %w", err)
	}
	managed := managedPorcelainEntries(worktree.ParsePorcelain(out), projectDir)

	sweep1Policy := prunePolicy()
	sweep1Policy.reclaimOrphanedDelegates = time.Duration(orphanedDelegateDays * float64(24*time.Hour))
	sweep1Policy.lockFiles = files
	removed1, skipped1, unlocked1, err := s.collectRegisteredLanes(ctx, run, managed, metaDir, sweep1Policy)
	if err != nil {
		return WorktreePruneResult{}, err
	}
	removed2, skipped2, err := s.reconcileOrphanSidecars(ctx, run, managed, metaDir, prunePolicy())
	if err != nil {
		return WorktreePruneResult{}, err
	}

	result := WorktreePruneResult{
		Removed:  append(removed1, removed2...),
		Skipped:  append(skipped1, skipped2...),
		Unlocked: unlocked1,
	}

	ran, skipReason, err := pruneGitRegistry(run, projectDir)
	if err != nil {
		return WorktreePruneResult{}, err
	}
	result.RegistryPruned = ran
	result.RegistrySkipReason = skipReason

	return result, nil
}

// laneSweepPolicy parameterizes the shared collection sweeps (spec §P3
// "Implementation parameterizes both prune sweeps"). The model-facing `prune`
// op passes prunePolicy() (unchanged behavior — abort on the first mutation
// failure, no grace, every managed worktree in scope, the full unchanged/merged
// disposability test). The no-model-in-the-loop P3 residue collector passes a
// policy that narrows scope to delegate lanes past a grace window, swaps in the
// local-refs-only D0-auto predicate, appends a Disposed mark for its own store's
// records, and — critically — treats every per-lane mutation failure as a lost
// race (skip-and-continue) rather than aborting the whole pass.
type laneSweepPolicy struct {
	// delegateOnly restricts collection to sidecars carrying a DelegateID (P3
	// residue collection); prune collects any managed lane.
	delegateOnly bool
	// grace, when > 0, skips any sidecar whose file mtime is younger than it
	// (P3's revival/hand-off window, keyed on the touched-on-KEEP mtime). Prune
	// sweep 1 disables it (0); prune sweep 2 keeps worktree.ReconcileGrace.
	grace time.Duration
	// disposableAt judges an entry that still has a live worktree (sweep 1): it
	// runs any cleanliness/tip checks itself and returns (ok, reasonTag, err).
	// A non-nil err is always a soft per-lane skip in BOTH policies.
	disposableAt func(run worktree.GitRunner, lanePath string, sc worktree.Sidecar) (ok bool, reason string, err error)
	// disposableBranch judges a branch with no registered worktree (sweep 2).
	disposableBranch func(run worktree.GitRunner, tip, baseSHA, mergeTarget string) (ok bool, reason string, err error)
	// abortOnError selects the mutation-failure contract: true → the sweep
	// returns the error and aborts (prune); false → the lane is reported skipped
	// and the sweep continues (P3: refusal/ENOENT is a normal race loss).
	abortOnError bool
	// prepareDispose, when non-nil, durably closes a stable delegate before the
	// first destructive collection operation.
	prepareDispose func(delegateID string) error
	// reclaimStrandedOwnMarker lets the sweep collect a lane carrying this
	// session's own marker while the session does not occupy it. Only the
	// model-facing prune sets it: it is a tool call serialized against the
	// session's own create/switch, whereas the timer-driven residue sweep can run
	// between a switch taking its marker and recording the lane as current.
	reclaimStrandedOwnMarker bool
	// lockFiles is the control environment's file layer, through which the sweep
	// proves the control policy lets it write the registry before it releases a
	// stranded own marker. Set together with reclaimStrandedOwnMarker.
	lockFiles execenv.FileMutator
	// reclaimOrphanedDelegates, when > 0, lets the sweep release a locked
	// delegate lane whose parent session provably cannot act as its disposer
	// (orphanedDelegateMarker): the parent's store records the delegate's
	// isolation disposal, or the parent session is gone or has been inactive
	// at least this long. The operator-directed orphan arm (#5471); the
	// model-facing prune sets it only when the caller passes
	// orphaned_delegate_days.
	reclaimOrphanedDelegates time.Duration
}

// ctxCancelled reports whether ctx's deadline/cancellation has fired. It exists
// as a named predicate so the sweeps' per-lane budget check reads as a control
// decision rather than an error inspection preceding a nil-error return.
func ctxCancelled(ctx context.Context) bool {
	return ctx.Err() != nil
}

// prunePolicy reproduces the model-facing `prune` sweep behavior exactly (spec
// §5): every managed lane in scope, no grace, abort on the first mutation
// failure, the full unchanged-or-merged disposability test (both the ancestry
// and cherry arms), and no Disposed mark.
func prunePolicy() laneSweepPolicy {
	return laneSweepPolicy{
		abortOnError:             true,
		reclaimStrandedOwnMarker: true,
		disposableAt: func(run worktree.GitRunner, lanePath string, sc worktree.Sidecar) (bool, string, error) {
			clean, offending, cErr := worktree.CleanTree(run, lanePath)
			if cErr != nil {
				return false, "status check failed: " + cErr.Error(), cErr
			}
			if !clean {
				return false, "dirty: " + strings.Join(offending, ", "), nil
			}
			tipOut, tErr := run("-C", lanePath, "rev-parse", "HEAD")
			if tErr != nil {
				return false, "rev-parse HEAD failed: " + tErr.Error(), tErr
			}
			return disposableOrMergeCheckFailure(run, strings.TrimSpace(tipOut), sc.BaseSHA, sc.MergeTarget)
		},
		disposableBranch: disposableOrMergeCheckFailure,
	}
}

// disposableOrMergeCheckFailure is disposableReason with a failed merge check
// reported as the skip reason, alongside the error that decides whether the
// sweep aborts.
func disposableOrMergeCheckFailure(run worktree.GitRunner, tip, baseSHA, mergeTarget string) (bool, string, error) {
	disposable, reasonTag, dErr := disposableReason(run, tip, baseSHA, mergeTarget)
	if dErr != nil {
		return false, "merge check failed: " + dErr.Error(), dErr
	}
	return disposable, reasonTag, nil
}

// collectRegisteredLanes disposes of registered managed worktrees (spec §5
// prune sweep 1): a worktree is collected (dir + branch + sidecar removed)
// iff it is unlocked (or carries only this session's own stale marker, which is
// no obstacle — see staleOwnMarker below), has no live work under it, has a
// sidecar, passes the policy's grace/delegate filters, and is disposable per
// the policy predicate.
// Every entry that fails one of those tests is reported skipped with the reason;
// any per-entry git query failure is treated as a soft skip. A mutation failure
// aborts (prune) or is reported as a lost race and the sweep continues (P3),
// per policy.abortOnError. The sweep stops early once ctx is cancelled: the P3
// close pass's shared budget, or the prune request.
func (s *Session) collectRegisteredLanes(ctx context.Context, run worktree.GitRunner, managed []managedEntry, metaDir string, policy laneSweepPolicy) (removed, skipped, unlocked []WorktreePruneEntry, err error) {
	// One clock per pass: every lane's staleness is judged against the same
	// instant. The orphan memo holds one folded store state per parent
	// session, so a pass over many lanes of one parent folds that parent's
	// journal once.
	now := time.Now()
	var orphanMemo map[string]delegatestore.State
	if policy.reclaimOrphanedDelegates > 0 {
		orphanMemo = make(map[string]delegatestore.State)
	}
	for _, e := range managed {
		if ctxCancelled(ctx) {
			break
		}
		// Not locked: the occupancy rule, owner-independent (spec §5 sweep 1;
		// EvPruneCandidate skips any locked state regardless of whose marker).
		lockSt := worktree.Unlocked
		if e.Locked {
			lockSt = worktree.ClassifyReason(e.LockReason, s.id, "")
		}
		// A lane carrying THIS session's own marker while the session does not
		// occupy it is crash residue — what a process death inside the lane, or a
		// resume whose re-entry was refused before the lane was recorded as
		// current, leaves behind. `remove` and `switch` already treat that residue
		// as unlocked-for-us (spec §5), so prune is the one operation that stranded
		// a lane under its own owner: it skipped the marker, leaving the lane
		// locked and un-collectible until a later close swept it. The marker is
		// therefore no obstacle to ELIGIBILITY here — but it is released only on
		// the path that actually collects the lane (just before collectLane), never
		// during evaluation. Eagerly unlocking a lane this pass then skips (dirty,
		// unmerged, in-grace, live work, not a delegate lane) is what would expose
		// it: the lock is the only thing stopping another session's prune, and its
		// live-work view cannot see this session's work under the lane. Only our
		// own marker is ever released; a foreign or delegate marker stays the skip
		// it was, so another live session's occupancy is never taken.
		staleOwnMarker := policy.reclaimStrandedOwnMarker && lockSt == worktree.OwnSession && !s.occupiesManagedLane(e.Path)
		// An orphaned delegate marker is the one foreign lock this pass may
		// treat as absent (#5471): its parent's disposal lifecycle is the
		// lock's only sanctioned releaser and provably cannot act. The
		// release itself waits for the rungs below, and only the marker the
		// plan was made around is ever released — the claim protocol re-reads
		// the lock before touching it.
		orphanMark := worktree.Marker{}
		if policy.reclaimOrphanedDelegates > 0 && lockSt == worktree.Foreign && e.LockReason != "" {
			if m, orphaned := s.orphanedDelegateMarker(ctx, e.LockReason, policy.reclaimOrphanedDelegates, now, orphanMemo); orphaned {
				orphanMark = m
			}
		}
		if !staleOwnMarker && orphanMark.DelegateID == "" && worktree.Decide(worktree.EvPruneCandidate, lockSt) == worktree.ActSkip {
			reason := "locked"
			if e.LockReason != "" {
				reason = fmt.Sprintf("locked (%s)", e.LockReason)
			}
			skipped = append(skipped, WorktreePruneEntry{Name: e.Name, Path: e.Path, Reason: reason})
			continue
		}

		// Has a sidecar: provenance unknown otherwise, not ours to judge.
		sc, scErr := worktree.ReadSidecar(metaDir, e.Name)
		if scErr != nil {
			skipped = append(skipped, WorktreePruneEntry{Name: e.Name, Path: e.Path, Reason: "sidecar-less"})
			continue
		}
		if orphanMark.DelegateID != "" && sc.DelegateID != orphanMark.DelegateID {
			// Provenance: the marker and the sidecar must name the same
			// delegate, or the lane is not the orphan the plan was made
			// around.
			skipped = append(skipped, WorktreePruneEntry{Name: e.Name, Path: e.Path, Reason: "locked (" + e.LockReason + ")"})
			continue
		}

		if policy.delegateOnly && sc.DelegateID == "" {
			skipped = append(skipped, WorktreePruneEntry{Name: e.Name, Path: e.Path, Reason: "not a delegate lane"})
			continue
		}

		ignoredIdleDelegateID := ""
		if policy.prepareDispose != nil {
			ignoredIdleDelegateID = sc.DelegateID
		}
		// No live work under it (belt and braces alongside the lock). P3 may
		// omit only the exact idle descriptor whose lane it is preparing to
		// close; ordinary prune and all other work remain strict blockers.
		if live := s.liveWorkUnderExceptIdleDelegate(e.Path, ignoredIdleDelegateID); len(live) > 0 {
			skipped = append(skipped, WorktreePruneEntry{Name: e.Name, Path: e.Path, Reason: "live work: " + strings.Join(live, ", ")})
			continue
		}
		orphanReleased := false
		if orphanMark.DelegateID != "" {
			released, relErr := s.releaseOrphanedDelegateMarker(policy.lockFiles, e.Path, orphanMark)
			if relErr != nil {
				skipped = append(skipped, WorktreePruneEntry{Name: e.Name, Path: e.Path, Reason: "releasing orphaned delegate marker: " + relErr.Error()})
				continue
			}
			if !released {
				skipped = append(skipped, WorktreePruneEntry{Name: e.Name, Path: e.Path, Reason: "lock changed since listing"})
				continue
			}
			orphanReleased = true
		}
		if policy.grace > 0 {
			if age, ageErr := worktree.SidecarAge(metaDir, e.Name); ageErr == nil && age < policy.grace {
				skipped = append(skipped, WorktreePruneEntry{Name: e.Name, Path: e.Path, Reason: "in-grace"})
				continue
			}
		}

		disposable, reasonTag, dErr := policy.disposableAt(run, e.Path, sc)
		// The unlock is the deliverable for a lane the release opened but this
		// pass cannot collect — not disposable, or not provably disposable: the
		// lane stays, visible to the merge and force paths that were the only
		// cleanup before, and to a later prune once its work lands.
		if dErr != nil || !disposable {
			if orphanReleased {
				unlocked = append(unlocked, WorktreePruneEntry{Name: e.Name, Path: e.Path, Reason: reasonTag})
				continue
			}
			skipped = append(skipped, WorktreePruneEntry{Name: e.Name, Path: e.Path, Reason: reasonTag})
			continue
		}

		// Collect: remove the worktree, delete the branch (-D, the merge gate
		// above already passed — spec §5 sweep 1's closing note, remove step 9's
		// rationale for never trusting `-d`), delete the sidecar. The optional
		// Disposed mark is appended after the remove and before the branch delete
		// (spec §P3: own-store records only).
		//
		// A lane that only survived the eligibility gate because its marker is
		// this session's own, stale one (staleOwnMarker) is released here — at the
		// one point where the pass has committed to collecting it, and after every
		// skip rung that could have kept the lane (and needed its lock) has been
		// passed. The release routes through the same EvLeave rule close uses, so a
		// foreign or delegate marker is still left untouched.
		//
		// The verdict that got us here came from the ONE `worktree list` snapshot
		// taken at the top of the pass, which every earlier lane's git calls have
		// since aged, so the marker is claimed and read in a single atomic step
		// (releaseStrandedOwnMarker): a marker someone else released since the
		// snapshot (a human running the documented `git worktree unlock` recovery,
		// or a second process on the same session id) and another session then
		// took is never unlocked, which would hand this lane's removal to prune
		// while that session is rooted in it. A lane whose lock is no longer ours
		// is skipped, not collected.
		if staleOwnMarker {
			released, relErr := s.releaseStrandedOwnMarker(policy.lockFiles, e.Path)
			if relErr != nil {
				skipped = append(skipped, WorktreePruneEntry{Name: e.Name, Path: e.Path, Reason: "releasing stranded own marker: " + relErr.Error()})
				continue
			}
			if !released {
				skipped = append(skipped, WorktreePruneEntry{Name: e.Name, Path: e.Path, Reason: "lock changed since listing"})
				continue
			}
		}
		if cErr := s.collectLane(run, metaDir, e.Name, sc.BranchOrName(), e.Path, true, policy); cErr != nil {
			// The release was made for a removal that did not happen. Put the
			// marker back so a lane this pass could not collect is exactly as
			// protected as it was, rather than left open to another session's
			// sweep (whose live-work view cannot see this session's work under it).
			if staleOwnMarker {
				s.restoreStrandedOwnMarker(e.Path)
			}
			if orphanReleased {
				s.restoreOrphanedDelegateMarker(e.Path, e.LockReason)
			}
			if policy.abortOnError {
				return nil, nil, nil, fmt.Errorf("manage_worktree prune: %w", cErr)
			}
			skipped = append(skipped, WorktreePruneEntry{Name: e.Name, Path: e.Path, Reason: "collect race: " + cErr.Error()})
			continue
		}
		removed = append(removed, WorktreePruneEntry{
			Name: e.Name, Path: e.Path,
			WorktreeRemoved: true, BranchRemoved: true, SidecarRemoved: true,
			Reason: reasonTag,
		})
	}

	return removed, skipped, unlocked, nil
}

// releaseClaimedLock releases a lane's lock iff the lock file still carries
// the marker matches() accepts, reporting whether it did. It is false, with
// nothing released, when the lane is now unlocked or carries some other
// marker. matches sees the raw lock-file bytes.
//
// Confirming the marker and unlocking cannot be two steps: between a check and
// `git worktree unlock` the marker can be released (the documented manual
// recovery, or a second process on the same session id) and the lane taken by
// another session, whose lock the unlock would then remove. Git has no
// owner-checked unlock, so the lock file itself is claimed with a rename, which
// moves exactly the file that was there at that instant. The claimed content
// is then read: the expected marker is deleted (the lane is unlocked), anything
// else is put back without displacing a lock taken since. The lock file is
// read first, so a foreign lock is taken away only when it replaced the
// expected marker between that read and the claim, and then only until the
// restore.
//
// That instant is the floor, not an oversight: a filesystem has no
// compare-and-delete, so no protocol can keep git's lock effective across
// inspecting a file it is about to remove. Reaching it takes the expected
// marker being removed and another session's lock appearing between the read
// and the rename, and costs a foreign lane its lock only if a third process
// then collects it in those microseconds (or this process dies before the
// restore). Every other ordering leaves the foreign lock in place.
//
// The control policy's write authority applies exactly as it does to the
// sandboxed `git worktree remove` that follows: a session whose policy withholds
// registry writes is refused before the lock is touched (requireRegistryWrite).
func (s *Session) releaseClaimedLock(files execenv.FileMutator, lanePath string, matches func([]byte) bool, claimPrefix string) (released bool, err error) {
	lockFile, err := laneLockFile(lanePath)
	if err != nil {
		return false, err
	}
	// Look before claiming: the claim briefly takes the lock away, so a lock
	// that is plainly not the expected one is never touched.
	reason, readErr := os.ReadFile(lockFile)
	if os.IsNotExist(readErr) {
		return false, nil
	}
	if readErr != nil {
		return false, readErr
	}
	if !matches(reason) {
		return false, nil
	}
	if err := requireRegistryWrite(files, filepath.Dir(lockFile)); err != nil {
		return false, err
	}
	if s.worktreeBeforeStrandedMarkerRelease != nil {
		s.worktreeBeforeStrandedMarkerRelease(lanePath)
	}
	claimed, err := os.CreateTemp(filepath.Dir(lockFile), claimPrefix+"-*")
	if err != nil {
		return false, err
	}
	claimedPath := claimed.Name()
	_ = claimed.Close()
	if err := os.Rename(lockFile, claimedPath); err != nil {
		_ = os.Remove(claimedPath)
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	claimedReason, claimedErr := os.ReadFile(claimedPath)
	if claimedErr == nil && matches(claimedReason) {
		// The lane is already unlocked; a leftover claim file is harmless to git.
		if rmErr := os.Remove(claimedPath); rmErr != nil {
			s.emit(events.WarningData{Message: fmt.Sprintf("removing the released lock claim %s failed: %v", claimedPath, rmErr)})
		}
		return true, nil
	}
	// Not the expected marker, or unreadable: restore it. Link fails when a
	// lock was taken in the meantime, and that newer lock stays.
	if linkErr := os.Link(claimedPath, lockFile); linkErr != nil && !os.IsExist(linkErr) {
		return false, fmt.Errorf("restoring the lock claimed from %s: %w", lanePath, linkErr)
	}
	_ = os.Remove(claimedPath)
	if claimedErr != nil {
		return false, fmt.Errorf("reading the claimed lock from %s: %w", lanePath, claimedErr)
	}
	return false, nil
}

// releaseStrandedOwnMarker releases the lock on a lane iff it still carries this
// session's own marker and the session does not occupy the lane, reporting
// whether it did. It is false, with nothing released, when the lane is now
// unlocked, carries a foreign or delegate marker, or is occupied after all.
// The claim protocol is the shared releaseClaimedLock's.
func (s *Session) releaseStrandedOwnMarker(files execenv.FileMutator, lanePath string) (released bool, err error) {
	if s.occupiesManagedLane(lanePath) {
		return false, nil
	}
	return s.releaseClaimedLock(files, lanePath, s.isOwnMarker, "locked.release")
}

// requireRegistryWrite proves, through the control environment's policy-checked
// file layer, that dir (a worktree registry entry) is writable under the control
// policy: it writes and removes a probe file there. The lock file itself is then
// edited with plain os calls, which have the exclusive-create and hard-link
// semantics the claim and restore depend on and the file layer does not offer.
//
// The probe authorizes the directory as it is now. It does not defend against a
// process that swaps the registry entry for a symlink between the probe and the
// edit: anything able to do that already holds write access to the worktree
// registry, the very grant the control policy gives this op, and could rewrite
// any lock directly. Anchoring each edit to a captured directory descriptor
// would need exclusive-create, rename and link operations on the file layer;
// the threat does not justify that surface.
func requireRegistryWrite(files execenv.FileMutator, dir string) error {
	probe := filepath.Join(dir, ".locked.probe")
	if err := files.WriteFileRaw(probe, nil, 0o644); err != nil {
		return err
	}
	return files.RemovePath(probe)
}

// isOwnMarker reports whether the contents of a lock file are this session's
// own occupancy marker.
func (s *Session) isOwnMarker(lockFileContents []byte) bool {
	return worktree.ClassifyReason(strings.TrimSpace(string(lockFileContents)), s.id, "") == worktree.OwnSession
}

// laneLockFile is the file git keeps a locked worktree's reason in, found
// through the lane's `.git` pointer file.
func laneLockFile(lanePath string) (string, error) {
	pointer, err := os.ReadFile(filepath.Join(lanePath, ".git"))
	if err != nil {
		return "", err
	}
	gitdir, ok := identifier.ParseGitdirPointer(string(pointer))
	if !ok {
		return "", fmt.Errorf("%s/.git is not a worktree pointer", lanePath)
	}
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(lanePath, gitdir)
	}
	return filepath.Join(gitdir, "locked"), nil
}

// restoreLockMarker writes a lock file carrying reason back onto a lane whose
// release was made for a collection that then failed, so a lane this pass
// could not collect keeps the protection it had. It writes the lock file
// directly, exactly as the release removed it: the failure may be the prune
// request's own cancellation, and every git query on that request's runner
// would then fail the same way and leave the lane unlocked. Creating the file
// exclusively makes it a no-op when the lane is gone (a remove that succeeded
// before a later step failed has nothing left to lock) and when the lane was
// locked since — a newer lock is never displaced. what names the marker kind
// in warnings. Best-effort: a failure warns, and the lane is left as the
// failure found it.
func (s *Session) restoreLockMarker(lanePath, reason, what string) {
	lockFile, err := laneLockFile(lanePath)
	if err != nil {
		return
	}
	f, err := os.OpenFile(lockFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if !os.IsExist(err) && !os.IsNotExist(err) {
			s.emit(events.WarningData{Message: fmt.Sprintf("re-locking %s worktree marker at %s after a failed collection failed: %v", what, lanePath, err)})
		}
		return
	}
	_, writeErr := f.WriteString(reason + "\n")
	if closeErr := f.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		s.emit(events.WarningData{Message: fmt.Sprintf("re-locking %s worktree marker at %s after a failed collection failed: %v", what, lanePath, writeErr)})
	}
}

// restoreStrandedOwnMarker puts this session's own marker back on a lane
// whose collection failed after its release, through the shared
// restoreLockMarker.
func (s *Session) restoreStrandedOwnMarker(lanePath string) {
	s.restoreLockMarker(lanePath, worktree.FormatSessionMarker(s.id), "stranded own")
}

// collectLane runs the collection mechanics for one lane (spec §5 sweep 1 /
// §P3): `git worktree remove` (only when a worktree is registered), then — for
// a policy that marks — the own-store Disposed mark, then `branch -D`, then the
// sidecar delete. A remove error with a definitively absent lane is a concurrent
// collector win, so this collector continues the remaining cleanup; a present
// lane or transient stat failure remains an error. Other failures are returned
// wrapped, and the caller decides whether to abort (prune) or treat them as a
// lost race (P3). The mark is best-effort inside markDisposed itself, so it never
// blocks the branch/sidecar cleanup.
// collectLane deletes the lane's branch via the branch parameter — a
// parent-named delegate lane's branch differs from the name its sidecar and
// directory are keyed by.
func (s *Session) collectLane(run worktree.GitRunner, metaDir, name, branch, path string, hasWorktree bool, policy laneSweepPolicy) error {
	if policy.prepareDispose != nil {
		if err := policy.prepareDispose(name); err != nil {
			return fmt.Errorf("preparing stable delegate %q for disposal: %w", name, err)
		}
	}
	if hasWorktree {
		if _, err := run("worktree", "remove", "--", path); err != nil {
			// Another collector may have completed the same remove first. Continue
			// only when the lane is definitively gone; doubt is non-destructive.
			if _, statErr := s.statLaneGitDir(path); !os.IsNotExist(statErr) {
				return fmt.Errorf("removing %s: %w", path, err)
			}
		}
	}
	if _, err := run("branch", "-D", branch); err != nil {
		// Another path may have completed this deletion first (a dispose that
		// deletes the branch before its worktree, or a collection whose sidecar
		// outlived the branch). An already-absent branch is this step's success
		// state, so continue to the sidecar delete. Doubt is non-destructive, as
		// in the worktree remove above: a branch that still exists, or a probe
		// that could not answer, stays a real failure.
		gone, probeErr := branchGone(run, branch)
		if probeErr != nil || !gone {
			return fmt.Errorf("deleting branch %q: %w", branch, err)
		}
	}
	if err := s.deleteWorktreeSidecar(metaDir, name); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("deleting sidecar for %q: %w", name, err)
	}
	return nil
}

// disposableReason judges whether tip is disposable per spec §5's shared
// unchanged/merged test: tip == baseSHA (unchanged), or merged into
// mergeTarget's recorded tip (never HEAD). reason is "unchanged",
// "merged (ancestry)", "merged (cherry)", "merge target unknown", or
// "unmerged" — always set, whether or not disposable is true, so callers can
// use it directly as a skip reason on the false path.
func disposableReason(run worktree.GitRunner, tip, baseSHA, mergeTarget string) (disposable bool, reason string, err error) {
	if tip == baseSHA {
		return true, "unchanged", nil
	}
	mr, err := worktree.Merged(run, tip, mergeTarget, baseSHA)
	if err != nil {
		return false, "", err
	}
	switch {
	case mr.Merged:
		return true, fmt.Sprintf("merged (%s)", mr.Arm), nil
	case mr.TargetUnknown:
		return false, "merge target unknown", nil
	default:
		return false, "unmerged", nil
	}
}

// reconcileOrphanSidecars reconciles metadata sidecars with no matching
// registered worktree (spec §5 prune sweep 2): a sidecar younger than
// worktree.ReconcileGrace (judged by file mtime) is left alone as
// possibly-racing-create residue; older ones are resolved per the branch's
// current state (gone → stale, adopted → sidecar dropped, disposable →
// branch+sidecar deleted, checked out elsewhere → skipped, unmerged →
// kept as residue).
func (s *Session) reconcileOrphanSidecars(ctx context.Context, run worktree.GitRunner, managed []managedEntry, metaDir string, policy laneSweepPolicy) (removed, skipped []WorktreePruneEntry, err error) {
	registered := make(map[string]bool)
	for _, e := range managed {
		registered[e.Name] = true
	}

	sidecars, corrupt, err := worktree.ListSidecarsWithErrors(metaDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("manage_worktree prune: listing metadata: %w", err)
	}

	// grace defaults to worktree.ReconcileGrace (prune's racing-create window);
	// P3 overrides it with laneGrace.
	grace := policy.grace
	if grace == 0 {
		grace = worktree.ReconcileGrace
	}

	// A reserved name whose bytes do not decode — a crash-torn create that no
	// error path could clean up — is invisible to ListSidecars, so reconciliation
	// must surface it rather than skip it: the name stays uncreatable until a
	// human removes the file, and naming it here is how that repair is
	// discoverable. It is reported, never deleted: malformed metadata does not
	// authorize a destructive cleanup. The same grace that protects an in-flight
	// create's not-yet-written sidecar applies here, so a create caught between
	// its O_EXCL open and its write is not misreported as corrupt.
	for _, ce := range corrupt {
		if ctxCancelled(ctx) {
			break
		}
		if registered[ce.Name] {
			continue // a live registered worktree exists; sweep 1 already reports it "sidecar-less"
		}
		age, ageErr := worktree.SidecarAge(metaDir, ce.Name)
		switch worktree.ClassifyCorruptReservation(ce.Error, age, ageErr, grace) {
		case worktree.CorruptReservationInGrace:
			skipped = append(skipped, WorktreePruneEntry{Name: ce.Name, Reason: "in-grace"})
		case worktree.CorruptReservationStaleCorrupt:
			skipped = append(skipped, WorktreePruneEntry{
				Name:   ce.Name,
				Reason: "corrupt sidecar: " + ce.Error.Error(),
			})
		default:
			// None: not an undecodable record. Indeterminate: deleted between the
			// listing read and the age probe. Neither has anything to report.
		}
	}

	for _, sc := range sidecars {
		if ctxCancelled(ctx) {
			break
		}
		if registered[sc.Name] {
			continue // a live registered worktree exists; sweep 1's job, not reconciliation's
		}
		if policy.delegateOnly && sc.DelegateID == "" {
			skipped = append(skipped, WorktreePruneEntry{Name: sc.Name, Reason: "not a delegate lane"})
			continue
		}

		age, ageErr := worktree.SidecarAge(metaDir, sc.Name)
		if ageErr == nil && age < grace {
			skipped = append(skipped, WorktreePruneEntry{Name: sc.Name, Reason: "in-grace"})
			continue
		}

		branch := sc.BranchOrName()
		if !branchExists(run, branch) {
			if err := s.deleteWorktreeSidecar(metaDir, sc.Name); err != nil && !os.IsNotExist(err) {
				if policy.abortOnError {
					return nil, nil, fmt.Errorf("manage_worktree prune: deleting stale sidecar %q: %w", sc.Name, err)
				}
				skipped = append(skipped, WorktreePruneEntry{Name: sc.Name, Reason: "collect race: " + err.Error()})
				continue
			}
			removed = append(removed, WorktreePruneEntry{Name: sc.Name, SidecarRemoved: true, Reason: "stale sidecar (no worktree, no branch)"})
			continue
		}

		tipOut, tErr := run("rev-parse", "--verify", "refs/heads/"+branch)
		if tErr != nil {
			skipped = append(skipped, WorktreePruneEntry{Name: sc.Name, Reason: "rev-parse failed: " + tErr.Error()})
			continue
		}
		tip := strings.TrimSpace(tipOut)

		// Adopted: the branch survived a prior branch-kept removal and the
		// user has since built on it (tip is neither the recorded base nor
		// the tip evener recorded at removal time) — evener's claim expires.
		if sc.WorktreeRemoved && worktree.Adopted(tip, sc.BaseSHA, sc.TipSHAAtRemoval) {
			if err := s.deleteWorktreeSidecar(metaDir, sc.Name); err != nil && !os.IsNotExist(err) {
				if policy.abortOnError {
					return nil, nil, fmt.Errorf("manage_worktree prune: deleting adopted sidecar %q: %w", sc.Name, err)
				}
				skipped = append(skipped, WorktreePruneEntry{Name: sc.Name, Reason: "collect race: " + err.Error()})
				continue
			}
			removed = append(removed, WorktreePruneEntry{Name: sc.Name, SidecarRemoved: true, Reason: "adopted"})
			continue
		}

		// Not adopted (tip == base_sha, tip == tip_sha_at_removal, or no
		// removal record): eligible for the merge-gated branch deletion.
		disposable, reasonTag, dErr := policy.disposableBranch(run, tip, sc.BaseSHA, sc.MergeTarget)
		if dErr != nil {
			skipped = append(skipped, WorktreePruneEntry{Name: sc.Name, Reason: reasonTag})
			continue
		}
		if !disposable {
			skipped = append(skipped, WorktreePruneEntry{Name: sc.Name, Reason: reasonTag})
			continue
		}

		// The branch is disposable — an orphan branch+sidecar left when a crash
		// interrupted a prior collection between the worktree remove and branch
		// -D. The branch delete is the destructive step here (no worktree remains
		// to remove), so it can legitimately be refused (checked out elsewhere);
		// the own-store Disposed mark is therefore appended only once it succeeds.
		if policy.prepareDispose != nil {
			if err := policy.prepareDispose(sc.Name); err != nil {
				skipped = append(skipped, WorktreePruneEntry{Name: sc.Name, Reason: "prepare disposal: " + err.Error()})
				continue
			}
		}
		if _, err := run("branch", "-D", branch); err != nil {
			reason := "checked out"
			if loc, ok := checkoutLocationOf(run, branch); ok {
				reason = "checked out at " + loc
			}
			skipped = append(skipped, WorktreePruneEntry{Name: sc.Name, Reason: reason})
			continue
		}
		if err := s.deleteWorktreeSidecar(metaDir, sc.Name); err != nil && !os.IsNotExist(err) {
			if policy.abortOnError {
				return nil, nil, fmt.Errorf("manage_worktree prune: deleting sidecar %q: %w", sc.Name, err)
			}
			skipped = append(skipped, WorktreePruneEntry{Name: sc.Name, Reason: "collect race: " + err.Error()})
			continue
		}
		removed = append(removed, WorktreePruneEntry{Name: sc.Name, BranchRemoved: true, SidecarRemoved: true, Reason: reasonTag})
	}

	return removed, skipped, nil
}

// pruneGitRegistry runs repo-wide `git worktree prune` registry hygiene
// (spec §5 prune sweep 3) only when every `prunable`-annotated porcelain
// entry is under the managed directory; a non-managed prunable entry (a
// user's own sibling worktree, momentarily or permanently absent) makes this
// deliberately not this tool's call to deregister, and the whole sweep is
// skipped with a reason naming the entry.
func pruneGitRegistry(run worktree.GitRunner, projectDir string) (ran bool, skipReason string, err error) {
	out, err := run("worktree", "list", "--porcelain")
	if err != nil {
		// Named for this sweep: prune reads the listing twice (sweep 1 and
		// here), and an identical message left the two failure paths
		// indistinguishable in both errors and tests.
		return false, "", fmt.Errorf("manage_worktree prune: sweep 3 listing worktrees: %w", err)
	}
	for _, e := range worktree.ParsePorcelain(out) {
		if !e.Prunable {
			continue
		}
		if _, ok := relPathUnderManagedDir(canonicalOrClean(e.Path), projectDir); !ok {
			return false, fmt.Sprintf("non-managed prunable entry: %s (%s)", e.Path, e.PrunableReason), nil
		}
	}
	if _, err := run("worktree", "prune"); err != nil {
		return false, "", fmt.Errorf("manage_worktree prune: git worktree prune: %w", err)
	}
	return true, "", nil
}

// resolveBaseFromActiveRoot resolves baseRef to an explicit commit SHA against
// activeRoot (spec §2 "Base resolution"). An absent base defaults to HEAD.
// baseRef is trimmed and rejected for whitespace, control characters, or a
// leading '-' before git sees it, so an option-like value can never be
// interpreted as a flag.
func resolveBaseFromActiveRoot(run worktree.GitRunner, activeRoot, baseRef string) (string, error) {
	ref := strings.TrimSpace(baseRef)
	if ref == "" {
		ref = "HEAD"
	}
	if strings.HasPrefix(ref, "-") {
		return "", fmt.Errorf("manage_worktree create: base_ref %q must not start with %q", ref, "-")
	}
	if strings.ContainsFunc(ref, func(r rune) bool { return r <= ' ' }) {
		return "", fmt.Errorf("manage_worktree create: base_ref %q must not contain whitespace or control characters", ref)
	}
	out, err := run("-C", activeRoot, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	sha := strings.TrimSpace(out)
	if err != nil || sha == "" {
		return "", fmt.Errorf("manage_worktree create: base_ref %q cannot be resolved to a commit from %s", ref, activeRoot)
	}
	return sha, nil
}

// branchExists reports whether refs/heads/<name> already exists.
func branchExists(run worktree.GitRunner, name string) bool {
	_, err := run("show-ref", "--verify", "--quiet", "refs/heads/"+name)
	return err == nil
}

// branchGone reports whether refs/heads/<name> is confirmed absent. It uses
// for-each-ref, which exits 0 when the pattern matches nothing, so a non-nil
// error means the probe itself could not answer and callers must treat it as
// doubt, not as absence: unlike branchExists, this distinguishes the two.
func branchGone(run worktree.GitRunner, name string) (bool, error) {
	out, err := run("for-each-ref", "--format=%(refname)", "refs/heads/"+name)
	if err != nil {
		return false, err
	}
	// for-each-ref also matches a literal pattern as a prefix up to a slash, so
	// refs/heads/foo matches refs/heads/foo/bar. Only an exact refname line means
	// the branch itself still exists; a descendant line means foo is gone.
	want := "refs/heads/" + name
	for line := range strings.SplitSeq(out, "\n") {
		if strings.TrimSpace(line) == want {
			return false, nil
		}
	}
	return true, nil
}

// managedWorktreeExists reports whether a managed worktree already lives at
// worktreePath (a directory carrying a .git pointer), so create only suggests
// switch when switch would actually succeed (spec §3 step 4).
func managedWorktreeExists(worktreePath string) bool {
	_, err := os.Stat(filepath.Join(worktreePath, ".git"))
	return err == nil
}

// branchAtRoot returns the branch checked out at root, or "" when root is on a
// detached HEAD (spec §6 merge_target).
func branchAtRoot(run worktree.GitRunner, root string) string {
	out, err := run("-C", root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}
