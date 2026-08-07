//go:build !linux && !darwin

package execenv

import (
	"fmt"
	"os"
)

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

// The fd-anchored sandboxFS operations live in securepath_fdops_unix.go /
// securepath_browse_fdops_unix.go. These stand-ins mirror openBeneathRoot's
// contract above: unreachable in practice (no sandboxFS is ever built here),
// fail closed if somehow reached.

func (s *sandboxFS) close() {}

func (s *sandboxFS) readFile(tool, abs string) ([]byte, error) {
	return nil, errSandboxUnsupported()
}

func (s *sandboxFS) writeFile(tool, abs string, data []byte, perm os.FileMode) error {
	return errSandboxUnsupported()
}

func (s *sandboxFS) remove(tool, abs string) error {
	return errSandboxUnsupported()
}

func (s *sandboxFS) rename(tool, oldAbs, newAbs string) error {
	return errSandboxUnsupported()
}

func (s *sandboxFS) mkdirAll(tool, abs string) error {
	return errSandboxUnsupported()
}

func (s *sandboxFS) exists(tool, abs string) bool { return false }

func (s *sandboxFS) listDir(tool, abs string, depth int) ([]DirEntry, error) {
	return nil, errSandboxUnsupported()
}

func (s *sandboxFS) glob(tool, base, pattern string, includeIgnored bool) ([]string, error) {
	return nil, errSandboxUnsupported()
}

func (s *sandboxFS) grepNative(pattern, base, globFilter string, caseInsensitive bool, maxResults int, outputMode string, contextLines ...int) (string, error) {
	return "", errSandboxUnsupported()
}
