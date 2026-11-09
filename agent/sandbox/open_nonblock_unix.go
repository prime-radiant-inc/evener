//go:build unix

package sandbox

import "syscall"

// openNonblock makes an open return at once on a FIFO, whose open would
// otherwise wait for a writer.
const openNonblock = syscall.O_NONBLOCK

// openNoFollow refuses to follow a final-component symlink. A worktree file is
// untrusted content, so a project config must not turn into a read of a link
// planted in the repository.
const openNoFollow = syscall.O_NOFOLLOW
