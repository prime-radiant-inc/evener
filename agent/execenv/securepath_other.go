//go:build !linux && !darwin

package execenv

import "fmt"

// This platform has no supported race-safe file-tool enforcement primitive. It is
// unreachable in practice — M1's fail-closed floor refuses every sandboxed mode
// off Linux/Darwin, so no ResolvedPolicy is ever enforced here and a sandboxFS is
// never built — but these definitions keep the build green and the floor honest:
// if one were somehow reached, it fails closed rather than silently reading.

func openBeneathRoot(rootFd int, rel string, flags int, mode uint32) (int, error) {
	return -1, errSandboxUnsupported()
}

func openAbsNoSymlinks(abs string, flags int, mode uint32) (int, error) {
	return -1, errSandboxUnsupported()
}

func openRootDir(path string) (int, error) {
	return -1, errSandboxUnsupported()
}

// canonicalRecheckRequired / canonicalPathOfFd exist only to satisfy the shared
// callers; this platform never builds a sandboxFS.
const canonicalRecheckRequired = false

func canonicalPathOfFd(fd int) (string, error) {
	return "", errSandboxUnsupported()
}

func errSandboxUnsupported() error {
	return fmt.Errorf("sandbox file-tool enforcement is unsupported on this platform")
}
