//go:build linux || darwin

package hub

import "syscall"

// daemonSysProcAttr detaches a spawned daemon into a session of its own, so
// terminal signals aimed at the hub — Ctrl-C's SIGINT to the foreground
// process group, SIGHUP when the terminal closes — never reach it. Spawned
// daemons are documented to outlive a hub restart (SpawnDaemon); without this
// they share the hub's process group and die with it.
func daemonSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
