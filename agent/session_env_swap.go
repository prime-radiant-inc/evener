package agent

import (
	"context"
	"errors"

	"primeradiant.com/evener/agent/execenv"
)

// errSwapWhileClosing is what an environment swap returns when the session
// began closing under it; the worktree op surfaces it to the caller.
var errSwapWhileClosing = errors.New("manage_worktree: the session is closing; environment swap refused")

// swapEnvAndRefresh atomically installs next as the session's execution
// environment and refreshes everything derived from it (envInfo, the git
// snapshot, and the tool/prompt caches). See spec §7 "envInfo +
// system-prompt refresh" for the full rationale; the two steps below are
// normative, not incidental structure.
//
// Callers must always construct next via WithWorkingDirectory on the current
// env (never execenv.NewLocalExecutionEnvironment), so PID tracking and the
// afero filesystem backing survive the swap — see local.go's
// WithWorkingDirectory doc comment. That invariant is the caller's
// responsibility (enterWorktree/exitWorktree/switch/remove); this helper
// installs whatever it is given, and moves the session's scratch onto it.
//
// The scratch follows the session across every swap: the environment the
// session currently holds is the one its close releases, and a clone owns
// nothing of its original, so without the move an enter parks the lease on
// an environment a later exit discards, and it is held for the rest of the
// daemon's uptime. (resumeWorktreeReentry makes the same move for the swap it
// performs before this helper can run.)
//
// The swap is fenced against close. Close cleans the environment the session
// holds when it runs, and between the move below and the install the session
// still holds the OLD environment, which owns nothing any more: a close in that
// window would leave next's lease with no teardown owner. So the swap refuses
// to start once the session is closing, and re-checks under s.mu before
// installing; a close that began meanwhile rolls the move back by retaining
// what next adopted (lease released, directory kept, the handoff a close makes)
// and returns errSwapWhileClosing for the op to surface. Both `closing` and the
// install are written under s.mu, so one of the two always sees the other.
//
// A swap that passes that first check is ADMITTED: it registers on envWorkWG
// under the same s.mu hold that read `closing` (the beginDispose idiom), so the
// Add happens-before Close()'s join, and Close waits for it before cleaning the
// environment. Without that wait a close walks past the refusal it just caused
// and reaps the process table while step 1's git is still forking on it, on
// contexts the close never reaches. The refusal alone is not enough: it lands at
// step 2, AFTER those commands have run.
//
// record, when non-nil, runs under the same s.mu hold that installs next, so
// the session's worktree occupancy — above all the environment an enter parks
// (worktreeRestoreEnv), whose scratch close retains — is published atomically
// with the swap. A close then observes either the pre-swap state entirely or
// the post-swap state with its parked environment set, never an installed
// next with nothing parked: in that gap a child sharing the old environment
// could mint a scratch there that nothing would ever release. record runs
// with s.mu held: it may only assign session fields, and must neither block
// nor take s.mu itself.
func (s *Session) swapEnvAndRefresh(next *execenv.LocalExecutionEnvironment, record func()) error {
	// Step 0 — move the session's scratch onto next BEFORE any command runs on
	// it: the git snapshot and the pre-warm below spawn through next, and a
	// command is what mints a scratch on an environment that owns none. Adopting
	// after them would find next already owning a fresh one, keep it, and retain
	// the session's original — a silently changed $EVENER_SCRATCH_DIR and an
	// extra retained directory per enter.
	s.mu.Lock()
	closing := s.closing
	current, _ := s.env.(*execenv.LocalExecutionEnvironment)
	if !closing {
		s.envWorkWG.Add(1)
	}
	s.mu.Unlock()
	if closing {
		return errSwapWhileClosing
	}
	defer s.envWorkWG.Done()
	if current != nil {
		next.AdoptSessionScratch(current)
	}
	// Step 0b — the context step 1's git runs under. Every command below forks
	// on the process table the session's close reaps, so none of them may
	// outlive that close: the refresh context is the session's own, which the
	// close cancels before it waits here and cleans the environment.
	refreshCtx, cancelRefresh := context.WithCancel(s.sessionContext())
	defer cancelRefresh()
	if hook := s.cfg.testOnly.swapEnvAfterAdopt; hook != nil {
		hook(refreshCtx)
	}

	// Step 1 — OUTSIDE s.mu: compute the new EnvInfo and its git snapshot, and
	// pre-warm next's git-root cache. The git snapshot forks several `git`
	// subprocesses and `git status` can take seconds on a big repo; s.mu must
	// never be held across a subprocess (it would stall every event emit,
	// Meta() autosave, and hub poll while forking). The pre-warm is load
	// bearing, not an optimization: step 2's refreshSystemPromptCache calls
	// renderSystemPrompt, which calls execenv.GitRootOrEmpty(next, ...) again —
	// next's memoization cache starts empty (WithWorkingDirectory gives it a
	// fresh gitRoots cache), so without this call step 2 would fork
	// `git rev-parse --show-toplevel` while holding s.mu.
	newWD := next.WorkingDirectory()
	ei := s.snapshotEnvironmentInfo(next)
	if !s.cfg.testOnly.skipGitSnapshot {
		if inRepo, branch, mod, untracked, commits := snapshotGit(refreshCtx, next, newWD); inRepo {
			ei.IsGitRepo = true
			ei.GitBranch = branch
			ei.GitModifiedFiles = mod
			ei.GitUntrackedFiles = untracked
			ei.GitRecentCommitTitles = commits
			ei.GitOriginURL = gitOriginURL(refreshCtx, next, newWD)
		}
	}
	if !s.cfg.NoProjectPrompts {
		// Pre-warm next's git-root cache; see the lock-order comment above.
		_ = execenv.GitRootOrEmptyContext(refreshCtx, next, newWD)
	}

	// Step 2 — under s.mu: atomically install env+envInfo (so the two are
	// never observed in a torn intermediate state) and rebuild the caches that
	// derive from them. next's git-root cache is already warm, so this render
	// hits the cache instead of forking.
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		next.RetainSessionScratch()
		return errSwapWhileClosing
	}
	ei.KnowledgeCutoff = s.envInfo.KnowledgeCutoff // profile-derived, not env-derived; swap must not clobber it
	s.env = next
	s.envInfo = ei
	if record != nil {
		record()
	}
	s.rebuildToolDefsCache()
	promptWarning := s.refreshSystemPromptCache(next)
	s.mu.Unlock()
	s.reportPromptRenderFailure(promptWarning)
	return nil
}
