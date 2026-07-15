package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/worktree"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/identifier"
)

// resumeWorktreeReentry re-enters the persisted active worktree BEFORE
// initSessionState runs (native worktree tools spec §7 "Persistence and
// resume"): the session must be rooted in its worktree before the
// environment snapshot, system prompt, and tool registry are built, so
// init's own envInfoFromEnv snapshot sees the right directory with no
// special refresh needed (spec §7: "Init's normal envInfoFromEnv snapshot
// then sees the right directory; no special refresh needed"). Called from
// RestoreSessionFromMetaWithConfig right after the Session struct literal is
// built, before initSessionState.
//
// It mutates s.env, s.worktreeCurrentPath, s.worktreeCurrentManaged, and
// s.worktreeRestoreEnv directly rather than through s.mu or
// enterWorktree/swapEnvAndRefresh: nothing else can observe s yet at this
// point in construction (mirrors the struct literal's own unlocked `env:
// env` assignment a few lines above the call site), and swapEnvAndRefresh's
// cache rebuild (tool defs, rendered system prompt) depends on state
// initSessionState has not built yet.
func (s *Session) resumeWorktreeReentry(meta schema.SessionMeta) error {
	path := strings.TrimSpace(meta.WorktreePath)
	if path == "" {
		return nil
	}
	local, ok := s.env.(*execenv.LocalExecutionEnvironment)
	if !ok {
		return nil // worktree re-entry is a local-execution-environment-only feature
	}
	restoreRoot := strings.TrimSpace(meta.WorktreeRestoreRoot)
	target := filepath.Clean(path)

	// notice lands the env at the persisted restore root (when one was
	// recorded) and surfaces a model-facing warning explaining why re-entry
	// did not happen (spec §7: worktree-gone and foreign-lock both "start at
	// the restore root... with a notice").
	notice := func(reason string) {
		if restoreRoot != "" {
			s.env = local.WithWorkingDirectory(restoreRoot)
			reason += "; resuming at " + restoreRoot
		}
		s.emit(events.EventWarning, events.WarningData{Message: reason})
	}

	// The path must still exist and still be a real (linked) worktree.
	if !worktreeGitEntryExists(filepath.Join(target, ".git")) {
		notice(fmt.Sprintf("previous working directory %s no longer exists", target))
		return nil
	}

	// Resolve the canonical Project FROM the target (not from the launch env's
	// cwd, which may be unrelated to it). Reroot via WithWorkingDirectory first:
	// ExecCommand confines a workingDir override to the env's own RootDir, and
	// local is rooted at the launch cwd, not at target.
	rootedAtTarget := local.WithWorkingDirectory(target)
	project, err := identifier.ResolveProjectWith(target, execenv.NewProjectResolver(rootedAtTarget))
	if err != nil {
		notice(fmt.Sprintf("previous working directory %s is no longer part of a git repository (%v)", target, err))
		return nil
	}
	if meta.WorktreeManaged {
		worktreeRoot, rootErr := s.worktreeRootForProject(s.currentStateDir(), project)
		if rootErr != nil {
			notice(fmt.Sprintf("previous managed worktree %s could not be resolved (%v)", target, rootErr))
			return nil
		}
		canonicalTarget := canonicalOrClean(target)
		canonicalProjectDir := canonicalOrClean(filepath.Join(worktreeRoot, project.ID))
		if !isUnderManagedDir(canonicalTarget, canonicalProjectDir) {
			notice(fmt.Sprintf("previous managed worktree %s is outside project lane %s", target, canonicalProjectDir))
			return nil
		}
		// Use the canonical target for registry, lock, and re-entry operations;
		// persisted paths may be symlink aliases of the registered worktree.
		target = canonicalTarget
	}
	mainRoot := project.CanonicalPath
	controlEnv := local.WithWorkingDirectory(mainRoot)
	run := s.newWorktreeGitRunner(context.Background(), controlEnv)

	// The path must still be a worktree git's own registry knows about (spec
	// §7: "validated as in switch by path").
	out, err := run("worktree", "list", "--porcelain")
	if err != nil {
		notice(fmt.Sprintf("previous working directory %s could not be verified as a worktree (%v)", target, err))
		return nil
	}
	registered := false
	for _, e := range worktree.ParsePorcelain(out) {
		if canonicalOrClean(e.Path) == target {
			registered = true
			break
		}
	}
	if !registered {
		notice(fmt.Sprintf("previous working directory %s is no longer a registered worktree", target))
		return nil
	}

	// Managed only: apply the idempotent EvResumeReenter lock rule (spec §5
	// table row "resume re-entry"; §7): unlocked -> lock; own marker -> adopt
	// (crash-resume, a literal re-lock is fatal on git); foreign -> do NOT
	// re-enter, land at the restore root with a notice. A path-entered
	// non-managed worktree carries no serf lock at all (spec §4 by-path step
	// 3), so it re-enters unconditionally.
	if meta.WorktreeManaged {
		locked, reason, lsErr := lockStateOf(run, target)
		if lsErr != nil {
			notice(fmt.Sprintf("previous worktree %s lock state could not be verified (%v)", target, lsErr))
			return nil
		}
		st := worktree.Unlocked
		if locked {
			st = worktree.ClassifyReason(reason, s.id, "")
		}
		switch worktree.Decide(worktree.EvResumeReenter, st) {
		case worktree.ActLock:
			marker := worktree.FormatSessionMarker(s.id)
			if _, err := run("worktree", "lock", "--reason", marker, target); err != nil {
				notice(fmt.Sprintf("failed to re-lock previous worktree %s (%v)", target, err))
				return nil
			}
		case worktree.ActAdopt:
			// Crash-resume case: the stale lock already carries this
			// session's own marker; adopt it rather than re-lock (a literal
			// re-lock is fatal on git).
		default: // ActRefuseToRestoreRoot: foreign (or delegate-owned) lock.
			occupant := reason
			if occupant == "" {
				occupant = "an unknown owner"
			}
			notice(fmt.Sprintf("previous worktree %s is now locked by %s", target, occupant))
			return nil
		}
	}

	// Re-enter: root the env in the worktree directly. No swapEnvAndRefresh
	// here — see the doc comment above.
	s.env = local.WithWorkingDirectory(target)
	s.worktreeCurrentPath = target
	s.worktreeCurrentManaged = meta.WorktreeManaged
	if restoreRoot != "" {
		s.worktreeRestoreEnv = local.WithWorkingDirectory(restoreRoot)
	}
	return nil
}

func worktreeGitEntryExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// applyInitInsideWorktreeLock implements the native worktree tools spec §5
// lock-state-machine table row "session init, launch cwd inside a managed
// worktree": when a session's env is already rooted inside a managed
// worktree and occupancy is not already tracked (resumeWorktreeReentry, if
// it ran, would have set it), apply the idempotent EvInitInside lock rule so
// the session is not silently prune-collectible mid-session (spec §5
// occupancy locks, rev-7 review finding: "a session merely launched inside a
// kept lane held no lock and was prune-collectible mid-session"). A foreign
// lock warns loudly and continues co-occupying rather than refusing — a
// session cannot be un-launched (spec §5: "if the lane is foreign-locked the
// session continues but warns loudly that it is co-occupying").
//
// Scoped to root sessions (s.cfg.spawn.parentSessionID == "") only: a
// delegate or an ordinary subagent spawned with a cwd inside a managed
// worktree does not take an independent lock here. Delegate lane locks are
// owned by the parent's own §9 create/revive/dispose lifecycle, not by the
// child session's init (spec §5: "The serf:dlg: lock on a delegate lane is
// owned by the parent's disposal lifecycle, not the child"); an ordinary
// (non-isolated) subagent sharing its parent's worktree must not compete for
// the parent's own lock or emit a spurious co-occupying warning on every
// spawn.
//
// isGitRepo is the caller's already-computed snapshotGit result (session_init.go),
// passed in to skip the ResolveMainRepoRoot git fork entirely for the common
// non-repo case.
//
// Invariant carried from Task 18 into Task 21: this scoping was sound when
// every spawn path hardcoded workingDir="" (a child always inherited
// whatever env the parent happened to be in, never a distinct one of its
// own); §9 isolation delegates now root the CHILD env at its OWN managed
// worktree (createDelegateWorktree, job_delegate.go createDelegate). The
// parentSessionID != "" check above still holds unconditionally for such a
// child — createDelegateWorktree already takes the serf:dlg: lock atomically
// at `git worktree add` time on the PARENT side, before the child spawns, so
// there is no unlocked window for this function to (mis)detect on the
// child's own init.
func (s *Session) applyInitInsideWorktreeLock(isGitRepo bool) {
	if !isGitRepo || s.cfg.spawn.parentSessionID != "" {
		return
	}
	s.mu.Lock()
	already := s.worktreeCurrentPath != ""
	s.mu.Unlock()
	if already {
		return
	}

	local, ok := s.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		return
	}
	activeRoot := local.WorkingDirectory()
	if resolved, err := filepath.EvalSymlinks(activeRoot); err == nil {
		activeRoot = resolved
	}
	project, err := identifier.ResolveProjectWith(activeRoot, execenv.NewProjectResolver(local))
	if err != nil || !projectIsGitCheckout(project) {
		return
	}
	worktreeRoot, err := s.worktreeRootForProject(s.currentStateDir(), project)
	if err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("could not resolve managed worktree state: %v", err)})
		return
	}
	projectDir := filepath.Join(worktreeRoot, project.ID)

	if !isUnderManagedDir(activeRoot, projectDir) {
		return
	}

	controlEnv := local.WithWorkingDirectory(project.CanonicalPath)
	run := s.newWorktreeGitRunner(context.Background(), controlEnv)
	locked, reason, err := lockStateOf(run, activeRoot)
	if err != nil {
		s.emit(events.EventWarning, events.WarningData{
			Message: fmt.Sprintf("could not inspect the lock on worktree %s at session start: %v", activeRoot, err),
		})
		return
	}
	st := worktree.Unlocked
	if locked {
		st = worktree.ClassifyReason(reason, s.id, "")
	}
	switch worktree.Decide(worktree.EvInitInside, st) {
	case worktree.ActLock:
		marker := worktree.FormatSessionMarker(s.id)
		if _, err := run("worktree", "lock", "--reason", marker, activeRoot); err != nil {
			s.emit(events.EventWarning, events.WarningData{
				Message: fmt.Sprintf("failed to lock worktree %s at session start: %v", activeRoot, err),
			})
			return
		}
	case worktree.ActAdopt:
		// Crash-resume case: already carries our own marker.
	case worktree.ActWarnCoOccupy:
		occupant := reason
		if occupant == "" {
			occupant = "an unknown owner"
		}
		s.emit(events.EventWarning, events.WarningData{
			Message: fmt.Sprintf("session started inside worktree %s, which is locked by %s; continuing and co-occupying it", activeRoot, occupant),
		})
	}

	s.mu.Lock()
	s.worktreeCurrentPath = activeRoot
	s.worktreeCurrentManaged = true
	s.mu.Unlock()
}
