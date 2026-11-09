package agent

import (
	"testing"

	"primeradiant.com/evener/gittest"
)

// TestSessionGitRunsUnderTheTestProcessGitConfig pins TestMain's git isolation
// at the boundary the worktree lifecycle spawns git through: that runner
// inherits the process environment, so TestMain's gittest.IsolateProcess must
// reach it.
func TestSessionGitRunsUnderTheTestProcessGitConfig(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	gittest.RequireIsolated(t, func(_ string, args ...string) (string, error) {
		return gitRunner(t.Context(), r.s.currentEnv())(args...)
	}, r.mainRoot)
}
