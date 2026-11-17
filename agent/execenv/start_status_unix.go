//go:build unix

package execenv

import (
	"errors"
	"syscall"
)

// startRefusedByKernel reports whether err is execve refusing a file the
// launcher resolved. ENOENT is handled separately as the shell's "not found";
// every other refusal here is the shell's 126.
//
// The list is deliberately explicit rather than "any errno except ENOENT":
// resource exhaustion and transient scheduling failures reach Start as errnos
// too, and a shell has no status for those, so they must not be reported as a
// refusal.
func startRefusedByKernel(err error) bool {
	for _, refusal := range []syscall.Errno{
		syscall.EACCES,       // no execute bit, or an interpreter without one
		syscall.EPERM,        // a mount or LSM denying the execution
		syscall.EISDIR,       // a directory (Linux reports EACCES for exec)
		syscall.ENOEXEC,      // a format this kernel will not load
		syscall.ENOTDIR,      // a path component that is not a directory
		syscall.ELOOP,        // a symlink loop
		syscall.ENAMETOOLONG, // a name the filesystem refuses
		syscall.ETXTBSY,      // the program is open for writing
	} {
		if errors.Is(err, refusal) {
			return true
		}
	}
	return false
}
