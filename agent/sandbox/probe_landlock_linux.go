//go:build linux

package sandbox

import "golang.org/x/sys/unix"

// probeLandlockABI returns the host's Landlock ABI version, or 0 if Landlock is
// unavailable (kernel too old, LSM not enabled, or blocked). It issues the
// canonical, side-effect-free version query:
// landlock_create_ruleset(NULL, 0, LANDLOCK_CREATE_RULESET_VERSION), which the
// kernel answers with the supported ABI version without creating any ruleset or
// restricting the caller. A negative/error return means unavailable.
func probeLandlockABI() int {
	abi, _, errno := unix.Syscall(
		uintptr(unix.SYS_LANDLOCK_CREATE_RULESET),
		0, // attr = NULL
		0, // size = 0
		uintptr(unix.LANDLOCK_CREATE_RULESET_VERSION),
	)
	if errno != 0 {
		return 0
	}
	return int(abi)
}
