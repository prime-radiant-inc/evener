//go:build unix

package plugins

import (
	"errors"
	"syscall"
)

// pathCannotExist reports whether err says the path names nothing that can be
// there at all: the filesystem refuses it for its length, a component of it is
// not a directory, or it carries a byte a name cannot. A caller asking what a
// path holds counts it as absent rather than as a failure, because the
// migration has to be able to look at a legacy name's directories in order to
// rename it, and one the filesystem will not even consider has none. Failing
// there instead leaves every lock-taking operation failing on the store, which
// is the state the migration exists to end.
func pathCannotExist(err error) bool {
	return errors.Is(err, syscall.ENAMETOOLONG) ||
		errors.Is(err, syscall.ENOTDIR) ||
		errors.Is(err, syscall.EINVAL)
}
