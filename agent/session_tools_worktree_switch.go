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
	"primeradiant.com/evener/agent/internal/worktree"
)

// switchToCurrentNoOp implements the EvEnterCurrent row of the lock state
// machine (spec §4 switch step 1; §5 table "switch to the worktree already
// occupied"; lockstate.go EvEnterCurrent) for a switch whose target is the
// worktree the session already occupies. Only the own-session occupancy
// state may no-op with the lock kept; every other observed state — including
// unlocked or foreign-locked, reachable only if something outside this
// session's own choreography changed the lock between occupying the target
// and this switch call — is refused rather than silently treated as a no-op.
// Decide is total and fails safe here exactly as it does everywhere else in
// the lock state machine.
func (s *Session) switchToCurrentNoOp(run worktree.GitRunner, target string, locked bool, reason string) (WorktreeSwitchResult, error) {
	st := worktree.Unlocked
	if locked {
		st = worktree.ClassifyReason(reason, s.id, "")
	}
	if worktree.Decide(worktree.EvEnterCurrent, st) != worktree.ActNone {
		detail := reason
		if detail == "" {
			detail = "unlocked"
		}
		return WorktreeSwitchResult{}, fmt.Errorf(
			"manage_worktree switch: %s is the worktree already occupied, but it is not locked with this session's own marker (%s); refusing",
			target, detail)
	}
	return WorktreeSwitchResult{Path: target, Branch: branchAtRoot(run, target), NoOp: true}, nil
}

// worktreeEnterManaged performs the by-name switch choreography (spec §4
// switch by-name steps 1-6) against an already-resolved managed worktree
// path. switchByPath's managed-directory reroute (step 2) shares it verbatim:
// once a by-path argument resolves inside the managed directory, entering it
// is indistinguishable from a by-name switch.
func (s *Session) worktreeEnterManaged(st worktreeState, run worktree.GitRunner, path string) (WorktreeSwitchResult, error) {
	target := filepath.Clean(path)
	errContext := "target"
	if st.currentWorktree != "" && filepath.Clean(st.currentWorktree) == target {
		errContext = "current worktree"
	}
	porcelain, err := readWorktreePorcelain(run)
	if err != nil {
		return WorktreeSwitchResult{}, fmt.Errorf("manage_worktree switch: inspecting the %s lock: %w", errContext, err)
	}
	return s.worktreeEnterManagedWithPorcelain(st, run, target, porcelain)
}

func (s *Session) worktreeEnterManagedWithPorcelain(st worktreeState, run worktree.GitRunner, path string, porcelain []worktree.PorcelainEntry) (WorktreeSwitchResult, error) {
	target := filepath.Clean(path)
	locked, reason := lockStateFromPorcelain(porcelain, target)

	// Step 1: switch-to-current routes through Decide(EvEnterCurrent, ...) —
	// only an ActNone (own-session marker) may no-op with the lock kept; any
	// other observed state is refused (spec §4 switch step 1; lockstate.go
	// EvEnterCurrent). Without the ActNone no-op, the ordinary
	// lock-target/unlock-old choreography below would unlock the session's
	// own active worktree out from under it.
	if st.currentWorktree != "" && filepath.Clean(st.currentWorktree) == target {
		return s.switchToCurrentNoOp(run, target, locked, reason)
	}

	// Step 2: the target must exist and be a real worktree (.git pointer file).
	if _, err := os.Stat(filepath.Join(target, ".git")); err != nil {
		return WorktreeSwitchResult{}, fmt.Errorf("manage_worktree switch: no worktree at %s", target)
	}
	targetState := worktree.Unlocked
	if locked {
		targetState = worktree.ClassifyReason(reason, s.id, "")
	}

	// Step 3: lock the target FIRST, then unlock the current worktree. Order
	// is load-bearing (spec §4 switch step 3): a lost race fails right here,
	// with nothing yet changed, instead of leaving the session unlocked out of
	// its old worktree.
	entered := false
	switch worktree.Decide(worktree.EvEnter, targetState) {
	case worktree.ActLock:
		marker := worktree.FormatSessionMarker(s.id)
		if _, err := run("worktree", "lock", "--reason", marker, target); err != nil {
			return WorktreeSwitchResult{}, fmt.Errorf("manage_worktree switch: locking the target: %w", err)
		}
		// The lock this switch took goes back on every failure from here on,
		// through a control runner of its own: the close that refuses the swap
		// has already cancelled the request context run is bound to. Like the
		// create rollback, it runs after the swap has left the close fence and
		// forks git on the process table a close reaps, so it takes an admission
		// of its own, spanning the whole switch for the same reason the create's
		// does and renamed the same way when the unlock starts; a lock left
		// behind here strands the target under this session's marker, which
		// prune skips.
		switchAdmission, switchFenced := s.beginEnvWork("switch to " + target)
		defer func() {
			if switchFenced {
				defer s.endEnvWork(switchAdmission)
			}
			if entered {
				return
			}
			if switchFenced {
				s.relabelEnvWork(switchAdmission, "switch target unlock for "+target)
			}
			unlock, done, err := s.worktreeCleanupRun(st.mainRepoRoot)
			if err != nil {
				s.emit(events.WarningData{Message: fmt.Sprintf("unlocking switch target %s after a failed switch failed: %v", target, err)})
				return
			}
			defer done()
			if _, unlockErr := unlock("worktree", "unlock", target); unlockErr != nil {
				s.emit(events.WarningData{Message: fmt.Sprintf("unlocking switch target %s after a failed switch failed: %v", target, unlockErr)})
			}
		}()
	case worktree.ActAdopt:
		// Already carries our own marker (crash-resume case); nothing to do.
	default:
		return WorktreeSwitchResult{}, fmt.Errorf("manage_worktree switch: %s is locked (%s)", target, reason)
	}
	if err := s.leaveCurrentWorktreeFromPorcelain(run, porcelain); err != nil {
		return WorktreeSwitchResult{}, err
	}

	// Steps 4-5: swap the env directly to the target (no intermediate
	// restore step) and refresh envInfo + the prompt cache. A sandbox re-root the
	// host cannot satisfy fails closed rather than switching in unconfined; a
	// swap the session's close refused fails the same way, and the target's
	// lock goes back above. The lock the leave released is deliberately NOT
	// restored: the refusal means the session is closing, and its close
	// unlocks its own lane on the way out (unlockOwnManagedWorktreeAtClose) —
	// a re-lock landing after that would strand the lane under a dead session's
	// marker, which prune skips and remove refuses. Resume handles either
	// state; worktreeCreate leaves its own leave alone for the same reason.
	if err := s.enterWorktree(target, true); err != nil {
		return WorktreeSwitchResult{}, err
	}
	entered = true

	// Step 6: report the path and branch.
	return WorktreeSwitchResult{Path: target, Branch: branchAtRoot(run, target)}, nil
}

// worktreeSwitchByName performs the switch-by-name operation (spec §4 switch,
// "By name"): resolve the managed path for name and run the shared entry
// choreography.
func (s *Session) worktreeSwitchByName(ctx context.Context, name string) (WorktreeSwitchResult, error) {
	if err := worktree.ValidateName(name); err != nil {
		return WorktreeSwitchResult{}, fmt.Errorf("manage_worktree switch: %w", err)
	}
	st := s.worktreeStateSnapshot()
	if err := st.resolutionError("switch"); err != nil {
		return WorktreeSwitchResult{}, err
	}
	if st.env == nil {
		return WorktreeSwitchResult{}, errors.New("manage_worktree requires a local execution environment")
	}
	if st.mainRepoRoot == "" {
		return WorktreeSwitchResult{}, errors.New("manage_worktree switch: not in a git repository")
	}
	run, done, err := s.worktreeControlRun(ctx, st.mainRepoRoot)
	if err != nil {
		return WorktreeSwitchResult{}, err
	}
	defer done()
	path := filepath.Join(st.worktreeRoot, st.project.ID, filepath.FromSlash(name))
	return s.worktreeEnterManaged(st, run, path)
}

// worktreeSwitchByPath performs the switch-by-path operation (spec §4 switch,
// "By path"): canonicalize rawPath, require it to be registered to this
// repository, reroute through the managed choreography if it resolves inside
// the managed directory, and otherwise swap the env with no lock mutation at
// all (evener does not manage locks on worktrees it did not create).
func (s *Session) worktreeSwitchByPath(ctx context.Context, rawPath string) (WorktreeSwitchResult, error) {
	st := s.worktreeStateSnapshot()
	if err := st.resolutionError("switch"); err != nil {
		return WorktreeSwitchResult{}, err
	}
	if st.env == nil {
		return WorktreeSwitchResult{}, errors.New("manage_worktree requires a local execution environment")
	}
	if st.mainRepoRoot == "" {
		return WorktreeSwitchResult{}, errors.New("manage_worktree switch: not in a git repository")
	}

	// Step 1: canonicalize the argument and require a match in `git worktree
	// list --porcelain` — git's own registry is the validator.
	canonicalArg, err := filepath.EvalSymlinks(rawPath)
	if err != nil {
		return WorktreeSwitchResult{}, fmt.Errorf("manage_worktree switch: path %q does not exist", rawPath)
	}
	canonicalArg = filepath.Clean(canonicalArg)

	run, done, err := s.worktreeControlRun(ctx, st.mainRepoRoot)
	if err != nil {
		return WorktreeSwitchResult{}, err
	}
	defer done()
	porcelain, err := readWorktreePorcelain(run)
	if err != nil {
		return WorktreeSwitchResult{}, fmt.Errorf("manage_worktree switch: listing worktrees: %w", err)
	}
	var matchedPath, matchedBranch string
	found := false
	for _, e := range porcelain {
		cp, evErr := filepath.EvalSymlinks(e.Path)
		if evErr != nil {
			continue // this entry's directory is momentarily absent; not a match we can canonicalize
		}
		if filepath.Clean(cp) == canonicalArg {
			matchedPath, matchedBranch, found = e.Path, strings.TrimPrefix(e.Branch, "refs/heads/"), true
			break
		}
	}
	if !found {
		return WorktreeSwitchResult{}, fmt.Errorf("manage_worktree switch: %q is not a worktree registered to this repository", rawPath)
	}

	// Step 2: reroute through the by-name choreography when the target
	// resolves inside the managed directory (full lock guard + choreography).
	projectDir := filepath.Join(st.worktreeRoot, st.project.ID)
	if isUnderManagedDir(canonicalArg, projectDir) {
		return s.worktreeEnterManagedWithPorcelain(st, run, matchedPath, porcelain)
	}

	// Step 3: a genuinely non-managed registered worktree — same env swap, NO
	// lock choreography on the target (spec §4 by-path step 3: "no lock
	// choreography — evener does not mutate lock state on worktrees it does
	// not manage"). There is no lock decision on this site at all, so a
	// redundant switch back to the worktree already occupied is a plain
	// path-compare no-op rather than a run through Decide(EvEnterCurrent,
	// ...) — that gate exists to protect the managed case's own-session
	// lock, which has no counterpart here. If the session is leaving a
	// managed worktree, it is still unlocked on the way out below.
	if st.currentWorktree != "" && filepath.Clean(st.currentWorktree) == filepath.Clean(matchedPath) {
		return WorktreeSwitchResult{Path: matchedPath, Branch: matchedBranch, NoOp: true}, nil
	}
	if err := s.leaveCurrentWorktreeFromPorcelain(run, porcelain); err != nil {
		return WorktreeSwitchResult{}, err
	}
	// A refused swap has nothing of its own to give back here (no target
	// lock), and the lock the leave released stays released — see the by-name
	// choreography for why a re-lock would strand it.
	if err := s.enterWorktree(matchedPath, false); err != nil {
		return WorktreeSwitchResult{}, err
	}
	return WorktreeSwitchResult{Path: matchedPath, Branch: matchedBranch}, nil
}

// worktreeAdopt performs the adopt operation: convert a worktree that sits
// under the managed root with no metadata sidecar into a managed one, by
// writing the sidecar it lacks. This is the ONLY route out of the unmanaged
// section (spec §6 "Metadata sidecar": a sidecar-less worktree under the
// managed dir is provenance-unknown, skipped by prune and treated by remove
// as another session's). Nothing adopts implicitly — list, remove and prune
// go on ignoring an un-adopted stray no matter how often they see it.
//
// The sidecar records THIS session as creator, so ownership scoping (spec
// §5 "Ownership scoping") applies to the adopted worktree from here on
// exactly as it would to one this session had created.
//
// Adopt takes no lock: locks follow occupancy, not provenance (spec §4 switch
// step 3), and adopting does not enter the worktree. It does honor the record's
// lock rule set for reading the target's state — a worktree locked by anyone
// but this session is an occupied worktree, and claiming provenance over
// someone else's occupancy is refused rather than raced.
func (s *Session) worktreeAdopt(ctx context.Context, rawPath string) (WorktreeAdoptResult, error) {
	st := s.worktreeStateSnapshot()
	if err := st.resolutionError("adopt"); err != nil {
		return WorktreeAdoptResult{}, err
	}
	if st.env == nil {
		return WorktreeAdoptResult{}, errors.New("manage_worktree requires a local execution environment")
	}
	if st.mainRepoRoot == "" {
		return WorktreeAdoptResult{}, errors.New("manage_worktree adopt: not in a git repository")
	}

	// Canonicalize the argument and require a match in git's own registry, the
	// same validator switch-by-path uses (spec §4 switch by-path step 1).
	canonicalArg, err := filepath.EvalSymlinks(rawPath)
	if err != nil {
		return WorktreeAdoptResult{}, fmt.Errorf("manage_worktree adopt: path %q does not exist", rawPath)
	}
	canonicalArg = filepath.Clean(canonicalArg)

	run, done, err := s.worktreeControlRun(ctx, st.mainRepoRoot)
	if err != nil {
		return WorktreeAdoptResult{}, err
	}
	defer done()
	porcelain, err := readWorktreePorcelain(run)
	if err != nil {
		return WorktreeAdoptResult{}, fmt.Errorf("manage_worktree adopt: listing worktrees: %w", err)
	}
	var entry worktree.PorcelainEntry
	found := false
	for _, e := range porcelain {
		cp, evErr := filepath.EvalSymlinks(e.Path)
		if evErr != nil {
			continue // momentarily absent; not a match we can canonicalize
		}
		if filepath.Clean(cp) == canonicalArg {
			entry, found = e, true
			break
		}
	}
	if !found {
		return WorktreeAdoptResult{}, fmt.Errorf("manage_worktree adopt: %q is not a worktree registered to this repository", rawPath)
	}

	// Only worktrees under the managed root can be adopted. A registered
	// worktree elsewhere stays switchable and exitable but never evener's to
	// remove or prune (spec §4 switch by-path step 4, "remove stays
	// managed-only"), and adoption is precisely what would change that.
	projectDir := filepath.Join(st.worktreeRoot, st.project.ID)
	rel, underManaged := relPathUnderManagedDir(canonicalArg, projectDir)
	if !underManaged {
		return WorktreeAdoptResult{}, fmt.Errorf(
			"manage_worktree adopt: %s is outside the managed worktree root %s; only worktrees under it can be adopted (you can still switch to it by path)",
			entry.Path, projectDir)
	}
	name := filepath.ToSlash(rel)
	if verr := worktree.ValidateName(name); verr != nil {
		return WorktreeAdoptResult{}, fmt.Errorf("manage_worktree adopt: %s cannot be adopted: %w", entry.Path, verr)
	}

	// A managed worktree's directory name IS its branch name: remove step 9
	// and prune's collector both act on refs/heads/<name>. Adopting a
	// directory whose branch differs would aim those at the wrong branch, so
	// the mismatch is refused instead of recorded.
	branch := strings.TrimPrefix(entry.Branch, "refs/heads/")
	if branch == "" {
		return WorktreeAdoptResult{}, fmt.Errorf("manage_worktree adopt: %s has a detached HEAD; check out branch %q in it first", entry.Path, name)
	}
	if branch != name {
		return WorktreeAdoptResult{}, fmt.Errorf(
			"manage_worktree adopt: %s has branch %q checked out but would be managed under the name %q; a managed worktree's directory name must equal its branch name",
			entry.Path, branch, name)
	}

	// Lock state: unlocked or our own crash residue is ours to claim; anything
	// else is another owner's occupancy (spec §5 "A lock with no reason or a
	// reason that doesn't parse as a evener marker is foreign"; a delegate marker
	// is a live lane and foreign to a session-level operation).
	if entry.Locked {
		if lockSt := worktree.ClassifyReason(entry.LockReason, s.id, ""); lockSt != worktree.OwnSession {
			detail := entry.LockReason
			if detail == "" {
				detail = "no reason given"
			}
			return WorktreeAdoptResult{}, fmt.Errorf(
				"manage_worktree adopt: %s is locked by another owner (%s); refusing to claim a worktree someone else occupies",
				entry.Path, detail)
		}
	}

	metaDir := metaDirForProject(projectDir)
	if _, scErr := worktree.ReadSidecar(metaDir, name); scErr == nil {
		return WorktreeAdoptResult{}, fmt.Errorf("manage_worktree adopt: %s is already managed as %q; adopt only takes over unmanaged worktrees", entry.Path, name)
	} else if !os.IsNotExist(scErr) {
		return WorktreeAdoptResult{}, fmt.Errorf("manage_worktree adopt: reading metadata: %w", scErr)
	}

	// merge_target is read from the MAIN repo root, not the active root that
	// create uses (spec §6). Adopt does not enter its target and the target may
	// even BE the active root, in which case an active-root merge target would
	// name the adopted lane's own branch — making it trivially "merged" and so
	// collectible by the next prune. The main checkout's branch is the stable,
	// non-self-referential answer.
	mergeTarget := branchAtRoot(run, st.mainRepoRoot)

	// base_sha is where this lane diverged from that target, never its current
	// HEAD: recording HEAD would make an adopted lane full of committed work
	// satisfy prune's `unchanged` predicate (tip == base) and be collected,
	// branch and all, on the next sweep. When no merge base can be computed
	// (detached main checkout, unrelated histories) both fields stay empty,
	// which lands every downstream predicate in its conservative arm — merge
	// target unknown, nothing collectible.
	baseSHA := ""
	if mergeTarget != "" {
		if mb, mbErr := run("merge-base", "refs/heads/"+name, "refs/heads/"+mergeTarget); mbErr == nil {
			baseSHA = strings.TrimSpace(mb)
		}
	}

	if mkErr := os.MkdirAll(metaDir, 0o755); mkErr != nil {
		return WorktreeAdoptResult{}, fmt.Errorf("manage_worktree adopt: create metadata dir: %w", mkErr)
	}
	sc := worktree.Sidecar{
		Name:           name,
		Branch:         branch,
		BaseSHA:        baseSHA,
		MergeTarget:    mergeTarget,
		OriginalRoot:   st.mainRepoRoot,
		CreatorSession: s.id,
		CreatedAt:      s.sclock().Now().UTC().Format(time.RFC3339),
	}
	// O_EXCL for the same reason create needs it: two concurrent adopts both
	// pass the read above, and a plain write would let the loser overwrite the
	// winner's provenance.
	if werr := s.writeWorktreeSidecar(metaDir, name, sc); werr != nil {
		if os.IsExist(werr) {
			return WorktreeAdoptResult{}, fmt.Errorf("manage_worktree adopt: %s is already being adopted", entry.Path)
		}
		return WorktreeAdoptResult{}, fmt.Errorf("manage_worktree adopt: write sidecar: %w", werr)
	}

	return WorktreeAdoptResult{Path: entry.Path, Name: name, Branch: branch, BaseSHA: baseSHA}, nil
}

// relockRestoreTargetWithLockState applies the idempotent EvRestoreLand lock
// rule to a restore target that resolves inside the managed directory (spec §4
// exit step 4; §5 "Restores follow the same rule"): lock if unlocked, adopt our
// own marker as a no-op, or warn and co-occupy on a foreign lock. A restore can
// never be refused, since the session has to land somewhere. Returns a non-empty
// warning string only for the co-occupy case.
func (s *Session) relockRestoreTargetWithLockState(run worktree.GitRunner, path string, locked bool, reason string) (string, error) {
	st := worktree.Unlocked
	if locked {
		st = worktree.ClassifyReason(reason, s.id, "")
	}
	action := worktree.Decide(worktree.EvRestoreLand, st)
	if action == worktree.ActLock {
		marker := worktree.FormatSessionMarker(s.id)
		if _, err := run("worktree", "lock", "--reason", marker, path); err != nil {
			return "", fmt.Errorf("manage_worktree exit: locking the restore target: %w", err)
		}
		return "", nil
	}
	if action == worktree.ActAdopt {
		return "", nil
	}
	return fmt.Sprintf("restore target %s is locked by another owner (%s); continuing and co-occupying it", path, reason), nil
}

// worktreeExit performs the exit operation (spec §4 "exit", all six steps).
func (s *Session) worktreeExit(ctx context.Context) (WorktreeExitResult, error) {
	st := s.worktreeStateSnapshot()
	if err := st.resolutionError("exit"); err != nil {
		return WorktreeExitResult{}, err
	}

	// Step 1: not in a worktree entered via this tool → clear, non-destructive
	// error, no side effects.
	if st.restoreEnv == nil {
		return WorktreeExitResult{}, errors.New("manage_worktree exit: not in a worktree")
	}
	leftPath := st.currentWorktree

	var run worktree.GitRunner
	var porcelain []worktree.PorcelainEntry
	if st.mainRepoRoot != "" {
		r, done, err := s.worktreeControlRun(ctx, st.mainRepoRoot)
		if err != nil {
			return WorktreeExitResult{}, err
		}
		defer done()
		run = r
		porcelain, err = readWorktreePorcelain(run)
		if err != nil {
			return WorktreeExitResult{}, fmt.Errorf("manage_worktree: inspecting the current worktree lock: %w", err)
		}
		// Step 2: unlock the worktree being left, if managed and locked with
		// this session's own marker (same EvLeave rule as switch-away /
		// create-away).
		if err := s.leaveCurrentWorktreeFromPorcelain(run, porcelain); err != nil {
			return WorktreeExitResult{}, err
		}
	}

	// Step 3: restore the saved env, clear it, recompute envInfo, refresh the
	// prompt cache (spec §7 exitWorktree()).
	restoredRoot, _, err := s.exitWorktree()
	if err != nil {
		return WorktreeExitResult{}, err
	}
	result := WorktreeExitResult{RestoredRoot: restoredRoot, LeftPath: leftPath}

	// Step 4: if the restore root is itself a managed worktree (the session
	// was launched inside one before entering others), apply the idempotent
	// lock rule to it.
	if run != nil {
		projectDir := filepath.Join(st.worktreeRoot, st.project.ID)
		warning, err := s.applyRestoreLandRelockFromPorcelain(run, restoredRoot, projectDir, porcelain)
		if err != nil {
			return WorktreeExitResult{}, err
		}
		result.Warning = warning
	}

	return result, nil
}

// applyRestoreLandRelockFromPorcelain applies the idempotent EvRestoreLand lock
// rule when restoredRoot resolves inside the managed directory rooted at
// projectDir (spec §5 "Restores follow the same rule"): exit's restore root (§4
// exit step 4) and remove-current's restore (§5 remove step 7) share this exact
// choreography. It is a no-op (empty warning, nil error) when restoredRoot is
// not under the managed directory. When the rule fires, it also records the
// session's occupancy (worktreeCurrentPath) so a later create/switch/close
// leaves it correctly (spec §3 step 7; §5 clean-close unlock).
func (s *Session) applyRestoreLandRelockFromPorcelain(run worktree.GitRunner, restoredRoot, projectDir string, porcelain []worktree.PorcelainEntry) (string, error) {
	canonicalRestored := filepath.Clean(restoredRoot)
	if resolved, evErr := filepath.EvalSymlinks(restoredRoot); evErr == nil {
		canonicalRestored = filepath.Clean(resolved)
	}
	if !isUnderManagedDir(canonicalRestored, projectDir) {
		return "", nil
	}
	locked, reason := lockStateFromPorcelain(porcelain, restoredRoot)
	warning, err := s.relockRestoreTargetWithLockState(run, restoredRoot, locked, reason)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.worktreeCurrentPath = restoredRoot
	s.worktreeCurrentManaged = true
	s.mu.Unlock()
	return warning, nil
}
