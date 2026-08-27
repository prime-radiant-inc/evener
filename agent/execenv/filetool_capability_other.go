//go:build !linux && !darwin

package execenv

// FileToolEnforceable reports whether this process can use the race-safe secure
// open primitive that confines file-tool operations on the current host.
func FileToolEnforceable() bool { return false }
