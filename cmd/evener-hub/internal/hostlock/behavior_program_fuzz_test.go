package hostlock

import "testing"

// FuzzHostLockBehaviorProgram replays one deterministic behavioral contract selected by the
// fuzz input. The seed corpus covers every production branch; mutation varies
// ordering and repetition without relying on network, wall clock, or host state.
func FuzzHostLockBehaviorProgram(f *testing.F) {
	checks := []func(*testing.T){
		checkAcquireLock_Success,
		checkAcquireLock_FailsIfHeld,
		checkAcquireLock_ReleaseUnblocks,
		checkAcquireLock_MkdirAllError,
		checkAcquireLock_OpenFileError,
	}
	for i := range checks {
		f.Add(uint8(i))
	}
	f.Fuzz(func(t *testing.T, selector uint8) {
		checks[int(selector)%len(checks)](t)
	})
}
