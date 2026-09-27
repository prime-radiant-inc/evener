//go:build linux || darwin

package dev

import "syscall"

// groupAlive reports whether process group pgid still has a member.
func groupAlive(pgid int) bool {
	return syscall.Kill(-pgid, 0) == nil
}
