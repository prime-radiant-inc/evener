//go:build !linux

package identifier

// fdOnPseudoMount has no procfs/sysfs/devtmpfs mount magic to consult here.
// Darwin has no procfs or sysfs, /dev is a plain directory on the root volume,
// and the special files under it are refused by the descriptor's type check;
// every other host has no procfs/sysfs/devtmpfs to classify either, so false is
// the answer there for the same reason. The constraint is "not linux" rather
// than "unix" because gitpointer.go is untagged and calls this, so a unix-only
// constraint would leave a non-unix build with no definition at all.
func fdOnPseudoMount(_ int) bool { return false }
