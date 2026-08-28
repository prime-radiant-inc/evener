package server

import "testing"

// setInsideAppProjectionCommitHook installs fn as the package-wide
// inside-commit hook (see insideAppProjectionCommitHook) for the duration of
// the test. The hook is one unsynchronized package variable, so a caller MUST
// NOT be a parallel test (two parallel installers would race each other and
// every committing goroutine), and it must join every goroutine that can
// commit before returning, so the cleanup's reset cannot race a live commit.
func setInsideAppProjectionCommitHook(t *testing.T, fn func()) {
	t.Helper()
	insideAppProjectionCommitHook = fn
	t.Cleanup(func() { insideAppProjectionCommitHook = nil })
}
