//go:build !unix

package sandbox

// openNonblock is zero where Evener does not sandbox; the descriptor check in
// hostProbeSystem.readFile still refuses anything but a regular file.
const openNonblock = 0

// openNoFollow is zero where Evener does not sandbox; such a host has no
// sandbox to escape, and the descriptor check still refuses non-regular files.
const openNoFollow = 0
