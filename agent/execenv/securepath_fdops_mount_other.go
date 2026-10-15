//go:build unix && !linux && !darwin

package execenv

// fdOnPseudoMount has no procfs/sysfs/devtmpfs mount magic to consult here, so
// the caller's spelled hint stays authoritative for the admission's read
// discipline — the same stand-in discipline securepath_fdops_mount_darwin.go
// discloses where the descriptor's mount cannot classify a pseudo stream.
func fdOnPseudoMount(_ int) bool { return false }
