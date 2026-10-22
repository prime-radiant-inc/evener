//go:build !unix

package plugins

// isCrossDevice is never true on platforms without Unix rename semantics.
func isCrossDevice(error) bool { return false }
