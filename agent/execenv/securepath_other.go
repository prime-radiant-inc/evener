//go:build !linux && !darwin

package execenv

import (
	"context"
	"fmt"
	"os"
)

// This platform has no supported race-safe file-tool enforcement primitive, so
// every operation below fails closed — READS included. A sandboxFS built here
// would not be a confined file-tool layer, it would be a broken one.
//
// Two resolver rules keep one from being built, and both are load-bearing:
//   - the fail-closed backend floor refuses every sandboxed MODE off Linux/Darwin
//     (chooseBackend), so no enforced policy exists here;
//   - Resolve refuses a write-blocked OFF policy — the one shape that confines the
//     file tools with no backend behind it — wherever FileToolEnforceable is false.
//
// Without the second rule the degraded read-only delegate would resolve here and
// hand back a delegate whose every file operation errors. If a third shape ever
// gains file-tool confinement, it must be refused here too.

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

func (s *sandboxFS) glob(ctx context.Context, tool, base, pattern string, includeIgnored bool) ([]string, int, error) {
	return nil, 0, errSandboxUnsupported()
}

func (s *sandboxFS) grepNative(ctx context.Context, pattern, base, globFilter string, caseInsensitive bool, maxResults int, outputMode string, contextLines ...int) (string, error) {
	return "", errSandboxUnsupported()
}
