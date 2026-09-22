//go:build windows

package agent

import "path/filepath"

// anchorRelativeStateDir anchors a relative --state-dir using the
// platform's own full-path resolution. Windows "relative" is three input
// classes — plain (state), volume-root (\state), and drive-relative
// (C:state) — and only full-path resolution anchors all three correctly:
// a drive-relative path belongs to the current directory of its own
// drive, not to this process's current drive, so concatenating the
// process cwd corrupts C:state into D:\cwd\C:state. filepath.Abs
// delegates to syscall.FullPath (GetFullPathName), which resolves every
// class against the right per-drive current directory. The lexical Clean
// Abs applies before the walk is harmless here: the component walk
// re-resolves every existing component of the anchored path anyway.
func anchorRelativeStateDir(dir string) (string, bool) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir, false
	}
	return abs, true
}
