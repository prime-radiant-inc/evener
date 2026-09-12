//go:build unix

package tui

import "syscall"

// nonblockingOpen keeps opening a path from blocking on what it names. A
// FIFO with no writer blocks an ordinary open until one arrives, which on the
// update loop means the interface stops responding with no way out; with this
// flag the open returns and the descriptor can be judged and refused.
const nonblockingOpen = syscall.O_NONBLOCK
