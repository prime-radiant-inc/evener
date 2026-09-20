//go:build !linux

package hub

import "errors"

// errNoAtomicRenameNoReplace is this platform's answer to renameNoReplaceAtomic:
// there is no no-replace rename to make, so the caller uses its checked move.
var errNoAtomicRenameNoReplace = errors.New("this platform has no atomic no-replace rename")

// renameNoReplaceAtomic reports that this platform cannot move a file without
// replacing a taken destination in one step.
func renameNoReplaceAtomic(string, string) error { return errNoAtomicRenameNoReplace }

// renameNoReplaceAtomicUnsupported reports the platform's own answer, so every
// caller has one code path and one set of words for it.
func renameNoReplaceAtomicUnsupported(err error) bool {
	return errors.Is(err, errNoAtomicRenameNoReplace)
}
