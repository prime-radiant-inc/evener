// Package binresolve locates sibling executables that ship alongside a
// running Go program.
//
// Many of the serf binaries (serf, serf-hub, serf-tui) ship together in
// the same directory and call out to each other. Without help from
// $PATH, exec.Command("serf", ...) inside serf-hub will fail when the
// operator runs the hub from a directory where it lives next to serf
// but neither is on $PATH. This package centralises the "look next to
// the running executable, then fall back to PATH" resolution so each
// caller picks it up the same way.
package binresolve

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Resolve returns the path to the named sibling binary. Resolution order:
//  1. explicit (if non-empty) is returned as-is after an executability
//     check.
//  2. If the binary exists next to currentExecutable (via filepath.Abs
//     + filepath.EvalSymlinks), return its absolute path.
//  3. Otherwise, fall through to PATH lookup via lookPath. When lookPath
//     is nil, exec.LookPath is used.
//
// On Windows, the platform-native ".exe" suffix is appended automatically
// when looking for the sibling.
//
// Returns the resolved path and a nil error on success. When all three
// resolution steps fail, Resolve returns the wrapped lookPath error so
// the caller can decide whether to surface it or rely on exec.Command's
// runtime PATH search.
func Resolve(name, explicit, currentExecutable string, lookPath func(string) (string, error)) (string, error) {
	if explicit != "" {
		if !isExecutable(explicit) {
			return "", fmt.Errorf("%s binary is not executable: %s", name, explicit)
		}
		return explicit, nil
	}
	target := name
	if runtime.GOOS == "windows" {
		target += ".exe"
	}
	if dir, ok := SiblingDir(currentExecutable); ok {
		sibling := filepath.Join(dir, target)
		if isExecutable(sibling) {
			return sibling, nil
		}
	}
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	path, err := lookPath(target)
	if err != nil {
		return "", fmt.Errorf("find %s: %w", name, err)
	}
	return path, nil
}

// SiblingDir returns the directory of the running binary as an absolute,
// symlink-resolved path. The currentExecutable argument is typically
// obtained from os.Executable() (or os.Args[0]) in main(); callers may
// pass a fixture path in tests.
//
// The path is canonicalised via filepath.Abs and filepath.EvalSymlinks
// so that a relative invocation like "./serf-tui" (which would trip
// exec.ErrDot when handed back to exec.Command) or a symlink such as
// /usr/local/bin/serf-tui -> /opt/serf/serf-tui still resolves to the
// directory that actually holds the binary. Returns ok=false when no
// usable path can be derived.
func SiblingDir(currentExecutable string) (string, bool) {
	candidate := strings.TrimSpace(currentExecutable)
	if candidate == "" {
		return "", false
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", false
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return filepath.Dir(abs), true
}

// isExecutable reports whether path names an existing, non-directory
// file with at least one execute bit set. On Windows the execute-bit
// check is skipped because NTFS does not surface it.
func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return info.Mode()&0o111 != 0
}
