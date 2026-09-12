//go:build !unix

package tui

// nonblockingOpen has no equivalent outside unix: Windows has no O_NONBLOCK
// for files, and the shapes it guards against (FIFOs opened by path, character
// devices under /dev) are unix shapes. The descriptor is still judged after
// the open, so a non-regular file is refused either way.
const nonblockingOpen = 0
