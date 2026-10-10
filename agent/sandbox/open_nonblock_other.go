//go:build !unix

package sandbox

// openNonblock is zero where Evener does not sandbox; the descriptor check in
// hostProbeSystem.readFile still refuses anything but a regular file.
const openNonblock = 0
