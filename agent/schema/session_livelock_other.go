//go:build !unix && !windows

package schema

// AcquireSessionLivelock has no livelock on this platform: the GOOS neither of
// the shipped arms covers. Not-applied means the caller must run without a
// livelock — which costs liveness visibility (other sessions' probes answer
// unknown and keep the caller's lanes locked, the safe direction), never
// correctness. Same shape and caveat as snapshot_flock_other.go.
func AcquireSessionLivelock(_, _ string) (release func(), applied bool, err error) {
	return func() {}, false, nil
}

// SessionLivelockHeld reports no livelock file on this platform: exists=false
// answers nothing, and callers must treat that as unknown, never as death.
func SessionLivelockHeld(_, _ string) (held, exists bool, err error) {
	return false, false, nil
}
