//go:build !unix && !windows

package schema

import "github.com/spf13/afero"

// lockSessionMetaCrossProcess has no cross-process lock on this platform, so only
// the in-process striped mutex serializes Revision updates.
//
// This file covers the remaining non-Unix GOOS other than Windows, but none of
// them is a shipped target: .goreleaser.yml builds linux and darwin only (and
// darwin/ios satisfy the `unix` constraint), so the production hub never runs
// here. The fallback exists so the package keeps compiling on those GOOS with the
// Unix-only flock source correctly carved out; it is not a promise of
// cross-process coordination on a platform the product does not target. Windows,
// the one non-Unix GOOS the GOOS=windows build-tag discipline vet in CI compiles,
// now has a real LockFileEx lock in snapshot_flock_windows.go.
func lockSessionMetaCrossProcess(_ afero.Fs, _, _ string) (func(), bool, error) {
	return func() {}, false, nil
}
