//go:build !windows

package mergeq

import "syscall"

// groupFDPublishes reports whether fd is an open descriptor this process can
// write to, asked of the descriptor itself rather than assumed from the number.
//
// The question has to be asked BEFORE the gate pipe is opened. A descriptor the
// launcher closed is not a handle: os.Pipe() allocates the lowest free numbers,
// so the gate's own write end takes exactly that number, and a publication that
// trusted the number would write the group record into the gate pipe -- which
// releases `gh` without ever publishing its group. Checking first is what makes a
// closed channel the ErrGroupPublish path instead of a started call nobody can
// stop.
//
// F_GETFL is the read side of the question: it reports the descriptor's flags, so
// it fails with EBADF on a number that names no descriptor, and its O_ACCMODE
// bits say whether that descriptor was opened for writing. O_RDONLY is refused:
// a write to it fails anyway, and saying so here keeps the failure a launcher
// error rather than a publication that could not land.
func groupFDPublishes(fd int) bool {
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFL, 0)
	if errno != 0 {
		return false
	}
	switch flags & syscall.O_ACCMODE {
	case syscall.O_WRONLY, syscall.O_RDWR:
		return true
	}
	return false
}
