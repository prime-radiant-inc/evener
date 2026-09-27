//go:build !unix

package schema

import "os"

// readFileNoFollowOS is the portable fallback: this platform has no
// O_NOFOLLOW, so a leaf symlink is followed (creating one requires elevated
// privileges on the common non-unix hosts), and the open is a plain read. The
// component walk (metaComponentWalk) still rejects symlinked intermediate
// directories; only the leaf-level no-follow guarantee is absent, matching
// the documented trade-off in execenv.OpenRegularNoFollow's non-unix variant.
func readFileNoFollowOS(path string) ([]byte, error) {
	return os.ReadFile(path)
}
