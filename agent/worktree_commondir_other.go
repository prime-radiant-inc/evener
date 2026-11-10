//go:build !unix

package agent

import "os"

// openCommondir is the non-Unix fallback. Evener does not support Windows; the
// caller still resolves and bounds the path before this open, so the fallback
// only exists to keep GOOS=windows vet compiling.
func openCommondir(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY, 0)
}
