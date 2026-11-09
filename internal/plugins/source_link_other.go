//go:build !linux

package plugins

import "os"

// openPluginSourceLink preserves the earlier check-then-open behavior on hosts
// with no descriptor-path oracle. The opened descriptor is still the file whose
// bytes are copied, so a substitution after this open cannot change the copy.
func openPluginSourceLink(root, path string) (*os.File, error) {
	if err := refuseGitSymlink(root, path); err != nil {
		return nil, err
	}
	return openRegularNoBlock(path)
}
