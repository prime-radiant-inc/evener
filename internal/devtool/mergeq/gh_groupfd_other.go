//go:build windows

package mergeq

// groupFDPublishes reports whether fd is an open descriptor this process can
// write to. Windows has no F_GETFL and no inherited channel to ask about: the
// launcher that hands one down is a POSIX shell script, so a descriptor arriving
// here is a number this process was never given. Answering "no" is the answer the
// publication path already acts on, and the call fails closed with
// ErrGroupPublish rather than writing a group record nowhere.
func groupFDPublishes(int) bool { return false }
