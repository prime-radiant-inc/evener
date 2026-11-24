//go:build !linux && !darwin

package agent

import "time"

// processStartedAt reports the process start time on Linux and macOS only.
// Other platforms have no reader wired up here, so daemon identity cannot be
// established from a rendezvous entry's pid: the owner-liveness oracle must
// treat such an entry as doubt (owner-unverifiable), never as owner-live on
// signal-0 liveness alone — a reused pid wearing a dead daemon's entry would
// otherwise hide an abandoned lane from recovery (review round 6, finding 2).
// Nothing is signaled there either: a group whose identity cannot be verified
// is never reaped (#4843).
func processStartedAt(pid int) (time.Time, bool) {
	return time.Time{}, false
}
