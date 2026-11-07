//go:build unix

package plugins

import (
	"io/fs"
	"os"
	"syscall"
)

// openRegularNoBlock opens path for reading without ever blocking on a named
// pipe, then confirms the opened descriptor is a regular file (#4847).
//
// Every caller checks a path for a regular file and then opens it, which leaves
// a window for a FIFO to be substituted: a blocking open parks until a writer
// appears, and a writer need never appear. O_NONBLOCK makes the open of a FIFO
// return at once, and the fstat of the descriptor then refuses it. The check
// sits on the descriptor rather than the path, so what is read is what was
// verified.
//
// A symlink is still followed: the source copy dereferences legitimate
// companion-file links, and a link to a FIFO is refused by the descriptor's own
// mode just the same.
func openRegularNoBlock(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = syscall.Close(fd)
		return nil, &os.PathError{Op: "open", Path: path, Err: fs.ErrInvalid}
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
