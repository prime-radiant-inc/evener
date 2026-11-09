//go:build linux

package plugins

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// pluginSourceBeforeLinkOpen is a test seam in the window between reading a
// source symlink and opening the target it named. A test uses it to retarget
// the link, which is the race the descriptor check exists to close.
var pluginSourceBeforeLinkOpen = func(string) {}

// openPluginSourceLink opens the target a source symlink names and verifies
// that the opened descriptor's own resolved path does not reach Git metadata.
// The name check and the bytes copied must share one identity: a link
// retargeted after an earlier path check cannot change what this descriptor
// reads, and /proc/self/fd names the file the descriptor actually holds.
func openPluginSourceLink(root, path string) (*os.File, error) {
	target, err := os.Readlink(path)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}
	pluginSourceBeforeLinkOpen(path)
	in, err := openRegularNoBlock(target)
	if err != nil {
		return nil, err
	}
	opened, err := os.Readlink(filepath.Join("/proc/self/fd", strconv.Itoa(int(in.Fd()))))
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
