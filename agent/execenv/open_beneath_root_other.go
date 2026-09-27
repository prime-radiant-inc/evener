//go:build !unix

package execenv

import (
	"errors"
	"fmt"
	"os"
)

// ErrNonTraversableRoot classifies refusal of a protected root boundary that
// is a symlink or is not a directory.
var ErrNonTraversableRoot = errors.New("root is a symlink or not a directory; refusing to follow it")

// OpenRegularBeneathRoot is the portable fallback: this platform has no
// openat or O_NOFOLLOW, so the descriptor-relative walk is not possible.
// The open is a plain read open (matching OpenRegularNoFollow's non-unix
// variant), and the descriptor is fstat'd regular before the caller reads a
// byte. Intermediate-component symlink protection relies on the pre-walk
// (symlinkErrorDeep) which is the best available guarantee on this platform.
// Production callers use OpenRegularBeneathRootNoFollow; direct tests retain
// this API to pin its followable-root semantics.
func OpenRegularBeneathRoot(path, root string) (*os.File, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, serr := file.Stat()
	if serr != nil {
		_ = file.Close()
		return nil, serr
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("open %q: not a regular file", path)
	}
	return file, nil
}

// OpenRegularBeneathRootNoFollow is the best available portable fallback for
// refusing a symlinked root or first walked directory component. Platforms in
// this file have no O_NOFOLLOW, so the root and component Lstats cannot be
// bound atomically to the path open. The guarantee is deterministic refusal
// only: either component can still be swapped between validation and open.
func OpenRegularBeneathRootNoFollow(path, root string) (*os.File, error) {
	return openRegularBeneathRootNoFollowPortable(path, root, OpenRegularBeneathRoot)
}
