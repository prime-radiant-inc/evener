//go:build !linux && !darwin

package execenv

// fdOnPseudoMount has no procfs/sysfs/devtmpfs mount magic to consult here, so
// the caller's spelled hint stays authoritative for the admission's read
// discipline — the same stand-in discipline securepath_fdops_mount_darwin.go
// discloses where the descriptor's mount cannot classify a pseudo stream.
//
// The constraint is the package's own no-pseudo-mount one rather than "unix":
// project_resolver.go is untagged and calls this from its pointer reader, so a
// unix-only constraint leaves the non-unix build with no definition at all.
// Every such host has no procfs/sysfs/devtmpfs to classify either, so false is
// the answer there for the same reason it is on the other Linux-less Unixes.
func fdOnPseudoMount(_ int) bool { return false }
