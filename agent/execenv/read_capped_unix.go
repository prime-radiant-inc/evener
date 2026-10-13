//go:build unix

package execenv

import "syscall"

// openNonblock makes an open return at once on a FIFO, whose read-only open
// would otherwise wait for a writer. It mirrors sandbox's openNonblock
// (open_nonblock_unix.go there); harmless for regular files, whose reads never
// honor it.
const openNonblock = syscall.O_NONBLOCK
