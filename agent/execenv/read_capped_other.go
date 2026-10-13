//go:build !unix

package execenv

// openNonblock is zero where the FIFO-blocking hazard is unix-shaped; the
// regular-file admission and the byte cap still apply. open_regular_other.go
// documents the same platform stance for OpenRegularNoFollow.
const openNonblock = 0
