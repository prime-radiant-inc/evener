//go:build !linux && !darwin

package dev

// groupAlive reports whether process group pgid still has a member. Without
// process groups (see execsupport/procgroup) there is none to drain.
func groupAlive(int) bool { return false }
