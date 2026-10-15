//go:build darwin

package execenv

// fdOnPseudoMount is the darwin stand-in for the linux mount-magic check.
// Darwin has no procfs or sysfs, /dev is a plain directory on the root
// volume, and the special files under it are refused by the type admission,
// so a descriptor never needs the mount question answered here — the caller's
// spelled hint stays authoritative.
func fdOnPseudoMount(_ int) bool {
	return false
}
