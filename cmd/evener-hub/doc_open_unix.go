//go:build unix

package hub

import "syscall"

// docOpenNonblock keeps a document open from waiting for a FIFO's writer.
const docOpenNonblock = syscall.O_NONBLOCK
