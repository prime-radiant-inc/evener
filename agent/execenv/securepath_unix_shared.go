//go:build linux || darwin

package execenv

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"slices"
)

// This file holds the platform-independent helpers that only the Unix
// fd-anchored enforcement paths call. They stay out of securepath.go so a
// build without those paths does not carry declarations with no caller, while
// the supported Unix build still shares one definition between its Linux,
// Darwin, and fd-operation files.

var (
	canonicalPathForFd = canonicalPathOfFd
	secureRandRead     = rand.Read
)

// approvedTarget returns where the granted operation on abs lands: the target
// the human approved. It refuses when abs no longer resolves there — a symlink
// in its parent was retargeted after approval — so the operation never reaches
// a location the human did not see.
func (s *sandboxFS) approvedTarget(tool, abs string) (string, error) {
	if target, ok := grantTarget(abs); !ok || target != s.grant.target {
		return "", s.deny(tool, abs, denyReasonSymlink)
	}
	return s.grant.target, nil
}

// errEscapesRoot is the sentinel a component walk returns when a path would
// escape its anchoring root (the component-walk analogue of openat2's EXDEV
// under RESOLVE_BENEATH). Kept package-level so both the darwin walk and the
// shared error mapper agree on it.
var errEscapesRoot = errors.New("path escapes sandbox root")

// errSymlinkComponent is the sentinel a component walk returns when it refused to
// traverse a component: a symlinked directory opened with O_DIRECTORY|O_NOFOLLOW
// surfaces as either ELOOP or ENOTDIR depending on kernel check order (and a real
// swap during a race can flip which), so the walks map BOTH to this one sentinel
// without a second, race-prone lstat — the open already refused to follow, and
// this only classifies the resulting error into a legible typed denial.
var errSymlinkComponent = errors.New("sandbox: refused non-traversable path component")

// isGrantedRoot reports whether abs is exactly one of the policy's granted roots
// (file-tool read or write roots), whose parent may lie outside the policy.
func (s *sandboxFS) isGrantedRoot(abs string) bool {
	return slices.Contains(s.policy.FileTool.ReadRoots, abs) || slices.Contains(s.policy.FileTool.WriteRoots, abs)
}

// tempName returns an unpredictable temp basename for the atomic write. The
// randomness avoids a griefing pre-create DoS (a predictable name could be
// pre-planted to make O_EXCL creation fail); combined with O_EXCL, a collision
// fails the create rather than clobbering anything.
func tempName() string {
	var b [12]byte
	if _, err := secureRandRead(b[:]); err != nil {
		// crypto/rand should never fail; fall back to a pid-tagged name rather than
		// panicking. Still O_EXCL-guarded at the create site.
		return fmt.Sprintf(".evener-sbtmp-%d", os.Getpid())
	}
	return ".evener-sbtmp-" + hex.EncodeToString(b[:])
}
