//go:build unix

package sandbox

import "syscall"

// openNonblock makes an open return at once on a FIFO, whose open would
// otherwise wait for a writer.
const openNonblock = syscall.O_NONBLOCK
