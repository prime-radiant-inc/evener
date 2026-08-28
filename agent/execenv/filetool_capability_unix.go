//go:build linux || darwin

package execenv

import "golang.org/x/sys/unix"

var securePathCapabilityProbe = func() error {
	fd, err := openAbsNoSymlinks("/", unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	return unix.Close(fd)
}

// FileToolEnforceable reports whether this process can use the race-safe secure
// open primitive that confines file-tool operations on the current host.
func FileToolEnforceable() bool {
	return securePathCapabilityProbe() == nil
}
