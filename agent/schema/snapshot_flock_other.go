//go:build !unix

package schema

import "github.com/spf13/afero"

// lockSessionMetaCrossProcess has no cross-process lock on this platform, so only
// the in-process striped mutex serializes Revision updates.
//
// This file covers every non-Unix GOOS, but none of them is a shipped target:
// .goreleaser.yml builds linux and darwin only (and darwin/ios satisfy the `unix`
// constraint), so the production hub never runs here. The fallback exists so the
// package keeps compiling under the GOOS=windows build-tag discipline vet in CI
// with the Unix-only flock source correctly carved out; it is not a promise of
// cross-process coordination on a platform the product does not target. A real
// lock (LockFileEx) would be needed before any non-Unix platform could ship.
func lockSessionMetaCrossProcess(_ afero.Fs, _, _ string) (func(), bool, error) {
	return func() {}, false, nil
}
