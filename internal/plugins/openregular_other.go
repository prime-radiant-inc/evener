//go:build !unix

package plugins

import (
	"os"
)

// openRegularNoBlock opens path for reading and confirms the descriptor is a
// regular file. This platform has no open flag that makes a named pipe return
// instead of waiting for a writer, so the open is the plain one; the descriptor
// check is kept so callers see the same refusal on every platform.
func openRegularNoBlock(path string) (*os.File, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, &os.PathError{Op: "open", Path: path, Err: errNotRegular}
	}
	return file, nil
}
