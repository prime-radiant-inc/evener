//go:build !unix

package hub

import "os"

// fileOwnerUID reports no uid: the platform does not record one, so the caller
// judges what it can see (the file's type and its permissions) and nothing else.
func fileOwnerUID(os.FileInfo) (int, bool) { return 0, false }

// fileModePerm reports no permission bits to judge: this platform does not
// record POSIX modes (Windows synthesizes 0666 for every file, 0444 when it is
// read-only), so a comparison against 0600 would refuse every file - and, for
// the endpoint fingerprint key, rotate it on every read. The caller judges what
// it can see (the file's type) and skips the mode.
func fileModePerm(os.FileInfo) (os.FileMode, bool) { return 0, false }
