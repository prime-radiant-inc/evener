//go:build linux

package schema

import (
	"os"

	"golang.org/x/sys/unix"
)

// markLivelockHeld sets the OFD write lock a non-taking F_GETLK probe reads
// as held-ness. Linux's F_GETLK does not report flock locks, so the flock the
// mint takes for ownership cannot serve the query; the OFD marker is the
// query-side evidence, and it dies with the file descriptor exactly as the
// flock does. This is the hostlock marker pattern
// (cmd/evener-hub/internal/hostlock).
func markLivelockHeld(f *os.File) error {
	lock := unix.Flock_t{Type: unix.F_WRLCK, Whence: 0, Start: 0, Len: 0}
	return unix.FcntlFlock(f.Fd(), unix.F_OFD_SETLK, &lock)
}
