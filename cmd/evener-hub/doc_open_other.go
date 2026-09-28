//go:build !unix

package hub

// docOpenNonblock is zero where there are no FIFOs to wait on.
const docOpenNonblock = 0
