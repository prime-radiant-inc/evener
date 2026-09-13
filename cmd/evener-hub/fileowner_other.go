//go:build !unix

package hub

import "os"

// fileOwnerUID reports no uid: the platform does not record one, so the caller
// judges what it can see (the file's type and its permissions) and nothing else.
func fileOwnerUID(os.FileInfo) (int, bool) { return 0, false }
