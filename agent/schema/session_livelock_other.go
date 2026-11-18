//go:build !linux && !darwin && !windows

package schema

// AcquireSessionLivelock has no livelock on this platform: the GOOS none of
// the arms with a real primitive covers — every Unix other than linux/darwin
// (freebsd, openbsd, netbsd, dragonfly, solaris, aix) and the non-Unix,
// non-Windows ports. Not-applied means the caller must run without a livelock
// — which costs liveness visibility (other sessions' probes answer unknown and
// keep the caller's lanes locked, the safe direction), never correctness. Same
// shape and caveat as snapshot_flock_other.go.
//
// The tag names the three GOOS this file must not cover, rather than testing
// !unix, to stay exhaustive against session_livelock_unix.go's narrowed
// linux || darwin arm: a !unix tag leaves freebsd and the rest of Unix with
// neither an implementation nor this fallback, breaking every importer of the
// two symbols below (issue #6796).
func AcquireSessionLivelock(_, _ string) (release func(), applied bool, err error) {
	return func() {}, false, nil
}

// SessionLivelockHeld reports no livelock file on this platform: exists=false
// answers nothing, and callers must treat that as unknown, never as death.
func SessionLivelockHeld(_, _ string) (held, exists bool, err error) {
	return false, false, nil
}
