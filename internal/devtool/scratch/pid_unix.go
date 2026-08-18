//go:build linux || darwin

package scratch

import "syscall"

// pidAlive reports whether a pid answers signal 0. EPERM is an answer: the
// process exists, it just isn't ours, and an existing process keeps its
// directory. (bash's `kill -0` reports EPERM as failure, so the shell
// reclaimer would delete that directory; keeping it is the safe direction for
// a recursive delete to differ in.)
func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
