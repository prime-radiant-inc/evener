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

// refuseGitSymlink refuses a source symlink whose lexical or resolved target
// names a .git component. Ordinary links keep copyTree's existing dereference
// behavior; only links into Git metadata are refused.
func refuseGitSymlink(src, path string) error {
	reachesGit, err := symlinkReachesGit(src, path)
	if err != nil {
		return err
	}
	if reachesGit {
		return gitSymlinkError(src, path)
	}
	return nil
}
