package server

import "testing"

// setInsideAppProjectionCommitHook installs fn as the package-wide
// inside-commit hook (see insideAppProjectionCommitHook) for the duration of
// the test. Callers must join every goroutine that can commit before the test
// returns, so the cleanup's reset cannot race a live commit.
func setInsideAppProjectionCommitHook(t *testing.T, fn func()) {
	t.Helper()
	insideAppProjectionCommitHook = fn
	t.Cleanup(func() { insideAppProjectionCommitHook = nil })
}
