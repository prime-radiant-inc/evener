//go:build unix

package execenv

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// OpenRegularBeneathRoot opens a regular file at path, walking every
// intermediate component relative to root through openat with O_NOFOLLOW so
// no symlink is ever traversed — not just at the leaf (OpenRegularNoFollow)
// but at every parent directory too. This closes the intermediate-component
// TOCTOU window: symlinkErrorDeep Lstats each component before the open,
// but a directory swapped for a symlink between the pre-walk and the open is
// refused (ELOOP from openat) rather than followed.
//
// The walk starts by opening root as a directory descriptor, then opens each
// component of filepath.Rel(root, path) one at a time beneath the previous
// descriptor. Intermediate components are opened with O_NOFOLLOW and
// O_NONBLOCK (so a FIFO at an intermediate does not block waiting for a
// writer), then fstat'd to confirm a directory. The final component is
// opened read-only with O_NOFOLLOW and O_NONBLOCK (so a FIFO at the leaf does
// not block), then fstat'd to confirm a regular file. The validation and the
// read share one descriptor tree, so nothing can be swapped between the check
// and the bytes.
//
// path must be beneath root (filepath.Rel returns a non-".." relative path);
// callers that pass a path outside root get an error from the escape guard.
func OpenRegularBeneathRoot(path, root string) (*os.File, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return nil, fmt.Errorf("open %q beneath %q: %w", path, root, err)
	}
	rel = filepath.Clean(rel)
	if rel == "." {
		return nil, fmt.Errorf("open %q: path is the root directory, not a file", path)
	}
	// Reject any ".." component — path must be beneath root.
	if strings.HasPrefix(rel, "..") {
		return nil, fmt.Errorf("open %q: path escapes root %q", path, root)
	}

	comps := strings.Split(rel, string(filepath.Separator))
	if len(comps) == 0 {
		return nil, fmt.Errorf("open %q: empty relative path", path)
	}

	// Open root as a directory descriptor.
	rootFd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: root, Err: err}
	}

	// Walk intermediate components (all but the last) as directories. We open
	// with O_RDONLY|O_NOFOLLOW|O_NONBLOCK (no O_DIRECTORY): O_NOFOLLOW alone
	// returns ELOOP for a symlink (which is what we want), while
	// O_NOFOLLOW|O_DIRECTORY returns ENOTDIR instead — less legible and
	// ambiguous with a real non-directory. O_NONBLOCK is essential here: an
	// openat(O_RDONLY) on a FIFO at an intermediate component blocks
	// indefinitely waiting for a writer, so without it the fstat directory
	// check below never runs and the whole read hangs — a regression of the
	// pre-fix full-path open, which rejected a FIFO intermediate immediately
	// (ENOTDIR during path resolution). O_NONBLOCK is harmless on directories;
	// it makes the FIFO open immediately (nonblocking), and the fstat +
	// "is not a directory" error then rejects it promptly. After opening,
	// fstat confirms the result is a directory.
	cur := rootFd
	curOwned := true
	closeCur := func() {
		if curOwned {
			_ = unix.Close(cur)
		}
	}
	for i := 0; i < len(comps)-1; i++ {
		next, err := unix.Openat(cur, comps[i], unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		closeCur()
		if err != nil {
			if errors.Is(err, unix.ELOOP) {
				return nil, fmt.Errorf("open %q: component %q is a symlink, refusing to follow it", path, comps[i])
			}
			return nil, &os.PathError{Op: "openat", Path: filepath.Join(root, filepath.Join(comps[:i+1]...)), Err: err}
		}
		// Confirm the opened component is a directory.
		var st unix.Stat_t
		if err := unix.Fstat(next, &st); err != nil {
			_ = unix.Close(next)
			return nil, &os.PathError{Op: "fstat", Path: filepath.Join(root, filepath.Join(comps[:i+1]...)), Err: err}
		}
		if st.Mode&unix.S_IFMT != unix.S_IFDIR {
			_ = unix.Close(next)
			return nil, fmt.Errorf("open %q: component %q is not a directory", path, comps[i])
		}
		cur = next
		curOwned = true
	}

	// Open the leaf component read-only with O_NOFOLLOW and O_NONBLOCK.
	leaf := comps[len(comps)-1]
	fd, err := unix.Openat(cur, leaf, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	closeCur()
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, fmt.Errorf("open %q: leaf path is a symlink, refusing to follow it", path)
		}
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}

	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("open %q: no file for descriptor", path)
	}
	info, serr := file.Stat()
	if serr != nil {
		_ = file.Close()
		return nil, serr
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("open %q: not a regular file", path)
	}
	return file, nil
}
