package agent

import "primeradiant.com/serf/agent/execenv"

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
// responsibility (enterWorktree/exitWorktree/switch/remove); this helper only
// installs whatever it is given.
func (s *Session) swapEnvAndRefresh(next *execenv.LocalExecutionEnvironment) {
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
	ei := envInfoFromEnv(next, s.sclock())
	if inRepo, branch, mod, untracked, commits := snapshotGit(next, newWD); inRepo {
		ei.IsGitRepo = true
		ei.GitBranch = branch
		ei.GitModifiedFiles = mod
		ei.GitUntrackedFiles = untracked
		ei.GitRecentCommitTitles = commits
		ei.GitOriginURL = gitOriginURL(next, newWD)
	}
	_ = execenv.GitRootOrEmpty(next, newWD) // pre-warm next's git-root cache; see comment above

	// Step 2 — under s.mu: atomically install env+envInfo (so the two are
	// never observed in a torn intermediate state) and rebuild the caches that
	// derive from them. next's git-root cache is already warm, so this render
	// hits the cache instead of forking.
	s.mu.Lock()
	ei.KnowledgeCutoff = s.envInfo.KnowledgeCutoff // profile-derived, not env-derived; swap must not clobber it
	s.env = next
	s.envInfo = ei
	s.rebuildToolDefsCache()
	s.refreshSystemPromptCache(next)
	s.mu.Unlock()
}
