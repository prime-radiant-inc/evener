//go:build !unix

package selfupdate

import "context"

// acquireInstallLock is a no-op where no flock primitive is wired up:
// concurrent installs from two processes are then serialized only by the
// atomic renames below (a complete binary or symlink always wins intact),
// which preserves correctness but not pairing between evener/evener-dev.
func acquireInstallLock(shareBinDir string) (release func(), err error) {
	return acquireInstallLockCtx(context.Background(), shareBinDir)
}

// acquireInstallLockCtx matches the unix signature; with no primitive to
// wait on there is nothing for ctx to cancel.
func acquireInstallLockCtx(_ context.Context, _ string) (release func(), err error) {
	return func() {}, nil
}
