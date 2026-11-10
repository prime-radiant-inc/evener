//go:build linux

package plugins

import (
	"os"
	"strconv"
	"strings"
)

// pluginSourceBeforeLinkOpen is a test seam in the window between reading a
// source symlink and opening the target it named. A test uses it to retarget
// the link, which is the race the descriptor check exists to close.
var pluginSourceBeforeLinkOpen = func(string) {}

// openPluginSourceLink refuses a source symlink whose lexical target names Git
// metadata, then opens the link path itself and verifies that the opened
// descriptor's own resolved path does not reach Git metadata. The lexical
// check catches a .git that is itself a symlink out of the tree; the
// descriptor check catches a retarget the name check cannot see, because
// /proc/self/fd names the file the descriptor actually holds.
func openPluginSourceLink(root, path string) (*os.File, error) {
	target, err := readLinkTarget(path)
	if err != nil {
		return nil, err
	}
	if targetTouchesGit(root, target) {
		return nil, gitSymlinkError(root, path)
	}
	pluginSourceBeforeLinkOpen(path)
	// Open the link path, not the lexical join: Join collapses ".." before the
	// kernel resolves intermediate symlinks, which can name different bytes.
	// The descriptor check below is the authority on what was opened.
	in, err := openRegularNoBlock(path)
	if err != nil {
		return nil, err
	}
	opened, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(int(in.Fd())))
	if err != nil {
		_ = in.Close()
		return nil, err
	}
	// An unlinked-but-open file resolves with this suffix. Its removed name is
	// still the metadata path that must not be published.
	opened = strings.TrimSuffix(opened, " (deleted)")
	// The descriptor path is resolved, so compare it with the resolved root:
	// a .git component above the source root is the marketplace's namespace,
	// not metadata inside the plugin (the same distinction targetTouchesGit
	// makes for the lexical check).
	if targetTouchesGit(resolvedOrSelf(root), opened) {
		_ = in.Close()
		return nil, gitSymlinkError(root, path)
	}
	return in, nil
}
